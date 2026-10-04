package upgrade

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestBackupFailureRestartsRunningVersionAndReports(t *testing.T) {
	e, r, calls := engineFixture(t)
	r.RunningBinary = "/running/crew-assistant"
	e.Backup = func(context.Context, Record) error { return errors.New("disk full") }
	if err := e.Request(r); err != nil {
		t.Fatal(err)
	}
	if err := e.FinishDrain(context.Background(), "upgrade"); err != nil {
		t.Fatal(err)
	}
	got, _ := ReadRecord(e.Path)
	if got.Step != BackupFailed || got.Pinned || got.Failure == "" || (*calls)[len(*calls)-1] != "exec:"+r.RunningBinary {
		t.Fatal(got, *calls)
	}
	result, err := e.Start(r.From, r.RunningBinary)
	if err != nil || !result.Failed {
		t.Fatal(result, err)
	}
}
func TestUnchangedFormulaDoesNotPin(t *testing.T) {
	e, r, _ := engineFixture(t)
	e.Install = func(context.Context, Record) (string, error) { return r.From, nil }
	if err := e.Request(r); err != nil {
		t.Fatal(err)
	}
	if err := e.FinishDrain(context.Background(), "upgrade"); err != nil {
		t.Fatal(err)
	}
	got, _ := ReadRecord(e.Path)
	if got.Step != InstallFailed || got.Pinned || got.Failure == "" {
		t.Fatal(got)
	}
}
func TestAbandonedNewerRetryRetainsFailureIdentity(t *testing.T) {
	e, r, _ := engineFixture(t)
	r.Step, r.Pinned, r.Failure = RolledBack, true, "old failure"
	if err := WriteRecord(e.Path, r); err != nil {
		t.Fatal(err)
	}
	r.To, r.Automatic = "v3.0.0", true
	if err := e.Request(r); err != nil {
		t.Fatal(err)
	}
	if err := e.FinishDrain(context.Background(), "signal"); err != nil {
		t.Fatal(err)
	}
	if err := e.Request(r); err != nil {
		t.Fatal("never-installed newer version suppressed", err)
	}
}
func TestRecordPathsIsolateStateFilesAndNormalizeRelativePaths(t *testing.T) {
	dir := t.TempDir()
	first, second := RecordPath(filepath.Join(dir, "first.db")), RecordPath(filepath.Join(dir, "second.db"))
	if first == second {
		t.Fatal("state files share a recovery journal")
	}
	e, r, _ := engineFixture(t)
	r.Step = RollingBack
	if err := WriteRecord(first, r); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadRecord(second); err != nil || got != nil {
		t.Fatal(got, err)
	}
	_ = e
}
func TestInstalledVersionProbeHasIndependentBound(t *testing.T) {
	i := BrewInstaller{ProbeBound: time.Millisecond, Run: func(ctx context.Context, _ string, args, env []string) ([]byte, error) {
		if args[0] == "upgrade" {
			return nil, nil
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("probe has no bound")
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	if _, err := i.Install(context.Background(), "/fake", "crew-assistant", nil); err == nil {
		t.Fatal("hanging probe accepted")
	}
}
func TestWatchdogRevalidatesAfterWaitingForJournal(t *testing.T) {
	for _, step := range []Step{Healthy, StoppedInProbation, RolledBack} {
		t.Run(string(step), func(t *testing.T) {
			e, r, _ := engineFixture(t)
			r.Step = Probation
			r.PID = 42
			r.StartedAt = time.Now()
			if err := WriteRecord(e.Path, r); err != nil {
				t.Fatal(err)
			}
			release, err := LockRecord(e.Path)
			if err != nil {
				t.Fatal(err)
			}
			finished := make(chan error, 1)
			w := Watchdog{Path: e.Path, Attempt: r.StartedAt, Alive: func(int) bool { t.Error("examined completed process"); return true }, Kill: func(int) error { t.Error("killed completed process"); return nil }}
			go func() {
				done, err := w.Tick()
				if !done && err == nil {
					err = errors.New("not completed")
				}
				finished <- err
			}()
			r.Step = step
			if err := writeRecord(e.Path, r); err != nil {
				t.Fatal(err)
			}
			release()
			if err := <-finished; err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestWatchdogRetriesRestartWithoutRestoringTwice(t *testing.T) {
	for _, label := range []string{"", "fixture.launchd"} {
		t.Run(label, func(t *testing.T) {
			e, r, _ := engineFixture(t)
			r.Step = Probation
			r.StartedAt = time.Now()
			r.LaunchdLabel = label
			if err := WriteRecord(e.Path, r); err != nil {
				t.Fatal(err)
			}
			restores, starts := 0, 0
			w := Watchdog{Path: e.Path, Attempt: r.StartedAt, Alive: func(int) bool { return false }, Acquire: func() (func(), bool, error) { return func() {}, true, nil }, Restore: func(Record) error { restores++; return nil }}
			start := func(Record) error {
				starts++
				if starts == 1 {
					return errors.New("temporary failure")
				}
				return nil
			}
			w.StartDetached, w.Kickstart = start, start
			if done, err := w.Tick(); err == nil || done {
				t.Fatal(done, err)
			}
			got, _ := ReadRecord(e.Path)
			if got.Step != RolledBack || !got.RestartPending {
				t.Fatal(got)
			}
			if done, err := w.Tick(); err != nil || done {
				t.Fatal(done, err)
			}
			if restores != 1 || starts != 2 {
				t.Fatal(restores, starts)
			}
			if _, err := e.Start(r.From, r.SavedBinary); err != nil {
				t.Fatal(err)
			}
			if err := e.ConfirmRecovery(context.Background(), time.Second, func(context.Context) error { return nil }); err != nil {
				t.Fatal(err)
			}
			if done, err := w.Tick(); err != nil || !done {
				t.Fatal(done, err)
			}
		})
	}
}
func TestFailureMarkerCannotMarkNewAttempt(t *testing.T) {
	e, r, _ := engineFixture(t)
	r.Step = InstallFailed
	r.StartedAt = time.Now()
	if err := WriteRecord(e.Path, r); err != nil {
		t.Fatal(err)
	}
	if err := e.MarkFailureOpened(r.StartedAt.Add(-time.Second)); !errors.Is(err, ErrInProgress) {
		t.Fatal(err)
	}
	got, _ := ReadRecord(e.Path)
	if got.FailedDecisionOpened {
		t.Fatal("marked another attempt")
	}
}
func TestOwnerStopBeforeAPIStandsDownWatchdog(t *testing.T) {
	e, r, _ := engineFixture(t)
	r.Step = Installing
	if err := WriteRecord(e.Path, r); err != nil {
		t.Fatal(err)
	}
	if err := e.BeginProbation(42, "fixture", nil, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := e.StopProbation(); err != nil {
		t.Fatal(err)
	}
	e.Interrupted = func() bool { return true }
	e.Exec = func(string, []string) error { t.Fatal("restarted after owner stop"); return nil }
	if err := e.Fail("startup failed"); err != nil {
		t.Fatal(err)
	}
	got, _ := ReadRecord(e.Path)
	if got.Step != StoppedInProbation {
		t.Fatal(got)
	}
}

func TestWatchdogNeverKillsReusedPID(t *testing.T) {
	e, r, _ := engineFixture(t)
	r.Step = Probation
	r.ProcessIdentity = "original"
	r.StartedAt = time.Now()
	r.PID = 42
	if err := WriteRecord(e.Path, r); err != nil {
		t.Fatal(err)
	}
	w := Watchdog{Path: e.Path, Attempt: r.StartedAt, Identity: func(int) string { return "different" }, Alive: func(int) bool { return true }, Kill: func(int) error { t.Fatal("killed reused PID"); return nil }, Acquire: func() (func(), bool, error) { return nil, false, nil }}
	if _, err := w.Tick(); err != nil {
		t.Fatal(err)
	}
}

func TestCompetingWatchdogsRestoreAndLaunchOnlyOnce(t *testing.T) {
	e, r, _ := engineFixture(t)
	r.Step = Probation
	r.StartedAt = time.Now()
	if err := WriteRecord(e.Path, r); err != nil {
		t.Fatal(err)
	}
	restored, started := 0, 0
	w := Watchdog{Path: e.Path, Attempt: r.StartedAt, Alive: func(int) bool { return false }, Acquire: func() (func(), bool, error) { return func() {}, true, nil }, Restore: func(Record) error { restored++; return nil }, StartDetached: func(Record) error { started++; return nil }}
	done := make(chan error, 2)
	go func() { _, err := w.Tick(); done <- err }()
	other := w
	go func() { _, err := other.Tick(); done <- err }()
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if restored != 1 || started != 1 {
		t.Fatal(restored, started)
	}
}
