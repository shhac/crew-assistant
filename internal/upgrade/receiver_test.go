package upgrade

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestFailureRestartsStayPendingAcrossCloseAndExecErrors(t *testing.T) {
	for _, step := range []Step{BackupFailed, InstallFailed} {
		t.Run(string(step), func(t *testing.T) {
			e, r, _ := engineFixture(t)
			r.RunningBinary = "/running"
			if step == BackupFailed {
				e.Backup = func(context.Context, Record) error { return errors.New("backup") }
			} else {
				e.Install = func(context.Context, Record) (string, error) { return r.From, errors.New("brew") }
			}
			armed := false
			e.Arm = func(got Record) error {
				armed = true
				if !got.RestartPending || got.Step != step {
					t.Fatal(got)
				}
				return nil
			}
			e.Close = func() error { return errors.New("shutdown") }
			e.Exec = func(binary string, _ []string) error {
				got, _ := ReadRecord(e.Path)
				if !got.RestartPending {
					t.Fatal(got)
				}
				return errors.New("exec failed")
			}
			if err := e.Request(r); err != nil {
				t.Fatal(err)
			}
			if err := e.FinishDrain(context.Background(), "upgrade"); err == nil {
				t.Fatal("exec failure hidden")
			}
			got, _ := ReadRecord(e.Path)
			if !armed || !got.RestartPending || watchdogDone(got) {
				t.Fatal(got, armed)
			}
			result, err := e.Start(r.From, RecoveryBinary(*got))
			if err != nil || !result.Recovery {
				t.Fatal(result, err)
			}
			if err := e.BeginRecovery(42, "receiver", []string{"serve"}, time.Second); err != nil {
				t.Fatal(err)
			}
			if err := e.ConfirmRecovery(context.Background(), time.Second, func(context.Context) error { return errors.New("API") }); err == nil {
				t.Fatal("bad receiver acknowledged")
			}
			starts := 0
			w := Watchdog{Path: e.Path, Alive: func(int) bool { return false }, Acquire: func() (func(), bool, error) { return func() {}, true, nil }, Restore: func(Record) error { t.Fatal("restored incomplete or unnecessary backups"); return nil }, StartDetached: func(next Record) error {
				starts++
				if next.Step != step {
					t.Fatal(next)
				}
				return nil
			}}
			if _, err := w.Tick(); err != nil || starts != 1 {
				t.Fatal(err, starts)
			}
		})
	}
}

func TestSignalTakesPrecedenceOverHandoverAndExecFailure(t *testing.T) {
	for _, where := range []string{"handover", "exec", "startup"} {
		t.Run(where, func(t *testing.T) {
			e, r, _ := engineFixture(t)
			stopped := false
			e.Interrupted = func() bool { return stopped }
			e.Restore = func(Record) error { t.Fatal("restored on stop"); return nil }
			e.Arm = func(Record) error { t.Fatal("armed on stop"); return nil }
			if where == "startup" {
				r.Step = Probation
				r.ProbationStarts = 1
				stopped = true
				if err := WriteRecord(e.Path, r); err != nil {
					t.Fatal(err)
				}
				if _, err := e.Start(r.To, "/new"); !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			} else {
				if where == "handover" {
					e.Handover = func(context.Context, Record) error { stopped = true; return errors.New("handover") }
				} else {
					e.Exec = func(string, []string) error { stopped = true; return errors.New("exec") }
				}
				if err := e.Request(r); err != nil {
					t.Fatal(err)
				}
				if err := e.FinishDrain(context.Background(), "upgrade"); !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			}
			got, _ := ReadRecord(e.Path)
			if got.Step != StoppedInProbation || got.RestartPending {
				t.Fatal(got)
			}
		})
	}
}

func TestArmFailureStillAttemptsSafeDirectRollback(t *testing.T) {
	e, r, _ := engineFixture(t)
	r.Step = Probation
	if err := WriteRecord(e.Path, r); err != nil {
		t.Fatal(err)
	}
	reported, restored, execed := false, false, false
	e.Report = func(string) { reported = true }
	e.Arm = func(Record) error { return errors.New("log permission") }
	e.Restore = func(Record) error { restored = true; return nil }
	e.Exec = func(string, []string) error { execed = true; return nil }
	_ = e.Fail("unhealthy")
	if !reported || !restored || !execed {
		t.Fatal(reported, restored, execed)
	}
}

func TestReceiverIdentityPublishedAtomicallyBeforeWatchdog(t *testing.T) {
	e, r, _ := engineFixture(t)
	r.Step = HandingOver
	r.PID = 10
	r.ProcessIdentity = "sender"
	r.StartedAt = time.Now()
	if err := WriteRecord(e.Path, r); err != nil {
		t.Fatal(err)
	}
	release, err := LockRecord(e.Path)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- e.BeginProbation(20, "receiver", []string{"serve"}, time.Second) }()
	// While the journal is owned, there is no partial receiver publication.
	got, _ := ReadRecord(e.Path)
	if got.PID != 10 || got.ProcessIdentity != "sender" {
		t.Fatal(got)
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	w := Watchdog{Path: e.Path, Attempt: r.StartedAt, Alive: func(pid int) bool { return pid == 20 }, Identity: func(pid int) string { return "receiver" }, Kill: func(int) error { t.Fatal("killed live receiver"); return nil }}
	if _, err := w.Tick(); err != nil {
		t.Fatal(err)
	}
	got, _ = ReadRecord(e.Path)
	if got.Step != Probation || got.PID != 20 || got.ProcessIdentity != "receiver" || len(got.Args) != 1 {
		t.Fatal(got)
	}
}

func TestInstalledTargetProbationSupersedesFailedRestart(t *testing.T) {
	e, r, _ := engineFixture(t)
	r.Step = InstallFailed
	r.RestartPending = true
	if err := WriteRecord(e.Path, r); err != nil {
		t.Fatal(err)
	}
	if err := e.BeginProbation(42, "receiver", nil, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := e.CheckHealth(context.Background(), time.Second, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	got, _ := ReadRecord(e.Path)
	if got.RestartPending || got.RecoveryStarting || got.Step != Healthy {
		t.Fatal(got)
	}
}

func TestStopDuringDirectRollbackClosesWithoutRestart(t *testing.T) {
	e, r, _ := engineFixture(t)
	r.Step = Probation
	if err := WriteRecord(e.Path, r); err != nil {
		t.Fatal(err)
	}
	stopped := false
	e.Interrupted = func() bool { return stopped }
	e.Close = func() error { stopped = true; return errors.New("shutdown") }
	e.Exec = func(string, []string) error { t.Fatal("exec despite stop"); return nil }
	e.Arm = func(Record) error { return nil }
	if err := e.Fail("unhealthy"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	got, _ := ReadRecord(e.Path)
	if got.Step != RollingBack || got.RestartPending || !got.OwnerStopped {
		t.Fatal(got)
	}
}
