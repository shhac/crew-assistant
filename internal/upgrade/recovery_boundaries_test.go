package upgrade

import (
	"context"
	"errors"
	"github.com/shhac/crew-assistant/internal/testutil"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFailureFirstTransitionIncludesRestartObligation(t *testing.T) {
	for _, step := range []Step{BackupFailed, InstallFailed} {
		t.Run(string(step), func(t *testing.T) {
			e, r, _ := engineFixture(t)
			// Stop at the boundary before any later journal write could add
			// an obligation missing from the first failure transition.
			e.Now = func() time.Time {
				got, err := ReadRecord(e.Path)
				if err != nil {
					t.Fatal(err)
				}
				if got != nil && got.Step == step && !got.RestartPending {
					panic("crash boundary")
				}
				return time.Unix(100, 0)
			}
			if step == BackupFailed {
				e.Backup = func(context.Context, Record) error { return errors.New("backup failed") }
			} else {
				e.Install = func(context.Context, Record) (string, error) { return r.From, errors.New("install failed") }
			}
			// Arm is the first effect after committing failure. Simulate a
			// crash here, before shutdown or exec can repair another write.
			e.Arm = func(Record) error {
				got, err := ReadRecord(e.Path)
				if err != nil || got.Step != step || !got.RestartPending {
					t.Fatalf("first failure transition: %+v %v", got, err)
				}
				panic("crash boundary")
			}
			if err := e.Request(r); err != nil {
				t.Fatal(err)
			}
			func() {
				defer func() {
					if recover() != "crash boundary" {
						t.Fatal("did not reach boundary")
					}
				}()
				_ = e.FinishDrain(context.Background(), "upgrade")
			}()
			e.Arm = nil
			got, err := e.Start(r.From, r.SavedBinary)
			if err != nil || !got.Recovery {
				t.Fatal(got, err)
			}
		})
	}
}

func TestInterruptedInstallStartupJournalsRecoveryWithFailure(t *testing.T) {
	e, r, _ := engineFixture(t)
	r.Step = Installing
	if err := WriteRecord(e.Path, r); err != nil {
		t.Fatal(err)
	}
	result, err := e.Start(r.From, r.SavedBinary)
	if err != nil || !result.Recovery {
		t.Fatal(result, err)
	}
	got, err := ReadRecord(e.Path)
	if err != nil || got.Step != InstallFailed || !got.RestartPending {
		t.Fatal(got, err)
	}
}

func TestOwnerStopDuringPartialRestoreSuppressesWatchdogRestart(t *testing.T) {
	e, r, _ := engineFixture(t)
	stopped := false
	e.Interrupted = func() bool { return stopped }
	dir := t.TempDir()
	state, config := filepath.Join(dir, "state.db"), filepath.Join(dir, "config.json")
	r.Backups = Backups{State: filepath.Join(dir, "old.db"), Config: filepath.Join(dir, "old.json")}
	for p, body := range map[string]string{state: "new state", config: "new config", r.Backups.State: "old state", r.Backups.Config: "old config", state + "-wal": "wal", state + "-shm": "shm"} {
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	e.Restore = func(r Record) error {
		return RestoreWithCheckpoint(state, config, r.Backups, func(step string) error {
			if step == "state-restored" {
				stopped = true
				return errors.New("partial restore")
			}
			return nil
		})
	}
	e.Exec = func(string, []string) error { t.Fatal("restarted after stop"); return nil }
	if err := e.rollback(&r, "failed health"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	got, _ := ReadRecord(e.Path)
	if got.Step != RollingBack || !got.OwnerStopped || got.RestartPending {
		t.Fatal(got)
	}
	// A stop observer must also preserve the restoration obligation.
	if err := e.StopProbation(); err != nil {
		t.Fatal(err)
	}
	restored := false
	w := Watchdog{Path: e.Path, Alive: func(int) bool { return true },
		Kill:          func(int) error { t.Fatal("killed stopped owner process"); return nil },
		Acquire:       func() (func(), bool, error) { return func() {}, true, nil },
		Restore:       func(r Record) error { restored = true; return Restore(state, config, r.Backups) },
		StartDetached: func(Record) error { t.Fatal("detached restart after stop"); return nil },
	}
	if done, err := w.Tick(); err != nil || !done || !restored {
		t.Fatal(done, restored, err)
	}
	got, _ = ReadRecord(e.Path)
	if got.Step != RolledBack || !got.Pinned || got.RestartPending {
		t.Fatal(got)
	}
	for p, want := range map[string]string{state: "old state", config: "old config"} {
		b, err := os.ReadFile(p)
		if err != nil || string(b) != want {
			t.Fatal(p, string(b), err)
		}
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(state + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatal(suffix, err)
		}
	}
	stopped = false
	if result, err := e.Start(r.From, r.SavedBinary); err != nil || result.Recovery {
		t.Fatal(result, err)
	}
}

func TestWatchdogRetriesUnknownLiveProcessIdentity(t *testing.T) {
	e, r, _ := engineFixture(t)
	r.Step, r.PID, r.ProcessIdentity = Probation, 123, "birth"
	r.Deadline = time.Now().Add(time.Hour)
	if err := WriteRecord(e.Path, r); err != nil {
		t.Fatal(err)
	}
	lookups := 0
	w := Watchdog{Path: e.Path, Alive: func(int) bool { return true }, Identity: func(int) string {
		lookups++
		if lookups == 1 {
			return ""
		}
		return "birth"
	}, Kill: func(int) error { t.Fatal("killed live receiver"); return nil }, Acquire: func() (func(), bool, error) { t.Fatal("claimed recovery"); return nil, false, nil }}
	for i := range 2 {
		if done, err := w.Tick(); done || (i == 0 && !errors.Is(err, errProcessInspection)) || (i == 1 && err != nil) {
			t.Fatal(done, err)
		}
		got, _ := ReadRecord(e.Path)
		if got.Step != Probation {
			t.Fatal(got)
		}
	}
}

func TestStateAliasesShareJournalAndRestoreDestination(t *testing.T) {
	dir := t.TempDir()
	state, config := filepath.Join(dir, "state.db"), filepath.Join(dir, "config.json")
	for _, p := range []string{state, config} {
		if err := os.WriteFile(p, []byte("new"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		testutil.RequireSymlinkResult(t, err)
	}
	fileAlias := filepath.Join(dir, "state-alias.db")
	if err := os.Symlink(state, fileAlias); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(alias, "state.db"), fileAlias} {
		if RecordPath(path) != RecordPath(state) {
			t.Fatal("alias missed journal", path)
		}
	}
	backups := Backups{State: filepath.Join(dir, "old.db"), Config: filepath.Join(dir, "old.json")}
	for _, p := range []string{backups.State, backups.Config} {
		if err := os.WriteFile(p, []byte("old"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := Restore(fileAlias, filepath.Join(alias, "config.json"), backups); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Lstat(fileAlias)
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("restore replaced alias")
	}
	b, _ := os.ReadFile(state)
	if string(b) != "old" {
		t.Fatal(string(b))
	}
}
