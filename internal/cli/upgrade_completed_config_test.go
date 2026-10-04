package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/lifecycle"
	"github.com/shhac/crew-assistant/internal/upgrade"
)

func TestCompletedUpgradeAllowsNewConfigAndRecoveryStillRefuses(t *testing.T) {
	for _, tc := range []struct {
		step                   upgrade.Step
		pinned, pending, allow bool
	}{
		{upgrade.Healthy, false, false, true},
		{upgrade.Abandoned, false, false, true},
		{upgrade.Abandoned, true, false, false},
		{upgrade.Probation, false, false, false},
		{upgrade.RollingBack, true, true, false},
		{upgrade.InstallFailed, false, true, false},
	} {
		t.Run(string(tc.step)+map[bool]string{true: "/allowed", false: "/protected"}[tc.allow], func(t *testing.T) {
			dir := t.TempDir()
			o := &options{statePath: filepath.Join(dir, "state.db"), configPath: filepath.Join(dir, "moved-config.json"), version: "v2.0.0"}
			r := upgrade.Record{Step: tc.step, From: "v1.0.0", To: o.version, ConfigPath: filepath.Join(dir, "original-config.json"), Pinned: tc.pinned, RestartPending: tc.pending}
			if err := upgrade.WriteRecord(upgrade.RecordPath(o.statePath), r); err != nil {
				t.Fatal(err)
			}
			stop, end := lifecycle.Watch(context.Background(), make(chan os.Signal), func(string) {})
			defer end()
			h, err := prepareUpgradeStart(o, stop, false)
			if tc.allow {
				if err != nil {
					t.Fatal(err)
				}
				defer h.lock.Unlock()
				if h.start.Probation || h.start.Recovery {
					t.Fatal(h.start)
				}
			} else if err == nil || !strings.Contains(err.Error(), "different config file") {
				t.Fatal(err)
			}
		})
	}
}

func TestStartupRefusesMissingIdentityBeforeOpeningState(t *testing.T) {
	dir := t.TempDir()
	o := &options{statePath: filepath.Join(dir, "state.db"), configPath: filepath.Join(dir, "config.json"), version: "v2.0.0", upgradeIdentity: func(int) string { return "" }}
	r := upgrade.Record{Step: upgrade.HandingOver, From: "v1.0.0", To: o.version, PID: 42, ProcessIdentity: "sender"}
	if err := upgrade.WriteRecord(upgrade.RecordPath(o.statePath), r); err != nil {
		t.Fatal(err)
	}
	stop, end := lifecycle.Watch(context.Background(), make(chan os.Signal), func(string) {})
	defer end()
	if _, err := prepareUpgradeStart(o, stop, false); err == nil || !strings.Contains(err.Error(), "process identity") {
		t.Fatal(err)
	}
	got, err := upgrade.ReadRecord(upgrade.RecordPath(o.statePath))
	if err != nil || got.Step != r.Step || got.ProcessIdentity != r.ProcessIdentity || got.PID != r.PID {
		t.Fatal(got, err)
	}
	if _, err := os.Stat(o.statePath); !os.IsNotExist(err) {
		t.Fatal("state opened without reliable identity", err)
	}
}

func TestInitialIdentityPublicationRetriesTransientInspection(t *testing.T) {
	for _, recover := range []bool{false, true} {
		calls := 0
		o := &options{upgradeIdentity: func(int) string {
			calls++
			if recover && calls == 2 {
				return "birth"
			}
			return ""
		}}
		identity := confirmedUpgradeIdentity(o)
		if recover {
			if identity != "birth" || calls != 2 {
				t.Fatal(identity, calls)
			}
		} else if identity != "" || calls != 3 {
			t.Fatal(identity, calls)
		}
	}
}
