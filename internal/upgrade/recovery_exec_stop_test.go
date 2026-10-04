package upgrade

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/lifecycle"
)

func TestExplicitRollbackReplacesPreviousStop(t *testing.T) {
	for _, step := range []Step{InstallFailed, HandingOver, Probation, StoppedInProbation, RollingBack} {
		for _, label := range []string{"", "fixture.launchd"} {
			for _, signal := range []bool{false, true} {
				t.Run(string(step)+"/"+label+map[bool]string{false: "/retry", true: "/stop"}[signal], func(t *testing.T) {
					e, r, _ := engineFixture(t)
					r.Step, r.RestartPending, r.LaunchdLabel = InstallFailed, true, label
					r.ConfigPath = "/original/config.json"
					r.Backups = Backups{State: "state-backup", Config: "config-backup"}
					if err := WriteRecord(e.Path, r); err != nil {
						t.Fatal(err)
					}
					signals, handled := make(chan os.Signal), make(chan struct{})
					stop, end := lifecycle.Watch(context.Background(), signals, func(string) { close(handled) })
					e.Interrupted = func() bool { return stop.Reason() == "signal" }
					execErr := errors.New("saved exec failed")
					e.Exec = func(string, []string) error { signals <- lifecycle.Signals[1]; <-handled; return execErr }
					if err := e.restartFailure(context.Background(), &r, r.SavedBinary); !errors.Is(err, context.Canceled) {
						t.Fatal(err)
					}
					end()
					got, err := ReadRecord(e.Path)
					if err != nil || !got.OwnerStopped || got.RestartPending {
						t.Fatal(got, err)
					}
					got.Step = step
					got.ProbationStarts = 1
					if err := WriteRecord(e.Path, *got); err != nil {
						t.Fatal(err)
					}
					signals, handled = make(chan os.Signal), make(chan struct{})
					stop, end = lifecycle.Watch(context.Background(), signals, func(string) { close(handled) })
					defer end()
					e.Interrupted = func() bool { return stop.Reason() == "signal" }
					e.Exec = func(string, []string) error {
						if signal {
							signals <- lifecycle.Signals[1]
							<-handled
						}
						return execErr
					}
					running := "v3.0.0"
					if step == Probation {
						running = r.To
					}
					if step == HandingOver || step == StoppedInProbation {
						running = r.From
					}
					if _, err := e.Start(running, "/installed"); !errors.Is(err, execErr) || errors.Is(err, context.Canceled) != signal {
						t.Fatal(err)
					}
					got, err = ReadRecord(e.Path)
					if err != nil || got.Step != RolledBack || !got.Pinned || got.OwnerStopped != signal || got.RestartPending == signal || got.RecoveryStarting || got.Backups != r.Backups || got.ConfigPath != r.ConfigPath {
						t.Fatal(got, err)
					}
					starts, kicks := 0, 0
					w := Watchdog{Path: e.Path, Alive: func(int) bool { return false }, Acquire: func() (func(), bool, error) { return func() {}, true, nil }, Restore: func(Record) error { t.Fatal("restored twice"); return nil }, StartDetached: func(Record) error { starts++; return nil }, Kickstart: func(Record) error { kicks++; return nil }}
					for range 3 {
						if _, err := w.Tick(); err != nil {
							t.Fatal(err)
						}
					}
					wantStarts, wantKicks := 0, 0
					if !signal {
						if label == "" {
							wantStarts = 1
						} else {
							wantKicks = 1
						}
					}
					if starts != wantStarts || kicks != wantKicks {
						t.Fatal(starts, kicks)
					}
				})
			}
		}
	}
}

func TestSignalWhileStartupRollbackWaitsForJournal(t *testing.T) {
	for _, step := range []Step{InstallFailed, RollingBack} {
		t.Run(string(step), func(t *testing.T) {
			e, r, _ := engineFixture(t)
			r.Step, r.OwnerStopped = step, true
			if err := WriteRecord(e.Path, r); err != nil {
				t.Fatal(err)
			}
			signals, handled := make(chan os.Signal), make(chan struct{})
			stop, end := lifecycle.Watch(context.Background(), signals, func(string) { close(handled) })
			defer end()
			e.Interrupted = func() bool { return stop.Reason() == "signal" }
			e.Exec = func(string, []string) error { t.Fatal("exec after current signal"); return nil }
			release, err := LockRecord(e.Path)
			if err != nil {
				t.Fatal(err)
			}
			entered, done := make(chan struct{}), make(chan error, 1)
			go func() { close(entered); _, err := e.Start("v3.0.0", "/installed"); done <- err }()
			<-entered
			signals <- lifecycle.Signals[1]
			<-handled
			release()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			got, err := ReadRecord(e.Path)
			if err != nil || !got.OwnerStopped || got.RestartPending || got.RecoveryStarting {
				t.Fatal(got, err)
			}
			if step == RollingBack && (got.Step != RolledBack || !got.Pinned) {
				t.Fatal(got)
			}
		})
	}
}

func TestFailedRecoveryExecHonorsSignal(t *testing.T) {
	for _, step := range []Step{BackupFailed, InstallFailed, RolledBack} {
		for _, label := range []string{"", "fixture.launchd"} {
			for _, signal := range []bool{false, true} {
				t.Run(string(step)+"/"+label+map[bool]string{true: "/stop", false: "/retry"}[signal], func(t *testing.T) {
					e, r, _ := engineFixture(t)
					signals := make(chan os.Signal)
					handled := make(chan struct{})
					stop, end := lifecycle.Watch(context.Background(), signals, func(string) { close(handled) })
					defer end()
					e.Interrupted = func() bool { return stop.Reason() == "signal" }
					r.LaunchdLabel = label
					r.RunningBinary = r.SavedBinary
					// A failed retry must retain the earlier rollback material.
					r.Step, r.Pinned = RolledBack, true
					r.Backups = Backups{State: "retained-state", Config: "retained-config"}
					if err := WriteRecord(e.Path, r); err != nil {
						t.Fatal(err)
					}
					execErr := errors.New("saved binary exec failed")
					var before *Record
					e.Exec = func(string, []string) error {
						before, _ = ReadRecord(e.Path)
						if signal {
							signals <- lifecycle.Signals[1]
							select {
							case <-handled:
							case <-time.After(time.Second):
								t.Fatal("SIGTERM not handled")
							}
						}
						return execErr
					}
					var err error
					if step == RolledBack {
						r.Step = Probation
						if err = WriteRecord(e.Path, r); err != nil {
							t.Fatal(err)
						}
						err = e.Fail("unhealthy")
					} else {
						if step == BackupFailed {
							e.Backup = func(context.Context, Record) error { return errors.New("backup failed") }
						} else {
							e.Install = func(context.Context, Record) (string, error) { return r.From, errors.New("install failed") }
						}
						if err = e.Request(r); err != nil {
							t.Fatal(err)
						}
						err = e.FinishDrain(context.Background(), "upgrade")
					}
					if !errors.Is(err, execErr) || errors.Is(err, context.Canceled) != signal {
						t.Fatal(err)
					}
					got, err := ReadRecord(e.Path)
					if err != nil || got.Step != step || got.RestartPending == signal || got.RecoveryStarting || got.OwnerStopped != signal || !got.Pinned {
						t.Fatal(got, err)
					}
					if got.PinBinary != before.PinBinary || got.PinBackups != before.PinBackups || got.PinTo != before.PinTo || got.Backups != before.Backups || got.SavedBinary != before.SavedBinary {
						t.Fatal("retained pin lost", got)
					}
					starts := 0
					for range 3 {
						w := Watchdog{Path: e.Path, Alive: func(int) bool { return false }, Acquire: func() (func(), bool, error) { return func() {}, true, nil }, Restore: func(Record) error { t.Fatal("completed recovery restored again"); return nil }, StartDetached: func(Record) error { starts++; return nil }, Kickstart: func(Record) error { starts++; return nil }}
						if _, err := w.Tick(); err != nil {
							t.Fatal(err)
						}
					}
					if signal && starts != 0 || !signal && starts == 0 {
						t.Fatal("unexpected watchdog starts", starts)
					}
				})
			}
		}
	}
}

func TestStopPersistenceUsesLatestLockedRecord(t *testing.T) {
	e, r, _ := engineFixture(t)
	r.Step, r.RestartPending = InstallFailed, true
	if err := WriteRecord(e.Path, r); err != nil {
		t.Fatal(err)
	}
	release, err := LockRecord(e.Path)
	if err != nil {
		t.Fatal(err)
	}
	entered, done := make(chan struct{}), make(chan error, 1)
	go func() { close(entered); done <- e.suppressRecovery(&r) }()
	<-entered
	latest := r
	latest.Step, latest.Pinned, latest.RecoveryStarting = RollingBack, true, true
	latest.PinBinary = "protected"
	if err := writeRecord(e.Path, latest); err != nil {
		t.Fatal(err)
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got, err := ReadRecord(e.Path)
	if err != nil || got.Step != RollingBack || !got.Pinned || got.PinBinary != "protected" || !got.OwnerStopped || got.RestartPending || got.RecoveryStarting {
		t.Fatal(got, err)
	}
	w := Watchdog{Path: e.Path, Alive: func(int) bool { return false }, Acquire: func() (func(), bool, error) { return func() {}, true, nil }, Restore: func(Record) error { return nil }, StartDetached: func(Record) error { t.Fatal("restart after suppression"); return nil }, Kickstart: func(Record) error { t.Fatal("kickstart after suppression"); return nil }}
	if _, err := w.Tick(); err != nil {
		t.Fatal(err)
	}
	got, _ = ReadRecord(e.Path)
	if got.Step != RolledBack || got.RestartPending || !got.Pinned {
		t.Fatal(got)
	}
}

func TestFailedRecoveryExecReportsStopJournalFailure(t *testing.T) {
	e, r, _ := engineFixture(t)
	r.Step, r.Pinned, r.RestartPending = InstallFailed, true, true
	r.PinBinary, r.PinBackups, r.PinTo = "retained", Backups{State: "old-state", Config: "old-config"}, "v0.9.0"
	path := e.Path
	if err := WriteRecord(path, r); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("blocker"), 0600); err != nil {
		t.Fatal(err)
	}
	stopped := false
	e.Interrupted = func() bool { return stopped }
	execErr := errors.New("exec failed")
	e.Exec = func(string, []string) error {
		stopped = true
		// Inject an inaccessible journal destination without corrupting the
		// last valid record that protects restoration and the saved binary.
		e.Path = filepath.Join(blocker, "record.json")
		return execErr
	}
	err := e.restartFailure(context.Background(), &r, r.SavedBinary)
	var journalErr *os.PathError
	if !errors.Is(err, execErr) || !errors.Is(err, context.Canceled) || !errors.As(err, &journalErr) {
		t.Fatal(err)
	}
	got, err := ReadRecord(path)
	if err != nil || !got.RestartPending || !got.Pinned || got.PinBinary != r.PinBinary || got.PinBackups != r.PinBackups || got.PinTo != r.PinTo {
		t.Fatal(got, err)
	}
}

func TestWatchdogWaitingForFailedRollbackExecObservesStop(t *testing.T) {
	e, r, _ := engineFixture(t)
	r.Step = Probation
	if err := WriteRecord(e.Path, r); err != nil {
		t.Fatal(err)
	}
	signals := make(chan os.Signal)
	handled := make(chan struct{})
	stop, end := lifecycle.Watch(context.Background(), signals, func(string) { close(handled) })
	defer end()
	e.Interrupted = func() bool { return stop.Reason() == "signal" }
	w := Watchdog{Path: e.Path, Alive: func(int) bool { return false }, Acquire: func() (func(), bool, error) { return func() {}, true, nil }, Restore: func(Record) error { t.Error("restored completed rollback"); return nil }, StartDetached: func(Record) error { t.Error("restarted stopped daemon"); return nil }, Kickstart: func(Record) error { t.Error("kickstarted stopped daemon"); return nil }}
	entered, finished := make(chan struct{}), make(chan error, 1)
	e.Exec = func(string, []string) error {
		// Fail holds the journal lock throughout restoration and exec. Queue
		// recovery before that lock is released by the stopped sender.
		go func() { close(entered); _, err := w.Tick(); finished <- err }()
		<-entered
		signals <- lifecycle.Signals[1]
		<-handled
		return errors.New("exec failed")
	}
	if err := e.Fail("unhealthy"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	got, err := ReadRecord(e.Path)
	if err != nil || got.Step != RolledBack || !got.Pinned || !got.OwnerStopped || got.RestartPending || got.RecoveryStarting {
		t.Fatal(got, err)
	}
}

func TestExplicitProbationReplacesPreviousStop(t *testing.T) {
	for _, label := range []string{"", "fixture.launchd"} {
		t.Run(label, func(t *testing.T) {
			e, r, _ := engineFixture(t)
			r.Step, r.RestartPending, r.LaunchdLabel = InstallFailed, true, label
			if err := WriteRecord(e.Path, r); err != nil {
				t.Fatal(err)
			}
			signals, handled := make(chan os.Signal), make(chan struct{})
			stop, end := lifecycle.Watch(context.Background(), signals, func(string) { close(handled) })
			e.Interrupted = func() bool { return stop.Reason() == "signal" }
			e.Exec = func(string, []string) error {
				signals <- lifecycle.Signals[1]
				<-handled
				return errors.New("exec failed")
			}
			if err := e.restartFailure(context.Background(), &r, r.SavedBinary); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			end()
			// A new explicit target start has no signal from the former process.
			e.Interrupted = func() bool { return false }
			result, err := e.Start(r.To, "/new")
			if err != nil || !result.Probation {
				t.Fatal(result, err)
			}
			if err := e.BeginProbation(42, "new-process", nil, time.Minute); err != nil {
				t.Fatal(err)
			}
			got, err := ReadRecord(e.Path)
			if err != nil || got.OwnerStopped || got.RestartPending || got.RecoveryStarting || got.Step != Probation {
				t.Fatal(got, err)
			}
			alive, inspected, restores, starts := true, 0, 0, 0
			w := Watchdog{Path: e.Path, Alive: func(int) bool { inspected++; return alive }, Identity: func(int) string { return "new-process" }, Acquire: func() (func(), bool, error) { return func() {}, true, nil }, Restore: func(Record) error { restores++; return nil }, StartDetached: func(Record) error { starts++; return nil }, Kickstart: func(Record) error { starts++; return nil }}
			for range 2 {
				if done, err := w.Tick(); err != nil || done {
					t.Fatal(done, err)
				}
			}
			if inspected == 0 || restores != 0 || starts != 0 {
				t.Fatal(inspected, restores, starts)
			}
			alive = false
			if _, err := w.Tick(); err != nil {
				t.Fatal(err)
			}
			got, _ = ReadRecord(e.Path)
			if restores != 1 || starts != 1 || got.OwnerStopped || !got.RestartPending || !got.Pinned {
				t.Fatal(got, restores, starts)
			}
		})
	}
}

func TestSignalWhileProbationWaitsForJournalRemainsAuthoritative(t *testing.T) {
	e, r, _ := engineFixture(t)
	r.Step, r.OwnerStopped = InstallFailed, true
	if err := WriteRecord(e.Path, r); err != nil {
		t.Fatal(err)
	}
	signals, handled := make(chan os.Signal), make(chan struct{})
	stop, end := lifecycle.Watch(context.Background(), signals, func(string) { close(handled) })
	defer end()
	e.Interrupted = func() bool { return stop.Reason() == "signal" }
	release, err := LockRecord(e.Path)
	if err != nil {
		t.Fatal(err)
	}
	entered, done := make(chan struct{}), make(chan error, 1)
	go func() { close(entered); done <- e.BeginProbation(42, "new-process", nil, time.Minute) }()
	<-entered
	signals <- lifecycle.Signals[1]
	<-handled
	release()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	got, err := ReadRecord(e.Path)
	if err != nil || got.Step != StoppedInProbation || got.ProbationStarts != 0 {
		t.Fatal(got, err)
	}
	w := Watchdog{Path: e.Path, Alive: func(int) bool { t.Fatal("inspected stopped process"); return true }}
	if done, err := w.Tick(); err != nil || !done {
		t.Fatal(done, err)
	}
}

func TestNewRecoveryIdentityReplacesOldStopUnlessCurrentlyInterrupted(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		t.Run(map[bool]string{false: "new-start", true: "current-stop"}[interrupted], func(t *testing.T) {
			e, r, _ := engineFixture(t)
			r.Step, r.OwnerStopped, r.RestartPending = InstallFailed, true, true
			if err := WriteRecord(e.Path, r); err != nil {
				t.Fatal(err)
			}
			e.Interrupted = func() bool { return interrupted }
			err := e.BeginRecovery(42, "new-process", nil, time.Minute)
			if errors.Is(err, context.Canceled) != interrupted || !interrupted && err != nil {
				t.Fatal(err)
			}
			got, err := ReadRecord(e.Path)
			if err != nil || got.OwnerStopped != interrupted || got.RestartPending == interrupted || got.RecoveryStarting == interrupted {
				t.Fatal(got, err)
			}
		})
	}
}

func TestRecoveryConfirmationDistinguishesStopFromHealthFailure(t *testing.T) {
	for _, outcome := range []string{"cancelled-probe", "observer-first", "health-failure", "deadline"} {
		t.Run(outcome, func(t *testing.T) {
			e, r, _ := engineFixture(t)
			r.Step, r.RestartPending, r.RecoveryStarting, r.Pinned = RolledBack, true, true, true
			if err := WriteRecord(e.Path, r); err != nil {
				t.Fatal(err)
			}
			signals, handled := make(chan os.Signal), make(chan struct{})
			stop, end := lifecycle.Watch(context.Background(), signals, func(string) { close(handled) })
			defer end()
			e.Interrupted = func() bool { return stop.Reason() == "signal" }
			probeDone := make(chan struct{})
			bound := time.Second
			if outcome == "deadline" {
				bound = 0
			}
			err := e.ConfirmRecovery(stop.Graceful, bound, func(ctx context.Context) error {
				defer close(probeDone)
				if outcome == "health-failure" {
					return context.Canceled
				} // Not an owner stop.
				if outcome == "deadline" {
					<-ctx.Done()
					return ctx.Err()
				}
				if outcome == "observer-first" {
					if err := e.StopProbation(); err != nil {
						t.Error(err)
					}
				}
				signals <- lifecycle.Signals[1]
				<-handled
				if outcome == "observer-first" {
					return nil
				}
				return ctx.Err()
			})
			select {
			case <-probeDone:
			case <-time.After(time.Second):
				t.Fatal("probe did not finish")
			}
			interrupted := outcome == "cancelled-probe" || outcome == "observer-first"
			if err == nil || errors.Is(err, context.Canceled) != interrupted {
				t.Fatal(err)
			}
			if interrupted {
				if err := e.StopProbation(); err != nil {
					t.Fatal(err)
				}
			}
			got, err := ReadRecord(e.Path)
			if err != nil || !got.Pinned || got.Step != RolledBack || got.RestartPending == interrupted || got.RecoveryStarting == interrupted {
				t.Fatal(got, err)
			}
		})
	}
}

func TestRecoveryStopObserverWinsBetweenProbeAndConfirmation(t *testing.T) {
	e, r, _ := engineFixture(t)
	r.Step, r.RestartPending, r.RecoveryStarting = InstallFailed, true, true
	if err := WriteRecord(e.Path, r); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	checked, resume := make(chan struct{}), make(chan struct{})
	first := true
	e.Interrupted = func() bool {
		if first {
			first = false
			close(checked)
			<-resume
			return false
		}
		return ctx.Err() != nil
	}
	done := make(chan error, 1)
	go func() { done <- e.ConfirmRecovery(ctx, time.Second, func(context.Context) error { return nil }) }()
	select {
	case <-checked:
	case <-time.After(time.Second):
		cancel()
		close(resume)
		t.Fatal("confirmation did not reach the pre-lock stop check")
	}
	// The observer suppresses restart after the probe and initial stop check,
	// but before confirmation reads the journal under its own lock.
	if err := e.StopProbation(); err != nil {
		close(resume)
		t.Fatal(err)
	}
	cancel()
	close(resume)
	if err := <-done; !errors.Is(err, context.Canceled) || errors.Is(err, ErrInProgress) {
		t.Fatal(err)
	}
	got, err := ReadRecord(e.Path)
	if err != nil || got.RestartPending || got.RecoveryStarting || got.Step != InstallFailed {
		t.Fatal(got, err)
	}
}
