package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/app"
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
)

func TestDashboardProjectDecisionMemoryFlow(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	store, err := core.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s := core.NewService(store, cfg)
	a := app.New(s, cfg, filepath.Join(dir, "config.json"), false)
	auth, _ := NewAuth(dir, "http://127.0.0.1:8340", "", nil)
	h := New(a, auth)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:8340"+path, strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:4321"
		r.Header.Set("Authorization", "Bearer "+auth.admin)
		r.Header.Set("X-Requested-With", "crew-assistant")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	created := call("POST", "/api/projects", `{"title":"Export","description":"CSV","acceptance_criteria":"Valid CSV"}`)
	if created.Code != 201 {
		t.Fatal(created.Body.String())
	}
	var project core.Project
	_ = json.Unmarshal(created.Body.Bytes(), &project)
	d, err := s.CreateDecision(context.Background(), core.DecisionInput{ProjectID: project.ID, Title: "CSV first?", Context: "Excel adds effort", Recommendation: "CSV first", Choices: []string{"CSV", "Excel"}})
	if err != nil {
		t.Fatal(err)
	}
	if w := call("POST", "/api/decisions/"+d.ID+"/resolve", `{"choice":"CSV"}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := call("POST", "/api/decisions/"+d.ID+"/resolve", `{"choice":"Excel"}`); w.Code != 409 {
		t.Fatal("repeated decision overwrote answer")
	}

	custom, _ := s.CreateDecision(context.Background(), core.DecisionInput{Title: "Custom?", Context: "Options incomplete", Recommendation: "One", Choices: []string{"One", "Two"}})
	if w := call("POST", "/api/decisions/"+custom.ID+"/resolve", `{"choice":"One","answer":"Other"}`); w.Code != 400 {
		t.Fatal("ambiguous answer accepted", w.Code)
	}
	if w := call("POST", "/api/decisions/"+custom.ID+"/resolve", `{"answer":"Use the existing option"}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"disposition":"custom"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	stale, _ := s.CreateDecision(context.Background(), core.DecisionInput{Title: "Stale?", Context: "Already resolved elsewhere", Recommendation: "One", Choices: []string{"One", "Two"}})
	if w := call("POST", "/api/decisions/"+stale.ID+"/dismiss", `{"reason":""}`); w.Code != 400 {
		t.Fatal("blank reason accepted")
	}
	if w := call("POST", "/api/decisions/"+stale.ID+"/dismiss", `{"reason":"Already configured"}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"dismissed"`) || strings.Contains(w.Body.String(), `"answer"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	memory := call("POST", "/api/memories", `{"content":"Prefer concise updates"}`)
	if memory.Code != 201 {
		t.Fatal(memory.Body.String())
	}
	var m core.Memory
	_ = json.Unmarshal(memory.Body.Bytes(), &m)
	if w := call("DELETE", "/api/memories/"+m.ID, ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := call("GET", "/api/state", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "resolved") {
		t.Fatal(w.Body.String())
	}
	if w := call("GET", "/", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "<html") {
		t.Fatal("embedded UI missing")
	}
}

// The dashboard receives execution health alongside the state it is already
// showing, so a stopped worker is visible without opening each project, and a
// quiet decision queue never stands in for a healthy one.
func TestCorrectMemoryReportsRefusalsToTheOwner(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	store, err := core.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s := core.NewService(store, cfg)
	a := app.New(s, cfg, filepath.Join(dir, "config.json"), false)
	auth, _ := NewAuth(dir, "http://127.0.0.1:8340", "", nil)
	h := New(a, auth)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:8340"+path, strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:4321"
		r.Header.Set("Authorization", "Bearer "+auth.admin)
		r.Header.Set("X-Requested-With", "crew-assistant")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	created := call("POST", "/api/memories", `{"content":"Worker model information is unavailable.","kind":"observation"}`)
	if created.Code != 201 {
		t.Fatal(created.Body.String())
	}
	var memory core.Memory
	_ = json.Unmarshal(created.Body.Bytes(), &memory)
	if memory.Source != "owner" {
		t.Fatalf("source = %q, want owner for a memory the owner recorded", memory.Source)
	}

	corrected := call("POST", "/api/memories/"+memory.ID+"/correct", `{"content":"The project worker runs Opus 5."}`)
	if corrected.Code != 201 {
		t.Fatal(corrected.Body.String())
	}

	again := call("POST", "/api/memories/"+memory.ID+"/correct", `{"content":"Third attempt."}`)
	if again.Code != 409 {
		t.Fatalf("correcting an already-corrected memory returned %d, want 409", again.Code)
	}
	missing := call("POST", "/api/memories/does-not-exist/correct", `{"content":"Anything."}`)
	if missing.Code != 404 {
		t.Fatalf("correcting an unknown memory returned %d, want 404", missing.Code)
	}
	empty := call("POST", "/api/memories/"+memory.ID+"/correct", `{"content":"  "}`)
	if empty.Code != 400 {
		t.Fatalf("an empty correction returned %d, want 400", empty.Code)
	}

	var state struct {
		Memories []core.Memory `json:"memories"`
	}
	_ = json.Unmarshal(call("GET", "/api/state", "").Body.Bytes(), &state)
	if len(state.Memories) != 2 {
		t.Fatalf("expected the original and its replacement, got %d", len(state.Memories))
	}
}

// The dashboard downloads an artifact by token. The route must stay behind
// owner access, must not accept a path, and must not let the name segment
// steer resolution.
