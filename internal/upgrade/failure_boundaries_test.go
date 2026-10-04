package upgrade

import (
	"context"
	"errors"
	"github.com/gofrs/flock"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestIncompletePinnedBackupRetainsProtectionAndRefusesTarget(t *testing.T) {
	e, r, _ := engineFixture(t)
	r.Step, r.Pinned = RolledBack, true
	r.Backups = Backups{State: "retained-state", Config: "retained-config"}
	if err := WriteRecord(e.Path, r); err != nil {
		t.Fatal(err)
	}
	next := r
	next.SavedBinary = "/incomplete/missing"
	next.Backups = Backups{State: "incomplete", Config: "incomplete"}
	next.RunningBinary = r.SavedBinary
	e.Backup = func(context.Context, Record) error { return errors.New("disk full") }
	if err := e.Request(next); err != nil {
		t.Fatal(err)
	}
	if err := e.FinishDrain(context.Background(), "upgrade"); err != nil {
		t.Fatal(err)
	}
	result, err := e.Start(r.From, r.SavedBinary)
	if err != nil || !result.Failed {
		t.Fatal(result, err)
	}
	got, _ := ReadRecord(e.Path)
	if got.Step != BackupFailed || !got.Pinned || got.PinBinary != r.SavedBinary || got.PinBackups != r.Backups {
		t.Fatal(got)
	}
	e.Exec = func(binary string, _ []string) error {
		if binary != r.SavedBinary {
			t.Fatal(binary)
		}
		return errors.New("pin exec")
	}
	if result, err = e.Start(r.To, "/brew/new"); err == nil || result.Probation {
		t.Fatal(result, err)
	}
	got.Pinned = false
	if err := WriteRecord(e.Path, *got); err != nil {
		t.Fatal(err)
	}
	if result, err = e.Start(r.To, "/brew/new"); err == nil || result.Probation {
		t.Fatal(result, err)
	}
}

func TestSignalWithBackupFailureNeverRestarts(t *testing.T) {
	for _, forced := range []bool{false, true} {
		t.Run(strconv.FormatBool(forced), func(t *testing.T) {
			e, r, _ := engineFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stopped := false
			e.Interrupted = func() bool { return stopped }
			e.Backup = func(context.Context, Record) error {
				stopped = true
				if forced {
					cancel()
				}
				return errors.New("backup interrupted")
			}
			e.Exec = func(string, []string) error { t.Fatal("restarted after owner stop"); return nil }
			if err := e.Request(r); err != nil {
				t.Fatal(err)
			}
			if err := e.FinishDrain(ctx, "upgrade"); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			got, _ := ReadRecord(e.Path)
			if got.Step != Abandoned {
				t.Fatal(got)
			}
		})
	}
}

func TestDirectRollbackKeepsRestartPendingUntilAPIAnswers(t *testing.T) {
	e, r, _ := engineFixture(t)
	r.Step = Probation
	if err := WriteRecord(e.Path, r); err != nil {
		t.Fatal(err)
	}
	e.Exec = func(string, []string) error {
		got, _ := ReadRecord(e.Path)
		if got.Step != RolledBack || !got.RestartPending {
			t.Fatal(got)
		}
		return errors.New("cannot exec")
	}
	if err := e.Fail("unhealthy"); err == nil {
		t.Fatal("exec failure hidden")
	}
	result, err := e.Start(r.From, r.SavedBinary)
	if err != nil || !result.Recovery {
		t.Fatal(result, err)
	}
	if err = e.BeginRecovery(42, "fixture", nil, time.Second); err != nil {
		t.Fatal(err)
	}
	if err = e.ConfirmRecovery(context.Background(), time.Second, func(context.Context) error { return errors.New("startup failed") }); err == nil {
		t.Fatal("failed API acknowledged")
	}
	got, _ := ReadRecord(e.Path)
	if !got.RestartPending || !got.RecoveryStarting {
		t.Fatal(got)
	}
	if err = e.ConfirmRecovery(context.Background(), time.Second, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	got, _ = ReadRecord(e.Path)
	if got.RestartPending {
		t.Fatal(got)
	}
}

func TestPruningCannotStopHealthyDaemonAndRetriesOnStart(t *testing.T) {
	e, r, _ := engineFixture(t)
	r.Step = HandingOver
	if err := WriteRecord(e.Path, r); err != nil {
		t.Fatal(err)
	}
	calls, reports := 0, 0
	e.Cleanup = func(Record, bool) error { calls++; return errors.New("permission denied") }
	e.Report = func(string) { reports++ }
	if err := e.CheckHealth(context.Background(), time.Second, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	got, _ := ReadRecord(e.Path)
	if got.Step != Healthy || !got.CleanupPending {
		t.Fatal(got)
	}
	if _, err := e.Start(r.To, "/new"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || reports != 2 {
		t.Fatal(calls, reports)
	}
	e.Cleanup = func(Record, bool) error { return nil }
	if _, err := e.Start(r.To, "/new"); err != nil {
		t.Fatal(err)
	}
	got, _ = ReadRecord(e.Path)
	if got.CleanupPending {
		t.Fatal(got)
	}
}

func TestNewWatchdogAttemptHasIndependentLease(t *testing.T) {
	e, r, _ := engineFixture(t)
	r.Step = Probation
	r.StartedAt = time.Now()
	if err := WriteRecord(e.Path, r); err != nil {
		t.Fatal(err)
	}
	old := flock.New(WatchdogLockPath(e.Path, r.StartedAt.Add(-time.Second)))
	if ok, err := old.TryLock(); err != nil || !ok {
		t.Fatal(ok, err)
	}
	defer old.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	called := false
	w := Watchdog{Path: e.Path, Attempt: r.StartedAt, Alive: func(int) bool { return false }, Acquire: func() (func(), bool, error) { return func() {}, true, nil }, Restore: func(Record) error { return nil }, StartDetached: func(Record) error { called = true; cancel(); return nil }}
	_ = w.Run(ctx)
	if !called {
		t.Fatal("old lease suppressed new recovery")
	}
}

func TestWatchdogReportsSafeRecoveryFailure(t *testing.T) {
	e, r, _ := engineFixture(t)
	r.Step = Probation
	r.StartedAt = time.Now()
	if err := WriteRecord(e.Path, r); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	message := ""
	w := Watchdog{Path: e.Path, Attempt: r.StartedAt, Alive: func(int) bool { return false }, Acquire: func() (func(), bool, error) { return nil, false, errors.New("secret-value") }, Report: func(s string) { message = s; cancel() }}
	_ = w.Run(ctx)
	if message == "" || strings.Contains(message, "secret-value") {
		t.Fatal(message)
	}
}
