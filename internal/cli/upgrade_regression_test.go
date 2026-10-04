package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/shhac/crew-assistant/internal/lifecycle"
	"github.com/shhac/crew-assistant/internal/upgrade"
	libcli "github.com/shhac/lib-agent-cli/cli"
)

func TestUpgradeLaunchdRequiresOwnJob(t *testing.T) {
	for _, tc := range []struct {
		label  string
		parent int
		want   string
	}{
		{"0", 1, ""}, {"0", 42, ""}, {"application.com.apple.Terminal.42", 1, ""}, {"application.com.iterm2.42", 42, ""}, {"fixture.crew", 42, ""}, {"fixture.crew", 1, "fixture.crew"}, {"", 1, ""},
	} {
		if got := upgradeLaunchdLabel(tc.label, tc.parent); got != tc.want {
			t.Fatal(tc, got)
		}
	}
}
func TestFirstSignalDoesNotCancelInstaller(t *testing.T) {
	signals := make(chan os.Signal, 2)
	stop, end := lifecycle.Watch(context.Background(), signals, func(string) {})
	defer end()
	stop.Drain("upgrade")
	ctx, cancel := upgradeInstallContext(stop)
	defer cancel()
	signals <- lifecycle.Signals[0]
	deadline := time.Now().Add(time.Second)
	for stop.Reason() != "signal" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if stop.Reason() != "signal" {
		t.Fatal("signal not handled")
	}
	select {
	case <-ctx.Done():
		t.Fatal("first signal cancelled admitted install")
	case <-time.After(100 * time.Millisecond):
	}
	signals <- lifecycle.Signals[0]
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("force did not cancel install")
	}
}
func TestOfflineRecoveryRestoresRetainedBackupsAndRefusesUnsafeCases(t *testing.T) {
	for _, tc := range []string{"success", "live", "existing", "bad-backup"} {
		t.Run(tc, func(t *testing.T) {
			dir := t.TempDir()
			o := &options{globals: &libcli.Globals{}, statePath: filepath.Join(dir, "state.db"), configPath: filepath.Join(dir, "config.json")}
			backup := upgrade.Backups{State: filepath.Join(dir, "old.db"), Config: filepath.Join(dir, "old.json")}
			for path, body := range map[string]string{o.statePath: "new state", o.configPath: "new config", o.statePath + "-wal": "wal", o.statePath + "-shm": "shm", backup.State: "old state", backup.Config: "old config"} {
				if err := os.WriteFile(path, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			r := upgrade.Record{Step: upgrade.Abandoned, Pinned: true, SavedBinary: filepath.Join(dir, "missing"), PinBinary: filepath.Join(dir, "old-missing"), PinBackups: backup, Backups: upgrade.Backups{State: "bad", Config: "bad"}}
			if tc == "existing" {
				if err := os.WriteFile(r.PinBinary, []byte("binary"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if tc == "bad-backup" {
				os.Remove(backup.Config)
			}
			if err := upgrade.WriteRecord(upgrade.RecordPath(o.statePath), r); err != nil {
				t.Fatal(err)
			}
			lock := flock.New(o.statePath + ".lock")
			if tc == "live" {
				if err := lock.Lock(); err != nil {
					t.Fatal(err)
				}
				defer lock.Unlock()
			}
			err := clearMissingRollbackBinary(o)
			if tc == "success" {
				// Emission may require the CLI writer; restoration must precede emission.
				got, _ := upgrade.ReadRecord(upgrade.RecordPath(o.statePath))
				if got.Pinned || got.Step != upgrade.Abandoned {
					t.Fatal(got, err)
				}
				for path, want := range map[string]string{o.statePath: "old state", o.configPath: "old config"} {
					b, _ := os.ReadFile(path)
					if string(b) != want {
						t.Fatal(path, string(b))
					}
				}
				for _, suffix := range []string{"-wal", "-shm"} {
					if _, err := os.Stat(o.statePath + suffix); !os.IsNotExist(err) {
						t.Fatal(err)
					}
				}
			} else {
				if err == nil {
					t.Fatal("unsafe offline recovery accepted")
				}
				got, _ := upgrade.ReadRecord(upgrade.RecordPath(o.statePath))
				if !got.Pinned {
					t.Fatal("pin cleared on failure")
				}
				b, _ := os.ReadFile(o.statePath)
				if strings.Contains(string(b), "old") {
					t.Fatal("state changed on refused or unstaged recovery")
				}
			}
		})
	}
}

func TestCleanupNormalizesRelativeRecordPath(t *testing.T) {
	dir := t.TempDir()
	path := upgrade.RecordPath(filepath.Join(dir, "state.db"))
	current := filepath.Join(filepath.Dir(path), "attempts", "current")
	older := filepath.Join(filepath.Dir(path), "attempts", "older")
	for _, d := range []string{current, older} {
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, path)
	if err != nil {
		t.Fatal(err)
	}
	r := upgrade.Record{SavedBinary: filepath.Join(current, "previous")}
	if err := cleanupUpgradeAttempts(relative, r, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(current); err != nil {
		t.Fatal("latest backups deleted", err)
	}
	if _, err := os.Stat(older); !os.IsNotExist(err) {
		t.Fatal("old backups retained", err)
	}
	if err := cleanupUpgradeAttempts(relative, r, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(current); !os.IsNotExist(err) {
		t.Fatal("abandoned backups retained", err)
	}
}
func TestConfigMismatchCannotRestore(t *testing.T) {
	r := upgrade.Record{ConfigPath: filepath.Join(t.TempDir(), "first.json")}
	if err := validateUpgradeConfig(r, filepath.Join(t.TempDir(), "second.json")); err == nil {
		t.Fatal("accepted other config")
	}
}

func TestProgressJournalFailureStillWaitsForAdmittedTurn(t *testing.T) {
	for _, failure := range []string{"missing", "unreadable", "initial", "observer"} {
		t.Run(failure, func(t *testing.T) {
			path := upgrade.RecordPath(filepath.Join(t.TempDir(), "state.db"))
			if failure != "missing" {
				r := upgrade.Record{Step: upgrade.Draining, From: "v1.0.0", To: "v2.0.0"}
				if err := upgrade.WriteRecord(path, r); err != nil {
					t.Fatal(err)
				}
			}
			if failure == "unreadable" {
				if err := os.WriteFile(path, []byte("{broken"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			stop, end := lifecycle.Watch(context.Background(), make(chan os.Signal), func(string) {})
			defer end()
			stop.Drain("upgrade")
			running := make(chan struct{})
			finished := make(chan error, 1)
			go func() {
				finished <- awaitUpgradeDrain(stop, running, path, func(context.Context, bool) error {
					if failure == "missing" || failure == "unreadable" {
						t.Error("observed invalid journal")
					}
					if failure == "initial" {
						return errors.New("initial progress failed")
					}
					return nil
				}, func(context.Context, bool) error { return errors.New("observer progress failed") })
			}()
			select {
			case err := <-finished:
				t.Fatal("drain bypassed running turn", err)
			case <-time.After(50 * time.Millisecond):
			}
			if stop.Force.Err() != nil {
				t.Fatal("running turn canceled")
			}
			close(running)
			if err := <-finished; err == nil {
				t.Fatal("progress failure hidden")
			}
		})
	}
}

func TestOfflineRecoveryRetriesAfterPartialRestore(t *testing.T) {
	for _, boundary := range []string{"state-restored", "config-restored"} {
		t.Run(boundary, func(t *testing.T) {
			dir := t.TempDir()
			o := &options{globals: &libcli.Globals{}, statePath: filepath.Join(dir, "state.db"), configPath: filepath.Join(dir, "config.json")}
			backup := upgrade.Backups{State: filepath.Join(dir, "backup.db"), Config: filepath.Join(dir, "backup.json")}
			for path, body := range map[string]string{o.statePath: "new state", o.configPath: "new config", backup.State: "old state", backup.Config: "old config", o.statePath + "-wal": "wal", o.statePath + "-shm": "shm"} {
				if err := os.WriteFile(path, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			r := upgrade.Record{Step: upgrade.Abandoned, Pinned: true, SavedBinary: filepath.Join(dir, "missing"), PinBinary: filepath.Join(dir, "retained-missing"), PinBackups: backup, Backups: upgrade.Backups{State: "incomplete", Config: "incomplete"}}
			path := upgrade.RecordPath(o.statePath)
			if err := upgrade.WriteRecord(path, r); err != nil {
				t.Fatal(err)
			}
			err := clearMissingRollbackBinaryWith(o, func(state, config string, b upgrade.Backups) error {
				return upgrade.RestoreWithCheckpoint(state, config, b, func(step string) error {
					if step == boundary {
						return errors.New("simulated crash")
					}
					return nil
				})
			})
			if err == nil {
				t.Fatal("fault boundary missed")
			}
			got, _ := upgrade.ReadRecord(path)
			if got.Step != upgrade.RollingBack || !got.Pinned || got.PinBackups != backup {
				t.Fatal(got)
			}
			if err = clearMissingRollbackBinary(o); err != nil {
				t.Fatal(err)
			}
			got, _ = upgrade.ReadRecord(path)
			if got.Pinned {
				t.Fatal(got)
			}
			for path, want := range map[string]string{o.statePath: "old state", o.configPath: "old config"} {
				b, _ := os.ReadFile(path)
				if string(b) != want {
					t.Fatal(path, string(b))
				}
			}
			for _, suffix := range []string{"-wal", "-shm"} {
				if _, err := os.Stat(o.statePath + suffix); !os.IsNotExist(err) {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestOldWatchdogLeaseDoesNotSuppressNewHostHelper(t *testing.T) {
	path := upgrade.RecordPath(filepath.Join(t.TempDir(), "state.db"))
	old := time.Now()
	next := old.Add(time.Second)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	lock := flock.New(upgrade.WatchdogLockPath(path, old))
	if ok, err := lock.TryLock(); err != nil || !ok {
		t.Fatal(ok, err)
	}
	defer lock.Unlock()
	called := false
	if err := startUpgradeWatchdogIfAbsent(path, next, func() error { called = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("old helper hides new attempt")
	}
}

func TestDemoRefusesProtectedUpgradeStateBeforeOpeningIt(t *testing.T) {
	for _, step := range []upgrade.Step{upgrade.RolledBack, upgrade.RollingBack} {
		dir := t.TempDir()
		o := &options{statePath: filepath.Join(dir, "state.db")}
		original := []byte("protected database")
		if err := os.WriteFile(o.statePath, original, 0600); err != nil {
			t.Fatal(err)
		}
		if err := upgrade.WriteRecord(upgrade.RecordPath(o.statePath), upgrade.Record{Step: step, Pinned: true}); err != nil {
			t.Fatal(err)
		}
		stop, end := lifecycle.Watch(context.Background(), make(chan os.Signal), func(string) {})
		defer end()
		if _, err := prepareUpgradeStart(o, stop, true); err == nil {
			t.Fatal("demo bypassed journal", step)
		}
		got, _ := os.ReadFile(o.statePath)
		if string(got) != string(original) {
			t.Fatal("demo changed protected state")
		}
		lock := flock.New(o.statePath + ".lock")
		if ok, err := lock.TryLock(); err != nil || !ok {
			t.Fatal("refusal leaked lock", ok, err)
		}
		lock.Unlock()
	}
}
func TestLockedTerminalStartupNamesRollbackAndDetachedLog(t *testing.T) {
	for _, label := range []string{"", "0", "application.com.apple.Terminal.fixture"} {
		t.Run(label, func(t *testing.T) {
			t.Setenv("XPC_SERVICE_NAME", label)
			dir := t.TempDir()
			o := &options{statePath: filepath.Join(dir, "state.db")}
			r := upgrade.Record{Step: upgrade.RolledBack, Pinned: true, From: "v1.0.0", To: "v2.0.0", Failure: "fixture health failure", DetachedLog: filepath.Join(dir, "rollback.log")}
			if err := upgrade.WriteRecord(upgrade.RecordPath(o.statePath), r); err != nil {
				t.Fatal(err)
			}
			lock := flock.New(o.statePath + ".lock")
			if ok, err := lock.TryLock(); err != nil || !ok {
				t.Fatal(ok, err)
			}
			defer lock.Unlock()
			stop, end := lifecycle.Watch(context.Background(), make(chan os.Signal), func(string) {})
			defer end()
			_, err := prepareUpgradeStart(o, stop, false)
			if err == nil {
				t.Fatal("lock not respected")
			}
			for _, want := range []string{"Rollback is in force", r.Failure, r.DetachedLog, "upgrade clear-rollback"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatal(err, want)
				}
			}
		})
	}
}
