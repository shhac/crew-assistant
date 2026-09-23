package app

import (
	"context"
	"encoding/json"
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
