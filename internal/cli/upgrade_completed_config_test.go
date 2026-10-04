package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/lifecycle"
	"github.com/shhac/crew-assistant/internal/upgrade"
)

func TestCompletedInstallFailureProtectsTargetProbationConfig(t *testing.T) {
	for _, version := range []string{"v2.0.0", "2.0.0", "v3.0.0"} {
		t.Run(version, func(t *testing.T) {
			dir := t.TempDir()
			state, original, moved := filepath.Join(dir, "state.db"), filepath.Join(dir, "config.json"), filepath.Join(dir, "moved.json")
			backups := upgrade.Backups{State: filepath.Join(dir, "old.db"), Config: filepath.Join(dir, "old.json")}
			for path, body := range map[string]string{state: "new state", original: "new config", moved: "moved config", backups.State: "old state", backups.Config: "old config"} {
				if err := os.WriteFile(path, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			binary := filepath.Join(dir, "previous")
			if err := os.WriteFile(binary, []byte("synthetic binary"), 0700); err != nil {
				t.Fatal(err)
			}
			r := upgrade.Record{Step: upgrade.InstallFailed, From: "v1.0.0", To: "v2.0.0", ConfigPath: original, SavedBinary: binary, Backups: backups}
			path := upgrade.RecordPath(state)
			if err := upgrade.WriteRecord(path, r); err != nil {
				t.Fatal(err)
			}
			stop, end := lifecycle.Watch(context.Background(), make(chan os.Signal), func(string) {})
			defer end()
			o := &options{statePath: state, configPath: moved, version: version, upgradeIdentity: func(int) string { return "fixture" }, upgradeStarter: func(string, []string, string) error { return nil }}
			if _, err := prepareUpgradeStart(o, stop, false); err == nil || !strings.Contains(err.Error(), "different config file") {
				t.Fatal(err)
			}
			got, err := upgrade.ReadRecord(path)
			if err != nil || got.Step != upgrade.InstallFailed || got.ConfigPath != original || got.ProbationStarts != 0 {
				t.Fatal(got, err)
			}
			if version == "v3.0.0" {
				return
			} // A different version also requires restoration.
			// Starting the target at the protected destination can safely fail
			// probation and restore it without touching the relocated config.
			o.configPath = original
			h, err := prepareUpgradeStart(o, stop, false)
			if err != nil {
				t.Fatal(err)
			}
			defer h.lock.Unlock()
			if !h.start.Probation {
				t.Fatal(h.start)
			}
			h.engine.Exec = func(string, []string) error { return errors.New("fake exec") }
			if err := h.engine.Fail("failed probation"); err == nil {
				t.Fatal("expected fake exec failure")
			}
			got, err = upgrade.ReadRecord(path)
			if err != nil || got.Step != upgrade.RolledBack || !got.Pinned || got.ConfigPath != original {
				t.Fatal(got, err)
			}
			for path, want := range map[string]string{state: "old state", original: "old config", moved: "moved config"} {
				body, err := os.ReadFile(path)
				if err != nil || string(body) != want {
					t.Fatal(path, string(body), err)
				}
			}
			if err := validateUpgradeStartConfig(*got, moved, r.From); err == nil {
				t.Fatal("relocated config bypassed rollback pin")
			}
		})
	}
}

func TestConfirmedFailureRecoveryAllowsConfigRelocation(t *testing.T) {
	for _, step := range []upgrade.Step{upgrade.BackupFailed, upgrade.InstallFailed} {
		t.Run(string(step), func(t *testing.T) {
			dir := t.TempDir()
			o := &options{statePath: filepath.Join(dir, "state.db"), configPath: filepath.Join(dir, "moved.json"), version: "v1.0.0"}
			path := upgrade.RecordPath(o.statePath)
			r := upgrade.Record{Step: step, From: o.version, To: "v2.0.0", ConfigPath: filepath.Join(dir, "original.json"), RestartPending: true}
			if err := upgrade.WriteRecord(path, r); err != nil {
				t.Fatal(err)
			}
			e := &upgrade.Engine{Path: path}
			if err := e.BeginRecovery(42, "fixture", nil, time.Second); err != nil {
				t.Fatal(err)
			}
			stop, end := lifecycle.Watch(context.Background(), make(chan os.Signal), func(string) {})
			defer end()
			if _, err := prepareUpgradeStart(o, stop, false); err == nil || !strings.Contains(err.Error(), "different config file") {
				t.Fatal(err)
			}
			if err := e.ConfirmRecovery(context.Background(), time.Second, func(context.Context) error { return nil }); err != nil {
				t.Fatal(err)
			}
			h, err := prepareUpgradeStart(o, stop, false)
			if err != nil {
				t.Fatal(err)
			}
			defer h.lock.Unlock()
			if h.start.Probation || h.start.Recovery {
				t.Fatal(h.start)
			}
		})
	}
}

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
		{upgrade.BackupFailed, false, true, false},
		{upgrade.BackupFailed, false, false, true},
		{upgrade.InstallFailed, false, false, true},
		{upgrade.BackupFailed, true, false, false},
		{upgrade.InstallFailed, true, false, false},
		{upgrade.RollingBack, false, false, false},
	} {
		t.Run(string(tc.step)+map[bool]string{true: "/allowed", false: "/protected"}[tc.allow], func(t *testing.T) {
			dir := t.TempDir()
			o := &options{statePath: filepath.Join(dir, "state.db"), configPath: filepath.Join(dir, "moved-config.json"), version: "v2.0.0"}
			r := upgrade.Record{Step: tc.step, From: "v1.0.0", To: o.version, ConfigPath: filepath.Join(dir, "original-config.json"), Pinned: tc.pinned, RestartPending: tc.pending}
			if tc.step == upgrade.BackupFailed || tc.step == upgrade.InstallFailed {
				o.version = r.From
			}
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
