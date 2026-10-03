package work

import (
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
)

// A check the machine slept through may have failed on timeouts that kept
// counting with the lid shut, so a failure is checked again once; a pass
// stands.
func TestACheckThatSleptThroughAFailureRunsAgain(t *testing.T) {
	t.Parallel()
	runner := &scriptedRunner{reviews: []string{revise, pass}}
	a, _, _ := loopApp(t, runner, t.TempDir())
	a.slept = func(time.Time) time.Duration { return time.Hour }
	task := settle(t, a)
	if len(task.Revisions) != 1 || len(task.Verdicts) != 1 || task.Verdicts[0].Outcome != core.VerdictPass {
		t.Fatalf("drafts %d, verdicts %+v", len(task.Revisions), task.Verdicts)
	}
}

func TestACheckThatStayedAwakeKeepsItsFailure(t *testing.T) {
	t.Parallel()
	runner := &scriptedRunner{reviews: []string{revise, pass}}
	a, _, _ := loopApp(t, runner, t.TempDir())
	a.slept = func(time.Time) time.Duration { return time.Second }
	task := settle(t, a)
	if len(task.Revisions) != 2 || task.Verdicts[0].Outcome != core.VerdictRevise {
		t.Fatalf("drafts %d, verdicts %+v", len(task.Revisions), task.Verdicts)
	}
}

// The real clocks: no sleep has happened in the moment since now.
func TestNoSleepIsSeenWhileAwake(t *testing.T) {
	// Serial: checks a real wall-clock bound without competing package tests.
	var lp Loop
	if slept := lp.sleptSince(time.Now()); slept > time.Second || slept < -time.Second {
		t.Fatalf("slept %v", slept)
	}
}
