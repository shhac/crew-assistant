//go:build !windows

package work

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
)

// pushTask is a code task on a project that lands by pushing onto main,
// run until its approved-to-be draft waits for the owner.
func pushTask(t *testing.T) (*Loop, core.Project, core.Task, string) {
	t.Helper()
	source := ownerRepo(t)
	runner := &codeRunner{scriptedRunner: scriptedRunner{reviews: []string{pass, pass, pass, pass}}}
	a, _, _ := loopApp(t, &runner.scriptedRunner, "")
	a.runner = runner
	ctx := context.Background()
	p := codeProject(t, a, source)
	if _, err := a.SetLanding(ctx, p.ID, core.LandPolicy{Via: core.LandPush, Target: "main"}); err != nil {
		t.Fatal(err)
	}
	task, err := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Add Feature"})
	if err != nil {
		t.Fatal(err)
	}
	settle(t, a)
	task = taskByID(t, a, task.ID)
	if task.Status != core.TaskWaiting || len(task.Revisions) == 0 {
		t.Fatalf("the task should wait for approval: %+v", task)
	}
	return a, p, task, source
}

// stopMidLanding records the landing's intent for the task's latest draft,
// as a landing does before pushing, with how the change is to land, and then
// stops the task.
func stopMidLanding(t *testing.T, a *Loop, p core.Project, task core.Task, method string) core.Revision {
	t.Helper()
	ctx := context.Background()
	r := task.Revisions[len(task.Revisions)-1]
	if _, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.Status, t.DecisionID, t.Approved = core.TaskLanding, "", r.N
		t.Delivering = &core.Delivering{Revision: r.N, At: time.Now().UTC()}
		if method != "" {
			t.LandDecision = &core.LandDecision{By: core.LandByPM, Land: true, Method: method, Reason: "nothing else waits on main", Revision: r.N, At: time.Now().UTC()}
		}
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.StopTask(ctx, p.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	return r
}

// A task stopped while its change is pushed onto the target, after the push
// but before it was recorded, is settled from the target: recorded as
// landed there, and nothing is pushed a second time.
func TestAStopWhilePushingRecordsTheChangeAsLanded(t *testing.T) {
	tests := []struct {
		name   string
		method string
		// onMain is what the push leaves on main, checked before settling.
		onMain func(t *testing.T, source, clone, start string, task core.Task, r core.Revision)
	}{
		{
			name:   "squashed into one commit",
			method: "",
			onMain: func(t *testing.T, source, clone, start string, task core.Task, r core.Revision) {
				if !landedAsOne(t, source, clone, "main", task) || ownerGit(t, source, "rev-parse", "main^") != start {
					t.Fatal("the push should leave the change as one commit on main")
				}
			},
		},
		{
			name:   "fast-forwarded keeping the task's commits",
			method: core.LandKeepCommits,
			onMain: func(t *testing.T, source, _, _ string, _ core.Task, r core.Revision) {
				if got := ownerGit(t, source, "rev-parse", "main"); got != r.Ref {
					t.Fatalf("main is at %s, want fast-forwarded to the draft %s", got, r.Ref)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, p, task, source := pushTask(t)
			ctx := context.Background()
			start := ownerGit(t, source, "rev-parse", "main")
			r := stopMidLanding(t, a, p, task, tc.method)

			// The push, already under way, goes to its end.
			m, stopped := a.testMedium(t, p.ID, task.ID)
			if stopped.Status != core.TaskStopped || stopped.Delivering == nil {
				t.Fatalf("the task should be stopped with its landing under way: %+v", stopped)
			}
			if tc.method == core.LandKeepCommits && !keepsCommits(stopped) {
				t.Fatal("the PM's choice to keep the commits did not hold")
			}
			target, err := m.deliver(ctx, stopped, r)
			if err != nil {
				t.Fatal(err)
			}
			if target != "main" {
				t.Fatalf("delivered to %q, want main", target)
			}
			tc.onMain(t, source, filepath.Join(p.ScratchDirectory, "clone"), start, stopped, r)
			pushed := ownerGit(t, source, "rev-parse", "main")
			commits := ownerGit(t, source, "rev-list", "--count", "main")

			step(t, a)
			done := taskByID(t, a, task.ID)
			if done.Status != core.TaskLanded || done.DeliveredTo != "main" || done.Delivering != nil {
				t.Fatalf("the pushed change should be recorded as landed on main: %+v", done)
			}
			if got := ownerGit(t, source, "rev-parse", "main"); got != pushed {
				t.Fatalf("settling moved main from %s to %s", pushed, got)
			}
			if got := ownerGit(t, source, "rev-list", "--count", "main"); got != commits {
				t.Fatalf("settling added commits to main: %s, was %s", got, commits)
			}
			snap, _ := a.Core.Snapshot(ctx)
			landed, _ := findProject(snap, p.ID)
			if landed.Landed == nil || landed.Landed.TaskID != task.ID || landed.Landed.Branch != "main" {
				t.Fatalf("the project should record what last landed: %+v", landed.Landed)
			}

			// Settled once, it is not settled again.
			step(t, a)
			if got := ownerGit(t, source, "rev-parse", "main"); got != pushed {
				t.Fatalf("a later step moved main to %s", got)
			}
		})
	}
}

// One stopped before its push went anywhere stays stopped, and main is
// left as it was.
func TestAStopBeforePushingLeavesTheTaskStopped(t *testing.T) {
	a, p, task, source := pushTask(t)
	start := ownerGit(t, source, "rev-parse", "main")
	stopMidLanding(t, a, p, task, "")
	step(t, a)
	got := taskByID(t, a, task.ID)
	if got.Status != core.TaskStopped || got.Delivering != nil || got.DeliveredTo != "" {
		t.Fatalf("a change that never went out should leave the task stopped: %+v", got)
	}
	if now := ownerGit(t, source, "rev-parse", "main"); now != start {
		t.Fatalf("settling pushed main from %s to %s", start, now)
	}
}
