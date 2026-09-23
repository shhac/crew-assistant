package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/app"
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
)

func TestWorkerDetailsRouteReturnsNamesAndRejectsCredentialEdits(t *testing.T) {
	cfg := config.Default()
	store, err := core.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := core.NewService(store, cfg)
	p, err := service.CreateProject(context.Background(), core.ProjectInput{Title: "Project"})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Workers = []config.Worker{{ID: "worker", Name: "Builder", ProjectID: p.ID, Workspace: "/fixture/workspace", Managed: true, Capabilities: []string{"implement"}}}
	if err := service.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	a := app.New(service, cfg, filepath.Join(t.TempDir(), "config.json"), false)
	mux := http.NewServeMux()
	workerDetailRoutes(mux, a)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/projects/"+p.ID+"/workers", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"Builder"`) || strings.Contains(w.Body.String(), "codex_home") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("PUT", "/api/projects/"+p.ID+"/workers/worker", strings.NewReader(`{"name":"Builder","engine":"codex","model":"test","effort":"high","codex_home":"/stolen/login"}`)))
	if w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
}
