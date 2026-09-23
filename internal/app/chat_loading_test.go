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
	cfg := config.Default()
	cfg.Model.CodexHome = "/synthetic/shared-login"
	reserved := false
	discover := func(_ context.Context, c engine.Config) ([]engine.ModelOption, error) {
		if c.Engine != "codex" || c.Model != "gpt-5.6-luna" || c.CodexHome != cfg.Model.CodexHome {
			t.Fatal(c)
		}
		return []engine.ModelOption{{ID: "gpt-5.6-luna", Efforts: []engine.ModelEffort{{ID: "low"}}}}, nil
	}
	complete := func(ctx context.Context, c engine.Config, m []engine.Message, tools []engine.Tool) (engine.Message, engine.Usage, error) {
		if c.Model != "gpt-5.6-luna" || c.Effort != "low" || c.APIKeyEnv != "" || c.Endpoint != "" || len(tools) != 0 {
			t.Fatal(c, tools)
		}
		if len(m) != 2 || strings.Contains(m[1].Content, "old secret") || !strings.Contains(m[1].Content, "recent answer") || !strings.Contains(m[1].Content, "plan a garden") {
			t.Fatal(m)
		}
		if err := c.BeforeRequest(ctx); err != nil {
			return engine.Message{}, engine.Usage{}, err
		}
		return engine.Message{Content: "Planting the next idea…"}, engine.Usage{}, nil
	}
	phrase, err := generateLoadingPhrase(context.Background(), cfg, "plan a garden", []engine.Message{{Role: "user", Content: "old secret"}, {Role: "assistant", Content: "recent answer"}, {Role: "system", Content: "not dialogue"}}, discover, complete, func(context.Context) error { reserved = true; return nil })
	if err != nil || !reserved || phrase != "Planting the next idea…" {
		t.Fatal(phrase, err, reserved)
	}
}

func TestLoadingCaptionNeverFallsBackToAPIOrUnsupportedEffort(t *testing.T) {
	cfg := config.Default()
	cfg.Model.Engine = "claude"
	cfg.Model.ClaudeHome = "/synthetic/shared-claude"
	calls := 0
	discover := func(context.Context, engine.Config) ([]engine.ModelOption, error) {
		return []engine.ModelOption{{ID: "haiku"}}, nil
	}
	complete := func(_ context.Context, c engine.Config, _ []engine.Message, _ []engine.Tool) (engine.Message, engine.Usage, error) {
		calls++
		if c.Engine != "claude" || c.Model != "haiku" || c.Effort != "" || c.ClaudeHome != cfg.Model.ClaudeHome {
			t.Fatal(c)
		}
		return engine.Message{Content: "Gathering a little perspective"}, engine.Usage{}, nil
	}
	if _, err := generateLoadingPhrase(context.Background(), cfg, "hello", nil, discover, complete, nil); err != nil {
		t.Fatal(err)
	}
	cfg.Model.Engine = "openai-compatible"
	if _, err := generateLoadingPhrase(context.Background(), cfg, "hello", nil, discover, complete, nil); err == nil || calls != 1 {
		t.Fatal("API caption attempted", err, calls)
	}
	cfg.Model.Engine = "codex"
	cfg.Chat.LoadingPhrases.Enabled = false
	if _, err := generateLoadingPhrase(context.Background(), cfg, "hello", nil, discover, complete, nil); err == nil || calls != 1 {
		t.Fatal("disabled caption attempted")
	}
}

func TestLoadingCaptionFailureAndOutputBounds(t *testing.T) {
	cfg := config.Default()
	discover := func(context.Context, engine.Config) ([]engine.ModelOption, error) {
		return []engine.ModelOption{{ID: "gpt-5.6-luna"}}, nil
	}
	for _, bad := range []string{"", "two\nlines", "[click](https://example.test)", strings.Repeat("a", 71), "one two three four five six seven eight nine"} {
		complete := func(context.Context, engine.Config, []engine.Message, []engine.Tool) (engine.Message, engine.Usage, error) {
			return engine.Message{Content: bad}, engine.Usage{}, nil
		}
		if _, err := generateLoadingPhrase(context.Background(), cfg, "hello", nil, discover, complete, nil); err == nil {
			t.Fatal("accepted invalid caption", bad)
		}
	}
	calls := 0
	unavailable := func(context.Context, engine.Config) ([]engine.ModelOption, error) {
		return nil, errors.New("unavailable")
	}
	complete := func(context.Context, engine.Config, []engine.Message, []engine.Tool) (engine.Message, engine.Usage, error) {
		calls++
		return engine.Message{}, engine.Usage{}, nil
	}
	if _, err := generateLoadingPhrase(context.Background(), cfg, "hello", nil, unavailable, complete, nil); err == nil || calls != 0 {
		t.Fatal(err, calls)
	}
	higherOnly := func(context.Context, engine.Config) ([]engine.ModelOption, error) {
		return []engine.ModelOption{{ID: "gpt-5.6-luna", Efforts: []engine.ModelEffort{{ID: "high"}}}}, nil
	}
	if _, err := generateLoadingPhrase(context.Background(), cfg, "hello", nil, higherOnly, complete, nil); err == nil || calls != 0 {
		t.Fatal("silently increased effort", err, calls)
	}
	if len([]rune(clipLoadingContext(strings.Repeat("界", 1000)))) != 600 {
		t.Fatal("context not bounded")
	}
}

func TestCancelledTurnSkipsLoadingWork(t *testing.T) {
	a := testApp(t)
	a.cfg.Model = config.Default().Model
	a.loadingDiscover = func(context.Context, engine.Config) ([]engine.ModelOption, error) {
		t.Fatal("cancelled turn discovered models")
		return nil, nil
	}
	a.loadingComplete = func(context.Context, engine.Config, []engine.Message, []engine.Tool) (engine.Message, engine.Usage, error) {
		t.Fatal("cancelled turn used inference")
		return engine.Message{}, engine.Usage{}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a.startChatLoading(ctx, "cancelled", "hello", nil)
}
