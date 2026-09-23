package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/engine"
)

// settledReply records one completed exchange and returns the reply's ID.
func settledReply(t *testing.T, a *App, id, message, reply string) string {
	t.Helper()
	ctx := context.Background()
	if _, err := a.Core.EnqueueChat(ctx, id, message); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Core.StartNextChat(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.Core.FinishChat(ctx, id, "completed", reply, ""); err != nil {
		t.Fatal(err)
	}
	turns, err := a.Core.ChatTurns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return turns[len(turns)-1].AssistantMessageID
}

// suggestionApp runs an assistant on engineName with both fake CLI logins.
func suggestionApp(t *testing.T, engineName string) (*App, *fakeCLIs) {
	t.Helper()
	a := testApp(t)
	a.cfg.Model = config.Default().Model
	a.cfg.Model.Engine = engineName
	f := newFakeCLIs(t)
	f.reply = `"What should I plant first?"`
	a.small = f.models()
	return a, f
}

func TestSuggestionRoutesToTheOwnEnginesApprovedModel(t *testing.T) {
	for engineName, want := range map[string]string{"codex": "codex/gpt-6-luna/low", "claude": "claude/haiku/"} {
		a, f := suggestionApp(t, engineName)
		after := settledReply(t, a, "one", "Plan the garden", "Here is a planting plan.")
		complete := a.small.complete
		a.small.complete = func(ctx context.Context, c engine.Config, m []engine.Message, tools []engine.Tool) (engine.Message, engine.Usage, error) {
			if c.APIKeyEnv != "" || c.Endpoint != "" {
				t.Fatal(c)
			}
			if len(m) != 2 || !strings.Contains(m[1].Content, "Owner: Plan the garden") || !strings.Contains(m[1].Content, "Assistant: Here is a planting plan.") {
				t.Fatal(m)
			}
			return complete(ctx, c, m, tools)
		}
		got, err := a.SuggestNextMessage(context.Background(), after)
		if err != nil || got != "What should I plant first?" || !equalStrings(f.used(), []string{want}) {
			t.Fatal(engineName, got, err, f.used())
		}
	}
}

func TestSuggestionFallsBackToTheOtherCLIInEachDirection(t *testing.T) {
	a, f := suggestionApp(t, "codex")
	f.discoverErr["codex"] = errors.New("codex: not logged in")
	after := settledReply(t, a, "one", "Hello", "Hi there.")
	if got, err := a.SuggestNextMessage(context.Background(), after); err != nil || got == "" || !equalStrings(f.used(), []string{"claude/haiku/"}) {
		t.Fatal(got, err, f.used())
	}
	a, f = suggestionApp(t, "claude")
	f.replyErr["claude"] = errors.New("429 rate limited")
	after = settledReply(t, a, "one", "Hello", "Hi there.")
	if got, err := a.SuggestNextMessage(context.Background(), after); err != nil || got == "" || !equalStrings(f.used(), []string{"claude/haiku/", "codex/gpt-6-luna/low"}) {
		t.Fatal(got, err, f.used())
	}
}

func TestSuggestionWithBothCLIsFailingIsQuietAndCheap(t *testing.T) {
	a, f := suggestionApp(t, "codex")
	f.replyErr["codex"] = errors.New("usage limit reached")
	f.discoverErr["claude"] = errors.New("claude: executable not found")
	after := settledReply(t, a, "one", "Hello", "Hi there.")
	got, err := a.SuggestNextMessage(context.Background(), after)
	// An outage is not something for the owner to act on: no notice, no suggestion.
	if err == nil || got != "" || errors.Is(err, ErrSuggestionUnavailable) || errors.Is(err, core.ErrConflict) {
		t.Fatal(got, err)
	}
	before := f.calls()
	if _, err = a.SuggestNextMessage(context.Background(), after); err == nil || f.calls() != before {
		t.Fatal("rested CLIs were tried again", err, f.calls()-before)
	}
	// The conversation itself still takes messages.
	if _, err = a.Core.EnqueueChat(context.Background(), "two", "Carry on"); err != nil {
		t.Fatal(err)
	}
}

func TestSuggestionEscalatesWhenNoApprovedModelIsAvailable(t *testing.T) {
	a, f := suggestionApp(t, "codex")
	after := settledReply(t, a, "one", "Hello", "Hi there.")
	// Each login offers other models, including the retired Luna, but not its approved one.
	f.offered["codex"] = f.offered["codex"][:2]
	f.offered["claude"] = f.offered["claude"][:1]
	_, err := a.SuggestNextMessage(context.Background(), after)
	if !errors.Is(err, ErrSuggestionUnavailable) || !strings.Contains(err.Error(), "gpt-6-luna is not offered to the codex login") || !strings.Contains(err.Error(), "haiku is not offered to the claude login") || len(f.used()) != 0 {
		t.Fatal(err, f.used())
	}
	a, f = suggestionApp(t, "openai-compatible")
	after = settledReply(t, a, "one", "Hello", "Hi there.")
	if _, err = a.SuggestNextMessage(context.Background(), after); !errors.Is(err, ErrSuggestionUnavailable) || f.calls() != 0 {
		t.Fatal(err, f.calls())
	}
	a, f = suggestionApp(t, "codex")
	a.Demo = true
	if _, err = a.SuggestNextMessage(context.Background(), after); err == nil || errors.Is(err, ErrSuggestionUnavailable) || f.calls() != 0 {
		t.Fatal(err, f.calls())
	}
}

func TestSuggestionEscalatesWhenALoadingCaptionFoundNoApprovedModelFirst(t *testing.T) {
	a, f := suggestionApp(t, "codex")
	f.offered["codex"] = f.offered["codex"][:2]
	f.offered["claude"] = f.offered["claude"][:1]
	ctx := context.Background()
	if _, err := a.Core.EnqueueChat(ctx, "one", "Plan the garden"); err != nil {
		t.Fatal(err)
	}
	turn, err := a.Core.StartNextChat(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The loading caption meets the missing models first and fails quietly.
	a.startChatLoading(ctx, turn.ID, turn.Message, nil)
	if err = a.Core.FinishChat(ctx, turn.ID, "completed", "Here is a plan.", ""); err != nil {
		t.Fatal(err)
	}
	turns, _ := a.Core.ChatTurns(ctx)
	before := f.calls()
	if before == 0 || turns[0].LoadingPhrase != "" {
		t.Fatal("loading caption did not try the CLIs", before, turns[0].LoadingPhrase)
	}
	// The suggestion that follows still tells the owner, without asking again.
	_, err = a.SuggestNextMessage(ctx, turns[0].AssistantMessageID)
	if !errors.Is(err, ErrSuggestionUnavailable) || !strings.Contains(err.Error(), "gpt-6-luna is not offered to the codex login") || f.calls() != before {
		t.Fatal(err, f.calls()-before)
	}
}

func TestSuggestionOnlyWhenTheConversationHasSettled(t *testing.T) {
	a, f := suggestionApp(t, "claude")
	ctx := context.Background()
	if _, err := a.SuggestNextMessage(ctx, ""); !errors.Is(err, core.ErrConflict) {
		t.Fatal("empty conversation", err)
	}
	after := settledReply(t, a, "one", "Hello", "Hi there.")
	// A pending reply: the owner's next message is queued.
	if _, err := a.Core.EnqueueChat(ctx, "two", "And another thing"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SuggestNextMessage(ctx, after); !errors.Is(err, core.ErrConflict) {
		t.Fatal("queued", err)
	}
	// A reply being written (streaming): the owner's message is the newest.
	if _, err := a.Core.StartNextChat(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SuggestNextMessage(ctx, after); !errors.Is(err, core.ErrConflict) {
		t.Fatal("running", err)
	}
	// A failed reply leaves the owner's message newest; there is nothing to follow.
	if err := a.Core.FinishChat(ctx, "two", "failed", "", "provider down"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SuggestNextMessage(ctx, after); !errors.Is(err, core.ErrConflict) {
		t.Fatal("failed", err)
	}
	if f.calls() != 0 {
		t.Fatal("unsettled conversation used a model", f.calls())
	}
}

func TestSuggestionDiscardedWhenTheConversationMovesOnDuringGeneration(t *testing.T) {
	a, _ := suggestionApp(t, "codex")
	after := settledReply(t, a, "one", "Hello", "Hi there.")
	a.small.complete = func(ctx context.Context, _ engine.Config, _ []engine.Message, _ []engine.Tool) (engine.Message, engine.Usage, error) {
		// The owner sends a message while the suggestion is being written.
		if _, err := a.Core.EnqueueChat(ctx, "two", "Actually, something else"); err != nil {
			t.Fatal(err)
		}
		return engine.Message{Content: "Tell me more."}, engine.Usage{}, nil
	}
	if got, err := a.SuggestNextMessage(context.Background(), after); !errors.Is(err, core.ErrConflict) || got != "" {
		t.Fatal(got, err)
	}
	// A request naming an older reply is stale too.
	b, _ := suggestionApp(t, "codex")
	older := settledReply(t, b, "one", "Hello", "Hi there.")
	settledReply(t, b, "two", "Thanks", "You're welcome.")
	if _, err := b.SuggestNextMessage(context.Background(), older); !errors.Is(err, core.ErrConflict) {
		t.Fatal(err)
	}
}

func TestSuggestionInvalidRepliesAndToolRequestsYieldNothing(t *testing.T) {
	history := []core.Message{{Role: "user", Content: "Hello"}, {Role: "assistant", Content: "Hi."}}
	for name, reply := range map[string]engine.Message{
		"tool":      {Content: "Sure", ToolCalls: []engine.ToolCall{{ID: "x", Type: "function"}}},
		"multiline": {Content: "one\ntwo"},
		"too long":  {Content: strings.Repeat("a", 281)},
		"control":   {Content: "bad\x07bell"},
	} {
		s := newFakeCLIs(t).models()
		s.complete = func(context.Context, engine.Config, []engine.Message, []engine.Tool) (engine.Message, engine.Usage, error) {
			return reply, engine.Usage{}, nil
		}
		if got, err := generateSuggestion(context.Background(), s, smallModelsFor(t, "codex"), history, nil); err == nil || got != "" {
			t.Fatal(name, got)
		}
	}
	f := newFakeCLIs(t)
	f.reply = "  "
	if got, err := generateSuggestion(context.Background(), f.models(), smallModelsFor(t, "codex"), history, nil); err != nil || got != "" {
		t.Fatal("no natural next message is not a failure", got, err)
	}
}
