package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/engine"
)

func TestLoadingCaptionUsesSmallCLIWithOnlyRecentContext(t *testing.T) {
	f := newFakeCLIs(t)
	s := f.models()
	complete := s.complete
	s.complete = func(ctx context.Context, c engine.Config, m []engine.Message, tools []engine.Tool) (engine.Message, engine.Usage, error) {
		if c.APIKeyEnv != "" || c.Endpoint != "" || c.CodexHome != config.Default().Model.CodexHome {
			t.Fatal(c)
		}
		if len(m) != 2 || strings.Contains(m[1].Content, "old secret") || !strings.Contains(m[1].Content, "recent answer") || !strings.Contains(m[1].Content, "plan a garden") {
			t.Fatal(m)
		}
		if err := c.BeforeRequest(ctx); err != nil {
			return engine.Message{}, engine.Usage{}, err
		}
		return complete(ctx, c, m, tools)
	}
	reserved := false
	phrase, err := generateLoadingPhrase(context.Background(), s, smallModelsFor(t, "codex"), "plan a garden", []engine.Message{{Role: "user", Content: "old secret"}, {Role: "assistant", Content: "recent answer"}, {Role: "system", Content: "not dialogue"}}, func(context.Context) error { reserved = true; return nil })
	if err != nil || !reserved || phrase != "Planting the next idea" {
		t.Fatal(phrase, err, reserved)
	}
	if !equalStrings(f.used(), []string{"codex/gpt-6-luna/low"}) {
		t.Fatal(f.used())
	}
}

func TestLoadingCaptionFallsBackToTheOtherCLI(t *testing.T) {
	f := newFakeCLIs(t)
	f.discoverErr["claude"] = errors.New("claude: not logged in")
	phrase, err := generateLoadingPhrase(context.Background(), f.models(), smallModelsFor(t, "claude"), "hello", nil, nil)
	if err != nil || phrase == "" || !equalStrings(f.used(), []string{"codex/gpt-6-luna/low"}) {
		t.Fatal(phrase, err, f.used())
	}
	f = newFakeCLIs(t)
	f.replyErr["codex"] = errors.New("usage limit reached")
	phrase, err = generateLoadingPhrase(context.Background(), f.models(), smallModelsFor(t, "codex"), "hello", nil, nil)
	if err != nil || phrase == "" || !equalStrings(f.used(), []string{"codex/gpt-6-luna/low", "claude/haiku/"}) {
		t.Fatal(phrase, err, f.used())
	}
}

func TestLoadingCaptionOutputBounds(t *testing.T) {
	for _, bad := range []string{"", "two\nlines", "[click](https://example.test)", strings.Repeat("a", 71), "one two three four five six seven eight nine"} {
		f := newFakeCLIs(t)
		f.reply = bad
		if _, err := generateLoadingPhrase(context.Background(), f.models(), smallModelsFor(t, "codex"), "hello", nil, nil); err == nil {
			t.Fatal("accepted invalid caption", bad)
		}
	}
	if len([]rune(clipLoadingContext(strings.Repeat("界", 1000)))) != 600 {
		t.Fatal("context not bounded")
	}
}

func TestLoadingCaptionSkipsAPIAssistantsAndDisabledCaptions(t *testing.T) {
	for _, change := range []func(*App){
		func(a *App) { a.cfg.Model.Engine = "openai-compatible" },
		func(a *App) { a.cfg.Chat.LoadingPhrases.Enabled = false },
	} {
		a := testApp(t)
		a.cfg.Model = config.Default().Model
		change(a)
		f := newFakeCLIs(t)
		a.small = f.models()
		a.startChatLoading(context.Background(), "turn", "hello", nil)
		if f.calls() != 0 {
			t.Fatal("caption attempted", f.calls())
		}
	}
}

func TestLoadingCaptionWithBothCLIsFailingLeavesTheTurnAlone(t *testing.T) {
	a := testApp(t)
	a.cfg.Model = config.Default().Model
	f := newFakeCLIs(t)
	f.discoverErr["codex"] = errors.New("codex: not installed")
	f.replyErr["claude"] = errors.New("usage limit reached")
	a.small = f.models()
	ctx := context.Background()
	if _, err := a.Core.EnqueueChat(ctx, "one", "Plan the garden"); err != nil {
		t.Fatal(err)
	}
	turn, err := a.Core.StartNextChat(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a.startChatLoading(ctx, turn.ID, turn.Message, nil)
	turns, _ := a.Core.ChatTurns(ctx)
	if turns[0].LoadingPhrase != "" || turns[0].Status != "running" {
		t.Fatal(turns[0])
	}
	// The next message spends nothing on either resting CLI.
	before := f.calls()
	a.startChatLoading(ctx, turn.ID, turn.Message, nil)
	if f.calls() != before {
		t.Fatal("rested CLIs were tried again", f.calls()-before)
	}
	// The reply itself is unaffected.
	if err := a.Core.FinishChat(ctx, turn.ID, "completed", "Here is a plan.", ""); err != nil {
		t.Fatal(err)
	}
}

func TestCancelledTurnSkipsLoadingWork(t *testing.T) {
	a := testApp(t)
	a.cfg.Model = config.Default().Model
	f := newFakeCLIs(t)
	a.small = f.models()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a.startChatLoading(ctx, "cancelled", "hello", nil)
	if f.calls() != 0 {
		t.Fatal("cancelled turn used a model", f.calls())
	}
}
