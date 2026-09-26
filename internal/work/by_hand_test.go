package work

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/media/gitrepo"
)

func settleCode(t *testing.T, a *Loop, id string) core.Task {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		progressed, err := a.loopStep(ctx, false)
		if err != nil {
			t.Fatal(err)
		}
		if !progressed {
			break
		}
	}
	snap, _ := a.Core.Snapshot(ctx)
	for _, task := range snap.Tasks {
		if task.ID == id {
			return task
		}
	}
	t.Fatal("no task", id)
	return core.Task{}
}

// The owner takes a draft into their own repository, changes it by hand and
// hands it back approved: it becomes the next draft, marked as theirs, the
// decision on the old one closes, QA still checks it, and it lands.
func TestTheOwnersChangeByHandBecomesTheNextDraft(t *testing.T) {
	source := ownerRepo(t)
	runner := &codeRunner{scriptedRunner: scriptedRunner{reviews: []string{pass, pass, pass}}}
	a, _, _ := loopApp(t, &runner.scriptedRunner, "")
	a.runner = runner
	ctx := context.Background()
	p := codeProject(t, a, source)
	task, _ := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Add Feature"})
	task = settleCode(t, a, task.ID)
	if task.Status != core.TaskWaiting || len(task.Revisions) != 1 {
		t.Fatalf("before: %+v", task)
	}
	decisionID := task.DecisionID

	place, err := a.Place(ctx, p.ID, task.ID)
	if err != nil || place.Draft == nil || place.Draft.N != 1 || place.Branch != task.Branch || place.Workspace == "" || place.Running {
		t.Fatalf("place %+v, %v", place, err)
	}
	if err := gitrepo.CheckoutDraft(ctx, place.Repo, place.Workspace, place.Draft.Ref, place.Branch, false); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(t.TempDir(), "by-hand")
	if err := gitrepo.AddWorktree(ctx, place.Repo, worktree, place.Branch); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(worktree, "feature.go"), []byte("package main\n\n// Feature, tidied by hand.\nfunc Feature() {}\n"), 0o600)
	ownerGit(t, worktree, "commit", "-q", "-am", "Tidy Feature by hand")

	adopted, err := a.AdoptDraft(ctx, p.ID, task.ID, "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	draft := adopted.Revisions[1]
	if draft.N != 2 || draft.By != core.DraftByOwner || draft.Summary != "Tidy Feature by hand" || adopted.Approved != 2 || adopted.Status != core.TaskReviewing {
		t.Fatalf("adopted %+v", adopted)
	}
	snap, _ := a.Core.Snapshot(ctx)
	if d, _ := findDecision(snap, decisionID); d.Status != core.DecisionDismissed {
		t.Fatalf("the decision on the old draft stayed %s", d.Status)
	}

	checks := len(runner.seen)
	landed := settleCode(t, a, task.ID)
	if landed.Status != core.TaskDelivered || len(runner.seen) != checks+1 || !strings.Contains(runner.seen[len(runner.seen)-1].Prompt, "Run exactly this") {
		t.Fatalf("after: status %s, %d more turns", landed.Status, len(runner.seen)-checks)
	}
	if got := ownerGit(t, source, "show", landed.DeliveredTo+":feature.go"); !strings.Contains(got, "tidied by hand") {
		t.Fatalf("delivered %s", got)
	}
}

// A change by hand has to build on where the task started, and can't be
// taken while a role is at work on the task.
func TestAChangeByHandIsRefusedWhenItCantCount(t *testing.T) {
	source := ownerRepo(t)
	runner := &codeRunner{scriptedRunner: scriptedRunner{reviews: []string{pass, pass}}}
	a, _, _ := loopApp(t, &runner.scriptedRunner, "")
	a.runner = runner
	ctx := context.Background()
	p := codeProject(t, a, source)
	task, _ := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Add Feature"})
	task = settleCode(t, a, task.ID)

	ownerGit(t, source, "checkout", "-q", "--orphan", "unrelated")
	ownerGit(t, source, "commit", "-q", "--allow-empty", "-m", "unrelated")
	if _, err := a.AdoptDraft(ctx, p.ID, task.ID, "unrelated", "", false); err == nil || !strings.Contains(err.Error(), "isn't built on") {
		t.Fatalf("an unrelated commit: %v", err)
	}
	if _, err := a.AdoptDraft(ctx, p.ID, task.ID, "no-such-branch", "", false); err == nil {
		t.Fatal("a missing ref was taken")
	}
	ownerGit(t, source, "checkout", "-q", "main")
	a.claim(task.ID)
	defer a.release(task.ID)
	if _, err := a.AdoptDraft(ctx, p.ID, task.ID, "main", "", false); err == nil || !strings.Contains(err.Error(), "at work") {
		t.Fatalf("taken while a step held the task: %v", err)
	}
}

// A change by hand handed over while the implementer works is refused for
// the whole of the step, not just the model's turn, so the two drafts never
// share a number and the owner's is never replaced unseen.
func TestAChangeByHandWaitsForTheStepInProgress(t *testing.T) {
	source := ownerRepo(t)
	runner := &codeRunner{scriptedRunner: scriptedRunner{reviews: []string{revise, pass, pass, pass}}}
	a, _, _ := loopApp(t, &runner.scriptedRunner, "")
	a.runner = runner
	ctx := context.Background()
	p := codeProject(t, a, source)
	task, _ := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Add Feature"})
	var refused error
	runner.onEdit = func(dir string, n int) bool {
		if n == 2 {
			_, refused = a.AdoptDraft(ctx, p.ID, task.ID, "main", "", false)
		}
		return true
	}
	task = settleCode(t, a, task.ID)
	if refused == nil || !strings.Contains(refused.Error(), "at work") {
		t.Fatalf("an adopt mid-step went through: %v", refused)
	}
	seen := map[int]bool{}
	for _, r := range task.Revisions {
		if seen[r.N] {
			t.Fatalf("two drafts numbered %d", r.N)
		}
		seen[r.N] = true
	}
}

// A reviewer is told a draft is the owner's own change, and the implementer
// to build on it.
func TestTheTeamIsToldADraftIsTheOwners(t *testing.T) {
	p := core.Project{Brief: core.Brief{Goal: "Add features"}, Playbook: &core.Playbook{Medium: core.MediumGit}}
	task := core.Task{Objective: "Add Feature", Base: "abc", Revisions: []core.Revision{{N: 1}, {N: 2, By: core.DraftByOwner, Summary: "Tidy Feature by hand"}}}
	reviewer := core.Role{Name: "Reviewer", Kinds: []string{core.RoleReviewer}}
	if got := checkerPrompt(p, task, task.Revisions[1], reviewer, p.Playbook); !strings.Contains(got, "owner made this draft by hand (Tidy Feature by hand)") {
		t.Fatalf("reviewer prompt: %s", got)
	}
	if got := writerPrompt(p, task, ""); !strings.Contains(got, "changed draft 2 by hand") {
		t.Fatalf("writer prompt: %s", got)
	}
}
