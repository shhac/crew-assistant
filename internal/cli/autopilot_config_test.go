package cli

import (
	"bytes"
	"github.com/gofrs/flock"
	"github.com/shhac/crew-assistant/internal/config"
	"os"
	"path/filepath"
	"testing"
)

func TestAutopilotProtectedSectionsWithDaemonLock(t *testing.T) {
	dir := t.TempDir()
	if _, err := runConfig(t, dir, "set", "autopilot.modes.ci-failures", "act"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lock := flock.New(filepath.Join(dir, "state.db.lock"))
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer lock.Unlock()
	for _, key := range []string{"autopilot", "autopilot.revisions", "autopilot.revisions.ci-failures", "autopilot.modes.future"} {
		if _, err := runConfig(t, dir, "unset", key); err == nil {
			t.Fatalf("protected deletion accepted: %s", key)
		}
	}
	if _, err := runConfig(t, dir, "unset", "autopilot.modes"); err == nil {
		t.Fatal("section reset bypassed daemon")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("rejected mutation changed disk", err)
	}
}

func TestAutopilotSectionResetAdvancesRevisions(t *testing.T) {
	dir := t.TempDir()
	if _, err := runConfig(t, dir, "set", "autopilot.modes.ci-failures", "act"); err != nil {
		t.Fatal(err)
	}
	if _, err := runConfig(t, dir, "unset", "autopilot.modes"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Autopilot.Revisions["ci-failures"] != 2 || cfg.Autopilot.Modes["ci-failures"] != "" {
		t.Fatal("reset lost revision fence")
	}
}

func TestAutopilotConfigKeysShareDefaultsAndNarrowUpdates(t *testing.T) {
	dir := t.TempDir()
	for _, test := range []struct{ id, mode string }{{"authorised-research", "suggest"}, {"landing-release-operator", "off"}} {
		got, err := runConfig(t, dir, "get", "autopilot.modes."+test.id)
		if err != nil || got["value"] != test.mode || got["set"] != false {
			t.Fatalf("default: %v %v", got, err)
		}
	}
	got, err := runConfig(t, dir, "set", "autopilot.modes.ci-failures", "act")
	if err != nil || got["value"] != "act" || got["set"] != true {
		t.Fatalf("set: %v %v", got, err)
	}
	got, err = runConfig(t, dir, "unset", "autopilot.modes.ci-failures")
	if err != nil || got["value"] != "suggest" || got["set"] != false {
		t.Fatalf("unset: %v %v", got, err)
	}
}
