package app

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/integrations/connections"
)

func TestConnectionToolsRespectDemoAndStrictArgs(t *testing.T) {
	a := testApp(t)
	a.Demo = true
	a.connectionClient = connections.Client{Run: func(context.Context, string, []string) ([]byte, error) {
		t.Fatal("demo spawned a CLI")
		return nil, nil
	}}
	if _, err := a.Execute(context.Background(), "query_connection", json.RawMessage(`{"connection_id":"x"}`)); err == nil {
		t.Fatal("demo queried")
	}
	if _, err := a.Execute(context.Background(), "list_connections", json.RawMessage(`{"shell":"echo"}`)); err == nil {
		t.Fatal("unknown args accepted")
	}
	if _, err := a.DiscoverConnectionProfiles(context.Background(), "lin"); err != nil {
		t.Fatal(err)
	}
}

func TestLinearResourceDoesNotEnrollProjects(t *testing.T) {
	a := testApp(t)
	cfg := a.Config()
	cfg.Connections = []config.Connection{{ID: "work", Name: "Work context", Tool: "lin", Profiles: []string{"company"}}}
	// Even an enabled legacy configuration must not be used as a fallback
	// when an explicit CLI connection is query-only.
	cfg.Linear.ImportAssignments = true
	cfg.Linear.TeamIDs = []string{"legacy-team"}
	cfg.Linear.APIKeyEnv = "ASSISTANT_TEST_MISSING_LINEAR_KEY"
	t.Setenv(cfg.Linear.APIKeyEnv, "")
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	calls := 0
	a.connectionClient = connections.Client{Run: func(_ context.Context, _ string, argv []string) ([]byte, error) {
		calls++
		if argv[0] == "auth" {
			return []byte(`{"alias":"company"}`), nil
		}
		return []byte(`{"id":"issue-1","identifier":"EX-1","title":"Resource context"}`), nil
	}}
	if err := a.SyncLinear(context.Background()); err != nil {
		t.Fatal(err)
	}
	snap, err := a.Snapshot(context.Background())
	if err != nil || calls != 0 || len(snap.Projects) != 0 {
		t.Fatalf("sync queried or enrolled: calls=%d state=%+v err=%v", calls, snap, err)
	}
	// Query access remains available without granting automatic enrollment.
	if _, err := a.Execute(context.Background(), "query_connection", json.RawMessage(`{"connection_id":"work","profile":"company","operation":"assignments"}`)); err != nil {
		t.Fatal(err)
	}
	if calls == 0 {
		t.Fatal("explicit resource query did not run")
	}
	snap, err = a.Snapshot(context.Background())
	if err != nil || len(snap.Projects) != 0 {
		t.Fatal("query created projects", err)
	}
}

func TestAssignmentImportOnlyUsesOptedInConnectionAndStopsLive(t *testing.T) {
	a := testApp(t)
	cfg := a.Config()
	cfg.Connections = []config.Connection{
		{ID: "work", Name: "Work context", Tool: "lin", Profiles: []string{"company"}},
		{ID: "personal", Name: "Personal tracking", Tool: "lin", Profiles: []string{"personal"}, ImportAssignments: true},
	}
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	calls := 0
	a.connectionClient = connections.Client{Run: func(_ context.Context, _ string, argv []string) ([]byte, error) {
		calls++
		if argv[0] == "auth" {
			return []byte("{\"alias\":\"personal\"}\n{\"alias\":\"company\"}"), nil
		}
		if strings.Contains(strings.Join(argv, " "), "company") {
			t.Fatal("queried unrelated work account")
		}
		return []byte(`{"id":"issue-1","identifier":"EX-1","title":"Personal task","statusType":"started"}`), nil
	}}
	if err := a.SyncLinear(context.Background()); err != nil {
		t.Fatal(err)
	}
	snap, err := a.Snapshot(context.Background())
	if err != nil || len(snap.Projects) != 1 || !strings.HasPrefix(snap.Projects[0].SourceID, "lin:personal:personal:") {
		t.Fatal(snap, err)
	}
	before := calls
	cfg.Connections[1].ImportAssignments = false
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if err := a.SyncLinear(context.Background()); err != nil {
		t.Fatal(err)
	}
	snap, err = a.Snapshot(context.Background())
	if err != nil || calls != before || len(snap.Projects) != 1 {
		t.Fatal("disabled import queried or removed an existing project", calls, before, err)
	}
	for _, integration := range snap.Integrations {
		if strings.HasPrefix(integration.ID, "connection:") && !strings.Contains(integration.Detail, "assignment import off") {
			t.Fatal("stale import status", integration)
		}
	}
}

func TestAssignmentImportCountsOpenIssuesAndReportsEachConnection(t *testing.T) {
	a := testApp(t)
	cfg := a.Config()
	cfg.Connections = []config.Connection{
		{ID: "personal", Name: "Personal", Tool: "lin", Profiles: []string{"personal", "broken"}, ImportAssignments: true},
		{ID: "side", Name: "Side", Tool: "lin", Profiles: []string{"side"}, ImportAssignments: true},
	}
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	a.connectionClient = connections.Client{Run: func(_ context.Context, _ string, argv []string) ([]byte, error) {
		if argv[0] == "auth" {
			return []byte("{\"alias\":\"personal\"}\n{\"alias\":\"broken\"}\n{\"alias\":\"side\"}"), nil
		}
		workspace := argv[slices.Index(argv, "--workspace")+1]
		switch workspace {
		case "broken":
			return nil, errors.New("offline")
		case "side":
			return []byte(`{"id":"s1","identifier":"SD-1","title":"Side task","status":"Todo","statusType":"unstarted"}
{"id":"s2","identifier":"SD-2","title":"Done","statusType":"completed"}`), nil
		}
		return []byte(`{"id":"p1","identifier":"PE-1","title":"Open","status":"In Progress","statusType":"started"}
{"id":"p2","identifier":"PE-2","title":"Cancelled","statusType":"canceled"}
{"id":"p3","identifier":"PE-3","title":"","statusType":"started"}
{"id":"","identifier":"PE-4","title":"No id","statusType":"started"}
"not an issue"
{"id":"p5","identifier":"PE-5","title":"Also open","status":"Todo","statusType":"unstarted"}`), nil
	}}
	err := a.syncCLIConnections(context.Background())
	if err == nil || !strings.Contains(err.Error(), "lin read failed") {
		t.Fatal(err)
	}
	snap, err := a.Core.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sources := []string{}
	for _, p := range snap.Projects {
		sources = append(sources, p.SourceID)
	}
	slices.Sort(sources)
	if want := []string{"lin:personal:personal:p1", "lin:personal:personal:p5", "lin:side:side:s1"}; !slices.Equal(sources, want) {
		t.Fatal(sources)
	}
	a.mu.RLock()
	personal, side := a.statuses["connection:personal"], a.statuses["connection:side"]
	a.mu.RUnlock()
	if personal.Status != "error" || personal.Name != "Personal" || !strings.Contains(personal.Detail, "lin read failed") {
		t.Fatal(personal)
	}
	if side.Status != "connected" || side.Name != "Side" || side.Detail != "1 assigned issues" {
		t.Fatal(side)
	}
}

func TestLegacyLinearImportRequiresExplicitOptIn(t *testing.T) {
	a := testApp(t)
	cfg := a.Config()
	cfg.Linear.TeamIDs = []string{"legacy-team"}
	cfg.Linear.APIKeyEnv = "ASSISTANT_TEST_MISSING_LINEAR_KEY"
	t.Setenv(cfg.Linear.APIKeyEnv, "")
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if err := a.SyncLinear(context.Background()); err != nil {
		t.Fatal("disabled legacy import accessed credentials", err)
	}
	cfg.Linear.ImportAssignments = true
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if err := a.SyncLinear(context.Background()); err == nil {
		t.Fatal("opt-in did not attempt legacy adapter")
	}
}
