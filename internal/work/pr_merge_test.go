//go:build !windows

package work

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
)

// By default the PM decides whether a ready pull request merges.
func TestThePMMergesAReadyPullRequest(t *testing.T) {
	s := newPRScenario(t, 2, withPM, opensUnasked, mergeBy(""))
	if task := s.current(t); task.Status != core.TaskAwaiting || !task.PROpen() {
		t.Fatalf("the pull request did not open: %s %s", task.Status, task.Detail)
	}
	s.readyPR(t)
	task := s.current(t)
	if task.Status != core.TaskLanded || len(s.gh.merges) != 1 {
		t.Fatalf("not merged on the PM's word: %s %s merges %v", task.Status, task.Detail, s.gh.merges)
	}
	if !activityHas(t, s.a, "The PM approved merging pull request #7") {
		t.Fatal("the PM's decision to merge is not in the activity")
	}
}

// The owner can keep merging for themselves; a pull request that changes
// while they decide is looked at again rather than merged as it was, and
// the task is not taken for closed.
func TestTheOwnerApprovesAMergeThatIsReconsideredWhenThePullRequestChanges(t *testing.T) {
	s := newPRScenario(t, 4, opensUnasked, mergeBy(core.ApproveBefore))
	s.current(t)
	s.readyPR(t)
	task := s.current(t)
	d := openDecision(t, s.a, task)
	if task.Status != core.TaskWaiting || task.Stage != core.StageReady || d.Title != "Merge pull request #7 for “Add A”" || !strings.Contains(d.Context, "Approving merges pull request #7") {
		t.Fatalf("not asked to merge: %s %s %q", task.Status, task.Stage, d.Title)
	}
	s.review(t, "One more thing: handle the nil case.", time.Now().Add(3*time.Minute))
	task = s.current(t)
	snap, _ := s.a.Core.Snapshot(s.ctx)
	if dismissed, _ := findDecision(snap, d.ID); dismissed.Status != core.DecisionDismissed || task.Status == core.TaskStopped || len(s.gh.merges) != 0 {
		t.Fatalf("decision %s, task %s %s, merges %v", dismissed.Status, task.Status, task.Detail, s.gh.merges)
	}
	if len(task.Revisions) != 2 {
		t.Fatalf("the review was not answered: %d revisions, %s", len(task.Revisions), task.Status)
	}
	s.readyPR(t)
	task = s.current(t)
	if d = openDecision(t, s.a, task); d.Title != "Merge pull request #7 for “Add A”" {
		t.Fatalf("not asked again: %q", d.Title)
	}
	s.a.Core.ChooseDecision(s.ctx, d.ID, choiceApprove)
	if task = s.current(t); task.Status != core.TaskLanded || len(s.gh.merges) != 1 || !slices.Contains(s.gh.merges[0], task.Revisions[1].Ref) {
		t.Fatalf("not merged on the owner's approval: %s merges %v", task.Status, s.gh.merges)
	}
}

// The PM can send a ready pull request back to the implementer with what
// still needs doing, rather than merging it or asking the owner; what the
// implementer does then is decided on afresh.
func TestThePMSendsAReadyPullRequestBack(t *testing.T) {
	s := newPRScenario(t, 4, withPM, opensUnasked, mergeBy(""))
	s.runner.mu.Lock()
	s.runner.pmLand = []string{`{"land": false, "reason": "it needs a changelog entry", "implementer": "Add a changelog entry for Feature."}`}
	s.runner.mu.Unlock()
	s.current(t)
	s.readyPR(t)
	task := s.current(t)
	if !slices.ContainsFunc(task.Direction, func(d string) bool { return strings.Contains(d, "changelog") }) || !activityHas(t, s.a, "sent Add A back to the implementer") {
		t.Fatalf("not sent back: direction %v", task.Direction)
	}
	snap, _ := s.a.Core.Snapshot(s.ctx)
	if slices.ContainsFunc(snap.Decisions, func(d core.Decision) bool { return d.TaskID == s.id }) {
		t.Fatal("the owner was asked although the PM decides")
	}
	// Only the draft made with the PM's note in view merged.
	if len(task.Revisions) != 2 || len(s.gh.merges) != 1 || !slices.Contains(s.gh.merges[0], task.Revisions[1].Ref) {
		t.Fatalf("revisions %d merges %v", len(task.Revisions), s.gh.merges)
	}
}

// A code freeze holds a ready pull request from merging, not from being
// watched; resuming merges it.
func TestAPausedProjectHoldsAReadyPullRequestUntilLandingResumes(t *testing.T) {
	s := newPRScenario(t, 2, opensUnasked, mergeBy(core.ApproveNone))
	if task := s.current(t); !task.PROpen() {
		t.Fatalf("the pull request did not open: %s %s", task.Status, task.Detail)
	}
	if _, err := s.a.Core.SetLandingPaused(s.ctx, s.p.ID, true, "release freeze"); err != nil {
		t.Fatal(err)
	}
	s.readyPR(t)
	task := s.current(t)
	if task.Status != core.TaskAwaiting || task.Stage != core.StageReady || !strings.Contains(task.Detail, "release freeze") || len(s.gh.merges) != 0 {
		t.Fatalf("not held: %s %s %q merges %v", task.Status, task.Stage, task.Detail, s.gh.merges)
	}
	if _, err := s.a.Core.SetLandingPaused(s.ctx, s.p.ID, false, ""); err != nil {
		t.Fatal(err)
	}
	if task = s.current(t); task.Status != core.TaskLanded || len(s.gh.merges) != 1 {
		t.Fatalf("not merged once landing resumed: %s %s merges %v", task.Status, task.Detail, s.gh.merges)
	}
}

// A merge GitHub refuses, such as for branch protection, comes to the owner
// rather than waiting on wakes that won't fire.
func TestARefusedMergeComesToTheOwner(t *testing.T) {
	s := newPRScenario(t, 2, opensUnasked, mergeBy(core.ApproveNone))
	s.current(t)
	run := s.a.github.Run
	s.a.github.Run = func(ctx context.Context, args ...string) ([]byte, error) {
		if strings.Join(args[:2], " ") == "pr merge" {
			return nil, fmt.Errorf("base branch policy prohibits the merge")
		}
		return run(ctx, args...)
	}
	s.readyPR(t)
	task := s.current(t)
	d := openDecision(t, s.a, task)
	if d.Kind != core.DecisionFailure || !strings.Contains(d.Context, "GitHub refused to merge pull request #7") || task.Delivering != nil {
		t.Fatalf("decision %s %q, delivering %+v", d.Kind, d.Context, task.Delivering)
	}
}

// Turning pull requests off has the PM choose for each task that started
// with them: one it moves lands the project's way, its pull request closed
// with a word on why, and is decided again before it lands.
func TestThePMMovesATaskOffPullRequestsWhenTheyAreTurnedOff(t *testing.T) {
	s := newPRScenario(t, 2, withPM, opensUnasked, mergeBy(core.ApproveBefore))
	if task := s.current(t); !task.PROpen() || task.Status != core.TaskAwaiting {
		t.Fatalf("the pull request did not open: %s %s", task.Status, task.Detail)
	}
	s.runner.mu.Lock()
	s.runner.pm = []string{fmt.Sprintf(`{"order": [], "pull_requests": [{"task": %q, "keep": false}], "note": "it hasn't been reviewed yet"}`, s.id)}
	s.runner.mu.Unlock()
	if _, err := s.a.SetLanding(s.ctx, s.p.ID, core.LandPolicy{Via: core.LandPush, Target: "main", Approve: core.ApproveBefore}); err != nil {
		t.Fatal(err)
	}
	task := s.current(t)
	closed := slices.IndexFunc(s.gh.posts, func(args []string) bool { return args[0] == "pr" && args[1] == "close" })
	if closed < 0 || !strings.Contains(argAfter(s.gh.posts[closed], "--comment"), "will land another way") {
		t.Fatalf("the pull request was not closed: %v", s.gh.posts)
	}
	if task.UsesPRs() || task.Proposal != nil || task.ClosePR != nil || task.Status != core.TaskWaiting {
		t.Fatalf("not moved off pull requests: %s %+v %+v", task.Status, task.Proposal, task.ClosePR)
	}
	if d := openDecision(t, s.a, task); d.Title != "Land “Add A” on main" {
		t.Fatalf("not asked to land it the new way: %q", d.Title)
	}
	if !activityHas(t, s.a, "The PM moved Add A off pull requests") {
		t.Fatal("the PM's choice is not in the activity")
	}
}

// Without a PM the owner is asked, and keeping its pull request leaves the
// task as it started.
func TestTheOwnerKeepsATaskOnItsPullRequestWhenTheyAreTurnedOff(t *testing.T) {
	s := newPRScenario(t, 2, opensUnasked, mergeBy(core.ApproveBefore))
	s.current(t)
	if _, err := s.a.SetLanding(s.ctx, s.p.ID, core.LandPolicy{Via: core.LandPush, Target: "main"}); err != nil {
		t.Fatal(err)
	}
	snap, _ := s.a.Core.Snapshot(s.ctx)
	i := slices.IndexFunc(snap.Decisions, func(d core.Decision) bool { return d.TaskID == s.id && d.Kind == core.DecisionPRFlow })
	if i < 0 || snap.Decisions[i].Status != core.DecisionOpen {
		t.Fatalf("the owner was not asked: %+v", snap.Decisions)
	}
	if _, err := s.a.Core.ChooseDecision(s.ctx, snap.Decisions[i].ID, core.ChoiceKeepPR); err != nil {
		t.Fatal(err)
	}
	task := s.current(t)
	if !task.UsesPRs() || !task.PROpen() || task.Status != core.TaskAwaiting || task.ClosePR != nil {
		t.Fatalf("keeping changed the task: %s %+v", task.Status, task.Proposal)
	}
}

// A pull request that won't close is tried again next pass, one already
// merged needs nothing more, and one GitHub keeps refusing is left to the
// owner rather than tried for ever.
func TestClosingAnEndedPullRequestIsTriedAFewTimes(t *testing.T) {
	s := newPRScenario(t, 2)
	s.open(t)
	closeTo := func(c core.PRClose) {
		t.Helper()
		if _, err := s.a.Core.UpdateTask(s.ctx, s.id, func(t *core.Task, _ *core.Project) (string, error) {
			t.ClosePR = &c
			return "", nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	pass := func() core.Task {
		t.Helper()
		snap, _ := s.a.Core.Snapshot(s.ctx)
		s.a.closeEndedPRs(s.ctx, snap)
		snap, _ = s.a.Core.Snapshot(s.ctx)
		task, _ := snap.FindTask(s.id)
		return task
	}
	run := s.a.github.Run
	refuse := true
	s.a.github.Run = func(ctx context.Context, args ...string) ([]byte, error) {
		if refuse && strings.Join(args[:2], " ") == "pr close" {
			return nil, fmt.Errorf("could not close")
		}
		return run(ctx, args...)
	}
	closeTo(core.PRClose{Repo: "o/r", Number: 7, Note: "Landing another way."})
	if task := pass(); task.ClosePR == nil || task.ClosePR.Failures != 1 {
		t.Fatalf("after a refusal: %+v", task.ClosePR)
	}
	if task := pass(); task.ClosePR == nil || task.ClosePR.Failures != 2 {
		t.Fatalf("after two: %+v", task.ClosePR)
	}
	if task := pass(); task.ClosePR != nil || !activityHas(t, s.a, "close it yourself") {
		t.Fatalf("not left to the owner: %+v", task.ClosePR)
	}
	// Already merged: closing fails, but there's nothing left to close.
	s.gh.set(func() { s.gh.merged = "abc" })
	closeTo(core.PRClose{Repo: "o/r", Number: 7, Note: "Landing another way."})
	if task := pass(); task.ClosePR != nil {
		t.Fatalf("a merged pull request was tried again: %+v", task.ClosePR)
	}
}
