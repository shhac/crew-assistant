//go:build !windows

package work

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/roles"
)

func ownerGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Owner", "GIT_AUTHOR_EMAIL=owner@example.test", "GIT_COMMITTER_NAME=Owner", "GIT_COMMITTER_EMAIL=owner@example.test")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// codeRunner edits the clone as an implementer would, and answers checks from
// a script, recording which kinds of role ran where.
type codeRunner struct {
	scriptedRunner
	edits int
	// onCheck runs before each check, to change the world between rounds.
	onCheck func()
	// onEdit, when set, runs in the implementer's workspace first; returning
	// false skips the usual change, so the round changes nothing else.
	onEdit func(dir string, n int) bool
}

func (r *codeRunner) Run(ctx context.Context, spec roles.Spec) (roles.Result, error) {
	checking := strings.Contains(spec.Prompt, "Run exactly this") || strings.Contains(spec.Prompt, "Do not modify anything")
	if !checking {
		r.mu.Lock()
		r.edits++
		r.seen = append(r.seen, spec)
		n := r.edits
		r.mu.Unlock()
		if r.onEdit != nil && !r.onEdit(spec.WorkDir, n) {
			return roles.Result{Text: "Nothing needed changing.", Session: []byte(`{"engine":"claude","id":"impl"}`)}, nil
		}
		body := "package main\n\n// Feature, attempt " + string(rune('0'+n)) + "\nfunc Feature() {}\n"
		if err := os.WriteFile(filepath.Join(spec.WorkDir, "feature.go"), []byte(body), 0600); err != nil {
			return roles.Result{}, err
		}
		return roles.Result{Text: "Added Feature.", Session: []byte(`{"engine":"claude","id":"impl"}`)}, nil
	}
	if r.onCheck != nil {
		r.onCheck()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, spec)
	reply := r.reviews[0]
	r.reviews = r.reviews[1:]
	return roles.Result{Text: reply}, nil
}

func TestCodeTaskRunsInACloneAndDeliversALocalBranch(t *testing.T) {
	source := t.TempDir()
	ownerGit(t, source, "init", "-q", "-b", "main")
	ownerGit(t, source, "config", "commit.gpgsign", "false")
	os.WriteFile(filepath.Join(source, "main.go"), []byte("package main\n"), 0600)
	ownerGit(t, source, "add", "-A")
	ownerGit(t, source, "commit", "-q", "-m", "start")
	os.WriteFile(filepath.Join(source, "wip.txt"), []byte("owner's own work"), 0600)
	start := ownerGit(t, source, "rev-parse", "HEAD")

	qaFail := `{"outcome":"revise","summary":"go vet failed.","findings":[{"criterion":"make check","note":"feature.go:3: missing doc"}],"question":""}`
	runner := &codeRunner{scriptedRunner: scriptedRunner{reviews: []string{pass, qaFail, pass, pass}}}
	a, _, _ := loopApp(t, &runner.scriptedRunner, "")
	a.runner = runner
	ctx := context.Background()
	canonical, _ := filepath.EvalSymlinks(source)
	p, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Service", Directories: []string{source}, Brief: core.BriefInput{Goal: "Add a feature"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.SetTeam(ctx, p.ID, TeamChoice{Template: "code", BranchPrefix: "paul/", Check: "make check"}); err != nil {
		t.Fatal(err)
	}
	// The earlier documents task from loopApp is not what this test is about.
	snap, _ := a.Core.Snapshot(ctx)
	a.StopTask(ctx, snap.Tasks[0].ProjectID, snap.Tasks[0].ID)
	task, err := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Add Feature"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		progressed, err := a.loopStep(ctx, false)
		if err != nil {
			t.Fatal(err)
		}
		if !progressed {
			break
		}
	}
	snap, _ = a.Core.Snapshot(ctx)
	for _, candidate := range snap.Tasks {
		if candidate.ID == task.ID {
			task = candidate
		}
	}
	if task.Status != core.TaskWaiting || len(task.Revisions) != 2 || task.Base != start || task.Playbook == nil || task.Playbook.Repo != canonical {
		t.Fatalf("task %+v", task)
	}
	d := openDecision(t, a, task)
	if d.Kind != decisionDelivery || !strings.Contains(d.Context, "paul/add-feature") || !strings.Contains(d.Context, "from main at "+start[:7]) || !strings.Contains(d.Context, "Nothing is pushed") {
		t.Fatalf("delivery decision %+v", d)
	}
	// QA ran with write access in the clone; the reviewer read-only; both saw
	// the build environment inside the clone. Roles load no instruction files,
	// so the implementer and reviewer are sent to the repository's own.
	var sawQA, sawReviewer bool
	prompts := []string{}
	for _, spec := range runner.seen {
		prompts = append(prompts, spec.Prompt)
		qa := strings.Contains(spec.Prompt, "Run exactly this")
		if qa && spec.Write {
			sawQA = true
		}
		if strings.Contains(spec.Prompt, "git diff "+start+"..HEAD") && strings.Contains(spec.Prompt, "QA runs `make check` separately") && !spec.Write {
			sawReviewer = true
		}
		if !qa && !strings.Contains(spec.Prompt, "AGENTS.md") {
			t.Fatalf("a role was not sent to the repository's instructions: %s", spec.Prompt)
		}
		if !strings.HasPrefix(spec.WorkDir, filepath.Join(p.ScratchDirectory, "clone")) || !strings.Contains(strings.Join(spec.Env, " "), "GOCACHE=") {
			t.Fatalf("a role ran outside the clone or without its build environment: %+v", spec)
		}
		// An offline build needs the modules the owner already has.
		if len(spec.Read) != 1 || !slices.Contains(spec.Env, "GOMODCACHE="+spec.Read[0]) {
			t.Fatalf("a role cannot read the Go module cache its build uses: read=%v env=%v", spec.Read, spec.Env)
		}
	}
	if !sawQA || !sawReviewer {
		t.Fatalf("qa=%v reviewer=%v", sawQA, sawReviewer)
	}
	if !strings.Contains(strings.Join(prompts, "\n"), "missing doc") {
		t.Fatal("QA's failure never reached the implementer")
	}
	if ownerGit(t, source, "status", "--porcelain") != "?? wip.txt" || ownerGit(t, source, "branch", "--list", "paul/*") != "" {
		t.Fatal("the owner's checkout changed before approval")
	}
	if _, err = a.Core.ResolveDecision(ctx, d.ID, choiceApprove); err != nil {
		t.Fatal(err)
	}
	task = settle(t, a)
	snap, _ = a.Core.Snapshot(ctx)
	for _, candidate := range snap.Tasks {
		if candidate.ID == task.ID || candidate.Objective == "Add Feature" {
			task = candidate
		}
	}
	if task.Status != core.TaskDelivered || task.DeliveredTo != "paul/add-feature" {
		t.Fatalf("not delivered: %+v", task)
	}
	if ownerGit(t, source, "rev-parse", "paul/add-feature") != task.Revisions[1].Ref || ownerGit(t, source, "rev-parse", "--abbrev-ref", "HEAD") != "main" || ownerGit(t, source, "status", "--porcelain") != "?? wip.txt" {
		t.Fatal("delivery did not create the branch at the approved revision, or touched the checkout")
	}
	files, err := a.RevisionPreview(ctx, p.ID, task.ID, 2)
	if err != nil || len(files) != 2 || !strings.Contains(files[1].Content, "attempt 2") {
		t.Fatalf("preview %+v %v", files, err)
	}
}

func TestACodeTeamOnlyWorksOnTheProjectsOwnFolders(t *testing.T) {
	a := testLoop(t)
	ctx := context.Background()
	p, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Service", Brief: core.BriefInput{Goal: "Add a feature"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.SetTeam(ctx, p.ID, TeamChoice{Template: "code", Check: "make check"}); err == nil {
		t.Fatal("a code team was set on a project with no repository")
	}
	if _, err = a.SetTeam(ctx, p.ID, TeamChoice{Template: "code", Check: "make check", Repo: t.TempDir()}); err == nil {
		t.Fatal("a code team was pointed at a folder the project does not link")
	}
}

func TestBranchNamesKeepWholeWords(t *testing.T) {
	for objective, want := range map[string]string{
		"Next-message suggestions in the chat composer": "next-message-suggestions-in-the-chat",
		"Add Feature":                  "add-feature",
		"!!!":                          "change",
		strings.Repeat("x", 50) + " y": strings.Repeat("x", 40),
	} {
		if got := slugify(objective); got != want {
			t.Errorf("slugify(%q) = %q, want %q", objective, got, want)
		}
	}
}

// The owner approves one change; the other, which started from the same
// point and touched the same file, catches up, resolves the conflict with its
// team and asks again, building on what landed. The owner only approves.
func TestTheSecondChangeCatchesUpWhenTheFirstLands(t *testing.T) {
	source := t.TempDir()
	ownerGit(t, source, "init", "-q", "-b", "main")
	ownerGit(t, source, "config", "commit.gpgsign", "false")
	os.WriteFile(filepath.Join(source, "main.go"), []byte("package main\n"), 0600)
	ownerGit(t, source, "add", "-A")
	ownerGit(t, source, "commit", "-q", "-m", "start")

	runner := &codeRunner{scriptedRunner: scriptedRunner{reviews: []string{pass, pass, pass, pass, pass, pass}}}
	a, _, _ := loopApp(t, &runner.scriptedRunner, "")
	a.runner = runner
	ctx := context.Background()
	p, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Service", Directories: []string{source}, Brief: core.BriefInput{Goal: "Add features"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.SetTeam(ctx, p.ID, TeamChoice{Template: "code", BranchPrefix: "paul/", Check: "make check"}); err != nil {
		t.Fatal(err)
	}
	snap, _ := a.Core.Snapshot(ctx)
	a.StopTask(ctx, snap.Tasks[0].ProjectID, snap.Tasks[0].ID)
	first, _ := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Add A"})
	second, _ := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Add B"})
	current := func(id string) core.Task {
		t.Helper()
		settle(t, a)
		snap, _ := a.Core.Snapshot(ctx)
		for _, task := range snap.Tasks {
			if task.ID == id {
				return task
			}
		}
		t.Fatalf("no task %s", id)
		return core.Task{}
	}
	first, second = current(first.ID), current(second.ID)
	if first.Status != core.TaskWaiting || second.Status != core.TaskWaiting || first.Base != second.Base {
		t.Fatalf("both should wait for approval from the same start: %+v\n%+v", first, second)
	}
	stale := openDecision(t, a, second)
	if _, err = a.Core.ResolveDecision(ctx, openDecision(t, a, first).ID, choiceApprove); err != nil {
		t.Fatal(err)
	}
	first, second = current(first.ID), current(second.ID)
	if first.Status != core.TaskDelivered || first.DeliveredTo != "paul/add-a" {
		t.Fatalf("the first change did not land: %+v", first)
	}
	snap, _ = a.Core.Snapshot(ctx)
	if d, _ := findDecision(snap, stale.ID); d.Status != "dismissed" {
		t.Fatalf("the out-of-date approval is still open: %+v", d)
	}
	if second.Status != core.TaskWaiting || len(second.Revisions) != 2 || second.Base != first.Revisions[0].Ref {
		t.Fatalf("the second change did not catch up: %+v", second)
	}
	var catchUp string
	for _, spec := range runner.seen {
		if strings.Contains(spec.Prompt, "Since this task started") {
			catchUp = spec.Prompt
		}
	}
	if !strings.Contains(catchUp, `"Add A" landed on branch paul/add-a`) || !strings.Contains(catchUp, "conflict markers you must resolve: feature.go") {
		t.Fatalf("the implementer was not told what landed and what conflicts: %q", catchUp)
	}
	d := openDecision(t, a, second)
	if !strings.Contains(d.Context, "It builds on Add A, which landed first") {
		t.Fatalf("the owner is not told the change builds on what landed: %s", d.Context)
	}
	if _, err = a.Core.ResolveDecision(ctx, d.ID, choiceApprove); err != nil {
		t.Fatal(err)
	}
	if second = current(second.ID); second.Status != core.TaskDelivered || second.DeliveredTo != "paul/add-b" {
		t.Fatalf("the second change did not land: %+v", second)
	}
	// Both land in the owner's repository, the second on top of the first.
	cmd := exec.Command("git", "merge-base", "--is-ancestor", "paul/add-a", "paul/add-b")
	cmd.Dir = source
	if err = cmd.Run(); err != nil {
		t.Fatal("the second branch does not include the first")
	}
	if got := ownerGit(t, source, "show", "paul/add-b:feature.go"); !strings.Contains(got, "attempt 3") || strings.Contains(got, "<<<<<<<") {
		t.Fatalf("the landed file is not the resolved one: %q", got)
	}
}

// Two changes delivered as branches, the second built on the first, land on
// main later under a new landing policy: in order, catching up with the
// owner's own commits, never forcing, and never touching uncommitted work.
func TestDeliveredChangesLandOnMainInTheOrderTheyWereBuilt(t *testing.T) {
	source := t.TempDir()
	ownerGit(t, source, "init", "-q", "-b", "main")
	ownerGit(t, source, "config", "commit.gpgsign", "false")
	os.WriteFile(filepath.Join(source, "main.go"), []byte("package main\n"), 0600)
	ownerGit(t, source, "add", "-A")
	ownerGit(t, source, "commit", "-q", "-m", "start")

	reviews := make([]string, 12)
	for i := range reviews {
		reviews[i] = pass
	}
	runner := &codeRunner{scriptedRunner: scriptedRunner{reviews: reviews}}
	a, _, _ := loopApp(t, &runner.scriptedRunner, "")
	a.runner = runner
	ctx := context.Background()
	p, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Service", Directories: []string{source}, Brief: core.BriefInput{Goal: "Add features"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.SetTeam(ctx, p.ID, TeamChoice{Template: "code", BranchPrefix: "paul/", Check: "make check"}); err != nil {
		t.Fatal(err)
	}
	snap, _ := a.Core.Snapshot(ctx)
	a.StopTask(ctx, snap.Tasks[0].ProjectID, snap.Tasks[0].ID)
	first, _ := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Add A"})
	second, _ := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Add B"})
	current := func(id string) core.Task {
		t.Helper()
		settle(t, a)
		snap, _ := a.Core.Snapshot(ctx)
		for _, task := range snap.Tasks {
			if task.ID == id {
				return task
			}
		}
		t.Fatalf("no task %s", id)
		return core.Task{}
	}
	approve := func(task core.Task) {
		t.Helper()
		if _, err := a.Core.ResolveDecision(ctx, openDecision(t, a, task).ID, choiceApprove); err != nil {
			t.Fatal(err)
		}
	}
	first, second = current(first.ID), current(second.ID)
	approve(first)
	first = current(first.ID)
	approve(current(second.ID))
	first, second = current(first.ID), current(second.ID)
	if first.Status != core.TaskDelivered || second.Status != core.TaskDelivered {
		t.Fatalf("both should be delivered as branches first: %s %s", first.Status, second.Status)
	}

	// The owner works on main meanwhile, and decides this project lands there.
	os.WriteFile(filepath.Join(source, "owner.go"), []byte("package main\n"), 0600)
	ownerGit(t, source, "add", "owner.go")
	ownerGit(t, source, "commit", "-q", "-m", "owner work")
	ownerWork := ownerGit(t, source, "rev-parse", "HEAD")
	if _, err = a.SetLanding(ctx, p.ID, core.LandPolicy{Via: core.LandPush, Target: "main", Means: "fast-forward main"}); err != nil {
		t.Fatal(err)
	}
	// Changing the team's engines never changes where its work lands.
	changed, err := a.SetTeam(ctx, p.ID, TeamChoice{Template: "code", ReviewerEngine: "claude", BranchPrefix: "paul/", Check: "make check"})
	if err != nil || changed.Playbook.Land.Target != "main" || changed.Playbook.Land.Way() != core.LandPush {
		t.Fatalf("choosing a team reset the landing policy: %+v %v", changed.Playbook, err)
	}

	if _, err = a.LandTask(ctx, p.ID, second.ID); err == nil || !strings.Contains(err.Error(), `built on "Add A"`) {
		t.Fatalf("the second change could land before the first it is built on: %v", err)
	}
	if _, err = a.LandTask(ctx, p.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	first = current(first.ID)
	if first.Status != core.TaskLanded || ownerGit(t, source, "rev-parse", "main") != first.Revisions[len(first.Revisions)-1].Ref {
		t.Fatalf("the first change did not land on main: %+v", first)
	}
	caughtUp := first.Revisions[len(first.Revisions)-1]
	if caughtUp.CleanMergeOf != first.Approved {
		t.Fatalf("landing did not catch up cleanly from the approved draft: %+v", caughtUp)
	}
	if _, err = os.Stat(filepath.Join(source, "feature.go")); err != nil {
		t.Fatal("the owner's clean checkout was not brought up to date")
	}

	// Uncommitted work in the owner's checkout stops the second landing
	// without touching it; after they commit, trying again lands it.
	os.WriteFile(filepath.Join(source, "main.go"), []byte("package main // wip\n"), 0600)
	if _, err = a.LandTask(ctx, p.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	second = current(second.ID)
	d := openDecision(t, a, second)
	if d.Kind != decisionFailure || !strings.Contains(d.Context, "uncommitted changes") {
		t.Fatalf("expected the owner to be asked about their uncommitted work: %+v", d)
	}
	if raw, _ := os.ReadFile(filepath.Join(source, "main.go")); string(raw) != "package main // wip\n" {
		t.Fatal("the owner's uncommitted work was touched")
	}
	ownerGit(t, source, "commit", "-q", "-am", "owner wip")
	ownerWip := ownerGit(t, source, "rev-parse", "HEAD")
	if _, err = a.Core.ResolveDecision(ctx, d.ID, choiceTryAgain); err != nil {
		t.Fatal(err)
	}
	second = current(second.ID)
	if second.Status != core.TaskLanded {
		t.Fatalf("the second change did not land: %+v", second)
	}
	// Nothing was lost, and the first landed before the second.
	head := ownerGit(t, source, "rev-parse", "main")
	for name, commit := range map[string]string{"owner work": ownerWork, "owner wip": ownerWip, "first": first.Revisions[len(first.Revisions)-1].Ref, "second's approved draft": second.Revisions[second.Approved-1].Ref} {
		cmd := exec.Command("git", "merge-base", "--is-ancestor", commit, head)
		cmd.Dir = source
		if cmd.Run() != nil {
			t.Errorf("main lost %s", name)
		}
	}
	if ownerGit(t, source, "rev-parse", "--abbrev-ref", "HEAD") != "main" || ownerGit(t, source, "status", "--porcelain") != "" {
		t.Fatal("the owner's checkout was left on another branch or dirty")
	}
}

// A target that moves every time the change catches up comes to the owner
// after a bounded number of catch-ups, instead of looping; their retry starts
// the count again.
func TestATargetThatKeepsMovingComesToTheOwner(t *testing.T) {
	source := t.TempDir()
	ownerGit(t, source, "init", "-q", "-b", "main")
	ownerGit(t, source, "config", "commit.gpgsign", "false")
	os.WriteFile(filepath.Join(source, "main.go"), []byte("package main\n"), 0600)
	ownerGit(t, source, "add", "-A")
	ownerGit(t, source, "commit", "-q", "-m", "start")
	reviews := make([]string, 40)
	for i := range reviews {
		reviews[i] = pass
	}
	moves := 0
	move := func() {
		moves++
		name := fmt.Sprintf("owner%d.go", moves)
		os.WriteFile(filepath.Join(source, name), []byte("package main\n"), 0600)
		ownerGit(t, source, "add", name)
		ownerGit(t, source, "commit", "-q", "-m", name)
	}
	runner := &codeRunner{scriptedRunner: scriptedRunner{reviews: reviews}}
	a, _, _ := loopApp(t, &runner.scriptedRunner, "")
	a.runner = runner
	ctx := context.Background()
	p, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Service", Directories: []string{source}, Brief: core.BriefInput{Goal: "Add a feature"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.SetTeam(ctx, p.ID, TeamChoice{Template: "code", BranchPrefix: "paul/", Check: "make check"}); err != nil {
		t.Fatal(err)
	}
	if _, err = a.SetLanding(ctx, p.ID, core.LandPolicy{Via: core.LandPush, Target: "main"}); err != nil {
		t.Fatal(err)
	}
	snap, _ := a.Core.Snapshot(ctx)
	a.StopTask(ctx, snap.Tasks[0].ProjectID, snap.Tasks[0].ID)
	task, _ := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Add A"})
	current := func() core.Task {
		t.Helper()
		settle(t, a)
		snap, _ := a.Core.Snapshot(ctx)
		for _, candidate := range snap.Tasks {
			if candidate.ID == task.ID {
				return candidate
			}
		}
		return core.Task{}
	}
	task = current()
	// From approval on, main moves before every check.
	runner.onCheck = move
	move()
	if _, err = a.Core.ResolveDecision(ctx, openDecision(t, a, task).ID, choiceApprove); err != nil {
		t.Fatal(err)
	}
	task = current()
	d := openDecision(t, a, task)
	if d.Kind != decisionFailure || !strings.Contains(d.Title, "keeps having to catch up") || task.CatchUps != maxCatchUps+1 {
		t.Fatalf("a moving target never came to the owner: %+v %+v", d, task)
	}
	// The owner lets it settle and tries again: the count starts afresh.
	runner.onCheck = nil
	if _, err = a.Core.ResolveDecision(ctx, d.ID, choiceTryAgain); err != nil {
		t.Fatal(err)
	}
	if task = current(); task.Status != core.TaskLanded {
		t.Fatalf("a retry once the target was quiet did not land: %s %s", task.Status, task.Detail)
	}
}

func TestLandingRefusesWhenItCannotTellTheOrder(t *testing.T) {
	source := t.TempDir()
	ownerGit(t, source, "init", "-q", "-b", "main")
	ownerGit(t, source, "config", "commit.gpgsign", "false")
	os.WriteFile(filepath.Join(source, "main.go"), []byte("package main\n"), 0600)
	ownerGit(t, source, "add", "-A")
	ownerGit(t, source, "commit", "-q", "-m", "start")
	runner := &codeRunner{scriptedRunner: scriptedRunner{reviews: []string{pass, pass}}}
	a, _, _ := loopApp(t, &runner.scriptedRunner, "")
	a.runner = runner
	ctx := context.Background()
	p, _ := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Service", Directories: []string{source}, Brief: core.BriefInput{Goal: "x"}})
	if _, err := a.SetTeam(ctx, p.ID, TeamChoice{Template: "code", BranchPrefix: "paul/", Check: "make check"}); err != nil {
		t.Fatal(err)
	}
	snap, _ := a.Core.Snapshot(ctx)
	a.StopTask(ctx, snap.Tasks[0].ProjectID, snap.Tasks[0].ID)
	task, _ := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Add A"})
	settle(t, a)
	snap, _ = a.Core.Snapshot(ctx)
	for _, candidate := range snap.Tasks {
		if candidate.ID == task.ID {
			task = candidate
		}
	}
	a.Core.ResolveDecision(ctx, openDecision(t, a, task).ID, choiceApprove)
	settle(t, a)
	// Another change in the project names a commit git has never seen.
	other, _ := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Ghost"})
	a.Core.UpdateTask(ctx, other.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.Status, t.Revisions = core.TaskDelivered, []core.Revision{{N: 1, Ref: strings.Repeat("f", 40)}}
		return "", nil
	})
	if _, err := a.SetLanding(ctx, p.ID, core.LandPolicy{Via: core.LandPush, Target: "main"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.LandTask(ctx, p.ID, task.ID); err == nil || !strings.Contains(err.Error(), "could not tell") {
		t.Fatalf("landed without knowing the order: %v", err)
	}
}
