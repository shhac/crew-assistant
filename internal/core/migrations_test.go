package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func writeRawState(t *testing.T, path, payload string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TABLE state (id INTEGER PRIMARY KEY CHECK(id=1), payload TEXT NOT NULL, version INTEGER NOT NULL DEFAULT 1); INSERT INTO state(id,payload) VALUES(1,?)`, payload); err != nil {
		t.Fatal(err)
	}
}

func TestStateFromAnEarlierSchemaIsUpgradedOnceAndBackedUp(t *testing.T) {
	saved := migrations
	t.Cleanup(func() { migrations = saved })
	runs := 0
	migrations = map[int]func(map[string]any) error{stateSchema - 1: func(doc map[string]any) error {
		runs++
		snap := doc["snapshot"].(map[string]any)
		snap["paused"] = true
		return nil
	}}
	path := filepath.Join(t.TempDir(), "state.db")
	earlier := stateSchema - 1
	writeRawState(t, path, fmt.Sprintf(`{"schema":%d,"snapshot":{"projects":[],"tasks":[]},"events":{},"model_calls":{}}`, earlier))
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := s.Snapshot(context.Background())
	if err != nil || !snap.Paused {
		t.Fatalf("not upgraded: %v %v", snap.Paused, err)
	}
	s.Close()
	if s, err = Open(path); err != nil {
		t.Fatal(err)
	}
	s.Close()
	backups, _ := filepath.Glob(fmt.Sprintf("%s.schema-%d.*.bak", path, earlier))
	if runs != 1 || len(backups) != 1 {
		t.Fatalf("ran %d times, backups %v", runs, backups)
	}
	db, err := sql.Open("sqlite", backups[0])
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var payload string
	if err = db.QueryRow("SELECT payload FROM state").Scan(&payload); err != nil || !strings.Contains(payload, fmt.Sprintf(`"schema":%d`, earlier)) {
		t.Fatalf("backup %q %v", payload, err)
	}
}

func TestStateFromALaterSchemaIsRefusedUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	later := `{"schema":99,"snapshot":{}}`
	writeRawState(t, path, later)
	if _, err := Open(path); !errors.Is(err, ErrStateSchema) || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("opened: %v", err)
	}
	db, _ := sql.Open("sqlite", path)
	defer db.Close()
	var payload string
	if err := db.QueryRow("SELECT payload FROM state").Scan(&payload); err != nil || payload != later {
		t.Fatalf("state changed: %q %v", payload, err)
	}
}

func TestUpgradeStateKeepsNumbersExact(t *testing.T) {
	out, err := upgradeState([]byte(`{"schema":1,"n":12345678901234567}`), 1, 2, map[int]func(map[string]any) error{1: func(map[string]any) error { return nil }})
	if err != nil || !strings.Contains(string(out), "12345678901234567") || !strings.Contains(string(out), `"schema":2`) {
		t.Fatalf("%s %v", out, err)
	}
	if _, err = upgradeState([]byte(`{"schema":0}`), 0, 2, map[int]func(map[string]any) error{1: nil}); !errors.Is(err, ErrStateSchema) {
		t.Fatalf("a missing step was skipped: %v", err)
	}
}

func TestALandingPolicyByPullRequestBecomesOneWithPullRequestsOn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	pr := `{"means":"merged","via":"pull-request","target":"main","method":"rebase","github":"o/r","approve":"before"}`
	writeRawState(t, path, `{"schema":2,"snapshot":{"projects":[{"id":"p1","playbook":{"template":"code","medium":"git","roles":[],"deliver":"owner","land":`+pr+`}},{"id":"p2","playbook":{"land":{"via":"push","target":"main","method":"fast-forward"}}}],"tasks":[{"id":"t1","project_id":"p1","status":"awaiting","playbook":{"land":`+pr+`}}]},"events":{},"model_calls":{}}`)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	snap, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := LandPolicy{Means: "merged", Target: "main", PullRequests: true, GitHub: "o/r", Merge: "rebase", Approve: "before"}
	if got := snap.Projects[0].Playbook.Land; got != want || got.Way() != LandPullRequest || got.validate() != nil {
		t.Fatalf("project %+v %v", got, got.validate())
	}
	if got := snap.Tasks[0].Playbook.Land; got != want {
		t.Fatalf("task %+v", got)
	}
	if got := snap.Projects[1].Playbook.Land; got != (LandPolicy{Via: LandPush, Target: "main", Method: "fast-forward"}) {
		t.Fatalf("a push policy changed: %+v", got)
	}
}
