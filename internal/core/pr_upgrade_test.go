package core

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

// The upgrade keeps each pull-request policy as it behaved: one that asked
// no one opens without asking, one that asked the owner, or said nothing,
// asks the owner; and a policy that named no merge method names none.
func TestPullRequestPoliciesUpgradeAsTheyBehaved(t *testing.T) {
	for approve, open := range map[string]string{"none": OpenImplementer, "before": OpenOwner, "": OpenOwner} {
		land := map[string]any{"via": "pull-request", "target": "main", "github": "o/r"}
		if approve != "" {
			land["approve"] = approve
		}
		doc := map[string]any{"snapshot": map[string]any{"projects": []any{map[string]any{"playbook": map[string]any{"land": land}}}}}
		if err := pullRequestsToggle(doc); err != nil {
			t.Fatal(err)
		}
		if land["open"] != open || land["approve"] != ApproveNone || land["pull_requests"] != true || land["via"] != nil {
			t.Errorf("approve %q became %+v", approve, land)
		}
		if _, ok := land["merge"]; ok {
			t.Errorf("approve %q named a merge method it never had: %+v", approve, land)
		}
	}
}

// An upgrade that fails leaves the state as it was: no backup, no new schema.
func TestAFailedUpgradeLeavesTheStateAsItWas(t *testing.T) {
	saved := migrations
	t.Cleanup(func() { migrations = saved })
	migrations = map[int]func(map[string]any) error{stateSchema - 1: func(map[string]any) error { return errors.New("cannot") }}
	path := filepath.Join(t.TempDir(), "state.db")
	before := `{"schema":` + string(rune('0'+stateSchema-1)) + `,"snapshot":{}}`
	writeRawState(t, path, before)
	if _, err := Open(path); err == nil {
		t.Fatal("a failed upgrade opened")
	}
	if backups, _ := filepath.Glob(path + ".schema-*.bak"); len(backups) != 0 {
		t.Fatalf("backed up before failing: %v", backups)
	}
	db, _ := sql.Open("sqlite", path)
	defer db.Close()
	var payload string
	if err := db.QueryRowContext(context.Background(), "SELECT payload FROM state").Scan(&payload); err != nil || payload != before {
		t.Fatalf("state changed: %q %v", payload, err)
	}
}
