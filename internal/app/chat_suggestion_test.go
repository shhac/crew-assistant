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

func suggestionApp(t *testing.T, engineName string) *App {
	t.Helper()
	a := testApp(t)
	a.cfg.Model = config.Default().Model
	a.cfg.Model.Engine = engineName
	return a
}

func TestSuggestionRoutesToTheApprovedSmallModelPerEngine(t *testing.T) {
	for engineName, want := range map[string]string{"codex": "gpt-5.6-luna", "claude": "haiku"} {
		a := suggestionApp(t, engineName)
		after := settledReply(t, a, "one", "Plan the garden", "Here is a planting plan.")
		a.suggestionDiscover = func(_ context.Context, c engine.Config) ([]engine.ModelOption, error) {
			if c.Engine != engineName || c.Model != want {
				t.Fatal(c)
			}
			return []engine.ModelOption{{ID: want}}, nil
		}
		a.suggestionComplete = func(ctx context.Context, c engine.Config, m []engine.Message, tools []engine.Tool) (engine.Message, engine.Usage, error) {
			if c.Engine != engineName || c.Model != want || c.APIKeyEnv != "" || c.Endpoint != "" || len(tools) != 0 {
				t.Fatal(c, tools)
			}
			if len(m) != 2 || !strings.Contains(m[1].Content, "Owner: Plan the garden") || !strings.Contains(m[1].Content, "Assistant: Here is a planting plan.") {
				t.Fatal(m)
			}
			if err := c.BeforeRequest(ctx); err != nil {
				return engine.Message{}, engine.Usage{}, err
			}
			return engine.Message{Content: `"What should I plant first?"`}, engine.Usage{}, nil
		}
		got, err := a.SuggestNextMessage(context.Background(), after)
		if err != nil || got != "What should I plant first?" {
			t.Fatal(engineName, got, err)
		}
	}
}

func TestSuggestionEscalatesUnavailableOrUnmappedModelsWithoutInference(t *testing.T) {
	a := suggestionApp(t, "codex")
	after := settledReply(t, a, "one", "Hello", "Hi there.")
	calls := 0
	a.suggestionComplete = func(context.Context, engine.Config, []engine.Message, []engine.Tool) (engine.Message, engine.Usage, error) {
		calls++
		return engine.Message{Content: "Anything else?"}, engine.Usage{}, nil
	}
	// The login offers other models, but not the approved one.
	a.suggestionDiscover = func(context.Context, engine.Config) ([]engine.ModelOption, error) {
		return []engine.ModelOption{{ID: "gpt-6-astra"}, {ID: "gpt-5.6-terra"}}, nil
	}
	_, err := a.SuggestNextMessage(context.Background(), after)
	if !errors.Is(err, ErrSuggestionUnavailable) || !strings.Contains(err.Error(), "gpt-5.6-luna") || calls != 0 {
		t.Fatal(err, calls)
	}
	// An effort the model does not offer is not silently raised.
	a.suggestionDiscover = func(context.Context, engine.Config) ([]engine.ModelOption, error) {
		return []engine.ModelOption{{ID: "gpt-5.6-luna", Efforts: []engine.ModelEffort{{ID: "high"}}}}, nil
	}
	if _, err = a.SuggestNextMessage(context.Background(), after); !errors.Is(err, ErrSuggestionUnavailable) || calls != 0 {
		t.Fatal(err, calls)
	}
	a.cfg.Model.Engine = "openai-compatible"
	a.suggestionDiscover = func(context.Context, engine.Config) ([]engine.ModelOption, error) {
		t.Fatal("unmapped engine discovered models")
		return nil, nil
	}
	if _, err = a.SuggestNextMessage(context.Background(), after); !errors.Is(err, ErrSuggestionUnavailable) || calls != 0 {
		t.Fatal(err, calls)
	}
	a.cfg.Model.Engine = "codex"
	a.Demo = true
	if _, err = a.SuggestNextMessage(context.Background(), after); err == nil || calls != 0 {
		t.Fatal(err, calls)
	}
}

func TestSuggestionOnlyWhenTheConversationHasSettled(t *testing.T) {
	a := suggestionApp(t, "claude")
	a.suggestionDiscover = func(context.Context, engine.Config) ([]engine.ModelOption, error) {
		t.Fatal("unsettled conversation used a model")
		return nil, nil
	}
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
}

func TestSuggestionDiscardedWhenTheConversationMovesOnDuringGeneration(t *testing.T) {
	a := suggestionApp(t, "codex")
	after := settledReply(t, a, "one", "Hello", "Hi there.")
	a.suggestionDiscover = func(context.Context, engine.Config) ([]engine.ModelOption, error) {
		return []engine.ModelOption{{ID: "gpt-5.6-luna"}}, nil
	}
	a.suggestionComplete = func(ctx context.Context, _ engine.Config, _ []engine.Message, _ []engine.Tool) (engine.Message, engine.Usage, error) {
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
	b := suggestionApp(t, "codex")
	older := settledReply(t, b, "one", "Hello", "Hi there.")
	settledReply(t, b, "two", "Thanks", "You're welcome.")
	if _, err := b.SuggestNextMessage(context.Background(), older); !errors.Is(err, core.ErrConflict) {
		t.Fatal(err)
	}
}

func TestSuggestionFailuresAndToolRequestsYieldNothing(t *testing.T) {
	cfg := config.Default()
	model, err := cfg.SuggestionModel()
	if err != nil {
		t.Fatal(err)
	}
	history := []core.Message{{Role: "user", Content: "Hello"}, {Role: "assistant", Content: "Hi."}}
	discover := func(context.Context, engine.Config) ([]engine.ModelOption, error) {
		return []engine.ModelOption{{ID: "gpt-5.6-luna", Efforts: []engine.ModelEffort{{ID: "low"}}}}, nil
	}
	for name, reply := range map[string]engine.Message{
		"tool":      {Content: "Sure", ToolCalls: []engine.ToolCall{{ID: "x", Type: "function"}}},
		"multiline": {Content: "one\ntwo"},
		"too long":  {Content: strings.Repeat("a", 281)},
		"control":   {Content: "bad\x07bell"},
	} {
		complete := func(context.Context, engine.Config, []engine.Message, []engine.Tool) (engine.Message, engine.Usage, error) {
			return reply, engine.Usage{}, nil
		}
		if got, err := generateSuggestion(context.Background(), model, history, discover, complete, nil); err == nil || got != "" {
			t.Fatal(name, got)
		}
	}
	failing := func(context.Context, engine.Config, []engine.Message, []engine.Tool) (engine.Message, engine.Usage, error) {
		return engine.Message{}, engine.Usage{}, errors.New("provider unavailable")
	}
	if got, err := generateSuggestion(context.Background(), model, history, discover, failing, nil); err == nil || got != "" {
		t.Fatal(got, err)
	}
	empty := func(context.Context, engine.Config, []engine.Message, []engine.Tool) (engine.Message, engine.Usage, error) {
		return engine.Message{Content: "  "}, engine.Usage{}, nil
	}
	if got, err := generateSuggestion(context.Background(), model, history, discover, empty, nil); err != nil || got != "" {
		t.Fatal("no natural next message is not a failure", got, err)
	}
}
