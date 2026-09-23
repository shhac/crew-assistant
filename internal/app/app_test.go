package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
)

func testApp(t *testing.T) *App {
	t.Helper()
	cfg := config.Default()
	cfg.Model.Engine = "openai-compatible"
	cfg.Model.Effort = ""
	cfg.Model.Model = ""
	cfg.Assistant.Name = "Quill"
	cfg.Model.APIKeyEnv = ""
	s, err := core.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return New(core.NewService(s, cfg), cfg, filepath.Join(t.TempDir(), "config.json"), false)
}
func TestChatModelUsesConfiguredNameAndPersistsToolEffects(t *testing.T) {
	a := testApp(t)
	var calls atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"create_project","arguments":"{\"title\":\"Export\",\"objective\":\"Improve exports\",\"acceptance_criteria\":[\"CSV validates\"]}"}}]}}],"usage":{"prompt_tokens":12,"completion_tokens":8,"total_tokens":20}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"I recorded the outcome and acceptance criteria."}}],"usage":{"prompt_tokens":20,"completion_tokens":10,"total_tokens":30}}`))
	}))
	defer remote.Close()
	cfg := a.Config()
	cfg.Model.BaseURL = remote.URL + "/v1"
	cfg.Model.Model = "fixture-model"
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
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

func TestCodexAvailabilityDoesNotRequireAPIKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	cfg := config.Default()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Model.CodexBin = binary
	if !modelAvailable(cfg.Model) {
		t.Fatal("Codex incorrectly required API credentials")
	}
	cfg.Model.Engine = "openai-compatible"
	if modelAvailable(cfg.Model) {
		t.Fatal("API engine ignored missing configured credential")
	}
	cfg.Model.Engine = "codex"
	cfg.Model.CodexBin = filepath.Join(t.TempDir(), "missing-codex")
	if modelAvailable(cfg.Model) {
		t.Fatal("missing Codex binary reported available")
	}
}
