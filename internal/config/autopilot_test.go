package config

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/shhac/crew-assistant/internal/autopilot"
)

func TestAutopilotPersistenceAndCheckedRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	original := Default()
	if err := Save(path, original); err != nil {
		t.Fatal(err)
	}
	for _, f := range autopilot.Catalog() {
		mode, err := original.Autopilot.EffectiveMode(f.ID)
		if err != nil || mode != f.DefaultMode {
			t.Fatalf("default %s: %s %v", f.ID, mode, err)
		}
	}
	var wg sync.WaitGroup
	for _, id := range []string{"authorised-research", "ci-failures"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if _, err := SetAutopilotMode(path, id, autopilot.Act, 0); err != nil {
				t.Error(err)
			}
		}(id)
	}
	wg.Wait()
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"authorised-research", "ci-failures"} {
		if loaded.Autopilot.Modes[id] != autopilot.Act {
			t.Fatalf("lost row %s", id)
		}
	}
	if err := Save(path, original); !errors.Is(err, ErrAutopilotConflict) {
		t.Fatalf("stale whole config: %v", err)
	}
	if _, err := SetAutopilotMode(path, "ci-failures", autopilot.Off, 0); !errors.Is(err, ErrAutopilotConflict) {
		t.Fatalf("stale row: %v", err)
	}
	if _, err := SetAutopilotMode(path, "ci-failures", "invalid", 1); err == nil {
		t.Fatal("invalid mode saved")
	}
	if _, err := SetAutopilotMode(path, "ci-failures", "", 1); err != nil {
		t.Fatal(err)
	}
	loaded, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	mode, _ := loaded.Autopilot.EffectiveMode("ci-failures")
	if mode != autopilot.Suggest {
		t.Fatalf("unset: %s", mode)
	}
}

func TestAutopilotMigrationPreservesChoicesAndFutureSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := `{"autopilot":{"modes":{"authorised-research":"off","ci-failures":"act","future-function":"suggest"}},"private_note":"keep"}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Autopilot.Modes["authorised-research"] != autopilot.Off || c.Autopilot.Modes["ci-failures"] != autopilot.Act {
		t.Fatal("explicit choice lost")
	}
	if _, err := SetAutopilotMode(path, "proposed-split", autopilot.Off, 0); err != nil {
		t.Fatal(err)
	}
	c, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Autopilot.Modes["future-function"] != autopilot.Suggest {
		t.Fatal("future choice lost")
	}
	if _, err := c.Autopilot.EffectiveMode("future-function"); err == nil {
		t.Fatal("unknown function enabled")
	}
}

func TestAutopilotCompetingOfflineWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	var wg sync.WaitGroup
	success := make(chan bool, 2)
	for _, mode := range []autopilot.Mode{autopilot.Off, autopilot.Act} {
		wg.Add(1)
		go func(mode autopilot.Mode) {
			defer wg.Done()
			_, err := SetAutopilotMode(path, "ci-failures", mode, 0)
			success <- err == nil
		}(mode)
	}
	wg.Wait()
	close(success)
	n := 0
	for ok := range success {
		if ok {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("successful stale writers: %d", n)
	}
}
