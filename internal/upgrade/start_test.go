package upgrade

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestStartRecoveryTable(t *testing.T) {
	for _, label := range []string{"", "fixture.launchd"} {
		for _, tc := range []struct {
			step                    Step
			running                 string
			wantStep                Step
			probation, failed, exec bool
		}{
			{Healthy, "v2.0.0", Healthy, false, false, false},
			{Abandoned, "v1.0.0", Abandoned, false, false, false},
			{Draining, "v1.0.0", Abandoned, false, false, false},
			{Draining, "v3.0.0", Abandoned, false, false, false},
			{BackingUp, "v1.0.0", Abandoned, false, false, false},
			{BackingUp, "v2.0.0", Abandoned, false, false, false},
			{Installing, "v1.0.0", InstallFailed, false, true, false},
			{InstallFailed, "v1.0.0", InstallFailed, false, true, false},
			{Installing, "v2.0.0", Installing, true, false, false},
			{HandingOver, "v2.0.0", HandingOver, true, false, false},
			{Probation, "v2.0.0", Probation, true, false, false},
			{StoppedInProbation, "v2.0.0", StoppedInProbation, true, false, false},
			{HandingOver, "v1.0.0", RolledBack, false, false, true},
			{Probation, "v1.0.0", RolledBack, false, false, true},
			{RollingBack, "v2.0.0", RolledBack, false, false, true},
			{HandingOver, "v3.0.0", RolledBack, false, false, true},
		} {
			t.Run(label+"/"+string(tc.step)+"/"+tc.running, func(t *testing.T) {
				e, r, calls := engineFixture(t)
				r.Step, r.LaunchdLabel = tc.step, label
				if err := WriteRecord(e.Path, r); err != nil {
					t.Fatal(err)
				}
				result, err := e.Start(tc.running, "/installed/crew-assistant")
				if (err != nil) != tc.exec {
					t.Fatal(result, err)
				}
				if result.Probation != tc.probation || result.Failed != tc.failed {
					t.Fatal(result)
				}
				got, _ := ReadRecord(e.Path)
				if got.Step != tc.wantStep {
					t.Fatal(got)
				}
				if tc.exec && (*calls)[len(*calls)-1] != "exec:/saved/previous" {
					t.Fatal(*calls)
				}
			})
		}
	}
}

func TestStartPinBeforeStateOpenAndDetachedMessage(t *testing.T) {
	e, r, calls := engineFixture(t)
	r.Step, r.Pinned, r.DetachedLog, r.Failure = RolledBack, true, "/fixture/rollback-serve.log", "API did not answer"
	if err := WriteRecord(e.Path, r); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Start(r.To, "/installed/crew-assistant"); err == nil {
		t.Fatal("returned exec would permit new binary to read rolled back state")
	}
	if len(*calls) != 1 || (*calls)[0] != "exec:/saved/previous" {
		t.Fatal(*calls)
	}
	result, err := e.Start(r.From, r.SavedBinary)
	if err != nil || !result.Failed {
		t.Fatal(result, err)
	}
	for _, text := range []string{"Rollback is in force", r.Failure, r.DetachedLog, "upgrade clear-rollback"} {
		if !strings.Contains(result.Message, text) {
			t.Fatal(result.Message)
		}
	}
	r.FailedDecisionOpened = true
	if err := WriteRecord(e.Path, r); err != nil {
		t.Fatal(err)
	}
	result, err = e.Start(r.From, r.SavedBinary)
	if err != nil || result.Failed {
		t.Fatal(result, err)
	}
}

func TestStartRepeatedCrashRollsBackAndRestoreFailureRefusesStart(t *testing.T) {
	e, r, _ := engineFixture(t)
	r.Step, r.ProbationStarts = Probation, 1
	if err := WriteRecord(e.Path, r); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Start(r.To, "/installed/crew-assistant"); err == nil {
		t.Fatal("second probation start accepted")
	}
	r.Step = RollingBack
	if err := WriteRecord(e.Path, r); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("unreadable backup")
	e.Restore = func(Record) error { return failure }
	if _, err := e.Start(r.From, r.SavedBinary); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	got, _ := ReadRecord(e.Path)
	if got.Step != RollingBack {
		t.Fatal(got)
	}
}

func TestStoppedProbationRestartRollsBackAndMissingPinNamesRecovery(t *testing.T) {
	e, r, _ := engineFixture(t)
	r.Step = HandingOver
	if err := WriteRecord(e.Path, r); err != nil {
		t.Fatal(err)
	}
	result, err := e.Start(r.To, "/installed")
	if err != nil || !result.Probation {
		t.Fatal(result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err = e.CheckHealth(ctx, time.Second, func(context.Context) error { cancel(); return ctx.Err() }); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Start(r.To, "/installed"); err == nil {
		t.Fatal("second startup did not roll back")
	}
	got, _ := ReadRecord(e.Path)
	if got.Step != RolledBack || !got.Pinned {
		t.Fatal(got)
	}
	e.Exec = func(string, []string) error { return os.ErrNotExist }
	if _, err = e.Start(r.To, "/installed"); err == nil || !strings.Contains(err.Error(), "clear-rollback --offline") {
		t.Fatal(err)
	}
}
