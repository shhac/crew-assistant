package work

import (
	"context"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
)

func pauseProject(t *testing.T, a *Loop, id string, paused bool) {
	t.Helper()
	if _, err := a.Core.SetProjectPaused(context.Background(), id, paused); err != nil {
		t.Fatal(err)
	}
}

func TestProjectPauseIsolatesWorkAndBothPausesHoldIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	runner := &scriptedRunner{reviews: []string{pass, pass}}
	a, p, first := loopApp(t, runner, "")
	other, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Other", Template: "draft", Brief: core.BriefInput{Goal: "Other notes", Criteria: []string{"Clear"}}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.Core.QueueTask(ctx, other.ID, core.TaskInput{Objective: "Other note"})
	if err != nil {
		t.Fatal(err)
	}
	pauseProject(t, a, p.ID, true)
	step(t, a)
	snap, _ := a.Core.Snapshot(ctx)
	one, _ := snap.FindTask(first.ID)
	two, _ := snap.FindTask(second.ID)
	if one.Status != core.TaskQueued || len(one.Revisions) != 0 || len(two.Revisions) != 1 {
		t.Fatalf("isolation: %+v %+v", one, two)
	}
	if err := a.Core.SetPaused(ctx, true); err != nil {
		t.Fatal(err)
	}
	pauseProject(t, a, p.ID, false)
	n := len(runner.seen)
	step(t, a)
	if len(runner.seen) != n {
		t.Fatal("project resume bypassed global pause")
	}
	pauseProject(t, a, p.ID, true)
	if err := a.Core.SetPaused(ctx, false); err != nil {
		t.Fatal(err)
	}
	step(t, a)
	snap, _ = a.Core.Snapshot(ctx)
	one, _ = snap.FindTask(first.ID)
	if one.Status != core.TaskQueued {
		t.Fatal("global resume bypassed project pause")
	}
	pauseProject(t, a, p.ID, false)
	step(t, a)
	snap, _ = a.Core.Snapshot(ctx)
	one, _ = snap.FindTask(first.ID)
	if len(one.Revisions) != 1 {
		t.Fatalf("did not resume: %+v", one)
	}
}

func TestProjectPauseLetsRunningStepRecordItsResult(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	started, release := make(chan struct{}), make(chan struct{})
	runner := &scriptedRunner{reviews: []string{pass}, onWriter: func(string) { close(started); <-release }}
	a, p, task := loopApp(t, runner, "")
	_, done, err := a.pass(ctx, true)
	if err != nil || len(done) != 1 {
		t.Fatalf("pass: %v %d", err, len(done))
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("writer never started")
	}
	pauseProject(t, a, p.ID, true)
	close(release)
	select {
	case panic := <-done[0]:
		if panic != nil {
			t.Fatal(panic)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not finish")
	}
	snap, _ := a.Core.Snapshot(ctx)
	got, _ := snap.FindTask(task.ID)
	if len(got.Revisions) != 1 || got.Status != core.TaskReviewing || len(got.Claims) != 0 {
		t.Fatalf("running result: %+v", got)
	}
	step(t, a)
	if len(runner.seen) != 1 {
		t.Fatal("next step started while paused")
	}
	pauseProject(t, a, p.ID, false)
	step(t, a)
	if len(runner.seen) != 2 {
		t.Fatal("next step did not resume")
	}
}

func TestProjectPauseHoldsPMAndTriageIncludingNoPM(t *testing.T) {
	t.Parallel()
	for _, noPM := range []bool{false, true} {
		t.Run(map[bool]string{false: "PM", true: "no PM"}[noPM], func(t *testing.T) {
			ctx := context.Background()
			runner := &scriptedRunner{}
			a, p, _, triage := pmTeam(t, runner)
			if noPM {
				pb := *p.Playbook
				var roles []core.Role
				for _, r := range pb.Roles {
					if !r.Holds(core.RolePM) {
						roles = append(roles, r)
					}
				}
				pb.Roles = roles
				if _, err := a.Core.SetPlaybook(ctx, p.ID, pb); err != nil {
					t.Fatal(err)
				}
			}
			pauseProject(t, a, p.ID, true)
			step(t, a)
			snap, _ := a.Core.Snapshot(ctx)
			got, _ := snap.FindTask(triage.ID)
			if len(runner.seen) != 0 || got.Status != core.TaskTriage || !snap.Projects[0].PMDue {
				t.Fatalf("PM/triage changed: %+v", snap)
			}
			pauseProject(t, a, p.ID, false)
			step(t, a)
			snap, _ = a.Core.Snapshot(ctx)
			got, _ = snap.FindTask(triage.ID)
			if got.Status == core.TaskTriage {
				t.Fatal("triage did not resume")
			}
		})
	}
}

func TestProjectPauseHoldsOwnerAnswerAndReviewerMessage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	runner := &scriptedRunner{reviews: []string{pass, pass}}
	a, p, task := loopApp(t, runner, "")
	task = settle(t, a)
	m, err := a.MessageTeam(ctx, p.ID, task.ID, "reviewer", core.FromOwner, "Check again")
	if err != nil {
		t.Fatal(err)
	}
	pauseProject(t, a, p.ID, true)
	n := len(runner.seen)
	step(t, a)
	snap, _ := a.Core.Snapshot(ctx)
	got, _ := snap.FindTask(task.ID)
	if len(runner.seen) != n || got.Messages[0].Status != core.MessageWaiting {
		t.Fatal("message answered while paused")
	}
	pauseProject(t, a, p.ID, false)
	step(t, a)
	snap, _ = a.Core.Snapshot(ctx)
	got, _ = snap.FindTask(task.ID)
	if got.Messages[0].ID != m.ID || got.Messages[0].Status != core.MessageAnswered {
		t.Fatal("message did not resume")
	}
	task = settle(t, a)
	d := openDecision(t, a, task)
	if _, err := a.Core.ChooseDecision(ctx, d.ID, choiceApprove); err != nil {
		t.Fatal(err)
	}
	pauseProject(t, a, p.ID, true)
	step(t, a)
	snap, _ = a.Core.Snapshot(ctx)
	got, _ = snap.FindTask(task.ID)
	if got.Status != core.TaskWaiting {
		t.Fatal("answer applied while paused")
	}
	pauseProject(t, a, p.ID, false)
	step(t, a)
	snap, _ = a.Core.Snapshot(ctx)
	got, _ = snap.FindTask(task.ID)
	if got.Status == core.TaskWaiting {
		t.Fatal("answer did not resume")
	}
}

func TestProjectPauseHoldsRecordSettlementsUntilResume(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, p, first := loopApp(t, &scriptedRunner{}, "")
	a.Build = func() (string, bool) { return "synthetic-build", true }
	a.Includes = func(context.Context, string, string, string, string) (bool, error) { return true, nil }
	delivery, err := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Delivery"})
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Blocked"})
	if err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	if _, err := a.Core.UpdateTask(ctx, first.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.RetryAt, t.HeldFor, t.Detail = future, "claude", "Usage hold"
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Core.UpdateTask(ctx, delivery.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.Status, t.Delivering = core.TaskLanded, &core.Delivering{Revision: 1}
		t.Revisions = []core.Revision{{N: 1, Ref: "synthetic-change"}}
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Core.UpdateTask(ctx, blocker.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.Blockers = []core.Blocker{{ID: "condition", Kind: core.BlockerDaemonIncludes, Task: delivery.ID}}
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
	settleRecords := func() {
		t.Helper()
		snap, err := a.Core.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.settleDeliveries(ctx, snap); err != nil {
			t.Fatal(err)
		}
		if err := a.checkBlockers(ctx, snap); err != nil {
			t.Fatal(err)
		}
		if err := a.releaseUsageHolds(ctx, snap); err != nil {
			t.Fatal(err)
		}
	}
	pauseProject(t, a, p.ID, true)
	settleRecords()
	snap, _ := a.Core.Snapshot(ctx)
	held, _ := snap.FindTask(first.ID)
	intent, _ := snap.FindTask(delivery.ID)
	blocked, _ := snap.FindTask(blocker.ID)
	if held.HeldFor == "" || intent.Delivering == nil || blocked.Blockers[0].ClearedAt != nil {
		t.Fatal("record advanced while paused")
	}
	pauseProject(t, a, p.ID, false)
	settleRecords()
	snap, _ = a.Core.Snapshot(ctx)
	held, _ = snap.FindTask(first.ID)
	intent, _ = snap.FindTask(delivery.ID)
	blocked, _ = snap.FindTask(blocker.ID)
	if held.HeldFor != "" || intent.Delivering != nil || blocked.Blockers[0].ClearedAt == nil {
		t.Fatalf("settlements did not resume: %+v %+v %+v", held, intent, blocked)
	}
}
