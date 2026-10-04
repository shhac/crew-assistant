package upgrade

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestReceiverRejectsMissingInitialProcessIdentity(t *testing.T) {
	for _, recovery := range []bool{false, true} {
		e, r, _ := engineFixture(t)
		r.Step = HandingOver
		if recovery {
			r.Step, r.RestartPending = RolledBack, true
		}
		if err := WriteRecord(e.Path, r); err != nil {
			t.Fatal(err)
		}
		var err error
		if recovery {
			err = e.BeginRecovery(42, "", nil, time.Second)
		} else {
			err = e.BeginProbation(42, "", nil, time.Second)
		}
		if err == nil {
			t.Fatal("receiver accepted unconfirmed identity")
		}
		got, _ := ReadRecord(e.Path)
		if got.Step != r.Step || got.PID != r.PID || got.ProbationStarts != 0 {
			t.Fatal(got)
		}
	}
}

func TestWatchdogRefusesLivePIDWithoutPublishedIdentity(t *testing.T) {
	e, r, _ := engineFixture(t)
	r.Step, r.PID = Probation, 42
	r.Deadline = time.Now().Add(-time.Second)
	if err := WriteRecord(e.Path, r); err != nil {
		t.Fatal(err)
	}
	w := Watchdog{Path: e.Path, Alive: func(int) bool { return true }, Identity: func(int) string { return "birth" }, Kill: func(int) error { t.Fatal("killed PID without published identity"); return nil }}
	if done, err := w.Tick(); done || !errors.Is(err, errProcessInspection) {
		t.Fatal(done, err)
	}
}

func TestWatchdogIdentityNeverTurnsMismatchOrUnknownIntoKill(t *testing.T) {
	for _, identities := range [][]string{{"reused", ""}, {"original", ""}, {""}} {
		e, r, _ := engineFixture(t)
		r.Step, r.ProcessIdentity, r.PID = Probation, "original", 42
		r.Deadline = time.Now().Add(-time.Second)
		if err := WriteRecord(e.Path, r); err != nil {
			t.Fatal(err)
		}
		i := 0
		w := Watchdog{Path: e.Path, Alive: func(int) bool { return true }, Identity: func(int) string {
			value := identities[min(i, len(identities)-1)]
			i++
			return value
		}, Kill: func(int) error { t.Fatal("killed an unconfirmed or unrelated process"); return nil }, Acquire: func() (func(), bool, error) { return nil, false, nil }}
		done, err := w.Tick()
		if done || err != nil && !errors.Is(err, errProcessInspection) {
			t.Fatal(done, err)
		}
	}
}

func TestWatchdogReportsPersistentUnknownIdentityPastDeadline(t *testing.T) {
	e, r, _ := engineFixture(t)
	r.Step, r.ProcessIdentity, r.PID = Probation, "original", 42
	r.StartedAt = time.Now()
	r.Deadline = time.Now().Add(-time.Second)
	if err := WriteRecord(e.Path, r); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clock := &automaticIdentityClock{at: time.Now()}
	reports := []string{}
	w := Watchdog{Path: e.Path, Attempt: r.StartedAt, Clock: clock, Alive: func(int) bool { return true }, Identity: func(int) string { return "" }, Kill: func(int) error { t.Fatal("killed unknown PID"); return nil }, Report: func(message string) {
		reports = append(reports, message)
		if len(reports) == 2 {
			cancel()
		}
	}}
	_ = w.Run(ctx)
	if len(reports) != 2 || !strings.Contains(reports[0], "process identity") {
		t.Fatal(reports)
	}
	got, _ := ReadRecord(e.Path)
	if got.Step != Probation {
		t.Fatal(got)
	}
}

type automaticIdentityClock struct{ at time.Time }

func (c *automaticIdentityClock) Now() time.Time { return c.at }
func (c *automaticIdentityClock) NewTimer(time.Duration) Timer {
	c.at = c.at.Add(time.Minute)
	ch := make(chan time.Time, 1)
	ch <- c.at
	return &readyIdentityTimer{ch}
}

type readyIdentityTimer struct{ ch chan time.Time }

func (t *readyIdentityTimer) C() <-chan time.Time { return t.ch }
func (t *readyIdentityTimer) Stop() bool          { return true }
