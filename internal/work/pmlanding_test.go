//go:build !windows

package work

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
)

// pmPush is a code project that lands on main by push, whose team has Pim
// as its PM, with landing left to the PM unless approve says otherwise.
type pmPush struct {
	a      *Loop
	p      core.Project
	runner *codeRunner
	source string
}

func newPMPush(t *testing.T, approve, maxRounds string, reviews ...string) pmPush {
	t.Helper()
	source := ownerRepo(t)
	runner := &codeRunner{scriptedRunner: scriptedRunner{reviews: reviews}}
	a, _, _ := loopApp(t, &runner.scriptedRunner, "")
	a.runner = runner
	ctx := context.Background()
	pim, err := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Pim", Kinds: []string{core.RolePM}, Engine: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Service", Directories: []string{source}, Brief: core.BriefInput{Goal: "Add features"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.SetTeam(ctx, p.ID, TeamChoice{Template: "code", BranchPrefix: "paul/", Check: "make check", PM: pim.ID, MaxRounds: maxRounds}); err != nil {
		t.Fatal(err)
	}
	if p, err = a.SetLanding(ctx, p.ID, core.LandPolicy{Via: core.LandPush, Target: "main", Approve: approve}); err != nil {
		t.Fatal(err)
	}
	snap, _ := a.Core.Snapshot(ctx)
	for _, task := range snap.Tasks {
		if task.ProjectID != p.ID {
			a.StopTask(ctx, task.ProjectID, task.ID)
		}
	}
	return pmPush{a: a, p: p, runner: runner, source: source}
}

func (w pmPush) task(t *testing.T, id string) core.Task {
	t.Helper()
	settle(t, w.a)
	snap, _ := w.a.Core.Snapshot(context.Background())
	found, ok := findTask(snap, w.p.ID, id)
	if !ok {
		t.Fatalf("no task %s", id)
	}
	return found
}

// asked counts the PM's turns deciding whether a change lands.
func (w pmPush) asked() int {
	w.runner.mu.Lock()
	defer w.runner.mu.Unlock()
	n := 0
	for _, spec := range w.runner.seen {
		if strings.Contains(spec.Prompt, "Decide whether this change lands") {
			n++
		}
	}
	return n
}

// commitOnMain is the owner committing a file to main meanwhile.
func (w pmPush) commitOnMain(t *testing.T, name, body string) {
	os.WriteFile(filepath.Join(w.source, name), []byte(body), 0600)
	ownerGit(t, w.source, "add", name)
	ownerGit(t, w.source, "commit", "-q", "-m", "owner: "+name)
}

func activityHas(t *testing.T, a *Loop, text string) bool {
	t.Helper()
	snap, _ := a.Core.Snapshot(context.Background())
	return slices.ContainsFunc(snap.Activity, func(e core.Activity) bool { return strings.Contains(e.Summary, text) })
}

const qaFailOnce = `{"outcome":"revise","summary":"make check failed.","findings":[{"criterion":"make check","note":"feature.go: missing doc"}],"question":""}`

func passes(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = pass
	}
	return out
}

func TestWithoutThePMOptionTheOwnerStillApproves(t *testing.T) {
	for _, approve := range []string{"", core.ApproveBefore} {
		w := newPMPush(t, approve, "", passes(4)...)
		task, _ := w.a.Core.QueueTask(context.Background(), w.p.ID, core.TaskInput{Objective: "Add A"})
		task = w.task(t, task.ID)
		if d := openDecision(t, w.a, task); d.Kind != core.DecisionDelivery || d.Title != "Land “Add A” on main" {
			t.Fatalf("approve %q: the owner was not asked: %+v", approve, d)
		}
		if w.asked() != 0 || task.LandDecision != nil {
			t.Fatalf("approve %q: the PM was asked to land", approve)
		}
	}
}

func TestThePMLandsASignedOffChangeOnAPushProject(t *testing.T) {
	w := newPMPush(t, core.ApprovePM, "", passes(4)...)
	w.runner.pmLand = []string{`{"land": true, "reason": "nothing else waits on main"}`}
	ctx := context.Background()
	task, _ := w.a.Core.QueueTask(ctx, w.p.ID, core.TaskInput{Objective: "Add A", Criteria: []string{"Keep it small"}})
	task = w.task(t, task.ID)
	if task.Status != core.TaskLanded || !landedAsOne(t, w.source, filepath.Join(w.p.ScratchDirectory, "clone"), "main", task) {
		t.Fatalf("the PM's approval did not land it: %s %s", task.Status, task.Detail)
	}
	snap, _ := w.a.Core.Snapshot(ctx)
	for _, d := range snap.Decisions {
		if d.TaskID == task.ID {
			t.Fatalf("the owner was asked: %+v", d)
		}
	}
	if task.LandDecision == nil || !task.LandDecision.Land || task.LandDecision.By != core.LandByPM || task.LandDecision.Reason != "nothing else waits on main" {
		t.Fatalf("the PM's decision is not on the task: %+v", task.LandDecision)
	}
	if !activityHas(t, w.a, "The PM approved Add A to land as one commit: nothing else waits on main") ||
		!activityHas(t, w.a, "The PM landed Add A on main as one commit: nothing else waits on main") {
		t.Fatal("the PM's decision is not in the activity")
	}
	// The PM decided once, reading only its prompt, which carried the change
	// and its checks.
	if w.asked() != 1 {
		t.Fatalf("the PM was asked %d times", w.asked())
	}
	for _, spec := range w.runner.seen {
		if strings.Contains(spec.Prompt, "Decide whether this change lands") {
			if spec.Write || !strings.Contains(spec.Prompt, "Add A") || !strings.Contains(spec.Prompt, "Keep it small") || !strings.Contains(spec.Prompt, "QA: Meets the brief.") {
				t.Fatalf("the PM's turn %+v", spec)
			}
		}
	}
	// The task's branch is cleaned up once its change has landed.
	if w.branchLeft(t, task) {
		t.Fatal("the landed task's branch was left in the clone")
	}
}

// branchLeft reports whether the task's branch is still in the clone.
func (w pmPush) branchLeft(t *testing.T, task core.Task) bool {
	t.Helper()
	return ownerGit(t, filepath.Join(w.p.ScratchDirectory, "clone"), "branch", "--list", task.Branch) != ""
}

// The PM may land a change keeping the team's own commits: main moves
// forward onto them as they are, and the branch is cleaned up as well.
func TestThePMCanLandAChangeKeepingItsCommits(t *testing.T) {
	w := newPMPush(t, core.ApprovePM, "", pass, qaFailOnce, pass, pass)
	w.runner.pmLand = []string{`{"land": true, "how": "fast-forward", "reason": "both drafts read well on their own"}`}
	ctx := context.Background()
	start := ownerGit(t, w.source, "rev-parse", "main")
	task, _ := w.a.Core.QueueTask(ctx, w.p.ID, core.TaskInput{Objective: "Add A"})
	task = w.task(t, task.ID)
	if task.Status != core.TaskLanded || len(task.Revisions) != 2 {
		t.Fatalf("not landed after two drafts: %s %s %d", task.Status, task.Detail, len(task.Revisions))
	}
	// main is the task's last draft itself, with the first under it.
	tip := task.Revisions[1].Ref
	if ownerGit(t, w.source, "rev-parse", "main") != tip || ownerGit(t, w.source, "rev-parse", "main^") != task.Revisions[0].Ref || ownerGit(t, w.source, "rev-parse", "main~2") != start {
		t.Fatalf("main was not fast-forwarded onto the task's commits:\n%s", ownerGit(t, w.source, "log", "--format=%H %s", "main"))
	}
	if !activityHas(t, w.a, "The PM landed Add A on main keeping its commits: both drafts read well on their own") {
		t.Fatal("how the PM landed it is not in the activity")
	}
	if w.branchLeft(t, task) {
		t.Fatal("the landed task's branch was left in the clone")
	}
}

// The owner lands a signed-off change themselves while the PM is still
// deciding; the PM's answer, arriving after, changes nothing.
func TestTheOwnerCanLandAheadOfThePM(t *testing.T) {
	w := newPMPush(t, core.ApprovePM, "", passes(2)...)
	w.runner.pmLand = []string{`{"land": false, "reason": "wait for the schema"}`}
	ctx := context.Background()
	var id string
	w.runner.onPMLand = func() {
		snap, _ := w.a.Core.Snapshot(ctx)
		if waiting, _ := findTask(snap, w.p.ID, id); !waiting.PMDeciding {
			t.Errorf("the task isn't shown as waiting on the PM: %+v", waiting)
		}
		if _, err := w.a.LandTask(ctx, w.p.ID, id); err != nil {
			t.Error(err)
		}
	}
	task, _ := w.a.Core.QueueTask(ctx, w.p.ID, core.TaskInput{Objective: "Add A"})
	id = task.ID
	task = w.task(t, task.ID)
	if task.Status != core.TaskLanded || task.LandDecision != nil || !landedAsOne(t, w.source, filepath.Join(w.p.ScratchDirectory, "clone"), "main", task) {
		t.Fatalf("the owner's landing did not stand: %s %s %+v", task.Status, task.Detail, task.LandDecision)
	}
	if !activityHas(t, w.a, "You're landing Add A ahead of the PM") || activityHas(t, w.a, "The PM held") {
		t.Fatal("the activity doesn't say the owner landed it, or the PM's late hold took effect")
	}
}

func TestThePMHoldsAChangeAndTheOwnerCanLandIt(t *testing.T) {
	w := newPMPush(t, core.ApprovePM, "", passes(4)...)
	w.runner.pmLand = []string{"```json\n{\"land\": false, \"reason\": \"the API change should land first\"}\n```"}
	ctx := context.Background()
	task, _ := w.a.Core.QueueTask(ctx, w.p.ID, core.TaskInput{Objective: "Add A"})
	start := ownerGit(t, w.source, "rev-parse", "main")
	task = w.task(t, task.ID)
	d := openDecision(t, w.a, task)
	if task.Status != core.TaskWaiting || d.Kind != core.DecisionDelivery || !strings.Contains(d.Title, "Pim held") || !strings.Contains(d.Context, "the API change should land first") {
		t.Fatalf("not held for the owner: %+v %+v", task, d)
	}
	if task.LandDecision == nil || task.LandDecision.Land || !activityHas(t, w.a, "The PM held Add A: the API change should land first") {
		t.Fatalf("the hold and its reason are not shown: %+v", task.LandDecision)
	}
	if ownerGit(t, w.source, "rev-parse", "main") != start {
		t.Fatal("a held change reached main")
	}
	// The owner lands it themselves.
	if _, err := w.a.Core.ChooseDecision(ctx, d.ID, choiceApprove); err != nil {
		t.Fatal(err)
	}
	if task = w.task(t, task.ID); task.Status != core.TaskLanded || task.LandDecision != nil {
		t.Fatalf("the owner's approval did not land it: %+v", task)
	}
	if w.asked() != 1 {
		t.Fatalf("the PM was asked again after the owner approved: %d", w.asked())
	}
}

func TestThePMIsNotAskedAboutAChangeThatIsNotSignedOff(t *testing.T) {
	w := newPMPush(t, core.ApprovePM, "", ask, pass)
	ctx := context.Background()
	// A reviewer's question goes to the owner, not the PM.
	task, _ := w.a.Core.QueueTask(ctx, w.p.ID, core.TaskInput{Objective: "Add A"})
	task = w.task(t, task.ID)
	if d := openDecision(t, w.a, task); d.Kind != core.DecisionQuestion || w.asked() != 0 {
		t.Fatalf("the PM was asked about a change with a question open: %+v", d)
	}
	w.a.StopTask(ctx, w.p.ID, task.ID)

	// A change whose dependency hasn't landed comes to the owner, saying why.
	w.runner.reviews = passes(2)
	dep, _ := w.a.Core.QueueTask(ctx, w.p.ID, core.TaskInput{Objective: "Schema"})
	w.a.Core.UpdateTask(ctx, dep.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.Status = core.TaskDelivered
		return "", nil
	})
	task, _ = w.a.Core.QueueTask(ctx, w.p.ID, core.TaskInput{Objective: "Add B"})
	w.a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.DependsOn = []string{dep.ID}
		return "", nil
	})
	task = w.task(t, task.ID)
	d := openDecision(t, w.a, task)
	if d.Kind != core.DecisionDelivery || !strings.Contains(d.Context, "The PM can't land it yet") || !strings.Contains(d.Context, "“Schema”") || w.asked() != 0 {
		t.Fatalf("a change with an unlanded dependency: %+v, asked %d", d, w.asked())
	}
}

func TestPullRequestAndBranchProjectsCannotLeaveLandingToThePM(t *testing.T) {
	w := newPMPush(t, "", "")
	ctx := context.Background()
	for _, land := range []core.LandPolicy{
		{Via: core.LandPullRequest, Target: "main", GitHub: "owner/service", Approve: core.ApprovePM},
		{Via: core.LandBranch, Approve: core.ApprovePM},
	} {
		if _, err := w.a.SetLanding(ctx, w.p.ID, land); err == nil || !strings.Contains(err.Error(), "only for changes that land by push") {
			t.Errorf("%s: %v", land.Via, err)
		}
	}
}

// The owner takes the decision back from the PM while a task is under way:
// it comes to them, although the task started while the PM decided.
func TestTheOwnerCanTakeTheDecisionBack(t *testing.T) {
	w := newPMPush(t, core.ApprovePM, "", passes(4)...)
	ctx := context.Background()
	w.runner.onCheck = func() {
		w.runner.onCheck = nil
		if _, err := w.a.SetLanding(ctx, w.p.ID, core.LandPolicy{Via: core.LandPush, Target: "main"}); err != nil {
			t.Error(err)
		}
	}
	task, _ := w.a.Core.QueueTask(ctx, w.p.ID, core.TaskInput{Objective: "Add A"})
	task = w.task(t, task.ID)
	if task.Playbook.Land.Approve != core.ApprovePM {
		t.Fatalf("the task should have started under the PM: %+v", task.Playbook.Land)
	}
	if d := openDecision(t, w.a, task); d.Kind != core.DecisionDelivery || w.asked() != 0 {
		t.Fatalf("the owner was not asked: %+v, PM asked %d", d, w.asked())
	}
}

// A PM that cannot answer leaves the decision to the owner, who can stop
// the task.
func TestAnUnreadablePMLeavesTheLandingToTheOwner(t *testing.T) {
	w := newPMPush(t, core.ApprovePM, "", passes(2)...)
	w.runner.pmLand = []string{"land it, I think", `{"land": true}`}
	ctx := context.Background()
	task, _ := w.a.Core.QueueTask(ctx, w.p.ID, core.TaskInput{Objective: "Add A"})
	task = w.task(t, task.ID)
	d := openDecision(t, w.a, task)
	if d.Kind != core.DecisionDelivery || !strings.Contains(d.Context, "Pim couldn't decide") || w.asked() != 2 || task.LandDecision != nil {
		t.Fatalf("the owner was not asked: %+v", d)
	}
	if _, err := w.a.StopTask(ctx, w.p.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	snap, _ := w.a.Core.Snapshot(ctx)
	if d, _ = findDecision(snap, d.ID); d.Status != core.DecisionDismissed {
		t.Fatalf("the decision is still open: %+v", d)
	}
}

// Each time main conflicts with the change the PM approved, it goes back to
// the implementer; the second time, the owner decides.
func TestRepeatedLandingConflictsComeToTheOwner(t *testing.T) {
	w := newPMPush(t, core.ApprovePM, "", passes(12)...)
	conflicts := 0
	w.runner.onPMLand = func() {
		conflicts++
		w.commitOnMain(t, "feature.go", fmt.Sprintf("package main\n\n// The owner's own feature, take %d\nfunc Feature() {}\n", conflicts))
	}
	ctx := context.Background()
	task, _ := w.a.Core.QueueTask(ctx, w.p.ID, core.TaskInput{Objective: "Add A"})
	task = w.task(t, task.ID)
	d := openDecision(t, w.a, task)
	if w.asked() != 2 || d.Kind != core.DecisionDelivery || !strings.Contains(d.Title, "failed to land twice") || !strings.Contains(d.Context, "Catching up conflicted") {
		t.Fatalf("repeated conflicts did not come to the owner: asked %d, %+v", w.asked(), d)
	}
	if len(task.LandingFailures) != 2 {
		t.Fatalf("failures %v", task.LandingFailures)
	}
	// The PM approved it twice, and it never landed: the activity says so.
	if !activityHas(t, w.a, "The PM approved Add A to land") || activityHas(t, w.a, "The PM landed") {
		t.Fatal("the activity says the PM landed a change that went back to the implementer")
	}
	// Each conflict went to the implementer to resolve.
	resolved := 0
	for _, spec := range w.runner.seen {
		if strings.Contains(spec.Prompt, "conflict markers you must resolve: feature.go") {
			resolved++
		}
	}
	if resolved != 2 {
		t.Fatalf("the implementer resolved %d conflicts", resolved)
	}
	// The owner lands the resolved change; main keeps their work under it.
	w.runner.onPMLand = nil
	if _, err := w.a.Core.ChooseDecision(ctx, d.ID, choiceApprove); err != nil {
		t.Fatal(err)
	}
	if task = w.task(t, task.ID); task.Status != core.TaskLanded || len(task.LandingFailures) != 0 || w.asked() != 2 {
		t.Fatalf("the owner's approval did not land it: %s %s", task.Status, task.Detail)
	}
}

// QA failing on the change the PM approved, merged with main, sends it back
// to the implementer with QA's findings; at the round limit, the owner
// decides rather than the PM.
func TestAFailedCheckOnTheMergedResultGoesBackThenToTheOwner(t *testing.T) {
	qaFail := `{"outcome":"revise","summary":"make check failed after the merge.","findings":[{"criterion":"make check","note":"owner.go and feature.go both declare Helper"}],"question":""}`
	w := newPMPush(t, core.ApprovePM, "2", pass, pass, qaFail, pass, pass)
	w.runner.onPMLand = func() {
		w.runner.onPMLand = nil
		w.commitOnMain(t, "owner.go", "package main\n")
	}
	ctx := context.Background()
	task, _ := w.a.Core.QueueTask(ctx, w.p.ID, core.TaskInput{Objective: "Add A"})
	task = w.task(t, task.ID)
	var toImplementer bool
	for _, spec := range w.runner.seen {
		toImplementer = toImplementer || spec.Write && !strings.Contains(spec.Prompt, "Run exactly this") && strings.Contains(spec.Prompt, "both declare Helper")
	}
	if !toImplementer {
		t.Fatal("QA's failure on the merged result never reached the implementer")
	}
	d := openDecision(t, w.a, task)
	if w.asked() != 1 || task.Round != 2 || d.Kind != core.DecisionDelivery || !strings.Contains(d.Title, "failed to land once") || !strings.Contains(d.Context, "The checks failed on it merged") {
		t.Fatalf("at the round limit the owner should decide: asked %d round %d %+v", w.asked(), task.Round, d)
	}
	if !activityHas(t, w.a, "The PM approved Add A to land") || activityHas(t, w.a, "The PM landed") || activityHas(t, w.a, "Add A landed") {
		t.Fatal("the activity says a change landed when QA failed it on the merged result")
	}
}

// A daemon that stops after the PM decided never asks again or lands twice:
// here the push went out before it stopped, and the restart finds it there.
func TestARestartAfterThePMDecidedNeverLandsTwice(t *testing.T) {
	w := newPMPush(t, core.ApprovePM, "", passes(4)...)
	ctx := context.Background()
	task, _ := w.a.Core.QueueTask(ctx, w.p.ID, core.TaskInput{Objective: "Add A"})
	for i := 0; i < 50 && task.Status != core.TaskLanding; i++ {
		step(t, w.a)
		snap, _ := w.a.Core.Snapshot(ctx)
		task, _ = findTask(snap, w.p.ID, task.ID)
	}
	if task.Status != core.TaskLanding || task.LandDecision == nil || w.asked() != 1 {
		t.Fatalf("the PM's decision was not recorded before landing: %+v", task)
	}
	tip := task.Revisions[len(task.Revisions)-1].Ref
	ownerGit(t, w.source, "fetch", "-q", filepath.Join(w.p.ScratchDirectory, "clone"), task.Branch)
	ownerGit(t, w.source, "merge", "-q", "--ff-only", tip)

	restarted := New(w.a.Core, w.a.Config, false)
	restarted.runner, restarted.meter = w.runner, w.a.meter
	settle(t, restarted)
	snap, _ := restarted.Core.Snapshot(ctx)
	task, _ = findTask(snap, w.p.ID, task.ID)
	if task.Status != core.TaskLanded || !strings.Contains(task.Detail, "already there") || w.asked() != 1 {
		t.Fatalf("the restart did not find it landed: %s %s, asked %d", task.Status, task.Detail, w.asked())
	}
	if !activityHas(t, restarted, "The PM landed Add A on main as one commit") {
		t.Fatal("the landing found after the restart isn't recorded as the PM's")
	}
	if ownerGit(t, w.source, "rev-parse", "main") != tip {
		t.Fatal("the change landed twice")
	}
	if w.branchLeft(t, task) {
		t.Fatal("the branch was left after the restart found the change landed")
	}
}
