package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/autopilot"
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
)

func TestAutopilotOwnerContracts(t *testing.T) {
	a, call := ownerApp(t)
	catalog := call("GET", "/api/autopilot", "")
	if catalog.Code != 200 {
		t.Fatal(catalog.Body.String())
	}
	var view struct {
		Functions []autopilot.Function `json:"functions"`
		Settings  autopilot.Settings   `json:"settings"`
	}
	if err := json.Unmarshal(catalog.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Functions) != 11 {
		t.Fatal("catalog mismatch")
	}
	for _, f := range view.Functions {
		if f.Available {
			t.Fatal("production function available")
		}
	}
	set := call("PUT", "/api/autopilot/modes/authorised-research", `{"mode":"act","revision":0}`)
	if set.Code != 200 {
		t.Fatal(set.Body.String())
	}
	stale := call("PUT", "/api/autopilot/modes/authorised-research", `{"mode":"off","revision":0}`)
	if stale.Code != 409 {
		t.Fatalf("stale row: %d %s", stale.Code, stale.Body.String())
	}
	// Reconcile admission after the refused stale edit.
	if _, err := a.ReloadConfig(); err != nil {
		t.Fatal(err)
	}
	if a.Config().Autopilot.Modes["authorised-research"] != autopilot.Act {
		t.Fatal("runtime mode was not applied")
	}
	if bad := call("PUT", "/api/autopilot/modes/ci-failures", `{"mode":"act","revision":0,"actor":"assistant"}`); bad.Code != 400 {
		t.Fatal("actor spoofing accepted")
	}
	p, err := a.Core.CreateProject(t.Context(), core.ProjectInput{Title: "Synthetic", Brief: core.BriefInput{Goal: "Test", Criteria: []string{"Test"}}, Template: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	permission := call("PUT", "/api/projects/"+p.ID+"/operator-permission", `{"allowed":true,"revision":0}`)
	if permission.Code != 200 {
		t.Fatal(permission.Body.String())
	}
	f, err := a.Autopilot.Register("authorised-research", "v1", []string{"rename-project"}, func(core.Snapshot, core.ConcreteAction) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	// Return to Suggest for an exact owner approval.
	set = call("PUT", "/api/autopilot/modes/authorised-research", `{"mode":"suggest","revision":1}`)
	if set.Code != 200 {
		t.Fatal(set.Body.String())
	}
	action, err := f.Submit(t.Context(), "source", "Clearer title", core.ConcreteAction{Kind: "rename-project", ProjectID: p.ID, Args: json.RawMessage(`{"title":"Renamed"}`)})
	if err != nil {
		t.Fatal(err)
	}
	pending := call("GET", "/api/autopilot/pending", "")
	if pending.Code != 200 || !strings.Contains(pending.Body.String(), action.ID) {
		t.Fatal("pending proposal absent")
	}
	path := "/api/autopilot/actions/" + action.ID
	if forged := call("POST", path+"/approve", `{"revision":1,"actor":"assistant"}`); forged.Code != 400 {
		t.Fatal("spoofed approval accepted")
	}
	if stale := call("POST", path+"/approve", `{"revision":2}`); stale.Code != 409 {
		t.Fatal("stale approval accepted")
	}
	approved := call("POST", path+"/approve", `{"revision":1}`)
	if approved.Code != 200 || !strings.Contains(approved.Body.String(), `"status":"performed"`) {
		t.Fatal(approved.Body.String())
	}
	if undo := call("POST", path+"/undo", `{"revision":1}`); undo.Code != 200 || !strings.Contains(undo.Body.String(), `"status":"undone"`) {
		t.Fatal(undo.Body.String())
	}
	history := call("GET", fmt.Sprintf("/api/autopilot/history?project_id=%s&forward=true&limit=2", p.ID), "")
	if history.Code != 200 || !strings.Contains(history.Body.String(), `"next"`) {
		t.Fatal(history.Body.String())
	}
	for _, query := range []string{"limit=201", "cursor=bad", "unexpected=true", "task_id=unknown", "forward=garbage", "forward=true&after=9223372036854775807"} {
		if bad := call("GET", "/api/autopilot/history?"+query, ""); bad.Code < 400 {
			t.Fatalf("invalid history accepted: %s", query)
		}
	}
}

func TestAutopilotEmptyMapsConfigRoundTrip(t *testing.T) {
	cfg := config.Default()
	cfg.Autopilot.Modes = map[string]autopilot.Mode{}
	cfg.Autopilot.Revisions = map[string]uint64{}
	a, auth, handler := newDashboard(t, cfg)
	body, err := json.Marshal(a.Config())
	if err != nil {
		t.Fatal(err)
	}
	// Seed the persisted document with explicit empty maps, which omitempty
	// removes from the ordinary owner API projection.
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	document["autopilot"] = map[string]any{"modes": map[string]any{}, "revisions": map[string]any{}}
	body, _ = json.Marshal(document)
	if response := send(handler, auth, "PUT", "/api/config", strings.NewReader(string(body)), asOwner); response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	response := send(handler, auth, "GET", "/api/config", strings.NewReader(""), asOwner)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	if saved := send(handler, auth, "PUT", "/api/config", strings.NewReader(response.Body.String()), asOwner); saved.Code != 200 {
		t.Fatal(saved.Code, saved.Body.String())
	}
}

func TestAutopilotRoutesRequireOwner(t *testing.T) {
	_, auth, h := newDashboard(t, config.Default())
	for _, route := range []struct{ method, path, body string }{
		{"GET", "/api/autopilot", ""}, {"GET", "/api/autopilot/history", ""}, {"GET", "/api/autopilot/pending", ""},
		{"PUT", "/api/autopilot/modes/ci-failures", `{"mode":"act","revision":0}`},
		{"PUT", "/api/projects/project/operator-permission", `{"allowed":true,"revision":0}`},
		{"POST", "/api/autopilot/actions/action/approve", `{"revision":1}`},
	} {
		response := send(h, auth, route.method, route.path, strings.NewReader(route.body), caller{})
		if response.Code != http.StatusUnauthorized && response.Code != http.StatusForbidden {
			t.Fatalf("%s %s: %d", route.method, route.path, response.Code)
		}
	}
}

func TestAutopilotReadsRejectMalformedQueriesAndHideStorageErrors(t *testing.T) {
	_, call := ownerApp(t)
	for _, path := range []string{"/api/autopilot/history", "/api/autopilot/pending"} {
		for _, query := range []string{"cursor=%ZZ", "project_id=%ZZ", "limit=1;after=2"} {
			if response := call("GET", path+"?"+query, ""); response.Code != 400 {
				t.Fatalf("%s?%s: %d %s", path, query, response.Code, response.Body.String())
			}
		}
	}
	_, auth, h := newDashboard(t, config.Default())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, path := range []string{"/api/autopilot/history", "/api/autopilot/pending", "/api/autopilot/actions/synthetic"} {
		r := httptest.NewRequest("GET", "http://127.0.0.1:8340"+path, nil).WithContext(ctx)
		r.RemoteAddr = "127.0.0.1:4321"
		r.Header.Set("Authorization", "Bearer "+auth.admin)
		response := httptest.NewRecorder()
		h.ServeHTTP(response, r)
		if response.Code != 500 || strings.Contains(response.Body.String(), "context") {
			t.Fatalf("storage failure leaked: %d %s", response.Code, response.Body.String())
		}
	}
}
