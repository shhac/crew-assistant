package work

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/roles"
	"github.com/shhac/lib-agent-harness/session"
)

func plannedCode(t *testing.T, reviews int, plans ...string) (*Loop, *codeRunner, core.Project) {
	t.Helper()
	source := ownerRepo(t)
	script := make([]string, reviews)
	for i := range script {
		script[i] = pass
	}
	runner := &codeRunner{scriptedRunner: scriptedRunner{reviews: script, plans: plans}}
	a, _, _ := loopApp(t, &runner.scriptedRunner, "")
	a.runner = runner
	return a, runner, codeProject(t, a, source)
}

func taskNow(t *testing.T, a *Loop, id string) core.Task {
	t.Helper()
	settle(t, a)
	snap, _ := a.Core.Snapshot(context.Background())
	found, _ := findTask(snap, "", id)
	return found
}

func TestATaskIsPlannedReadOnlyAndEveryoneWorksFromThePlan(t *testing.T) {
	a, runner, p := plannedCode(t, 6, `{"summary": "Add Feature beside main.", "exists": ["main.go has main"], "changes": ["add feature.go"], "out_of_scope": ["the CLI"], "questions": [], "depends_on": []}`)
	task, _ := a.Core.QueueTask(context.Background(), p.ID, core.TaskInput{Objective: "Add A"})
	task = taskNow(t, a, task.ID)
	if task.Plan == nil || task.Plan.Summary != "Add Feature beside main." || task.Plan.Role != "Researcher" || task.Round != 1 {
		t.Fatalf("plan %+v round %d", task.Plan, task.Round)
	}
	var researcher, writer, reviewer bool
	for _, spec := range runner.seen {
		switch {
		case strings.Contains(spec.Prompt, "Plan this task before anything is written"):
			researcher = !spec.Write
		case spec.Write && !strings.Contains(spec.Prompt, "Run exactly this"):
			writer = strings.Contains(spec.Prompt, "The plan Researcher worked out") && strings.Contains(spec.Prompt, "- the CLI")
		case strings.Contains(spec.Prompt, "Do not modify anything"):
			reviewer = strings.Contains(spec.Prompt, "Add Feature beside main.") && strings.Contains(spec.Prompt, "goes beyond it")
		}
	}
	if !researcher || !writer || !reviewer {
		t.Fatalf("read-only researcher %v, writer with the plan %v, reviewer with the plan %v", researcher, writer, reviewer)
	}
}

func TestTheResearchersQuestionsComeBeforeAnyCode(t *testing.T) {
	a, runner, p := plannedCode(t, 6, `{"summary": "Unclear.", "questions": ["Which colour?"]}`)
	ctx := context.Background()
	task, _ := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Paint it"})
	task = taskNow(t, a, task.ID)
	d := openDecision(t, a, task)
	if d.Kind != core.DecisionQuestion || !strings.Contains(d.Title, "Researcher has questions") || !strings.Contains(d.Context, "1. Which colour?") || task.Stage != core.StageResearching || len(task.Revisions) != 0 {
		t.Fatalf("decision %+v task %s %s", d, task.Status, task.Stage)
	}
	if runner.edits != 0 {
		t.Fatal("code was written before the questions were answered")
	}
	a.Core.AnswerDecision(ctx, d.ID, "Blue")
	task = taskNow(t, a, task.ID)
	if task.Round != 1 || len(task.Revisions) == 0 || !strings.Contains(strings.Join(task.Direction, "\n"), "Answer to a question (1. Which colour?): Blue") {
		t.Fatalf("the answer should start round 1: round %d revisions %d direction %v", task.Round, len(task.Revisions), task.Direction)
	}
}

func TestATaskThatDependsOnUnlandedWorkWaitsAndIsResearchedAgain(t *testing.T) {
	a, runner, p := plannedCode(t, 12)
	ctx := context.Background()
	first, _ := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Add A"})
	second, _ := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Add B"})
	runner.plans = []string{plainPlan, `{"summary": "B builds on A.", "depends_on": ["` + first.ID + `"]}`}
	first = taskNow(t, a, first.ID)
	second = taskNow(t, a, second.ID)
	if first.Status != core.TaskWaiting || second.Status != core.TaskQueued || second.Plan != nil || len(second.WaitsFor) != 1 || second.WaitsFor[0] != "Add A" {
		t.Fatalf("B should wait for A: A %s, B %s %+v %v", first.Status, second.Status, second.Plan, second.WaitsFor)
	}
	if _, err := a.Core.ChooseDecision(ctx, openDecision(t, a, first).ID, choiceApprove); err != nil {
		t.Fatal(err)
	}
	second = taskNow(t, a, second.ID)
	if second.Plan == nil || second.Plan.Summary != "Do the task as asked." || second.Status != core.TaskWaiting {
		t.Fatalf("once A landed, B should be researched again and go on: %s %+v", second.Status, second.Plan)
	}
}

func TestAPlanThatCannotBeReadIsKeptAsWritten(t *testing.T) {
	a, _, p := plannedCode(t, 6, "no json here", "still no json")
	task, _ := a.Core.QueueTask(context.Background(), p.ID, core.TaskInput{Objective: "Add A"})
	task = taskNow(t, a, task.ID)
	if task.Plan == nil || task.Plan.Summary != "still no json" || len(task.Revisions) == 0 {
		t.Fatalf("plan %+v", task.Plan)
	}
}

func TestOneMemberResearchesAndImplementsFromOneSeat(t *testing.T) {
	a := testLoop(t)
	ctx := context.Background()
	source := ownerRepo(t)
	p, _ := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Service", Directories: []string{source}, Brief: core.BriefInput{Goal: "x"}})
	ada, _ := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Ada", Kinds: []string{core.RoleResearcher, core.RoleImplementer}, Engine: "claude"})
	rn, _ := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Rune", Kinds: []string{core.RoleReviewer}, Engine: "codex"})
	project, err := a.SetTeam(ctx, p.ID, TeamChoice{Template: "code", Check: "make check", Implementer: ada.ID, Researcher: ada.ID})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range project.Playbook.Roles {
		names = append(names, r.Name+":"+strings.Join(r.Kinds, "+"))
	}
	if strings.Join(names, " ") != "Ada:implementer+researcher Reviewer:reviewer QA:qa" {
		t.Fatalf("seats %v", names)
	}
	if _, err := a.SetTeam(ctx, p.ID, TeamChoice{Template: "code", Check: "make check", Researcher: rn.ID}); err == nil || !strings.Contains(err.Error(), "doesn't hold the researcher role") {
		t.Fatalf("a member without the researcher role researched: %v", err)
	}
	none, _ := a.SetTeam(ctx, p.ID, TeamChoice{Template: "code", Check: "make check", Researcher: NoResearcher})
	for _, r := range none.Playbook.Roles {
		if r.Holds(core.RoleResearcher) {
			t.Fatal("none should leave research out")
		}
	}
	if _, err := a.SetTeam(ctx, p.ID, TeamChoice{Template: "code", Check: "make check", Implementer: ada.ID, Reviewer: ada.ID}); err == nil {
		t.Fatal("a member reviewing their own work was accepted")
	}
}

func TestAResearcherThatFailsResearchesAgainWhateverTheOwnerAnswers(t *testing.T) {
	ctx := context.Background()
	permanent := &session.CapabilityError{Engine: "claude", Code: session.CapabilitySandboxUnavailable, Phase: session.BeforeLaunch}
	for _, answer := range []string{choiceTryAgain, "Look at the CLI too"} {
		a, runner, p := plannedCode(t, 6)
		runner.fail = []error{permanent}
		task, _ := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Add A"})
		task = taskNow(t, a, task.ID)
		d := openDecision(t, a, task)
		if d.Kind != core.DecisionFailure || task.ResumeStatus != core.TaskResearching || task.Stage != core.StageResearching {
			t.Fatalf("a failed researcher reaches the owner: %+v %s %s", d, task.ResumeStatus, task.Stage)
		}
		if answer == choiceTryAgain {
			a.Core.ChooseDecision(ctx, d.ID, answer)
		} else {
			a.Core.AnswerDecision(ctx, d.ID, answer)
		}
		task = taskNow(t, a, task.ID)
		if task.Plan == nil || len(task.Revisions) == 0 {
			t.Fatalf("%q should research again, then write: plan %+v, %d revisions", answer, task.Plan, len(task.Revisions))
		}
		planned := slices.ContainsFunc(runner.seen, func(spec roles.Spec) bool {
			return strings.Contains(spec.Prompt, "Plan this task before anything is written") && strings.Contains(spec.Prompt, answer)
		})
		if answer != choiceTryAgain && !planned {
			t.Fatalf("the researcher never read %q", answer)
		}
	}
}
