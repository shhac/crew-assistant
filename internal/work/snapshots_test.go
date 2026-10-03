//go:build !windows

package work

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/media/gitrepo"
	"github.com/shhac/crew-assistant/internal/media/localdocs"
	"github.com/shhac/crew-assistant/internal/roles"
)

// checkRunner is a code team whose checks let the test act while they run.
type checkRunner struct {
	*codeRunner
	onCheck func(spec roles.Spec)
	onWrite func(spec roles.Spec)
}

func (r checkRunner) Run(ctx context.Context, spec roles.Spec) (roles.Result, error) {
	if r.onCheck != nil && ((strings.Contains(spec.Prompt, "Use run_check for the project check") || strings.Contains(spec.Prompt, "Run exactly this")) || strings.Contains(spec.Prompt, "Do not modify anything")) {
		r.onCheck(spec)
	}
	if r.onWrite != nil && strings.Contains(spec.Prompt+spec.FreshPrompt, "Run the relevant tests yourself") {
		r.onWrite(spec)
	}
	return r.codeRunner.Run(ctx, spec)
}

// checkedCopy is the copy of the revision a check reads: where a reviewer
// works, or what QA is given to read beside its scratch folder.
func checkedCopy(spec roles.Spec) string {
	if spec.Write {
		return spec.Read[len(spec.Read)-1]
	}
	return spec.WorkDir
}

func codeTask(t *testing.T, reviews ...string) (*Loop, *codeRunner, core.Project, core.Task) {
	t.Helper()
	runner := &codeRunner{scriptedRunner: scriptedRunner{reviews: reviews}}
	a, _, _ := loopApp(t, &runner.scriptedRunner, "")
	a.runner = runner
	p := codeProject(t, a, ownerRepo(t))
	task, err := a.Core.QueueTask(context.Background(), p.ID, core.TaskInput{Objective: "Add Feature"})
	if err != nil {
		t.Fatal(err)
	}
	return a, runner, p, task
}

func (lp *Loop) testMedium(t *testing.T, projectID, taskID string) (gitMedium, core.Task) {
	t.Helper()
	snap, _ := lp.Core.Snapshot(context.Background())
	p, _ := findProject(snap, projectID)
	task, ok := findTask(snap, projectID, taskID)
	if !ok {
		t.Fatal("no task", taskID)
	}
	m, err := lp.gitMediumFor(context.Background(), p, taskPlaybook(p, task))
	if err != nil {
		t.Fatal(err)
	}
	return m, task
}

// A check reads a copy of the draft of its own, which stays exactly the draft
// while the implementer works on another task in that task's clone, and on
// this task's next draft in its own; every verdict names what it checked.
func TestAChecksCopyHoldsStillWhileTheImplementerWorksOn(t *testing.T) {
	t.Parallel()
	a, code, p, task := codeTask(t, pass, pass)
	ctx := context.Background()
	var checked []string
	a.runner = checkRunner{codeRunner: code, onCheck: func(spec roles.Spec) {
		dir := checkedCopy(spec)
		head := ownerGit(t, dir, "rev-parse", "HEAD")
		before, _ := os.ReadFile(filepath.Join(dir, "feature.go"))
		m, now := a.testMedium(t, p.ID, task.ID)
		// Another task's implementer at work, in that task's own clone.
		other := core.Task{ID: "another-task", Objective: "Another", Base: now.Base, Branch: "crew-task/another-task"}
		if err := m.reset(ctx, other); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(m.workspace(other), "feature.go"), []byte("package main // another task's\n"), 0o600)
		if _, err := m.snapshot(ctx, other, 1); err != nil {
			t.Fatal(err)
		}
		// This task's next draft under way, in its own clone.
		if err := m.reset(ctx, now); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(m.workspace(now), "feature.go"), []byte("package main // the next draft\n"), 0o600)
		os.WriteFile(filepath.Join(m.workspace(now), "extra.go"), []byte("package main\n"), 0o600)
		if _, err := m.snapshot(ctx, now, len(now.Revisions)+1); err != nil {
			t.Fatal(err)
		}
		for _, clone := range []string{m.workspace(now), m.workspace(other), m.repo.Workspace()} {
			if strings.HasPrefix(dir, clone) {
				t.Fatalf("the check reads %s, inside a clone someone works in", dir)
			}
		}
		after, _ := os.ReadFile(filepath.Join(dir, "feature.go"))
		_, extra := os.Stat(filepath.Join(dir, "extra.go"))
		if ownerGit(t, dir, "rev-parse", "HEAD") != head || string(after) != string(before) || extra == nil {
			t.Fatalf("the check's copy changed under it: %q, then %q", before, after)
		}
		checked = append(checked, head)
	}}
	task = settleCode(t, a, task.ID)
	if task.Status != core.TaskWaiting || len(task.Revisions) != 1 || len(task.Verdicts) != 2 || len(checked) != 2 {
		t.Fatalf("task %s, %d drafts, %d verdicts, %d checks", task.Status, len(task.Revisions), len(task.Verdicts), len(checked))
	}
	draft := task.Revisions[0].Ref
	for i, v := range task.Verdicts {
		if v.Ref != draft || checked[i] != draft {
			t.Fatalf("verdict %+v checked %s, not the draft %s", v, checked[i], draft)
		}
	}
}

// A check that changes the copy it was given has its verdict discarded, and
// runs again; only the check of the unchanged draft counts.
func TestACheckThatChangesItsCopyIsDiscardedAndRunsAgain(t *testing.T) {
	t.Parallel()
	a, code, p, task := codeTask(t, pass, pass, pass)
	ctx := context.Background()
	tampered := false
	a.runner = checkRunner{codeRunner: code, onCheck: func(spec roles.Spec) {
		if tampered || spec.Write {
			return
		}
		tampered = true
		os.Chmod(spec.WorkDir, 0o700)
		os.WriteFile(filepath.Join(spec.WorkDir, "planted.go"), []byte("package main\n"), 0o600)
	}}
	task = settleCode(t, a, task.ID)
	if !tampered || len(task.Verdicts) != 0 || task.Failures != 1 || task.RetryAt.IsZero() || task.Status != core.TaskReviewing {
		t.Fatalf("a check that changed its copy counted: %+v", task)
	}
	if entries, _ := os.ReadDir(filepath.Join(p.ScratchDirectory, "checks")); len(entries) != 0 {
		t.Fatal("the changed copy was left behind")
	}
	a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.RetryAt = t.RetryAt.AddDate(-1, 0, 0)
		return "", nil
	})
	task = settleCode(t, a, task.ID)
	if task.Status != core.TaskWaiting || len(task.Verdicts) != 2 {
		t.Fatalf("the check did not run again: %+v", task)
	}
	for _, v := range task.Verdicts {
		if v.Ref != task.Revisions[0].Ref {
			t.Fatalf("verdict %+v does not name the draft", v)
		}
	}
}

// A draft is recorded only once the project's clone holds it, under a ref
// of its own that is never moved; one that can't be handed over is not
// recorded, and the round runs again on a fresh attempt.
func TestADraftCountsOnlyOnceTheProjectsCloneHoldsIt(t *testing.T) {
	t.Parallel()
	a, _, p, task := codeTask(t, pass, pass)
	ctx := context.Background()
	for task.Status != core.TaskWriting {
		step(t, a)
		_, task = a.testMedium(t, p.ID, task.ID)
	}
	// Something already holds the name the first attempt would use: the
	// round's claim takes one attempt, and its handoff the next.
	project := filepath.Join(p.ScratchDirectory, "clone")
	first := task.Attempt + 2
	taken := gitrepo.TaskRef(task.ID, 1, first)
	ownerGit(t, project, "update-ref", taken, ownerGit(t, project, "rev-parse", "HEAD"))
	step(t, a)
	_, task = a.testMedium(t, p.ID, task.ID)
	if len(task.Revisions) != 0 || task.Handoff != nil || task.Failures != 1 || task.Attempt != first {
		t.Fatalf("a draft the project's clone does not hold was recorded: %+v", task)
	}
	a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.RetryAt = t.RetryAt.AddDate(-1, 0, 0)
		return "", nil
	})
	step(t, a)
	_, task = a.testMedium(t, p.ID, task.ID)
	name := gitrepo.TaskRef(task.ID, 1, first+2)
	if len(task.Revisions) != 1 || task.Handoff != nil || task.Status != core.TaskReviewing {
		t.Fatalf("the round did not run again: %+v", task)
	}
	if at := ownerGit(t, project, "rev-parse", name); at != task.Revisions[0].Ref {
		t.Fatalf("draft 1 is %s, but the project's clone keeps %s", task.Revisions[0].Ref, at)
	}
	if ownerGit(t, project, "rev-parse", taken) == task.Revisions[0].Ref {
		t.Fatal("a ref already written was moved")
	}
}

// handoffAt leaves a task as a daemon that stopped mid-handoff would: its
// first draft committed in its own clone, and the intent to record it
// stored.
func handoffAt(t *testing.T) (*Loop, core.Project, core.Task, gitMedium, core.Handoff) {
	t.Helper()
	a, _, p, task := codeTask(t, pass, pass)
	ctx := context.Background()
	for task.Status != core.TaskWriting {
		step(t, a)
		_, task = a.testMedium(t, p.ID, task.ID)
	}
	m, _ := a.testMedium(t, p.ID, task.ID)
	task, err := a.prepareWorkspace(ctx, task, m)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(m.workspace(task), "feature.go"), []byte("package main\n\nfunc Feature() {}\n"), 0o600)
	r, err := m.snapshot(ctx, task, 1)
	if err != nil {
		t.Fatal(err)
	}
	r.Summary = "Added Feature."
	seat, _ := task.Role("Implementer")
	h := core.Handoff{Revision: r, Writer: seat.Name, Seat: &seat, Session: []byte(`{"engine":"claude","id":"impl"}`), Reply: "Added Feature."}
	task, err = a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.Attempt++
		h.Name = gitrepo.TaskRef(t.ID, 1, t.Attempt)
		t.Handoff = &h
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return a, p, task, m, h
}

// restart is the daemon starting again over the same record.
func restart(t *testing.T, a *Loop) *Loop {
	t.Helper()
	again := New(a.Core, a.Config, false)
	again.runner, again.meter = a.runner, a.meter
	if err := again.resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	return again
}

// A daemon stopped at any point of handing a draft over neither loses nor
// repeats it: past the point the ref could be written, it is recorded once;
// before, or with the draft gone, the round runs again. Either way no ref is
// left that no revision records.
func TestARestartMidHandoffNeitherLosesNorRepeatsADraft(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	recorded := func(t *testing.T, a *Loop, p core.Project, task core.Task, h core.Handoff) {
		t.Helper()
		m, task := a.testMedium(t, p.ID, task.ID)
		if len(task.Revisions) != 1 || task.Revisions[0].Ref != h.Revision.Ref || task.Handoff != nil || task.Status != core.TaskReviewing {
			t.Fatalf("the draft was not recorded exactly once: %+v", task)
		}
		if th, ok := task.Thread(core.RoleImplementer, *h.Seat); !ok || string(th.Session) != string(h.Session) {
			t.Fatal("the implementer's conversation was lost with the restart")
		}
		if refs, _ := m.repo.TaskRefs(ctx); len(refs) != 1 || refs[h.Name] != h.Revision.Ref {
			t.Fatalf("refs %v", refs)
		}
	}
	rerun := func(t *testing.T, a *Loop, p core.Project, task core.Task) {
		t.Helper()
		m, task := a.testMedium(t, p.ID, task.ID)
		if len(task.Revisions) != 0 || task.Handoff != nil || task.Status != core.TaskWriting {
			t.Fatalf("the round should run again: %+v", task)
		}
		if refs, _ := m.repo.TaskRefs(ctx); len(refs) != 0 {
			t.Fatalf("stray refs %v", refs)
		}
		// It does, and its draft is recorded once.
		task = settleCode(t, a, task.ID)
		if len(task.Revisions) != 1 || task.Status != core.TaskWaiting {
			t.Fatalf("the round that ran again: %+v", task)
		}
	}
	t.Run("before publishing", func(t *testing.T) {
		t.Parallel()
		a, p, task, _, h := handoffAt(t)
		a = restart(t, a)
		recorded(t, a, p, task, h)
		recorded(t, restart(t, a), p, task, h)
	})
	t.Run("after publishing", func(t *testing.T) {
		t.Parallel()
		a, p, task, m, h := handoffAt(t)
		if err := m.publish(ctx, task, h.Revision.Ref, h.Name); err != nil {
			t.Fatal(err)
		}
		recorded(t, restart(t, a), p, task, h)
	})
	t.Run("after recording", func(t *testing.T) {
		t.Parallel()
		a, p, task, m, h := handoffAt(t)
		if err := m.publish(ctx, task, h.Revision.Ref, h.Name); err != nil {
			t.Fatal(err)
		}
		if err := a.commitHandoff(ctx, task.ID, h.Name); err != nil {
			t.Fatal(err)
		}
		recorded(t, restart(t, a), p, task, h)
	})
	t.Run("with the draft lost", func(t *testing.T) {
		t.Parallel()
		a, p, task, m, _ := handoffAt(t)
		if err := m.repo.RemoveTask(task.ID); err != nil {
			t.Fatal(err)
		}
		rerun(t, restart(t, a), p, task)
	})
	t.Run("with the ref taken", func(t *testing.T) {
		t.Parallel()
		a, p, task, m, h := handoffAt(t)
		if err := m.repo.Publish(ctx, m.repo, task.Base, h.Name); err != nil {
			t.Fatal(err)
		}
		rerun(t, restart(t, a), p, task)
	})
	t.Run("before the intent was stored", func(t *testing.T) {
		t.Parallel()
		a, p, task, _, h := handoffAt(t)
		a.dropHandoff(ctx, task.ID, h.Name)
		rerun(t, restart(t, a), p, task)
	})
}

// A clean catch-up merge is made as a draft is, in the task's own clone, and
// the project's clone holds it only once its handoff publishes it; a daemon
// stopped after storing the intent records it once on starting again.
func TestACleanCatchUpIsHandedOverAsADraftIs(t *testing.T) {
	t.Parallel()
	a, _, p, task := codeTask(t, pass, pass)
	ctx := context.Background()
	task = settleCode(t, a, task.ID)
	m, task := a.testMedium(t, p.ID, task.ID)
	source := m.playbook.Repo
	os.WriteFile(filepath.Join(source, "owner.go"), []byte("package main\n"), 0o600)
	ownerGit(t, source, "add", "owner.go")
	ownerGit(t, source, "commit", "-q", "-m", "owner work")
	tip, err := m.repo.Fetch(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	l := line{Commit: tip, Name: "main", What: "main moved on"}
	moved, merge, err := m.cleanMerge(ctx, task, l)
	if err != nil || merge == "" {
		t.Fatalf("no clean merge: %v", err)
	}
	if m.repo.Holds(ctx, merge) || !m.repo.Task(task.ID).Holds(ctx, merge) {
		t.Fatal("the merge reached the project's clone before its handoff was stored")
	}
	files, err := m.files(ctx, moved, merge)
	if err != nil || !slices.Contains(files, "feature.go") {
		t.Fatalf("files %v: %v", files, err)
	}
	h := core.Handoff{
		Revision: core.Revision{N: 2, Files: files, Ref: merge, Summary: "Merged in without conflicts."},
		CatchUp:  &core.CatchUp{Base: moved.Base, From: moved.From, Carry: true, Name: l.Name, What: l.What},
	}
	a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.Attempt++
		h.Name = gitrepo.TaskRef(t.ID, 2, t.Attempt)
		t.Handoff = &h
		return "", nil
	})
	a = restart(t, a)
	_, task = a.testMedium(t, p.ID, task.ID)
	if len(task.Revisions) != 2 || task.Revisions[1].Ref != merge || task.Revisions[1].CleanMergeOf != 1 || task.Handoff != nil {
		t.Fatalf("the catch-up was not recorded once: %+v", task)
	}
	if at, _ := m.repo.RefAt(ctx, h.Name); at != merge {
		t.Fatalf("the project's clone keeps %q for the catch-up, not %s", at, merge)
	}
}

// The implementer's next task gets a clone and branch of its own; the one
// before keeps its own until it finishes, and nothing is left of it after.
func TestEachTaskGetsItsOwnCloneAndBranch(t *testing.T) {
	t.Parallel()
	a, _, p, first := codeTask(t, passes(8)...)
	ctx := context.Background()
	second, _ := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Add Other"})
	// The first waits on the owner while the second is worked.
	settleCode(t, a, first.ID)
	snap := snapshotOf(t, a)
	first, _ = findTask(snap, p.ID, first.ID)
	second, _ = findTask(snap, p.ID, second.ID)
	m, _ := a.testMedium(t, p.ID, first.ID)
	one, two := m.workspace(first), m.workspace(second)
	if len(first.Revisions) != 1 || len(second.Revisions) != 1 || one == two || second.Branch == first.Branch {
		t.Fatalf("first %+v, second %+v", first, second)
	}
	for _, task := range []core.Task{first, second} {
		clone := m.workspace(task)
		if ownerGit(t, clone, "rev-parse", "--abbrev-ref", "HEAD") != task.Branch || ownerGit(t, clone, "rev-parse", "HEAD") != task.Revisions[0].Ref {
			t.Fatalf("%s's clone is not on its own branch at its draft", task.Objective)
		}
	}
	if ownerGit(t, two, "branch", "--list", first.Branch) != "" || ownerGit(t, one, "branch", "--list", second.Branch) != "" || ownerGit(t, m.repo.Workspace(), "branch", "--list", "crew-task/*") != "" {
		t.Fatal("a task's branch is outside its own clone")
	}
	a.Core.ChooseDecision(ctx, openDecision(t, a, first).ID, choiceApprove, core.FromOwner)
	first = settleCode(t, a, first.ID)
	if first.Status != core.TaskDelivered {
		t.Fatalf("first %s", first.Status)
	}
	if _, err := os.Stat(one); !os.IsNotExist(err) {
		t.Fatal("the delivered task's clone was left")
	}
	if _, err := os.Stat(two); err != nil {
		t.Fatal("the waiting task's clone went with the other's")
	}
	// Once nothing waits on the first, its refs go; the second's stay.
	if err := a.tidy(ctx, false); err != nil {
		t.Fatal(err)
	}
	second, _ = findTask(snapshotOf(t, a), p.ID, second.ID)
	refs, _ := m.repo.TaskRefs(ctx)
	for ref, commit := range refs {
		if gitrepo.RefTask(ref) != second.ID || !keeps(second, ref, commit) {
			t.Fatalf("a finished task's ref was kept: %v", refs)
		}
	}
	if len(refs) != len(second.Revisions) {
		t.Fatalf("refs %v for %d drafts", refs, len(second.Revisions))
	}
}

// While a draft is being checked, whether in its turn or because someone
// asked the checker, no new draft can replace it; the check that was asked
// for judges the draft it was given.
func TestNoDraftReplacesOneBeingChecked(t *testing.T) {
	t.Parallel()
	a, code, p, task := codeTask(t, pass, pass, pass)
	ctx := context.Background()
	var refused []error
	a.runner = checkRunner{codeRunner: code, onCheck: func(roles.Spec) {
		_, err := a.AdoptDraft(ctx, p.ID, task.ID, "main", "", false)
		refused = append(refused, err)
	}}
	task = settleCode(t, a, task.ID)
	if _, err := a.MessageTeam(ctx, p.ID, task.ID, "Reviewer", core.FromOwner, "Look again at the tests"); err != nil {
		t.Fatal(err)
	}
	task = settleCode(t, a, task.ID)
	if len(refused) != 3 {
		t.Fatalf("%d checks ran", len(refused))
	}
	for _, err := range refused {
		if err == nil || !strings.Contains(err.Error(), "at work") {
			t.Fatalf("a draft was handed over mid-check: %v", err)
		}
	}
	if len(task.Revisions) != 1 || task.Messages[0].Status != core.MessageAnswered {
		t.Fatalf("drafts %d, message %+v", len(task.Revisions), task.Messages[0])
	}
	for _, v := range task.Verdicts {
		if v.Ref != task.Revisions[0].Ref {
			t.Fatalf("verdict %+v does not name the draft it checked", v)
		}
	}
}

// Checks and landing read only what the project's clone keeps: with the
// task's own clone gone, the draft is still checked and lands.
func TestChecksAndLandingNeedNoTaskClone(t *testing.T) {
	t.Parallel()
	a, _, p, task := codeTask(t, pass, pass)
	ctx := context.Background()
	for task.Status != core.TaskReviewing {
		step(t, a)
		_, task = a.testMedium(t, p.ID, task.ID)
	}
	m, _ := a.testMedium(t, p.ID, task.ID)
	if err := m.repo.RemoveTask(task.ID); err != nil {
		t.Fatal(err)
	}
	task = settleCode(t, a, task.ID)
	if task.Status != core.TaskWaiting || len(task.Verdicts) != 2 {
		t.Fatalf("the draft was not checked without its clone: %+v", task)
	}
	a.Core.ChooseDecision(ctx, openDecision(t, a, task).ID, choiceApprove, core.FromOwner)
	task = settleCode(t, a, task.ID)
	if task.Status != core.TaskDelivered || ownerGit(t, m.playbook.Repo, "rev-parse", task.DeliveredTo) != task.Revisions[0].Ref {
		t.Fatalf("the draft did not land without its clone: %+v", task)
	}
	if _, err := os.Stat(m.workspace(task)); !os.IsNotExist(err) {
		t.Fatal("landing made the task a clone")
	}
}

// Written work has a workspace per task too, and its verdicts name the
// draft they checked by its digest.
func TestADocumentsVerdictNamesTheDraftItChecked(t *testing.T) {
	t.Parallel()
	runner := &scriptedRunner{reviews: []string{revise, pass}}
	a, p, task := loopApp(t, runner, "")
	task = settle(t, a)
	if len(task.Revisions) != 2 || len(task.Verdicts) != 2 {
		t.Fatalf("task %+v", task)
	}
	for _, v := range task.Verdicts {
		r := task.Revisions[v.Revision-1]
		if r.Ref == "" || v.Ref != r.Ref {
			t.Fatalf("verdict %+v does not name draft %+v", v, r)
		}
	}
	if task.Revisions[0].Ref == task.Revisions[1].Ref {
		t.Fatal("two different drafts have the same name")
	}
	for _, spec := range runner.seen {
		if spec.Write && spec.WorkDir != filepath.Join(p.ScratchDirectory, "tasks", task.ID, "workspace") {
			t.Fatalf("the writer worked outside the task's own workspace: %s", spec.WorkDir)
		}
	}
}

// docsQARunner is a documents team with QA, whose checks let the test act
// while they run.
type docsQARunner struct {
	*scriptedRunner
	onQA func(spec roles.Spec) string
}

func (r docsQARunner) Run(ctx context.Context, spec roles.Spec) (roles.Result, error) {
	if strings.Contains(spec.Prompt, "Use run_check for the project check") || strings.Contains(spec.Prompt, "Run exactly this") {
		return roles.Result{Text: r.onQA(spec)}, nil
	}
	return r.scriptedRunner.Run(ctx, spec)
}

func docsTaskWithQA(t *testing.T, reviews ...string) (*Loop, *scriptedRunner, core.Project, core.Task) {
	t.Helper()
	runner := &scriptedRunner{reviews: reviews}
	a, p, task := loopApp(t, runner, "")
	playbook := *p.Playbook
	playbook.Roles = append(slices.Clone(playbook.Roles), core.Role{Name: "QA", Kinds: []string{core.RoleQA}, Engine: "codex"})
	playbook.Check = "vale ."
	p, err := a.Core.SetPlaybook(context.Background(), p.ID, playbook)
	if err != nil {
		t.Fatal(err)
	}
	return a, runner, p, task
}

// Document QA reads a read-only copy of the draft of its own and writes only
// to a scratch folder; the copy stays exactly the draft while the writer
// works on the next draft and on another task, and the verdict names it.
func TestDocumentQAChecksACopyThatHoldsStill(t *testing.T) {
	t.Parallel()
	a, scripted, p, task := docsTaskWithQA(t, pass)
	docs, err := localdocs.Open(p.ScratchDirectory)
	if err != nil {
		t.Fatal(err)
	}
	var checked string
	a.runner = docsQARunner{scriptedRunner: scripted, onQA: func(spec roles.Spec) string {
		if strings.Contains(spec.Prompt, "run_check") || !strings.Contains(spec.Prompt, "Run exactly this from the repository root, once:\nvale .") {
			t.Fatalf("document QA must use its shell check: %s", spec.Prompt)
		}
		for _, tool := range spec.Tools {
			if tool.Name == "run_check" {
				t.Fatal("document QA unexpectedly offers run_check")
			}
		}
		if len(spec.Read) == 0 {
			t.Fatalf("QA was given no copy to read beside its working directory %s", spec.WorkDir)
		}
		dir := checkedCopy(spec)
		before, _ := os.ReadFile(filepath.Join(dir, "note.md"))
		if !spec.Write || strings.HasPrefix(spec.WorkDir, dir) || strings.HasPrefix(dir, docs.Workspace(task.ID)) || !strings.Contains(spec.Prompt, "The draft is at "+dir) {
			t.Fatalf("QA works in %s over the draft at %s", spec.WorkDir, dir)
		}
		if err := os.WriteFile(filepath.Join(spec.WorkDir, "out.txt"), []byte("check output"), 0o600); err != nil {
			t.Fatalf("QA's scratch folder is not writable: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "note.md"), []byte("changed by QA"), 0o600); err == nil {
			t.Fatal("QA's copy of the draft is writable")
		}
		// The next draft under way, and another task's.
		os.WriteFile(filepath.Join(docs.Workspace(task.ID), "note.md"), []byte("the next draft"), 0o600)
		if _, _, err := docs.Snapshot(task.ID, 2); err != nil {
			t.Fatal(err)
		}
		os.MkdirAll(docs.Workspace("another-task"), 0o700)
		os.WriteFile(filepath.Join(docs.Workspace("another-task"), "note.md"), []byte("another task's"), 0o600)
		if _, _, err := docs.Snapshot("another-task", 1); err != nil {
			t.Fatal(err)
		}
		after, _ := os.ReadFile(filepath.Join(dir, "note.md"))
		if string(after) != string(before) || string(before) != "Draft 1" {
			t.Fatalf("QA's copy changed under it: %q, then %q", before, after)
		}
		checked = dir
		return pass
	}}
	task = settle(t, a)
	if checked == "" || task.Status != core.TaskWaiting || len(task.Revisions) != 1 || len(task.Verdicts) != 2 {
		t.Fatalf("checked %q: %+v", checked, task)
	}
	for _, v := range task.Verdicts {
		if v.Ref != task.Revisions[0].Ref {
			t.Fatalf("verdict %+v does not name the draft", v)
		}
	}
	if _, err := os.Stat(checked); !os.IsNotExist(err) {
		t.Fatal("QA's copy was left behind")
	}
}

// Document QA that changes its copy of the draft has its verdict discarded,
// and runs again.
func TestDocumentQAThatChangesItsCopyIsDiscarded(t *testing.T) {
	t.Parallel()
	a, scripted, _, task := docsTaskWithQA(t, pass)
	tampered := false
	a.runner = docsQARunner{scriptedRunner: scripted, onQA: func(spec roles.Spec) string {
		if !tampered {
			tampered = true
			// The draft QA was given, wherever that is.
			dir := spec.WorkDir
			if len(spec.Read) > 0 && strings.Contains(spec.Prompt, "The draft is at "+checkedCopy(spec)) {
				dir = checkedCopy(spec)
			}
			os.Chmod(filepath.Join(dir, "note.md"), 0o600)
			os.WriteFile(filepath.Join(dir, "note.md"), []byte("fixed by QA"), 0o600)
		}
		return pass
	}}
	task = settle(t, a)
	if !tampered || task.Failures != 1 || task.RetryAt.IsZero() || task.Status != core.TaskReviewing {
		t.Fatalf("a check that changed its copy counted: %+v", task)
	}
	for _, v := range task.Verdicts {
		if v.Role == "QA" {
			t.Fatalf("QA's verdict on a changed copy was kept: %+v", v)
		}
	}
}

// A pass carried over to a clean catch-up keeps naming the draft it checked,
// and the task's record says so where that is not the draft it counts for.
func TestACarriedOverPassNamesWhatItChecked(t *testing.T) {
	t.Parallel()
	now := time.Now()
	checked := strings.Repeat("a", 40)
	carried := carriedOver([]core.Verdict{{Revision: 1, Ref: checked, Role: "Reviewer", Outcome: core.VerdictPass, BriefVersion: 1, Summary: "Fine."}}, 1, 2, func(seat string) string { return seat }, map[string]bool{"Reviewer": true}, 1, now)
	if len(carried) != 1 || carried[0].Revision != 2 || carried[0].Ref != checked {
		t.Fatalf("carried %+v", carried)
	}
	task := core.Task{
		Revisions: []core.Revision{{N: 1, Ref: checked, Summary: "Added it."}, {N: 2, Ref: strings.Repeat("b", 40), CleanMergeOf: 1, Summary: "Merged in."}},
		Verdicts:  append(carried, core.Verdict{Revision: 2, Ref: strings.Repeat("b", 40), Role: "QA", Outcome: core.VerdictPass, Summary: "Green."}),
	}
	history := historyText(task, true)
	if !strings.Contains(history, "- Reviewer, pass (checked aaaaaaa): Carried over") || !strings.Contains(history, "- QA, pass: Green.") {
		t.Fatalf("history:\n%s", history)
	}
}

// The legacy copy setting retains its writable tree, while hosted-check
// guidance describes its own copy and never invents an app.
func TestQACheckInCopyKeepsHostedCheckGuidance(t *testing.T) {
	t.Parallel()
	a, code, p, task := codeTask(t, pass, pass)
	ctx := context.Background()
	if _, err := a.SetTeam(ctx, p.ID, TeamChoice{Template: "code", BranchPrefix: "paul/", Check: "make check", CheckInCopy: "yes"}); err != nil {
		t.Fatal(err)
	}
	var tree string
	a.runner = checkRunner{codeRunner: code, onCheck: func(spec roles.Spec) {
		if !spec.Write {
			return
		}
		tree = filepath.Join(spec.WorkDir, "tree")
		if !strings.Contains(spec.Prompt, "run_check runs the check in its own writable copy") || strings.Contains(spec.Prompt, "app may build") {
			t.Fatalf("QA was not sent to its copy: %s", spec.Prompt)
		}
		if err := os.WriteFile(filepath.Join(tree, "feature.go"), []byte("written by the check"), 0o600); err != nil {
			t.Fatalf("QA's copy is not writable: %v", err)
		}
	}}
	task = settleCode(t, a, task.ID)
	if tree == "" || task.Status != core.TaskWaiting || len(task.Verdicts) != 2 || task.Verdicts[1].Ref != task.Revisions[0].Ref {
		t.Fatalf("QA in a copy: %+v", task)
	}
}

// Hosted checks cover QA localhost independently; only the implementer
// retains session localhost for individual tests. Saving keeps the setting.
func TestHostedCheckDoesNotGiveQASessionLoopback(t *testing.T) {
	t.Parallel()
	a, code, p, task := codeTask(t, pass, pass)
	ctx := context.Background()
	quinn, err := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Quinn", Kinds: []string{core.RoleQA}, Engine: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetTeam(ctx, p.ID, TeamChoice{Template: "code", BranchPrefix: "paul/", Check: "make check", QA: quinn.ID, CheckInCopy: "yes", CheckLoopback: "yes"}); err != nil {
		t.Fatal(err)
	}
	saved, err := a.SetTeam(ctx, p.ID, TeamChoice{Template: "code", BranchPrefix: "paul/", Check: "make check", QA: quinn.ID})
	if err != nil || !saved.Playbook.CheckLoopback || !saved.Playbook.CheckInCopy {
		t.Fatalf("saving the team without them dropped the check's settings: %+v, %v", saved.Playbook, err)
	}
	loopback := map[bool]bool{}
	var writerLoopback []bool
	a.runner = checkRunner{codeRunner: code, onCheck: func(spec roles.Spec) {
		loopback[spec.Write] = spec.Loopback
	}, onWrite: func(spec roles.Spec) {
		writerLoopback = append(writerLoopback, spec.Loopback)
	}}
	task = settleCode(t, a, task.ID)
	if task.Status != core.TaskWaiting || loopback[true] || loopback[false] {
		t.Fatalf("hosted checks must not give QA (true) or reviewer (false) session loopback: %v, task %s", loopback, task.Status)
	}
	if len(writerLoopback) == 0 || slices.Contains(writerLoopback, false) {
		t.Fatalf("the implementer can't run tests that need a local server: %v", writerLoopback)
	}
	off, err := a.SetTeam(ctx, p.ID, TeamChoice{Template: "code", BranchPrefix: "paul/", Check: "make check", QA: quinn.ID, CheckLoopback: "no"})
	if err != nil || off.Playbook.CheckLoopback || !off.Playbook.CheckInCopy {
		t.Fatalf("turning it off: %+v, %v", off.Playbook, err)
	}
}

// A task keeps the team it started with, so the owner moves one waiting on
// them onto the project's team as it is now, such as a QA whose engine can
// run the check; never one queued or being worked on.
func TestATaskWaitingForTheOwnerCanTakeOnTheProjectsTeam(t *testing.T) {
	t.Parallel()
	a, _, p, task := codeTask(t, pass, ask)
	ctx := context.Background()
	if _, err := a.UseProjectTeam(ctx, p.ID, task.ID); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("a queued task took on the team: %v", err)
	}
	task = settleCode(t, a, task.ID)
	if task.Status != core.TaskWaiting {
		t.Fatalf("task %s", task.Status)
	}
	quinn, err := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Quinn", Kinds: []string{core.RoleQA}, Engine: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetTeam(ctx, p.ID, TeamChoice{Template: "code", BranchPrefix: "paul/", Check: "make check", QA: quinn.ID, CheckLoopback: "yes"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.UseProjectTeam(ctx, "elsewhere", task.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("another project's task: %v", err)
	}
	moved, err := a.UseProjectTeam(ctx, p.ID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	qa := slices.IndexFunc(moved.Playbook.Roles, func(r core.Role) bool { return r.Holds(core.RoleQA) })
	if !moved.Playbook.CheckLoopback || qa < 0 || moved.Playbook.Roles[qa].Member != quinn.ID || moved.Roles[qa].Member != quinn.ID {
		t.Fatalf("the task's team: %+v", moved.Playbook)
	}
}

func snapshotOf(t *testing.T, a *Loop) core.Snapshot {
	t.Helper()
	snap, err := a.Core.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func TestRestartRecordsResolvedDraftBaseTogether(t *testing.T) {
	t.Parallel()
	a, p, task, _, _ := handoffAt(t)
	_, err := a.Core.UpdateTask(context.Background(), task.ID, func(task *core.Task, _ *core.Project) (string, error) {
		task.Handoff.DraftCatchUp = &core.DraftCatchUp{Base: "new-base", From: "main", What: "main moved on since this request started (it is now at abc1234)", Conflicts: []string{"feature.go"}}
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	a = restart(t, a)
	a = restart(t, a)
	_, task = a.testMedium(t, p.ID, task.ID)
	if len(task.Revisions) != 1 || task.Base != "new-base" || task.From != "main" {
		t.Fatalf("handoff lost its base or repeated the draft: %+v", task)
	}
	resolutions := 0
	for _, entry := range snapshotOf(t, a).Activity {
		if strings.Contains(entry.Summary, "resolved the conflicts in feature.go with what landed (main moved on since this request started (it is now at abc1234))") {
			resolutions++
		}
	}
	if resolutions != 1 {
		t.Fatalf("got %d resolution records after restarting twice, want 1", resolutions)
	}
}
