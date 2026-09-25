package work

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/roles"
	"github.com/shhac/lib-agent-harness/session"
)

const askDesign = "I need a steer first.\n```design\nFormal or casual?\n```"

// seatDesigner gives a project's team Dee, a member who only designs.
func seatDesigner(t *testing.T, a *Loop, projectID string) core.Member {
	t.Helper()
	ctx := context.Background()
	dee, err := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Dee", Kinds: []string{core.RoleDesigner}, Engine: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.SetSeat(ctx, projectID, core.RoleDesigner, dee.ID); err != nil {
		t.Fatal(err)
	}
	return dee
}

// stepUntil runs loop steps until the task is as wanted.
func stepUntil(t *testing.T, a *Loop, id string, want func(core.Task) bool) core.Task {
	t.Helper()
	for i := 0; i < 50; i++ {
		snap, _ := a.Core.Snapshot(context.Background())
		task, _ := findTask(snap, "", id)
		if want(task) {
			return task
		}
		progressed, err := a.loopStep(context.Background(), false)
		if err != nil {
			t.Fatal(err)
		}
		if !progressed {
			t.Fatalf("the loop stopped first: %s %+v", task.Status, task)
		}
	}
	t.Fatal("the loop never got there")
	return core.Task{}
}

func withDesigner(task core.Task) bool { return task.Status == core.TaskDesigning }

// turns counts the role turns whose prompt says this.
func turns(r *scriptedRunner, says string) []roles.Spec {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []roles.Spec
	for _, spec := range r.seen {
		if strings.Contains(spec.Prompt, says) {
			out = append(out, spec)
		}
	}
	return out
}

func writerTurns(r *scriptedRunner) []roles.Spec {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []roles.Spec
	for _, spec := range r.seen {
		if spec.Write {
			out = append(out, spec)
		}
	}
	return out
}

func TestTheResearcherHandsTheTaskToTheDesignerAndGetsItBack(t *testing.T) {
	a, runner, p := plannedCode(t, 6, `{"design": "Tabs or a sidebar?"}`)
	seatDesigner(t, a, p.ID)
	task, _ := a.Core.QueueTask(context.Background(), p.ID, core.TaskInput{Objective: "Add A"})
	task = stepUntil(t, a, task.ID, withDesigner)
	if task.Stage != core.StageResearching || !task.WithDesigner || task.Checking != "Dee" || task.Detail != "With Dee for design input" || task.Plan != nil {
		t.Fatalf("with the designer: stage %s, with designer %v, checking %q, %q", task.Stage, task.WithDesigner, task.Checking, task.Detail)
	}
	task = taskNow(t, a, task.ID)
	if len(task.Design) != 1 {
		t.Fatalf("design %+v", task.Design)
	}
	r := task.Design[0]
	if r.From != "Researcher" || r.Step != core.TaskResearching || r.Question != "Tabs or a sidebar?" || r.Designer != "Dee" || r.Input != "Keep it plain." || r.Open() {
		t.Fatalf("request %+v", r)
	}
	if task.Plan == nil || task.Plan.Role != "Researcher" || len(task.Revisions) == 0 {
		t.Fatalf("the researcher should plan with the input, then the work goes on: %+v", task.Plan)
	}
	designer := turns(&runner.scriptedRunner, "asks for your design input")
	if len(designer) != 1 || designer[0].Write || !strings.Contains(designer[0].Prompt, "Tabs or a sidebar?") || !strings.Contains(designer[0].Prompt, "Give design input only") {
		t.Fatalf("one read-only designer turn with the question: %+v", designer)
	}
	research := turns(&runner.scriptedRunner, "Plan this task before anything is written")
	if len(research) != 2 || !strings.Contains(research[1].Prompt, "Dee answered: Keep it plain.") || !strings.Contains(research[0].Prompt, `{"design": "your question for the designer"}`) {
		t.Fatalf("the researcher should get the input back: %d turns", len(research))
	}
}

// The reply the researcher is finally told to give offers the design form
// exactly while a hand-off is offered, so following it can ask for input.
func TestTheResearchersReplyContractOffersDesignOnlyWhileItCanAsk(t *testing.T) {
	p := core.Project{Title: "Service", Brief: core.Brief{Goal: "Faster"}}
	researcher := core.Role{Name: "Researcher", Kinds: []string{core.RoleResearcher}}
	dee := core.Role{Name: "Dee", Kinds: []string{core.RoleDesigner}}
	contract := func(task core.Task) string {
		prompt := researcherPrompt(p, task, nil)
		return prompt[strings.LastIndex(prompt, "Reply with only"):]
	}
	withDee := core.Task{Round: 1, Roles: []core.Role{researcher, dee}}
	if got := contract(withDee); !strings.Contains(got, `{"design": "your question for the designer"}`) || !strings.Contains(got, `"summary"`) {
		t.Fatalf("with a designer, the contract should offer a plan or a design question: %s", got)
	}
	if _, _, got, err := parsePlan(`{"design": "your question for the designer"}`, true); err != nil || got != "your question for the designer" {
		t.Fatalf("the contract's design form should be read as a hand-off: %q %v", got, err)
	}
	used := withDee
	for range core.DesignLimit {
		used.Design = append(used.Design, core.DesignRequest{Step: core.TaskResearching, Round: 1})
	}
	for name, task := range map[string]core.Task{"no designer": {Round: 1, Roles: []core.Role{researcher}}, "limit reached": used} {
		if got := contract(task); strings.Contains(got, `"design"`) {
			t.Errorf("%s: the contract offered a design question: %s", name, got)
		}
	}
}

func TestTheImplementerHandsTheTaskToTheDesignerAndGetsItBack(t *testing.T) {
	runner := &scriptedRunner{reviews: []string{pass}, writerReplies: []string{askDesign}}
	a, p, task := loopApp(t, runner, "")
	seatDesigner(t, a, p.ID)
	held := stepUntil(t, a, task.ID, withDesigner)
	if held.Stage != core.StageImplementing || !held.WithDesigner || held.Checking != "Dee" || len(held.Revisions) != 0 {
		t.Fatalf("with the designer: stage %s %v %q, %d revisions", held.Stage, held.WithDesigner, held.Checking, len(held.Revisions))
	}
	task = settle(t, a)
	if len(task.Design) != 1 || task.Design[0].From != "Writer" || task.Design[0].Step != core.TaskWriting || task.Design[0].Input != "Keep it plain." {
		t.Fatalf("design %+v", task.Design)
	}
	if len(task.Revisions) != 1 || task.Round != 1 || strings.Contains(task.Revisions[0].Summary, "Formal or casual") {
		t.Fatalf("one draft, in the first round, written after the input: %d revisions, round %d", len(task.Revisions), task.Round)
	}
	writes := writerTurns(runner)
	if len(writes) != 2 || writes[0].Resume != nil || string(writes[1].Resume) != `{"engine":"claude","id":"writer"}` {
		t.Fatalf("the writer should resume its session: %d turns", len(writes))
	}
	if !strings.Contains(writes[1].Prompt, "Writer asked: Formal or casual?") || !strings.Contains(writes[1].Prompt, "Dee answered: Keep it plain.") || !strings.Contains(writes[0].Prompt, "```design") {
		t.Fatalf("the writer should get the input back: %s", writes[1].Prompt)
	}
	designer := turns(runner, "asks for your design input")
	if len(designer) != 1 || designer[0].Write {
		t.Fatalf("one read-only designer turn: %+v", designer)
	}
}

func TestATeamWithoutADesignerNeverHandsATaskOver(t *testing.T) {
	runner := &scriptedRunner{reviews: []string{pass}, writerReplies: []string{askDesign}}
	a, _, _ := loopApp(t, runner, "")
	task := settle(t, a)
	if len(task.Design) != 0 || len(task.Revisions) != 1 || task.Revisions[0].Summary != askDesign {
		t.Fatalf("the reply should be the draft's summary, as it always was: %+v %+v", task.Design, task.Revisions)
	}
	if prompt := writerTurns(runner)[0].Prompt; strings.Contains(prompt, "design") {
		t.Fatalf("a team without a designer was told about one: %s", prompt)
	}
	coded, researcher, p := plannedCode(t, 6)
	queued, _ := coded.Core.QueueTask(context.Background(), p.ID, core.TaskInput{Objective: "Add A"})
	queued = taskNow(t, coded, queued.ID)
	if len(queued.Design) != 0 || strings.Contains(turns(&researcher.scriptedRunner, "Plan this task")[0].Prompt, "design") {
		t.Fatal("a researcher without a designer was told about one")
	}
}

func TestHandOffsToTheDesignerAreBounded(t *testing.T) {
	runner := &scriptedRunner{reviews: []string{pass}, writerReplies: []string{askDesign, askDesign, askDesign}}
	a, p, task := loopApp(t, runner, "")
	seatDesigner(t, a, p.ID)
	ctx := context.Background()
	task = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Status == core.TaskWaiting })
	d := openDecision(t, a, task)
	if d.Kind != core.DecisionQuestion || !strings.Contains(d.Title, "Writer wants more design input") || !strings.Contains(d.Context, "Formal or casual?") || task.Stage != core.StageImplementing {
		t.Fatalf("the third ask goes to the owner: %+v, stage %s", d, task.Stage)
	}
	if len(task.Design) != 3 || task.Design[2].Decision != d.ID || task.Design[2].Designer != "" || len(turns(runner, "asks for your design input")) != core.DesignLimit {
		t.Fatalf("design %+v", task.Design)
	}
	if !strings.Contains(writerTurns(runner)[2].Prompt, "the most it allows") {
		t.Fatal("the writer was not told it had had all the design input it could")
	}
	a.Core.AnswerDecision(ctx, d.ID, "Casual")
	task = settle(t, a)
	if len(task.Revisions) != 1 || task.Round != 1 || !strings.Contains(strings.Join(task.Direction, "\n"), "Casual") || task.Design[2].Open() {
		t.Fatalf("the answer goes back to the writer in the same round: %d revisions, round %d, direction %v", len(task.Revisions), task.Round, task.Direction)
	}
}

func TestADesignerThatEscalatesBringsTheOwnerADecisionAndTheTaskGoesBack(t *testing.T) {
	a, runner, p := plannedCode(t, 6, `{"design": "Tabs or a sidebar?"}`)
	runner.designs = []string{`{"input": "", "escalate": {"evidence": "Two teams use the board differently.", "alternatives": ["Tabs", "A sidebar"], "consequences": "Tabs hide work; a sidebar costs width.", "recommendation": "A sidebar"}}`}
	seatDesigner(t, a, p.ID)
	ctx := context.Background()
	task, _ := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Add A"})
	task = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Status == core.TaskWaiting })
	d := openDecision(t, a, task)
	if !strings.Contains(d.Title, "Dee needs your call") || d.Recommendation != "A sidebar" || !strings.Contains(d.Context, "Evidence: Two teams") || !strings.Contains(d.Context, "2. A sidebar") || !strings.Contains(d.Context, "Consequences:") || task.Stage != core.StageResearching {
		t.Fatalf("escalation %+v, stage %s", d, task.Stage)
	}
	a.Core.AnswerDecision(ctx, d.ID, "Go with tabs")
	task = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Status != core.TaskWaiting })
	if task.Status != core.TaskResearching || task.Round != 1 || task.Design[0].Open() {
		t.Fatalf("the answer goes back to the researcher: %s round %d", task.Status, task.Round)
	}
	task = taskNow(t, a, task.ID)
	research := turns(&runner.scriptedRunner, "Plan this task before anything is written")
	if task.Plan == nil || len(research) != 2 || !strings.Contains(research[1].Prompt, "Go with tabs") {
		t.Fatalf("the researcher should plan with the owner's answer: %+v", task.Plan)
	}
}

func TestADesignerThatFailsIsAskedAgainAndTheTaskStaysWhereItWas(t *testing.T) {
	permanent := &session.CapabilityError{Engine: "claude", Code: session.CapabilitySandboxUnavailable, Phase: session.BeforeLaunch}
	runner := &scriptedRunner{reviews: []string{pass}, writerReplies: []string{askDesign}, fail: []error{nil, permanent}}
	a, p, task := loopApp(t, runner, "")
	seatDesigner(t, a, p.ID)
	task = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Status == core.TaskWaiting })
	d := openDecision(t, a, task)
	if d.Kind != core.DecisionFailure || task.ResumeStatus != core.TaskDesigning || task.Stage != core.StageImplementing {
		t.Fatalf("a failed designer reaches the owner: %+v %s %s", d, task.ResumeStatus, task.Stage)
	}
	a.Core.ChooseDecision(context.Background(), d.ID, choiceTryAgain)
	task = settle(t, a)
	if task.Design[0].Input != "Keep it plain." || len(task.Revisions) != 1 || task.Round != 1 {
		t.Fatalf("the designer should answer on the retry: %+v, %d revisions, round %d", task.Design, len(task.Revisions), task.Round)
	}
}

func TestStoppingAndMessagingATaskWithTheDesigner(t *testing.T) {
	runner := &scriptedRunner{reviews: []string{pass}, writerReplies: []string{askDesign}}
	a, p, task := loopApp(t, runner, "")
	seatDesigner(t, a, p.ID)
	ctx := context.Background()
	task = stepUntil(t, a, task.ID, withDesigner)
	if _, err := a.Core.SendTeamMessage(ctx, p.ID, task.ID, "Dee", core.FromOwner, "Hello"); !errors.Is(err, core.ErrConflict) || !strings.Contains(err.Error(), "gives design input") {
		t.Fatalf("a message to the designer: %v", err)
	}
	if _, err := a.Core.SendTeamMessage(ctx, p.ID, task.ID, "Writer", core.FromOwner, "Keep it short"); err != nil {
		t.Fatalf("a message to the implementer while the designer works: %v", err)
	}
	if _, err := a.StopTask(ctx, p.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	// A designer turn that finishes after the stop records nothing.
	if _, err := a.Core.RecordDesign(ctx, task.ID, task.Design[0].ID, "Dee", "Too late", nil); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("a stopped task took design input: %v", err)
	}
	task = settle(t, a)
	if task.Status != core.TaskStopped || !task.Design[0].Open() || len(turns(runner, "asks for your design input")) != 0 {
		t.Fatalf("stopped %s, request %+v", task.Status, task.Design[0])
	}
}

func TestARestartResumesADesignHandOffOnce(t *testing.T) {
	runner := &scriptedRunner{reviews: []string{pass}, writerReplies: []string{askDesign}}
	a, p, task := loopApp(t, runner, "")
	seatDesigner(t, a, p.ID)
	stepUntil(t, a, task.ID, withDesigner)
	restart := func() *Loop {
		b := New(a.Core, a.Config, false)
		b.runner, b.meter = runner, a.meter
		return b
	}
	task = settle(t, restart())
	if task.Design[0].Input != "Keep it plain." || len(task.Revisions) != 1 {
		t.Fatalf("the hand-off should resume after a restart: %+v", task)
	}
	settle(t, restart())
	if n := len(turns(runner, "asks for your design input")); n != 1 {
		t.Fatalf("the designer ran %d times", n)
	}
	if n := len(writerTurns(runner)); n != 2 {
		t.Fatalf("the writer ran %d times", n)
	}
}

func TestADesignerIsSeatedFromAMemberAloneOrBesideOtherRoles(t *testing.T) {
	a := testLoop(t)
	ctx := context.Background()
	p, _ := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Service", Directories: []string{t.TempDir()}, Brief: core.BriefInput{Goal: "x"}})
	dee, _ := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Dee", Kinds: []string{core.RoleDesigner}, Engine: "claude"})
	ada, _ := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Ada", Kinds: []string{core.RoleImplementer, core.RoleDesigner}, Engine: "claude"})
	rex, _ := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Rex", Kinds: []string{core.RoleReviewer}, Engine: "codex"})
	names := func(p core.Project) string {
		var out []string
		for _, r := range p.Playbook.Roles {
			out = append(out, r.Name+":"+strings.Join(r.Kinds, "+"))
		}
		return strings.Join(out, " ")
	}
	project, err := a.SetTeam(ctx, p.ID, TeamChoice{Template: "code", Check: "make check", Designer: dee.ID})
	if err != nil || names(project) != "Researcher:researcher Implementer:implementer Reviewer:reviewer QA:qa Dee:designer" {
		t.Fatalf("team %s %v", names(project), err)
	}
	if project, _ = a.SetTeam(ctx, p.ID, TeamChoice{Template: "code", Check: "make check", Designer: NoResearcher}); slices.ContainsFunc(project.Playbook.Roles, func(r core.Role) bool { return r.Holds(core.RoleDesigner) }) {
		t.Fatal("none should leave the team without a designer")
	}
	if project, err = a.SetSeat(ctx, p.ID, core.RoleImplementer, ada.ID); err != nil {
		t.Fatal(err)
	}
	if project, err = a.SetSeat(ctx, p.ID, core.RoleDesigner, ada.ID); err != nil || names(project) != "Researcher:researcher Ada:implementer+designer Reviewer:reviewer QA:qa" {
		t.Fatalf("Ada should design from her own seat: %s %v", names(project), err)
	}
	if project, err = a.SetSeat(ctx, p.ID, core.RoleDesigner, ""); err != nil || names(project) != "Researcher:researcher Ada:implementer Reviewer:reviewer QA:qa" {
		t.Fatalf("with no one, the team has no designer: %s %v", names(project), err)
	}
	if _, err = a.SetSeat(ctx, p.ID, core.RoleDesigner, rex.ID); err == nil || !strings.Contains(err.Error(), "doesn't hold the designer role") {
		t.Fatalf("a member who doesn't design was seated as the designer: %v", err)
	}
	writing, _ := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Notes", Brief: core.BriefInput{Goal: "Notes"}})
	if _, err = a.SetTeam(ctx, writing.ID, TeamChoice{Template: "draft", Designer: dee.ID}); err != nil {
		t.Fatalf("a writing team can have a designer too: %v", err)
	}
	// A seat that is its own designer is never offered a hand-off to itself.
	task := core.Task{Roles: []core.Role{{Name: "Ada", Kinds: []string{core.RoleImplementer, core.RoleDesigner}}}}
	if designsFor(task, task.Roles[0]) {
		t.Fatal("Ada was offered a hand-off to herself")
	}
}
