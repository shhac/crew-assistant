package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
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
func TestStateExposesBlockedWorkWithNoOpenDecisions(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Workers = []config.Worker{{ID: "test", Name: "Suggestions worker", Endpoint: "http://127.0.0.1:9999", Capabilities: []string{"implement"}}}
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
	attention := func() []core.ProjectAttention {
		t.Helper()
		var out struct {
			Attention []core.ProjectAttention `json:"attention"`
			Decisions []core.Decision         `json:"decisions"`
		}
		if err := json.Unmarshal(call("GET", "/api/state", "").Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		for _, d := range out.Decisions {
			if d.Status == "open" {
				t.Fatalf("fixture unexpectedly has an open decision: %+v", d)
			}
		}
		return out.Attention
	}

	created := call("POST", "/api/projects", `{"title":"Suggestions","description":"Suggest","acceptance_criteria":"Reviewed"}`)
	if created.Code != 201 {
		t.Fatal(created.Body.String())
	}
	var project core.Project
	_ = json.Unmarshal(created.Body.Bytes(), &project)
	for _, item := range attention() {
		if item.NextAction == "owner" {
			t.Fatalf("a fresh project should not demand the owner: %+v", item)
		}
	}

	ctx := context.Background()
	agent, err := s.Delegate(ctx, core.DelegateInput{ProjectID: project.ID, ProfileID: "test", Role: "worker", Task: "Draft suggestions", AcceptanceCriteria: "Reviewed", Capabilities: []string{"implement"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginDispatch(ctx, agent.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkDispatched(ctx, agent.ID, "external-"+agent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateAgent(ctx, agent.ID, core.AgentUpdate{Status: "blocked", Summary: "The attempt stopped without a classified provider error.", ProviderFailureKind: "unknown", ModelFailureEvidence: "untyped_error"}); err != nil {
		t.Fatal(err)
	}

	got := attention()
	if len(got) == 0 {
		t.Fatal("no attention reported while a worker is blocked")
	}
	if got[0].Execution != "blocked" {
		t.Fatalf("execution = %q, want blocked", got[0].Execution)
	}
	if got[0].NextAction != "owner" || got[0].OpenDecisions != 0 {
		t.Fatalf("blocked work with no open decision must still be the owner's turn: %+v", got[0])
	}
	if got[0].AgentID != agent.ID || got[0].AgentName != "Suggestions worker" {
		t.Fatalf("attention does not name the affected worker: %+v", got[0])
	}
	if got[0].Reason == "" {
		t.Fatal("attention reports no reason for the blocker")
	}
}

// Correcting a memory is an owner-visible mutation on the daemon's only record
// of what it believes, so a refusal has to reach the owner rather than looking
// like success.
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
func TestArtifactDownloadRequiresAccessAndAToken(t *testing.T) {
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

	artifacts := filepath.Join(s.StateDirectory(), "managed-workers", "p1", "broker", "runs", "run-1", "artifacts")
	if err := os.MkdirAll(artifacts, 0o700); err != nil {
		t.Fatal(err)
	}
	patch := filepath.Join(artifacts, "changes.patch")
	if err := os.WriteFile(patch, []byte("diff --git a/a b/a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	allLinks, err := a.ArtifactLinks(core.Snapshot{Agents: []core.Agent{{ID: "a1", Evidence: []string{"Patch: " + patch}}}})
	if err != nil {
		t.Fatal(err)
	}
	token := allLinks[patch]
	if token == "" {
		t.Fatal("no token minted")
	}

	get := func(path string, authorized bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "http://127.0.0.1:8340"+path, nil)
		r.RemoteAddr = "127.0.0.1:4321"
		if authorized {
			r.Header.Set("Authorization", "Bearer "+auth.admin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	// Without owner access the route is closed like the rest of the API.
	if got := get("/api/artifacts/"+token+"/changes.patch", false); got.Code != 401 {
		t.Fatalf("unauthenticated download returned %d, want 401", got.Code)
	}

	// The daemon has no agent recording this evidence, so the link is not live.
	if got := get("/api/artifacts/"+token+"/changes.patch", true); got.Code != 404 {
		t.Fatalf("a token without live evidence returned %d, want 404", got.Code)
	}

	// With the evidence actually recorded, the same token downloads the file.
	cfg.Workers = []config.Worker{{ID: "test", Name: "Builder", Endpoint: "http://127.0.0.1:9999", Capabilities: []string{"implement"}}}
	if err := s.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	project, err := s.CreateProject(ctx, core.ProjectInput{Title: "Artifacts", Description: "d", AcceptanceCriteria: "c"})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := s.Delegate(ctx, core.DelegateInput{ProjectID: project.ID, ProfileID: "test", Role: "worker", Task: "Build", AcceptanceCriteria: "Reviewed", Capabilities: []string{"implement"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginDispatch(ctx, agent.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkDispatched(ctx, agent.ID, "external-"+agent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateAgent(ctx, agent.ID, core.AgentUpdate{Status: "completed", Summary: "done", Evidence: []string{"Patch: " + patch}}); err != nil {
		t.Fatal(err)
	}

	ok := get("/api/artifacts/"+token+"/changes.patch", true)
	if ok.Code != 200 {
		t.Fatalf("download returned %d: %s", ok.Code, ok.Body.String())
	}
	if ok.Body.String() != "diff --git a/a b/a\n" {
		t.Fatalf("served %q", ok.Body.String())
	}
	if ct := ok.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("content type = %q; a served artifact must not be rendered inline", ct)
	}
	if cd := ok.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Fatalf("content disposition = %q, want an attachment", cd)
	}

	// The trailing segment is a download name; it cannot select another file.
	if got := get("/api/artifacts/"+token+"/anything-else.txt", true); got.Code != 200 {
		t.Fatalf("name segment affected resolution: %d", got.Code)
	}
	// A path is not a token.
	for _, bad := range []string{"/api/artifacts/" + patch + "/changes.patch", "/api/artifacts/..%2f..%2fetc%2fpasswd/x", "/api/artifacts/deadbeef/changes.patch"} {
		if got := get(bad, true); got.Code == 200 {
			t.Fatalf("%s was served", bad)
		}
	}
}
