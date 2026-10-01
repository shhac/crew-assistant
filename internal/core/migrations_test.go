package core

import (
	"context"
	"database/sql"
	"errors"
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
	writeRawState(t, path, `{"schema":1,"snapshot":{"projects":[],"tasks":[],"model_calls":{"big":12345678901234567}},"events":{},"model_calls":{}}`)
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
	backups, _ := filepath.Glob(path + ".schema-1.*.bak")
	if runs != 1 || len(backups) != 1 {
		t.Fatalf("ran %d times, backups %v", runs, backups)
	}
	db, err := sql.Open("sqlite", backups[0])
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var payload string
	if err = db.QueryRow("SELECT payload FROM state").Scan(&payload); err != nil || !strings.Contains(payload, `"schema":1`) {
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
