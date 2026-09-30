package core

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

func addManual(t *testing.T, s *Service, p Project, task Task, by string, landing bool) Task {
	t.Helper()
	out, err := s.SetBlocker(testContext, BlockerInput{Project: p.ID, Task: task.ID, Kind: BlockerManual, Description: "the build is ready", By: by, LandingOnly: landing})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func TestExternalConditionsGateStartsAndSkipBlockedQueueHeads(t *testing.T) {
	for _, next := range []bool{false, true} {
		t.Run(map[bool]string{false: "Schedule", true: "NextTask"}[next], func(t *testing.T) {
			s, p, tasks := linkedProject(t)
			blocked := addManual(t, s, p, tasks[0], LinkedByOwner, false)
			if next {
				got, ok, err := s.NextTask(testContext)
				if err != nil || !ok || got.ID != tasks[1].ID {
					t.Fatalf("next: %+v %v %v", got, ok, err)
				}
			} else {
				got, err := s.Schedule(testContext, anyone)
				if err != nil || len(got) == 0 || got[0].Task.ID != tasks[1].ID {
					t.Fatalf("schedule: %+v %v", got, err)
				}
			}
			if taskByID(t, s, blocked.ID).Status != TaskQueued {
				t.Fatal("blocked head started")
			}
			finish(t, s, tasks[1].ID, TaskStopped)
			if _, err := s.ClearBlocker(testContext, p.ID, blocked.ID, blocked.Blockers[0].ID, LinkedByOwner, "ready"); err != nil {
				t.Fatal(err)
			}
			got, ok, err := s.NextTask(testContext)
			if err != nil || !ok || got.ID != blocked.ID {
				t.Fatalf("cleared: %+v %v %v", got, ok, err)
			}
		})
	}
	s, p, tasks := linkedProject(t)
	addManual(t, s, p, tasks[0], LinkedByPM, true)
	got, ok, err := s.NextTask(testContext)
	if err != nil || !ok || got.ID != tasks[0].ID {
		t.Fatalf("landing-only blocked start: %+v", got)
	}
}
func TestExternalBlockerPermissionsAndHistory(t *testing.T) {
	s, p, tasks := linkedProject(t)
	blocked := addManual(t, s, p, tasks[0], LinkedByOwner, false)
	id := blocked.Blockers[0].ID
	if _, err := s.ClearBlocker(testContext, p.ID, blocked.ID, id, LinkedByPM, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("team cleared owner's: %v", err)
	}
	cleared, err := s.ClearBlocker(testContext, p.ID, blocked.ID, id, LinkedByOwner, "ready")
	if err != nil || cleared.Blockers[0].ClearedAt == nil || cleared.Blockers[0].Description != "the build is ready" {
		t.Fatalf("history: %+v %v", cleared, err)
	}
	if _, err := s.ClearBlocker(testContext, p.ID, blocked.ID, id, LinkedByOwner, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("second clear: %v", err)
	}
	finish(t, s, blocked.ID, TaskWriting)
	if _, err := s.SetBlocker(testContext, BlockerInput{Project: p.ID, Task: blocked.ID, Kind: BlockerManual, Description: "ready", By: LinkedByPM}); !errors.Is(err, ErrConflict) {
		t.Fatalf("team start hold: %v", err)
	}
	addManual(t, s, p, blocked, LinkedByPM, true)
	addManual(t, s, p, blocked, LinkedByOwner, false)
	team := taskByID(t, s, blocked.ID).Blockers[1]
	if _, err := s.ClearBlocker(testContext, p.ID, blocked.ID, team.ID, LinkedByPM, ""); err != nil {
		t.Fatal(err)
	}
}
func TestExternalConditionsHoldLanding(t *testing.T) {
	s, _ := fixture(t)
	p, task := pmLandingTask(t, s, VerdictPass, VerdictPass)
	blocked := addManual(t, s, p, task, LinkedByOwner, true)
	if why := signedOffNow(t, s, task.ID); len(why) != 1 || !strings.Contains(why[0], "held until") {
		t.Fatalf("signoff: %v", why)
	}
	if taskByID(t, s, task.ID).PMDeciding {
		t.Fatal("PM can land blocked change")
	}
	if _, err := s.LandAheadOfPM(testContext, p.ID, task.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("owner landed: %v", err)
	}
	finish(t, s, task.ID, TaskLanding)
	got, err := s.Schedule(testContext, anyone)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range got {
		if g.Task.ID == task.ID && g.Claim.Step == TaskLanding {
			t.Fatal("claimed blocked landing")
		}
	}
	if wait := taskByID(t, s, task.ID).Waiting; wait == nil || wait.Kind != "blocker" {
		t.Fatalf("wait: %+v", wait)
	}
	if _, err := s.ClearBlocker(testContext, p.ID, task.ID, blocked.Blockers[0].ID, LinkedByOwner, ""); err != nil {
		t.Fatal(err)
	}
	got, err = s.Schedule(testContext, anyone)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, g := range got {
		if g.Task.ID == task.ID && g.Claim.Step == TaskLanding {
			found = true
		}
	}
	if !found {
		t.Fatalf("cleared landing not claimed: %+v", got)
	}
}
func TestDaemonConditionsValidateTargetsAndFenceClearing(t *testing.T) {
	s, _ := fixture(t)
	p, target := pmLandingTask(t, s, VerdictPass, VerdictPass)
	waiting, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Follow up"})
	if err != nil {
		t.Fatal(err)
	}
	otherP := newProject(t, s)
	other, _ := s.QueueTask(testContext, otherP.ID, TaskInput{Objective: "Elsewhere"})
	for _, id := range []string{waiting.ID, other.ID, "missing"} {
		if _, err := s.SetBlocker(testContext, BlockerInput{Project: p.ID, Task: waiting.ID, Other: id, Kind: BlockerDaemonIncludes, By: LinkedByOwner}); err == nil {
			t.Fatalf("accepted %s", id)
		}
	}
	docsTarget, err := s.QueueTask(testContext, otherP.ID, TaskInput{Objective: "A docs target"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetBlocker(testContext, BlockerInput{Project: otherP.ID, Task: other.ID, Other: docsTarget.ID, Kind: BlockerDaemonIncludes, By: LinkedByOwner}); err == nil {
		t.Fatal("docs accepted")
	}
	waiting, err = s.SetBlocker(testContext, BlockerInput{Project: p.ID, Task: waiting.ID, Other: target.Ref, Kind: BlockerDaemonIncludes, By: LinkedByOwner})
	if err != nil {
		t.Fatal(err)
	}
	id := waiting.Blockers[0].ID
	finish(t, s, target.ID, TaskLanded)
	if err := s.ClearDaemonBlocker(testContext, waiting.ID, id, target.ID, 2); err != nil {
		t.Fatal(err)
	}
	if taskByID(t, s, waiting.ID).Blockers[0].ClearedAt != nil {
		t.Fatal("stale revision cleared")
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := s.ClearDaemonBlocker(testContext, waiting.ID, id, target.ID, 1); err != nil {
			t.Error(err)
		}
	}()
	go func() {
		defer wg.Done()
		_, err := s.ClearBlocker(testContext, p.ID, waiting.ID, id, LinkedByOwner, "")
		if err != nil && !errors.Is(err, ErrConflict) {
			t.Error(err)
		}
	}()
	wg.Wait()
	b := taskByID(t, s, waiting.ID).Blockers[0]
	if b.ClearedAt == nil || (b.ClearedBy != "daemon" && b.ClearedBy != LinkedByOwner) {
		t.Fatalf("race: %+v", b)
	}
}

func TestResearchReturnsToQueueForAStartConditionOnly(t *testing.T) {
	for _, landing := range []bool{false, true} {
		t.Run(map[bool]string{false: "start", true: "landing"}[landing], func(t *testing.T) {
			s, p, tasks := linkedProject(t)
			finish(t, s, tasks[0].ID, TaskResearching)
			// A first research turn has no revision yet.
			_, err := s.UpdateTask(testContext, tasks[0].ID, func(t *Task, _ *Project) (string, error) { t.Revisions = nil; return "", nil })
			if err != nil {
				t.Fatal(err)
			}
			addManual(t, s, p, tasks[0], LinkedByPM, landing)
			got, err := s.RecordPlan(testContext, tasks[0].ID, Plan{Summary: "Build it"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			want := TaskQueued
			if landing {
				want = TaskWriting
			}
			if got.Status != want {
				t.Fatalf("got %s want %s", got.Status, want)
			}
		})
	}
}

func TestPMCannotOpenAHoldDecisionForAnExternalBlockerAddedDuringItsTurn(t *testing.T) {
	s, _ := fixture(t)
	p, task := pmLandingTask(t, s, VerdictPass, VerdictPass)
	addManual(t, s, p, task, LinkedByOwner, true)
	_, err := s.DecideLanding(testContext, task.ID, LandDecision{Land: false, Revision: 1, Reason: "wait for the build"}, DecisionInput{Title: "Land it?", Context: "Wait", Recommendation: "Wait", Choices: []string{"Approve", "Changes"}})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("PM escalated an external condition: %v", err)
	}
	snap, err := s.Snapshot(testContext)
	if err != nil {
		t.Fatal(err)
	}
	fresh, _ := snap.FindTask(task.ID)
	if fresh.DecisionID != "" || fresh.LandDecision != nil || fresh.Status != TaskDeciding {
		t.Fatalf("recorded a stale PM decision: %+v", fresh)
	}
	for _, d := range snap.Decisions {
		if d.TaskID == task.ID {
			t.Fatal("opened an owner decision")
		}
	}
}
