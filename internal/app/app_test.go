package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/diagnostics"
	"github.com/shhac/crew-assistant/internal/testutil"
)

func testApp(t *testing.T) *App {
	t.Helper()
	cfg := config.Default()
	cfg.Model.Engine = "openai-compatible"
	cfg.Model.Effort = ""
	cfg.Model.Model = ""
	cfg.Assistant.Name = "Quill"
	cfg.Engines.OpenAICompatible.APIKeyEnv = ""
	s, err := core.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return New(core.NewService(s, cfg), cfg, filepath.Join(t.TempDir(), "config.json"), Options{})
}
func TestChatModelUsesConfiguredNameAndPersistsToolEffects(t *testing.T) {
	a := testApp(t)
	var calls atomic.Int32
	remote := testutil.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Error(r.URL.Path)
		}
		var payload struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if !strings.Contains(payload.Messages[0].Content, "Quill") {
			t.Error("configured name absent from model prompt")
		}
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"create_project","arguments":"{\"title\":\"Export\",\"goal\":\"Improve exports\",\"audience\":\"\",\"constraints\":\"\",\"template\":\"draft\",\"criteria\":[\"CSV validates\"],\"directories\":null}"}}]}}],"usage":{"prompt_tokens":12,"completion_tokens":8,"total_tokens":20}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"I recorded the outcome and acceptance criteria."}}],"usage":{"prompt_tokens":20,"completion_tokens":10,"total_tokens":30}}`))
	}))
	defer remote.Close()
	cfg := a.Config()
	cfg.Engines.OpenAICompatible.BaseURL = remote.URL + "/v1"
	cfg.Model.Model = "fixture-model"
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	startTestQueue(t, a)
	result, err := a.Chat(context.Background(), "Set up the export project")
	if err != nil {
		t.Fatal(err)
	}
	if result.Usage.TotalTokens != 50 {
		t.Fatal(result.Usage)
	}
	snapshot, _ := a.Core.Snapshot(context.Background())
	if len(snapshot.Projects) != 1 || len(snapshot.Messages) != 2 {
		t.Fatalf("lost model effects: %+v", snapshot)
	}
}
func TestDemoCannotCallModel(t *testing.T) {
	a := testApp(t)
	a.Demo = true
	if _, err := a.Chat(context.Background(), "do work"); err == nil {
		t.Fatal("demo invoked model")
	}
}

func TestTheAppIsBuiltWhole(t *testing.T) {
	cfg := config.Default()
	s, err := core.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	logs := diagnostics.New(io.Discard)
	a := New(core.NewService(s, cfg), cfg, "", Options{Diagnostics: logs, DrawWithCodex: true})
	if a.Diagnostics != logs || a.Work.Diagnostics != logs || a.Painter == nil {
		t.Fatal("the app and its loop should share one log, and Codex should draw")
	}
	if demo := New(core.NewService(s, cfg), cfg, "", Options{Demo: true, DrawWithCodex: true}); demo.Painter != nil {
		t.Fatal("a demo drew with Codex")
	}
}

// A change to the config file, such as `crew-assistant config set`, reaches
// the running daemon without a restart.
func TestTheDaemonTakesOnAChangedConfigFile(t *testing.T) {
	a := testApp(t)
	cfg := a.Config()
	floor := 2
	cfg.Engines.Claude.UsageFloor.WeekPercent = &floor
	if err := config.Save(a.configPath, cfg); err != nil {
		t.Fatal(err)
	}
	if changed, err := a.ReloadConfig(); err != nil || !changed {
		t.Fatalf("changed %v, %v", changed, err)
	}
	if _, week := a.Config().Engines.Floors("claude"); week != 2 {
		t.Fatalf("weekly floor %d", week)
	}
	if changed, err := a.ReloadConfig(); err != nil || changed {
		t.Fatalf("an unchanged file changed something: %v %v", changed, err)
	}
	cfg.Slack.OwnerUserID = "U123"
	config.Save(a.configPath, cfg)
	if _, err := a.ReloadConfig(); err == nil || a.Config().Slack.OwnerUserID != "" {
		t.Fatal("a change that needs a restart was taken on")
	}
}
