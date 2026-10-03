//go:build !windows

package work

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/roles"
)

func gitOut(dir string, args ...string) string {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		return "error: " + err.Error()
	}
	return strings.TrimSpace(string(out))
}

func ownerCommits(t *testing.T, source, file, body, message string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(source, file), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	ownerGit(t, source, "add", file)
	ownerGit(t, source, "commit", "-q", "-m", message)
	return ownerGit(t, source, "rev-parse", "HEAD")
}

// implementerTold reports whether a turn that writes was prompted with all
// of says.
func implementerTold(runner *codeRunner, says ...string) bool {
	return slices.ContainsFunc(runner.seen, func(spec roles.Spec) bool {
		if !spec.Write {
			return false
		}
		for _, s := range says {
			if !strings.Contains(spec.Prompt, s) {
				return false
			}
		}
		return true
	})
}

// pushProjectApp is a loop with a code project on source that lands by
// pushing onto main.
func pushProjectApp(t *testing.T, runner *codeRunner, source string) (*Loop, core.Project) {
	t.Helper()
	a, _, _ := loopApp(t, &runner.scriptedRunner, "")
	a.runner = runner
	p := codeProject(t, a, source)
	if _, err := a.SetLanding(context.Background(), p.ID, core.LandPolicy{Via: core.LandPush, Target: "main"}); err != nil {
		t.Fatal(err)
	}
	return a, p
}

// When the owner's main moves on while a draft is checked and the draft is
// sent back, the revision round starts from a clean merge of main into the
// task: the task's clone is reset to that merge, the task is measured from
// main's new tip, the implementer is told, and its next draft includes what
// landed.
func TestARevisionRoundStartsFromACleanMergeOfWhatLanded(t *testing.T) {
	t.Parallel()
	source := ownerRepo(t)
	start := ownerGit(t, source, "rev-parse", "main")
	reviews := append([]string{revise}, passes(10)...)
	runner := &codeRunner{scriptedRunner: scriptedRunner{reviews: reviews}}
	var moved sync.Once
	landed := ""
	runner.onCheck = func() {
		moved.Do(func() {
			landed = ownerCommits(t, source, "other.go", "package main\n\nfunc Other() {}\n", "owner work")
		})
	}
	var mu sync.Mutex
	var roundTwoHead, roundTwoParents string
	otherInWorkspace := false
	runner.onEdit = func(dir string, n int) bool {
		if n == 2 {
			mu.Lock()
			roundTwoHead = gitOut(dir, "rev-parse", "HEAD")
			roundTwoParents = gitOut(dir, "log", "-1", "--format=%P", "HEAD")
			_, err := os.Stat(filepath.Join(dir, "other.go"))
			otherInWorkspace = err == nil
			mu.Unlock()
		}
		return true
	}
	a, p := pushProjectApp(t, runner, source)
	task, err := a.Core.QueueTask(context.Background(), p.ID, core.TaskInput{Objective: "Add Feature"})
	if err != nil {
		t.Fatal(err)
	}
	settle(t, a)
	task = taskByID(t, a, task.ID)
	if landed == "" || len(task.Revisions) < 2 {
		t.Fatalf("main should have moved during round one's checks and a second round followed: %+v", task)
	}

	first, second := task.Revisions[0], task.Revisions[1]
	mu.Lock()
	defer mu.Unlock()
	if !otherInWorkspace {
		t.Fatal("the revision round's workspace does not hold what landed")
	}
	if parents := strings.Fields(roundTwoParents); len(parents) != 2 || parents[0] != first.Ref || parents[1] != landed {
		t.Fatalf("the workspace should be reset to a merge of draft 1 (%s) and main (%s); HEAD %s has parents %v", first.Ref, landed, roundTwoHead, parents)
	}
	if task.Base != landed || task.From != "main" {
		t.Fatalf("the task should be measured from main's new tip %s, not its start %s: base %s from %s", landed, start, task.Base, task.From)
	}
	clone := filepath.Join(p.ScratchDirectory, "clone")
	if err := exec.Command("git", "-C", clone, "merge-base", "--is-ancestor", landed, second.Ref).Run(); err != nil {
		t.Fatalf("draft 2 (%s) does not include the landed commit %s", second.Ref, landed)
	}
	if err := exec.Command("git", "-C", clone, "merge-base", "--is-ancestor", roundTwoHead, second.Ref).Run(); err != nil {
		t.Fatalf("draft 2 was not built on the merge %s", roundTwoHead)
	}
	if !activityHas(t, a, "Implementer finished version 2 of Add Feature, including what landed: main moved on since this request started (it is now at "+landed[:7]+")") {
		t.Fatal("clean integration missing from draft activity")
	}
	if !implementerTold(runner, "main moved on", "merged into this branch for you without conflicts") {
		t.Fatal("the implementer was not told what was merged in")
	}
}

// When the owner rewrites main while a change waits, and the change conflicts
// with what main now holds, landing sends it back and the implementer gets
// the task's own change replayed onto the rewritten main with the conflicts
// left to resolve: nothing main dropped comes back.
func TestAConflictWithARewrittenMainReachesTheImplementer(t *testing.T) {
	t.Parallel()
	source := ownerRepo(t)
	ownerCommits(t, source, "dropped.go", "package main // dropped later\n", "a commit the owner will drop")
	runner := &codeRunner{scriptedRunner: scriptedRunner{reviews: passes(12)}}
	var mu sync.Mutex
	var roundTwoFeature string
	droppedBack, midReplay := false, false
	runner.onEdit = func(dir string, n int) bool {
		if n == 2 {
			mu.Lock()
			body, _ := os.ReadFile(filepath.Join(dir, "feature.go"))
			roundTwoFeature = string(body)
			_, err := os.Stat(filepath.Join(dir, "dropped.go"))
			droppedBack = err == nil
			midReplay = gitOut(dir, "diff", "--name-only", "--diff-filter=U") == "feature.go"
			mu.Unlock()
		}
		return true
	}
	a, p := pushProjectApp(t, runner, source)
	ctx := context.Background()
	task, err := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Add Feature"})
	if err != nil {
		t.Fatal(err)
	}
	settle(t, a)
	task = taskByID(t, a, task.ID)
	if task.Status != core.TaskWaiting || len(task.Revisions) != 1 {
		t.Fatalf("the change should wait for approval after one round: %+v", task)
	}

	// The owner drops their last commit from main and adds a Feature of
	// their own.
	ownerGit(t, source, "reset", "-q", "--hard", "HEAD~1")
	rewritten := ownerCommits(t, source, "feature.go", "package main\n\n// Feature, the owner's\nfunc Feature() {}\n", "owner's feature")
	if _, err := a.Core.ChooseDecision(ctx, openDecision(t, a, task).ID, choiceApprove); err != nil {
		t.Fatal(err)
	}
	settle(t, a)
	task = taskByID(t, a, task.ID)

	if runner.edits < 2 {
		t.Fatalf("the implementer was not sent back to resolve the conflict: %s %s", task.Status, task.Detail)
	}
	if !implementerTold(runner, "main moved on", "conflict markers you must resolve: feature.go") {
		t.Fatal("the implementer was not given the replayed conflict")
	}
	mu.Lock()
	defer mu.Unlock()
	if !midReplay || !strings.Contains(roundTwoFeature, "<<<<<<<") || !strings.Contains(roundTwoFeature, "the owner's") || !strings.Contains(roundTwoFeature, "attempt 1") {
		t.Fatalf("the workspace should hold both Features in conflict: unmerged %t\n%s", midReplay, roundTwoFeature)
	}
	if droppedBack {
		t.Fatal("replaying brought back what the owner dropped")
	}
	if task.Base != rewritten || task.From != "main" {
		t.Fatalf("the task should be measured from the rewritten main %s: base %s from %s", rewritten, task.Base, task.From)
	}
	clone := filepath.Join(p.ScratchDirectory, "clone")
	last := task.Revisions[len(task.Revisions)-1]
	if parents := gitOut(clone, "log", "-1", "--format=%P", last.Ref); parents != rewritten {
		t.Fatalf("the resolved draft should sit on the rewritten main alone, not merge the old history: parents %s", parents)
	}
	if strings.Contains(gitOut(clone, "log", "--format=%s", last.Ref), "a commit the owner will drop") {
		t.Fatal("the dropped commit came back into the task's history")
	}
}

func TestUnrecordedReplayKeepsTheOldBase(t *testing.T) {
	t.Parallel()
	source := ownerRepo(t)
	ownerCommits(t, source, "dropped.go", "package main\n", "dropped")
	runner := &codeRunner{scriptedRunner: scriptedRunner{reviews: passes(12)}}
	a, p := pushProjectApp(t, runner, source)
	task := queue(t, a, p, "Add Feature")
	settle(t, a)
	task = taskByID(t, a, task.ID)
	oldBase := task.Base
	ownerGit(t, source, "reset", "-q", "--hard", "HEAD~1")
	newBase := ownerCommits(t, source, "feature.go", "package main\nfunc Feature() {}\n", "replacement")
	m, task := a.testMedium(t, p.ID, task.ID)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		prepared, err := a.prepareWorkspace(ctx, task, m)
		if err != nil {
			t.Fatal(err)
		}
		moved, _, integration, err := a.takeInLanded(ctx, prepared, m)
		if err != nil {
			t.Fatal(err)
		}
		if moved.Base != newBase || integration == nil || len(integration.Conflicts) == 0 {
			t.Fatalf("missing replay: %+v", integration)
		}
		if taskByID(t, a, task.ID).Base != oldBase {
			t.Fatal("base advanced without a draft")
		}
		if _, err := os.Stat(filepath.Join(m.workspace(task), "dropped.go")); !os.IsNotExist(err) {
			t.Fatal("dropped work returned")
		}
	}
	writer, _ := task.Role("Implementer")
	a.runner = failingCatchUpRunner{runner}
	if err := a.write(ctx, p, task, m, writer); err != nil {
		t.Fatal(err)
	}
	task = taskByID(t, a, task.ID)
	if task.Base != oldBase || len(task.Revisions) != 1 || task.Failures != 1 {
		t.Fatalf("failed turn advanced the record: %+v", task)
	}
	a.runner = runner
	if err := a.write(ctx, p, task, m, writer); err != nil {
		t.Fatal(err)
	}
	task = taskByID(t, a, task.ID)
	if task.Base != newBase || len(task.Revisions) != 2 {
		t.Fatalf("resolved draft not recorded: %+v", task)
	}
	clone := filepath.Join(p.ScratchDirectory, "clone")
	if strings.Contains(gitOut(clone, "log", "--format=%s", task.Revisions[1].Ref), "dropped") {
		t.Fatal("failed turn revived dropped history")
	}

}

func TestLeftoverConflictMarkersRetryWithNamedDecision(t *testing.T) {
	t.Parallel()
	source := ownerRepo(t)
	runner := &codeRunner{scriptedRunner: scriptedRunner{reviews: passes(12)}}
	a, p := pushProjectApp(t, runner, source)
	task := queue(t, a, p, "Add Feature")
	settle(t, a)
	task = taskByID(t, a, task.ID)
	ownerCommits(t, source, "feature.go", "package main\nfunc Feature() {}\n", "owner feature")
	runner.onEdit = func(string, int) bool { return false }
	ctx := context.Background()
	if _, err := a.Core.ChooseDecision(ctx, openDecision(t, a, task).ID, choiceApprove); err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= roleRetries; i++ {
		settle(t, a)
		task = taskByID(t, a, task.ID)
		if len(task.Revisions) != 1 {
			t.Fatal("recorded unresolved draft")
		}
		if i < roleRetries {
			_, err := a.Core.UpdateTask(ctx, task.ID, func(task *core.Task, _ *core.Project) (string, error) {
				task.RetryAt = task.RetryAt.AddDate(-1, 0, 0)
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	d := openDecision(t, a, task)
	if !strings.Contains(d.Title, "couldn't resolve its conflict") || !strings.Contains(d.Context, "feature.go") || task.ResumeStatus != core.TaskWriting {
		t.Fatalf("unnamed conflict: %+v / %+v", d, task)
	}
	refusals := 0
	for _, entry := range snapshotOf(t, a).Activity {
		if strings.Contains(entry.Summary, "left conflict markers in feature.go") {
			refusals++
			if strings.Contains(entry.Summary, "trying again") {
				t.Fatalf("refusal promises a retry even when exhausted: %s", entry.Summary)
			}
		}
	}
	if refusals != roleRetries+1 {
		t.Fatalf("got %d refusal entries, want %d", refusals, roleRetries+1)
	}
	if _, err := a.Core.ChooseDecision(ctx, d.ID, choiceTryAgain); err != nil {
		t.Fatal(err)
	}
	step(t, a)
	if taskByID(t, a, task.ID).Status != core.TaskWriting {
		t.Fatal("retry did not resume writing")
	}
}

func TestApprovedCleanCatchUpLandsWithoutAnotherDecision(t *testing.T) {
	t.Parallel()
	source := ownerRepo(t)
	runner := &codeRunner{scriptedRunner: scriptedRunner{reviews: passes(12)}}
	a, p := pushProjectApp(t, runner, source)
	task := queue(t, a, p, "Add Feature")
	settle(t, a)
	task = taskByID(t, a, task.ID)
	ctx := context.Background()
	if _, err := a.Core.ChooseDecision(ctx, openDecision(t, a, task).ID, choiceApprove); err != nil {
		t.Fatal(err)
	}
	ownerCommits(t, source, "owner.go", "package main\n", "owner work")
	settle(t, a)
	task = taskByID(t, a, task.ID)
	if task.Status != core.TaskLanded || runner.edits != 1 {
		t.Fatalf("clean catch-up required more work: %+v", task)
	}
	if !activityHas(t, a, "caught up cleanly") || !activityHas(t, a, "Add Feature landed") {
		t.Fatal("catch-up or landing missing from activity")
	}
	snap, err := a.Core.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range snap.Decisions {
		if d.TaskID == task.ID && d.Status == core.DecisionOpen {
			t.Fatalf("unexpected owner decision: %+v", d)
		}
	}
}

type failingCatchUpRunner struct{ roles.Runner }

func (r failingCatchUpRunner) Run(ctx context.Context, spec roles.Spec) (roles.Result, error) {
	if strings.Contains(spec.Prompt, "conflict markers you must resolve") {
		return roles.Result{}, errors.New("synthetic failed resolving turn")
	}
	return r.Runner.Run(ctx, spec)
}
