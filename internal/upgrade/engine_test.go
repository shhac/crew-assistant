package upgrade

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func engineFixture(t *testing.T) (*Engine, Record, *[]string) {
	t.Helper()
	path := RecordPath(filepath.Join(t.TempDir(), "state.db"))
	calls := []string{}
	assert := func(step Step, call string) {
		t.Helper()
		r, err := ReadRecord(path)
		if err != nil || r == nil || r.Step != step {
			t.Fatalf("%s before journal: %+v %v", call, r, err)
		}
		calls = append(calls, call)
	}
	e := &Engine{Path: path, Now: func() time.Time { return time.Unix(100, 0) }}
	e.Drain = func() bool { assert(Draining, "drain"); return true }
	e.Backup = func(context.Context, Record) error { assert(BackingUp, "backup"); return nil }
	e.Install = func(ctx context.Context, r Record) (string, error) {
		if ctx.Err() != nil {
			t.Fatal("installer inherited cancelled drain")
		}
		assert(Installing, "install")
		return r.To, nil
	}
	e.Handover = func(context.Context, Record) error { assert(HandingOver, "handover"); return nil }
	e.Close = func() error { calls = append(calls, "close"); return nil }
	e.Restore = func(Record) error { assert(RollingBack, "restore"); return nil }
	e.Exec = func(binary string, _ []string) error { calls = append(calls, "exec:"+binary); return nil }
	r := Record{From: "v1.0.0", To: "v2.0.0", Prefix: "/fixture", SavedBinary: "/saved/previous", Args: []string{"serve"}}
	return e, r, &calls
}

func TestRequestsDuringHealthReturnConflictWithoutWaiting(t *testing.T) {
	e, r, _ := engineFixture(t)
	r.Step = HandingOver
	if err := WriteRecord(e.Path, r); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- e.CheckHealth(context.Background(), time.Second, func(context.Context) error { close(entered); <-release; return nil })
	}()
	<-entered
	request := make(chan error, 1)
	go func() { request <- e.Request(r) }()
	select {
	case err := <-request:
		if !errors.Is(err, ErrInProgress) {
			t.Fatal(err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("request blocked on health probe")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRefusedDrainIsStoppingAndAutomaticDoesNotRetryFailure(t *testing.T) {
	e, r, _ := engineFixture(t)
	e.Drain = func() bool { return false }
	if err := e.Request(r); !errors.Is(err, ErrStopping) || errors.Is(err, ErrInProgress) {
		t.Fatal(err)
	}
	r.Step, r.Failure = RolledBack, "failed health"
	if err := WriteRecord(e.Path, r); err != nil {
		t.Fatal(err)
	}
	r.Automatic = true
	if err := e.Request(r); !errors.Is(err, ErrFailedVersion) {
		t.Fatal(err)
	}
	r.Automatic = false
	e.Drain = func() bool { return true }
	if err := e.Request(r); err != nil {
		t.Fatal(err)
	}
}

func TestRetryKeepsPreviousPinUntilBackupsAndHandover(t *testing.T) {
	e, r, _ := engineFixture(t)
	r.Step, r.Pinned, r.Failure = RolledBack, true, "previous health failure"
	if err := WriteRecord(e.Path, r); err != nil {
		t.Fatal(err)
	}
	previous := r.SavedBinary
	r.SavedBinary = "/saved/new-attempt"
	if err := e.Request(r); err != nil {
		t.Fatal(err)
	}
	got, _ := ReadRecord(e.Path)
	if !got.Pinned || got.PinBinary != previous {
		t.Fatal("pin was cleared before retry backups", got)
	}
	if _, err := e.Start(r.To, "/installed"); err == nil {
		t.Fatal("pin exec returned")
	}
	// Start used a fake exec, so the durable record must still be draining.
	got, _ = ReadRecord(e.Path)
	if got.Step != Draining {
		t.Fatal(got)
	}
	if err := e.FinishDrain(context.Background(), "upgrade"); err != nil {
		t.Fatal(err)
	}
	got, _ = ReadRecord(e.Path)
	if got.Pinned || got.PinBinary != "" || got.Step != HandingOver {
		t.Fatal(got)
	}
}

func TestBeginProbationBeforeMigrationMakesCrashRestore(t *testing.T) {
	e, r, calls := engineFixture(t)
	r.Step = Installing
	if err := WriteRecord(e.Path, r); err != nil {
		t.Fatal(err)
	}
	if err := e.BeginProbation(42, "fixture", nil, time.Second); err != nil {
		t.Fatal(err)
	}
	got, _ := ReadRecord(e.Path)
	if got.Step != Probation || got.ProbationStarts != 1 || got.PID != 42 || got.Deadline.IsZero() {
		t.Fatal(got)
	}
	// The new version may have migrated state now, before API health.
	if _, err := e.Start(r.From, r.SavedBinary); err == nil {
		t.Fatal("old version allowed to open migrated state")
	}
	got, _ = ReadRecord(e.Path)
	if got.Step != RolledBack || !got.Pinned {
		t.Fatal(got, *calls)
	}
}

func TestEngineJournalPrecedesEffectsAndDrainWaits(t *testing.T) {
	e, r, calls := engineFixture(t)
	if err := e.Request(r); err != nil {
		t.Fatal(err)
	}
	if err := e.Request(r); !errors.Is(err, ErrInProgress) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*calls, []string{"drain"}) {
		t.Fatal("installed before running turn finished", *calls)
	}
	if err := e.FinishDrain(context.Background(), "upgrade"); err != nil {
		t.Fatal(err)
	}
	want := []string{"drain", "backup", "install", "handover", "exec:/fixture/opt/crew-assistant/bin/crew-assistant"}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatal(*calls)
	}
	if err := e.CheckHealth(context.Background(), time.Second, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	got, _ := ReadRecord(e.Path)
	if got.Step != Healthy || got.Pinned || got.ProbationStarts != 1 {
		t.Fatal(got)
	}
}

func TestSignalAbandonsUpgradeBeforeBackup(t *testing.T) {
	e, r, calls := engineFixture(t)
	if err := e.Request(r); err != nil {
		t.Fatal(err)
	}
	if err := e.FinishDrain(context.Background(), "signal"); err != nil {
		t.Fatal(err)
	}
	got, _ := ReadRecord(e.Path)
	if got.Step != Abandoned || len(*calls) != 1 {
		t.Fatal(got, *calls)
	}
}

func TestFailedHealthRestoresBeforePreviousBinary(t *testing.T) {
	e, r, calls := engineFixture(t)
	if err := e.Request(r); err != nil {
		t.Fatal(err)
	}
	if err := e.FinishDrain(context.Background(), "upgrade"); err != nil {
		t.Fatal(err)
	}
	*calls = nil
	if err := e.CheckHealth(context.Background(), time.Millisecond, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}); !errors.Is(err, ErrRolledBack) {
		t.Fatal(err)
	}
	got, _ := ReadRecord(e.Path)
	if got.Step != RolledBack || !got.Pinned || got.Failure == "" {
		t.Fatal(got)
	}
	if !reflect.DeepEqual(*calls, []string{"close", "restore", "exec:/saved/previous"}) {
		t.Fatal(*calls)
	}
}

func TestHealthBoundIncludesUnresponsiveProbe(t *testing.T) {
	e, r, _ := engineFixture(t)
	r.Step = HandingOver
	if err := WriteRecord(e.Path, r); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	defer close(release)
	done := make(chan error, 1)
	go func() {
		done <- e.CheckHealth(context.Background(), time.Millisecond, func(context.Context) error { <-release; return nil })
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrRolledBack) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("unresponsive probe bypassed health bound")
	}
	got, _ := ReadRecord(e.Path)
	if got.Step != RolledBack || !got.Pinned {
		t.Fatal(got)
	}
}

func TestRollbackDoesNotExecIfCloseOrRestoreFails(t *testing.T) {
	for _, failClose := range []bool{true, false} {
		e, r, calls := engineFixture(t)
		r.Step = Probation
		if err := WriteRecord(e.Path, r); err != nil {
			t.Fatal(err)
		}
		failure := errors.New("fixture failure")
		if failClose {
			e.Close = func() error { return failure }
		} else {
			e.Restore = func(Record) error { return failure }
		}
		if err := e.CheckHealth(context.Background(), time.Second, func(context.Context) error { return failure }); !errors.Is(err, failure) {
			t.Fatal(err)
		}
		got, _ := ReadRecord(e.Path)
		if got.Step != RollingBack {
			t.Fatal(got)
		}
		for _, call := range *calls {
			if call == "exec:/saved/previous" {
				t.Fatal("old daemon started without restored state")
			}
		}
	}
}

func TestOwnerStopDuringProbationSecondStartRollsBack(t *testing.T) {
	e, r, _ := engineFixture(t)
	r.Step = HandingOver
	if err := WriteRecord(e.Path, r); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := e.CheckHealth(ctx, time.Second, func(context.Context) error { cancel(); return ctx.Err() }); err != nil {
		t.Fatal(err)
	}
	got, _ := ReadRecord(e.Path)
	if got.Step != StoppedInProbation {
		t.Fatal(got)
	}
	if err := e.CheckHealth(context.Background(), time.Second, func(context.Context) error { return nil }); !errors.Is(err, ErrRolledBack) {
		t.Fatal(err)
	}
	got, _ = ReadRecord(e.Path)
	if got.Step != RolledBack || !got.Pinned || got.ProbationStarts != 1 {
		t.Fatal(got)
	}
}

func TestInstallFailureWithNewVersionContinuesToProbation(t *testing.T) {
	for _, installed := range []string{"v1.0.0", "v2.0.0", ""} {
		e, r, calls := engineFixture(t)
		e.Install = func(context.Context, Record) (string, error) { return installed, errors.New("safe installer failure") }
		if err := e.Request(r); err != nil {
			t.Fatal(err)
		}
		if err := e.FinishDrain(context.Background(), "upgrade"); err != nil {
			t.Fatal(err)
		}
		got, _ := ReadRecord(e.Path)
		if installed == r.To {
			if got.Step != HandingOver {
				t.Fatal(got)
			}
		} else if got.Step != InstallFailed || got.Pinned || (*calls)[len(*calls)-1] != "exec:/saved/previous" {
			t.Fatal(got, *calls)
		}
	}
}
