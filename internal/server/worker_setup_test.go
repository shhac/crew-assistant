package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/app"
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/engine"
)

func TestCoordinateAsksForOutcomeAndKeepsProjectName(t *testing.T) {
	var received string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []engine.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		for _, m := range request.Messages {
			if m.Role == "user" {
				received = m.Content
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"What outcome would you like?"}}]}`))
	}))
	defer provider.Close()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Model.Engine, cfg.Model.BaseURL, cfg.Model.APIKeyEnv = "openai-compatible", provider.URL, ""
	store, err := core.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := core.NewService(store, cfg)
	project, err := service.CreateProject(context.Background(), core.ProjectInput{Title: "A [project]", Description: "An existing project", AcceptanceCriteria: "Agreed outcome"})
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(service, cfg, filepath.Join(dir, "config.json"), false)
	auth, err := NewAuth(dir, "http://127.0.0.1:8340", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := New(a, auth)
	call := func(path, body string, authorized bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://127.0.0.1:8340"+path, strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:4321"
		r.Header.Set("X-Requested-With", "crew-assistant")
		if authorized {
			r.Header.Set("Authorization", "Bearer "+auth.admin)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	path := "/api/projects/" + project.ID
	for _, endpoint := range []string{"/coordinate", "/worker"} {
		if w := call(path+endpoint, `{}`, false); w.Code != 401 {
			t.Fatal("anonymous setup allowed", w.Code)
		}
	}
	if w := call(path+"/coordinate", `{}`, true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if !strings.Contains(received, "[A \\[project\\]](#/projects/"+project.ID+")") || !strings.Contains(received, "Ask about the outcome before preparing or starting") {
		t.Fatal(received)
	}
	if w := call(path+"/coordinate", `{"next":"Improve keyboard navigation"}`, true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if !strings.HasSuffix(received, "What I want to do next: Improve keyboard navigation") {
		t.Fatal(received)
	}
	snapshot, err := service.Snapshot(context.Background())
	if err != nil || len(snapshot.Agents) != 0 {
		t.Fatal("discussion commissioned work", err)
	}
	if w := call(path+"/worker", `{}`, true); w.Code == 200 {
		t.Fatal("worker prepared without a linked folder")
	}
	if w := call("/api/projects/missing/coordinate", `{}`, true); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if w := call(path+"/coordinate", `{"next":"`+strings.Repeat("x", 12001)+`"}`, true); w.Code != 400 {
		t.Fatal(w.Code)
	}
}
