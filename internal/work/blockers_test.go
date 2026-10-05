//go:build !windows

package work

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/integrations/github"
)

func TestDaemonVersionConditionsFindSquashAndPreservedLandings(t *testing.T) {
	t.Parallel()
	for _, squash := range []bool{true, false} {
		t.Run(map[bool]string{true: "squash", false: "keep"}[squash], func(t *testing.T) {
			ctx := context.Background()
			lp, _, _ := loopApp(t, &scriptedRunner{}, "")
			source := ownerRepo(t)
			p := codeProject(t, lp, source)
			before := ownerGit(t, source, "rev-parse", "HEAD")
			target, err := lp.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "A change"})
			if err != nil {
				t.Fatal(err)
			}
			ownerGit(t, source, "checkout", "-q", "-b", "draft")
			if err := os.WriteFile(filepath.Join(source, "change.txt"), []byte("change"), 0600); err != nil {
				t.Fatal(err)
			}
			ownerGit(t, source, "add", "change.txt")
			ownerGit(t, source, "commit", "-qm", "draft")
			revision := ownerGit(t, source, "rev-parse", "HEAD")
			ownerGit(t, source, "checkout", "-q", "main")
			r := core.Revision{N: 1, Ref: revision}
			if squash {
				ownerGit(t, source, "merge", "--squash", "draft")
				ownerGit(t, source, "commit", "-qm", landingMessage(target, r))
			} else {
				ownerGit(t, source, "merge", "--ff-only", "draft")
			}
			landed := ownerGit(t, source, "rev-parse", "HEAD")
			_, err = lp.Core.UpdateTask(ctx, target.ID, func(task *core.Task, _ *core.Project) (string, error) {
				task.Status = core.TaskLanded
				task.Revisions = []core.Revision{r}
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			waiting, err := lp.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Use the new daemon"})
			if err != nil {
				t.Fatal(err)
			}
			waiting, err = lp.SetBlocker(ctx, core.BlockerInput{Project: p.ID, Task: waiting.ID, Other: target.ID, Kind: core.BlockerDaemonIncludes, By: core.LinkedByOwner})
			if err != nil {
				t.Fatal(err)
			}
			lp.Build = func() (string, bool) { return before, true }
			snap, _ := lp.Core.Snapshot(ctx)
			if _, started, err := lp.pass(ctx, true); err != nil || len(started) > 0 {
				t.Fatalf("blocked pass: %d starts, %v", len(started), err)
			}
			snap, _ = lp.Core.Snapshot(ctx)
			blocked, _ := snap.FindTask(waiting.ID)
			if blocked.Blockers[0].ClearedAt != nil || blocked.Blockers[0].Check == "" {
				t.Fatalf("old build: %+v", blocked.Blockers)
			}
			if _, ok, err := lp.Core.NextTask(ctx); err != nil || ok {
				t.Fatalf("blocked task started: %v %v", ok, err)
			}
			lp.Build = func() (string, bool) { return landed, true }
			if err := lp.checkBlockers(ctx, snap); err != nil {
				t.Fatal(err)
			}
			snap, _ = lp.Core.Snapshot(ctx)
			blocked, _ = snap.FindTask(waiting.ID)
			if blocked.Blockers[0].ClearedBy != "daemon" {
				t.Fatalf("new build: %+v", blocked.Blockers)
			}
			started, ok, err := lp.Core.NextTask(ctx)
			if err != nil || !ok || started.ID != waiting.ID {
				t.Fatalf("cleared task: %+v %v %v", started, ok, err)
			}
		})
	}
}

func TestDaemonCheckWaitsForLandingAndRetriesErrorsWithoutEscalation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	lp, _, _ := loopApp(t, &scriptedRunner{}, "")
	p := codeProject(t, lp, ownerRepo(t))
	target, _ := lp.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Change"})
	waiting, _ := lp.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Wait"})
	waiting, err := lp.SetBlocker(ctx, core.BlockerInput{Project: p.ID, Task: waiting.ID, Other: target.ID, Kind: core.BlockerDaemonIncludes, By: core.LinkedByOwner})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	lp.Includes = func(context.Context, string, string, string, string) (bool, error) {
		calls++
		return false, errors.New("missing object")
	}
	lp.Build = func() (string, bool) { return "build", true }
	check := func() core.Task {
		t.Helper()
		snap, _ := lp.Core.Snapshot(ctx)
		if err := lp.checkBlockers(ctx, snap); err != nil {
			t.Fatal(err)
		}
		snap, _ = lp.Core.Snapshot(ctx)
		got, _ := snap.FindTask(waiting.ID)
		return got
	}
	check()
	if calls != 0 {
		t.Fatal("checked unlanded task")
	}
	_, err = lp.Core.UpdateTask(ctx, target.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.Status = core.TaskLanded
		t.Revisions = []core.Revision{{N: 1, Ref: "revision"}}
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	lp.Build = func() (string, bool) { return "", false }
	if got := check(); !strings.Contains(got.Blockers[0].Check, "doesn't record") || calls != 0 {
		t.Fatalf("unstamped: %+v", got.Blockers)
	}
	lp.Build = func() (string, bool) { return "build", true }
	check()
	got := check()
	if calls != 1 || got.Blockers[0].ClearedAt != nil || got.Blockers[0].Check == "" {
		t.Fatalf("retry: %d %+v", calls, got.Blockers)
	}
	lp.blockerChecks.Range(func(k, v any) bool {
		c := v.(blockerCheck)
		c.retry = c.retry.Add(-2 * time.Minute)
		lp.blockerChecks.Store(k, c)
		return true
	})
	check()
	if calls != 2 {
		t.Fatalf("didn't retry: %d", calls)
	}
}

func TestPMBlockerToolsAndPrompts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	lp, p, task := loopApp(t, &scriptedRunner{}, "")
	pm := lp.managerTools(p.ID, core.Role{})
	args := map[string]string{"task_id": task.ID, "kind": "manual", "description": "the service is ready", "other_task_id": "", "holds": "landing"}
	if got := callTool(t, pm, "set_blocker", args); got.IsError {
		t.Fatal(got.Content)
	}
	snap, _ := lp.Core.Snapshot(ctx)
	task, _ = snap.FindTask(task.ID)
	id := task.Blockers[0].ID
	for _, tool := range []string{"read_task", "list_tasks"} {
		got := callTool(t, pm, tool, map[string]string{"task_id": task.ID})
		if got.IsError || !strings.Contains(got.Content, id) || !strings.Contains(got.Content, "held until") {
			t.Fatalf("%s: %+v", tool, got)
		}
	}
	if got := callTool(t, pm, "clear_blocker", map[string]string{"task_id": task.ID, "blocker_id": id}); got.IsError {
		t.Fatal(got.Content)
	}
	task, err := lp.SetBlocker(ctx, core.BlockerInput{Project: p.ID, Task: task.ID, Kind: core.BlockerManual, Description: "owner condition", LandingOnly: true, By: core.LinkedByOwner})
	if err != nil {
		t.Fatal(err)
	}
	if got := callTool(t, pm, "clear_blocker", map[string]string{"task_id": task.ID, "blocker_id": task.Blockers[1].ID}); !got.IsError {
		t.Fatal("PM cleared owner's")
	}
	if lines := strings.Join(blockerLines(task), "\n"); !strings.Contains(lines, "held until: owner condition") || !strings.Contains(lines, "(set by the owner)") {
		t.Fatal(lines)
	}
	for _, tools := range []roleTools{lp.answerTools(p.ID, core.Role{}), lp.toolsFor(task, core.RoleResearcher, core.Role{})} {
		if tools.offers("set_blocker") || tools.offers("clear_blocker") {
			t.Fatal("tools leaked")
		}
	}
}

func TestPMListPromptIncludesOwnerConditions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	lp, p, task := loopApp(t, &scriptedRunner{}, "")
	if _, err := lp.SetBlocker(ctx, core.BlockerInput{Project: p.ID, Task: task.ID, Kind: core.BlockerManual, Description: "a new build", By: core.LinkedByOwner}); err != nil {
		t.Fatal(err)
	}
	snap, _ := lp.Core.Snapshot(ctx)
	var b strings.Builder
	pmTasks(&b, snap, p)
	if !strings.Contains(b.String(), "held until: a new build") || !strings.Contains(b.String(), "(set by the owner)") {
		t.Fatal(b.String())
	}
}

func TestBlockerAddedAfterClaimStillStopsPullRequestMerge(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	lp, p, task := loopApp(t, &scriptedRunner{}, "")
	task, err := lp.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.Status = core.TaskLanding
		t.Revisions = []core.Revision{{N: 1, Ref: "abc"}}
		t.Playbook = &core.Playbook{Land: core.LandPolicy{PullRequests: true, Target: "main", Approve: core.ApproveNone}}
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lp.SetBlocker(ctx, core.BlockerInput{Project: p.ID, Task: task.ID, Kind: core.BlockerManual, Description: "a new build", LandingOnly: true, By: core.LinkedByOwner}); err != nil {
		t.Fatal(err)
	}
	lp.github = github.Client{Run: func(context.Context, ...string) ([]byte, error) {
		t.Fatal("merge reached the service while held")
		return nil, nil
	}}
	m := gitMedium{playbook: core.Playbook{Land: core.LandPolicy{PullRequests: true, Target: "main", GitHub: "o/r", Approve: core.ApproveNone}}}
	// The stale task is the one held by a landing already claimed before the owner added the condition.
	pr := github.PR{State: "OPEN", HeadRefOid: "abc", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN", ReviewDecision: "APPROVED"}
	err = lp.reactTo(ctx, p, task, m, task.Revisions[0], core.Proposal{Pushed: "abc"}, pr)
	if err != nil {
		t.Fatalf("blocked merge should wait quietly: %v", err)
	}
	snap, _ := lp.Core.Snapshot(ctx)
	held, _ := snap.FindTask(task.ID)
	// It waits on the pull request, still watched, rather than trying again
	// and again; clearing the condition looks at it again.
	if held.Delivering != nil || held.Status != core.TaskAwaiting || !strings.Contains(held.Detail, "a new build") {
		t.Fatalf("blocked merge changed delivery: %+v", held)
	}
	if _, err := lp.Core.ClearBlocker(ctx, p.ID, task.ID, held.Blockers[0].ID, core.LinkedByOwner, "built"); err != nil {
		t.Fatal(err)
	}
	snap, _ = lp.Core.Snapshot(ctx)
	if cleared, _ := snap.FindTask(task.ID); cleared.Status != core.TaskLanding {
		t.Fatalf("clearing the condition left it waiting: %s", cleared.Status)
	}
}

// A successful merge request can mean queued, rather than actually merged.
func TestPendingMergeHoldsBlockerChangesUntilOutcome(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	lp, p, task := loopApp(t, &scriptedRunner{}, "")
	task, err := lp.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.Status = core.TaskLanding
		t.Revisions = []core.Revision{{N: 1, Ref: "abc"}}
		t.Proposal = &core.Proposal{Number: 7, Pushed: "abc"}
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	lp.github = github.Client{Run: func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) > 1 && args[0] == "pr" && args[1] == "merge" {
			calls++
			return nil, nil
		}
		if len(args) > 1 && args[0] == "api" && args[1] == "graphql" {
			return []byte(`{"data":{"repository":{"pullRequest":{"state":"OPEN","headRefOid":"abc","reviewThreads":{"nodes":[]}}}}}`), nil
		}
		return []byte(`{"state":"OPEN","headRefOid":"abc"}`), nil
	}}
	m := gitMedium{github: lp.github, playbook: core.Playbook{Land: core.LandPolicy{PullRequests: true, Target: "main", GitHub: "o/r", Approve: core.ApproveNone}}}
	prop := core.Proposal{Pushed: "abc"}
	pr := github.PR{State: "OPEN", HeadRefOid: "abc", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN", ReviewDecision: "APPROVED"}
	if err := lp.reactTo(ctx, p, task, m, task.Revisions[0], prop, pr); err != nil {
		t.Fatal(err)
	}
	snap, _ := lp.Core.Snapshot(ctx)
	fresh, _ := snap.FindTask(task.ID)
	if fresh.Delivering == nil && !fresh.PRMergePending() || calls != 1 {
		t.Fatalf("successful request lost delivery intent: %+v calls=%d", fresh.Delivering, calls)
	}
	// Reconcile before considering new landing conditions. Until observation
	// proves it unmerged, the request still holds its durable delivery intent.
	blocker := core.BlockerInput{Project: p.ID, Task: task.ID, Kind: core.BlockerManual, Description: "a new build", LandingOnly: true, By: core.LinkedByPM}
	if _, err := lp.SetBlocker(ctx, blocker); !errors.Is(err, core.ErrConflict) {
		t.Fatal("unconfirmed delivery did not retain its hold", err)
	}
	if _, landed, err := mergedPR(ctx, m, fresh, fresh.Revisions[0]); !errors.Is(err, errDeliveryPending) || landed {
		t.Fatal("could not reconcile OPEN request", err)
	}
	if now := taskByID(t, lp, task.ID); now.Delivering == nil && !now.PRMergePending() {
		t.Fatal("OPEN cleared a pending delivery")
	}
	pr.MergeInFlight = true
	if err := lp.reactTo(ctx, p, fresh, m, fresh.Revisions[0], prop, pr); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("queued merge bypassed its blocker")
	}
	scheduled, err := lp.Core.Schedule(ctx, func(core.Role) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range scheduled {
		if s.Task.ID == task.ID {
			t.Fatal("blocked queued merge was claimed")
		}
	}
}

type blockedDeliveryMedium struct {
	medium
	delivered bool
}

func (m *blockedDeliveryMedium) deliveryNote(core.Task) string { return "The change lands locally." }

func (m *blockedDeliveryMedium) deliver(context.Context, core.Task, core.Revision) (string, error) {
	m.delivered = true
	return "delivered", nil
}

func TestBlockerAddedAfterClaimStopsLocalDeliveryQuietly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	lp, p, task := loopApp(t, &scriptedRunner{}, "")
	task, err := lp.Core.UpdateTask(ctx, task.ID, func(t *core.Task, p *core.Project) (string, error) {
		t.Status = core.TaskLanding
		t.Approved = 1
		t.Revisions = []core.Revision{{N: 1}}
		t.Playbook = p.Playbook
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lp.SetBlocker(ctx, core.BlockerInput{Project: p.ID, Task: task.ID, Kind: core.BlockerManual, Description: "a new build", LandingOnly: true, By: core.LinkedByOwner}); err != nil {
		t.Fatal(err)
	}
	m := &blockedDeliveryMedium{}
	if err := lp.land(ctx, p, task, m); err != nil {
		t.Fatalf("normal waiting reported as failure: %v", err)
	}
	if m.delivered {
		t.Fatal("delivered from a stale claim")
	}
	snap, _ := lp.Core.Snapshot(ctx)
	fresh, _ := snap.FindTask(task.ID)
	if fresh.Delivering != nil || fresh.Status != core.TaskLanding {
		t.Fatalf("changed while held: %+v", fresh)
	}
}

func TestLandTaskRefusesBlockedDeliveredChange(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	lp, p, task := loopApp(t, &scriptedRunner{}, "")
	if _, err := lp.SetBlocker(ctx, core.BlockerInput{Project: p.ID, Task: task.ID, Kind: core.BlockerManual, Description: "a new build", LandingOnly: true, By: core.LinkedByOwner}); err != nil {
		t.Fatal(err)
	}
	_, err := lp.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.Status = core.TaskDelivered
		t.Revisions = []core.Revision{{N: 1}}
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lp.LandTask(ctx, p.ID, task.ID); !errors.Is(err, core.ErrConflict) || !strings.Contains(err.Error(), "a new build") {
		t.Fatalf("owner landed a held change: %v", err)
	}
}

func TestStoppedAndRemovedDaemonTargetsLeaveAReasonWithoutGit(t *testing.T) {
	t.Parallel()
	for _, removed := range []bool{false, true} {
		t.Run(map[bool]string{false: "stopped", true: "removed"}[removed], func(t *testing.T) {
			ctx := context.Background()
			lp, _, _ := loopApp(t, &scriptedRunner{}, "")
			p := codeProject(t, lp, ownerRepo(t))
			target, err := lp.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Change"})
			if err != nil {
				t.Fatal(err)
			}
			waiting, err := lp.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Use it"})
			if err != nil {
				t.Fatal(err)
			}
			waiting, err = lp.SetBlocker(ctx, core.BlockerInput{Project: p.ID, Task: waiting.ID, Kind: core.BlockerDaemonIncludes, Other: target.ID, By: core.LinkedByOwner})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := lp.StopTask(ctx, p.ID, target.ID); err != nil {
				t.Fatal(err)
			}
			snap, _ := lp.Core.Snapshot(ctx)
			if removed {
				for i, t := range snap.Tasks {
					if t.ID == target.ID {
						snap.Tasks = append(snap.Tasks[:i], snap.Tasks[i+1:]...)
						break
					}
				}
			}
			lp.Build = func() (string, bool) { return "build", true }
			lp.Includes = func(context.Context, string, string, string, string) (bool, error) {
				t.Fatal("git was called for absent target")
				return false, nil
			}
			if err := lp.checkBlockers(ctx, snap); err != nil {
				t.Fatal(err)
			}
			snap, _ = lp.Core.Snapshot(ctx)
			fresh, _ := snap.FindTask(waiting.ID)
			reason := "was stopped"
			if removed {
				reason = "no longer here"
			}
			if fresh.Blockers[0].ClearedAt != nil || !strings.Contains(fresh.Blockers[0].Check, reason) {
				t.Fatalf("target reason: %+v", fresh.Blockers)
			}
		})
	}
}

func TestBlockerContextDistinguishesOwnerAndAssistant(t *testing.T) {
	t.Parallel()
	for _, by := range []string{core.LinkedByOwner, core.LinkedByAssistant} {
		lines := strings.Join(blockerLines(core.Task{Blockers: []core.Blocker{{ID: "b", Kind: core.BlockerManual, Description: "ready", By: by}}}), "\n")
		if !strings.Contains(lines, "(set by the "+by+")") {
			t.Fatal(lines)
		}
	}
}

func TestPMLandingWaitsForExternalConditionWithoutOwnerDecision(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{core.BlockerManual, core.BlockerDaemonIncludes} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			w := newPMPush(t, core.ApprovePM, "")
			target, err := w.a.Core.QueueTask(ctx, w.p.ID, core.TaskInput{Objective: "Earlier change"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.a.StopTask(ctx, w.p.ID, target.ID); err != nil {
				t.Fatal(err)
			}
			task, err := w.a.Core.QueueTask(ctx, w.p.ID, core.TaskInput{Objective: "Ready to land"})
			if err != nil {
				t.Fatal(err)
			}
			task, err = w.a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, p *core.Project) (string, error) {
				t.Status = core.TaskDeciding
				t.Playbook = p.Playbook
				t.Roles = p.Playbook.Roles
				t.Round = 1
				t.MaxRounds = 3
				t.Revisions = []core.Revision{{N: 1, Ref: "abc", BriefVersion: p.Brief.Version}}
				for _, checker := range t.Checkers() {
					t.Verdicts = append(t.Verdicts, core.Verdict{Role: checker.Name, Revision: 1, BriefVersion: p.Brief.Version, Outcome: core.VerdictPass})
				}
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			snap, _ := w.a.Core.Snapshot(ctx)
			if why := core.SignedOff(snap, task); len(why) > 0 {
				t.Fatal(why)
			}
			held, err := w.a.SetBlocker(ctx, core.BlockerInput{Project: w.p.ID, Task: task.ID, Kind: kind, Other: target.ID, Description: "a new daemon", LandingOnly: true, By: core.LinkedByOwner})
			if err != nil {
				t.Fatal(err)
			}
			if err := w.a.pmLanding(ctx, w.p, task, task.Revisions[0], &blockedDeliveryMedium{}); err != nil {
				t.Fatal(err)
			}
			scheduled, err := w.a.Core.Schedule(ctx, func(core.Role) string { return "" })
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range scheduled {
				if s.Task.ID == task.ID {
					t.Fatal("PM's blocked decision claimed again")
				}
			}
			snap, _ = w.a.Core.Snapshot(ctx)
			fresh, _ := snap.FindTask(task.ID)
			if w.asked() != 0 || fresh.DecisionID != "" || fresh.Status != core.TaskDeciding || fresh.Waiting == nil || fresh.Waiting.Kind != "blocker" {
				t.Fatalf("blocked PM escalated or kept running: %+v", fresh)
			}
			for _, d := range snap.Decisions {
				if d.TaskID == task.ID {
					t.Fatal("opened an owner decision")
				}
			}
			if _, err := w.a.ClearBlocker(ctx, w.p.ID, task.ID, held.Blockers[0].ID, core.LinkedByOwner, "ready"); err != nil {
				t.Fatal(err)
			}
			if err := w.a.pmLanding(ctx, w.p, task, task.Revisions[0], &blockedDeliveryMedium{}); err != nil {
				t.Fatal(err)
			}
			snap, _ = w.a.Core.Snapshot(ctx)
			fresh, _ = snap.FindTask(task.ID)
			if w.asked() != 1 || fresh.Status != core.TaskLanding || fresh.LandDecision == nil || fresh.LandDecision.By != core.LandByPM {
				t.Fatalf("PM approval skipped after clear: %+v", fresh)
			}
		})
	}
}
