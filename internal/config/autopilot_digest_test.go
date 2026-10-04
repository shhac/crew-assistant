package config

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/shhac/crew-assistant/internal/autopilot"
)

func TestDigestNarrowCASAndWholeConfigProtection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	original := Default()
	if original.Autopilot.DailyDigest.Enabled {
		t.Fatal("default digest enabled")
	}
	if err := Save(path, original); err != nil {
		t.Fatal(err)
	}
	saved, err := SetAutopilotDigest(path, autopilot.DailyDigest{Enabled: true, At: "08:00"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Autopilot.DailyDigest.Revision != 1 {
		t.Fatal("revision did not advance")
	}
	if err := Save(path, original); !errors.Is(err, ErrAutopilotConflict) {
		t.Fatal("whole-config save overwrote digest")
	}
	if _, err := SetAutopilotDigest(path, autopilot.DailyDigest{}, 0); !errors.Is(err, ErrAutopilotConflict) {
		t.Fatal("stale digest CAS accepted")
	}
	if _, err := SetAutopilotDigest(path, autopilot.DailyDigest{At: "8:00"}, 1); err == nil {
		t.Fatal("invalid digest time accepted")
	}
	if _, err := SetAutopilotMode(path, "ci-failures", autopilot.Act, 0); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil || loaded.Autopilot.DailyDigest != saved.Autopilot.DailyDigest {
		t.Fatal("mode update overwrote digest")
	}
}
