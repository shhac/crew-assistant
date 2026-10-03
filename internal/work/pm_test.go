package work

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
)

// pmTeam is a writing project whose team has Pim as its PM, with the first
// task queued before Pim joined and a second asked for after, which waits
// in triage for Pim.
func pmTeam(t *testing.T, runner *scriptedRunner) (*Loop, core.Project, core.Task, core.Task) {
	t.Helper()
	ctx := context.Background()
	a, p, first := loopApp(t, runner, "")
	pim, err := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Pim", Kinds: []string{core.RolePM}, Engine: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if p, err = a.SetTeam(ctx, p.ID, TeamChoice{Template: "draft", PM: pim.ID}); err != nil {
		t.Fatal(err)
	}
	second, err := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Second note"})
	if err != nil {
		t.Fatal(err)
	}
	return a, p, first, second
}

func step(t *testing.T, a *Loop) {
	t.Helper()
	if _, err := a.loopStep(context.Background(), false); err != nil {
		t.Fatal(err)
	}
}

func TestThePMOrdersTheListBeforeTheNextTaskStarts(t *testing.T) {
	t.Parallel()
	runner := &scriptedRunner{reviews: []string{pass, pass}}
	a, p, first, second := pmTeam(t, runner)
	runner.pm = []string{fmt.Sprintf(`{"triage": [{"task": %q, "to": "research"}], "order": [%q, %q], "depends": [], "note": "the second is smaller", "questions": []}`, second.ID, second.ID, first.ID)}
	step(t, a)
	if len(runner.seen) != 1 || runner.seen[0].Write || !strings.Contains(runner.seen[0].Prompt, "Second note") {
		t.Fatalf("the PM's turn %+v", runner.seen)
	}
	snap, _ := a.Core.Snapshot(context.Background())
	project, _ := findProject(snap, p.ID)
	if project.PMDue || project.OrderedBy != core.OrderedByPM {
		t.Fatalf("project %+v", project)
	}
	step(t, a)
	snap, _ = a.Core.Snapshot(context.Background())
	started, _ := findTask(snap, "", second.ID)
	if started.Status == core.TaskQueued {
		t.Fatal("the loop didn't start the task the PM put first")
	}
	if !slices.ContainsFunc(snap.Activity, func(e core.Activity) bool {
		return e.Kind == "task.ordered" && strings.Contains(e.Summary, "the second is smaller")
	}) {
		t.Fatal("the PM's change isn't in the activity")
	}
}

func TestAnUnreadablePMIsSkippedAndTheWorkGoesOn(t *testing.T) {
	t.Parallel()
	runner := &scriptedRunner{reviews: []string{pass, pass}, pm: []string{"no idea", "still no idea"}}
	a, p, _, _ := pmTeam(t, runner)
	step(t, a)
	snap, _ := a.Core.Snapshot(context.Background())
	project, _ := findProject(snap, p.ID)
	if project.PMDue || project.OrderedBy != "" {
		t.Fatalf("project %+v", project)
	}
	if !slices.ContainsFunc(snap.Activity, func(e core.Activity) bool {
		return e.Kind == "task.ordered" && strings.Contains(e.Summary, "couldn't look")
	}) {
		t.Fatal("the skipped look isn't in the activity")
	}
	step(t, a)
	if runner.writes == 0 {
		t.Fatal("the work waited on the PM")
	}
}

func TestThePMsQuestionsGoToTheOwnerAndItWaitsForTheAnswer(t *testing.T) {
	t.Parallel()
	runner := &scriptedRunner{reviews: []string{pass, pass, pass, pass}, pm: []string{`{"order": [], "questions": ["Which note matters more?"]}`}}
	a, p, _, _ := pmTeam(t, runner)
	ctx := context.Background()
	step(t, a)
	snap, _ := a.Core.Snapshot(ctx)
	i := slices.IndexFunc(snap.Decisions, func(d core.Decision) bool { return d.Kind == core.DecisionPMQuestion })
	if i < 0 || !strings.Contains(snap.Decisions[i].Context, "Which note matters more?") || !strings.Contains(snap.Decisions[i].Title, "Pim") {
		t.Fatalf("decisions %+v", snap.Decisions)
	}
	if _, err := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Third note"}); err != nil {
		t.Fatal(err)
	}
	looks := func() int {
		n := 0
		for _, spec := range runner.seen {
			if strings.Contains(spec.Prompt, "You keep the to-do list") {
				n++
			}
		}
		return n
	}
	step(t, a)
	if looks() != 1 {
		t.Fatal("the PM looked again before the owner answered")
	}
	if _, err := a.Core.ChooseDecision(ctx, snap.Decisions[i].ID, "Use your judgment"); err != nil {
		t.Fatal(err)
	}
	step(t, a)
	if looks() != 2 || !strings.Contains(runner.seen[len(runner.seen)-1].Prompt, "The owner told you: Use your judgment") {
		t.Fatalf("the PM's next look %d", looks())
	}
}

func TestAPMJoinsTheSeatItsMemberAlreadyHolds(t *testing.T) {
	t.Parallel()
	a, p, _ := loopApp(t, &scriptedRunner{}, "")
	ctx := context.Background()
	ada, _ := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Ada", Kinds: []string{core.RoleImplementer, core.RolePM}, Engine: "claude"})
	rex, _ := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Rex", Kinds: []string{core.RoleReviewer}, Engine: "codex"})
	project, err := a.SetTeam(ctx, p.ID, TeamChoice{Template: "draft", Implementer: ada.ID, PM: ada.ID})
	if err != nil {
		t.Fatal(err)
	}
	seat, ok := project.PMSeat()
	if !ok || seat.Name != "Ada" || !seat.Holds(core.RoleImplementer) || len(project.Playbook.Roles) != 2 {
		t.Fatalf("roles %+v", project.Playbook.Roles)
	}
	if _, err := a.SetTeam(ctx, p.ID, TeamChoice{Template: "draft", PM: rex.ID}); err == nil {
		t.Fatal("a member without the pm role became the PM")
	}
	ivy, _ := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Ivy", Kinds: []string{core.RoleResearcher, core.RoleImplementer}, Engine: "claude"})
	if _, err := a.SetTeam(ctx, p.ID, TeamChoice{Template: "draft", Implementer: ivy.ID, Researcher: ivy.ID}); err == nil || !strings.Contains(err.Error(), "has no researcher") {
		t.Fatalf("a draft team took a researcher: %v", err)
	}
}

func TestARolesJSONIsReadFromAFenceProseOrAlone(t *testing.T) {
	t.Parallel()
	for reply, want := range map[string]string{
		`{"note": "bare"}`: "bare",
		"Here you go:\n{\"note\": \"prose\"}\nThanks.":                "prose",
		"{\"note\": \"early\"}\n```json\n{\"note\": \"fenced\"}\n```": "fenced",
		"```json\n{\"note\": \"unclosed\"}":                           "unclosed",
	} {
		var got struct{ Note string }
		if err := decodeReply(reply, &got); err != nil || got.Note != want {
			t.Errorf("%q read as %q, %v", reply, got.Note, err)
		}
	}
	var got struct{ Note string }
	if err := decodeReply("no json at all", &got); err == nil {
		t.Error("a reply without JSON was read")
	}
}

func TestThePMsAnswerAndWhatItIsTold(t *testing.T) {
	t.Parallel()
	answer, questions, err := parsePM("```json\n{\"order\": [\"b\", \"a\"], \"depends\": [{\"task\": \" a \", \"on\": [\"b\"]}, {\"task\": \"c\", \"on\": null}], \"note\": \"b first\", \"questions\": [\" \", \"Why c?\"]}\n```")
	if err != nil || !slices.Equal(answer.Order, []string{"b", "a"}) || !slices.Equal(answer.Depends["a"], []string{"b"}) || answer.Depends["c"] != nil || !slices.Equal(questions, []string{"Why c?"}) {
		t.Fatalf("answer %+v questions %v: %v", answer, questions, err)
	}
	p := core.Project{ID: "p", Title: "Site", OrderedBy: core.OrderedByOwner, PMDirection: "Docs first", Brief: core.Brief{Goal: "Ship"}}
	snap := core.Snapshot{Tasks: []core.Task{
		{ID: "a", Ref: "S-1", ProjectID: "p", Status: core.TaskQueued, Objective: "Search", Plan: &core.Plan{Summary: "Add a box", Changes: []string{"search.go"}}},
		{ID: "b", Ref: "S-2", ProjectID: "p", Status: core.TaskQueued, Objective: "Docs", DependsOn: []string{"a"}},
		{ID: "x", ProjectID: "other", Status: core.TaskQueued, Objective: "Elsewhere"},
	}}
	prompt := pmPrompt(snap, p)
	for _, want := range []string{"The owner set the current order", "The owner told you: Docs first", "plan: Add a box", "changes: search.go", "- S-2 (b) (queued): Docs", "waits for: S-1 (a)"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the PM isn't told %q", want)
		}
	}
	for _, want := range []string{"treat it as the owner's priorities", "Never simply move back what they moved", "Give the reason in note", "Never ask the owner to approve or confirm an order", "only decisions the owner must make"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("missing guidance %q", want)
		}
	}
	for _, old := range []string{"only place tasks queued since", "only what the owner must decide about the order"} {
		if strings.Contains(prompt, old) {
			t.Errorf("old guidance %q", old)
		}
	}
	if strings.Contains(prompt, "Elsewhere") {
		t.Error("the PM is told about another project's work")
	}
}

func TestAPMThatLeftOrFailedNeverHoldsUpTheWork(t *testing.T) {
	t.Parallel()
	runner := &scriptedRunner{reviews: []string{pass, pass}}
	a, p, _, _ := pmTeam(t, runner)
	ctx := context.Background()
	if _, err := a.SetTeam(ctx, p.ID, TeamChoice{Template: "draft"}); err != nil {
		t.Fatal(err)
	}
	step(t, a)
	snap, _ := a.Core.Snapshot(ctx)
	if project, _ := findProject(snap, p.ID); project.PMDue {
		t.Fatal("a team without a PM still waits for one")
	}

	runner = &scriptedRunner{reviews: []string{pass, pass}, fail: []error{errors.New("usage limit")}}
	a, p, _, _ = pmTeam(t, runner)
	step(t, a)
	snap, _ = a.Core.Snapshot(ctx)
	project, _ := findProject(snap, p.ID)
	if project.PMDue || !slices.ContainsFunc(snap.Activity, func(e core.Activity) bool { return strings.Contains(e.Summary, "usage limit") }) {
		t.Fatalf("a failed PM turn: due %v", project.PMDue)
	}
}

func TestTheAssistantCanAskThePMAndItChangesNothing(t *testing.T) {
	t.Parallel()
	runner := &scriptedRunner{reviews: []string{pass, pass}}
	a, p, first, second := pmTeam(t, runner)
	ctx := context.Background()
	answer, err := a.AskPM(ctx, p.ID, "Why is the second note waiting?")
	if err != nil || answer == "" {
		t.Fatalf("answer %q, %v", answer, err)
	}
	asked := runner.seen[len(runner.seen)-1]
	if asked.Write || !strings.Contains(asked.Prompt, "The owner's assistant asks you") || !strings.Contains(asked.Prompt, first.ID) || !strings.Contains(asked.Prompt, second.ID) {
		t.Fatalf("the PM was asked %q", asked.Prompt)
	}
	if got := strings.Join(specTools(asked), " "); got != "list_tasks read_task read_notes add_note" {
		t.Fatalf("a PM answering the assistant may only leave notes: %s", got)
	}
	snap, _ := a.Core.Snapshot(ctx)
	project, _ := findProject(snap, p.ID)
	if project.OrderedBy != "" || !slices.ContainsFunc(snap.Activity, func(e core.Activity) bool { return e.Kind == "pm.asked" }) {
		t.Fatalf("asking changed the list or went unrecorded: %+v", project)
	}
	if _, err := a.AskPM(ctx, p.ID, " "); err == nil {
		t.Fatal("an empty question was put to the PM")
	}
	a.SetTeam(ctx, p.ID, TeamChoice{Template: "draft"})
	if _, err := a.AskPM(ctx, p.ID, "Anything?"); err == nil || !strings.Contains(err.Error(), "has no PM") {
		t.Fatalf("a project without a PM: %v", err)
	}
}

func statusOf(t *testing.T, a *Loop, id string) core.Task {
	t.Helper()
	snap, _ := a.Core.Snapshot(context.Background())
	task, ok := findTask(snap, "", id)
	if !ok {
		t.Fatalf("no task %s", id)
	}
	return task
}

func TestTriageGoesOnWhenThePMLeftOrCouldNotLook(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// The PM left after the work was asked for.
	runner := &scriptedRunner{reviews: []string{pass, pass}}
	a, p, _, second := pmTeam(t, runner)
	if second.Status != core.TaskTriage {
		t.Fatalf("the owner's task with a PM is %s", second.Status)
	}
	if _, err := a.SetTeam(ctx, p.ID, TeamChoice{Template: "draft"}); err != nil {
		t.Fatal(err)
	}
	step(t, a)
	if got := statusOf(t, a, second.ID); got.Status != core.TaskQueued {
		t.Fatalf("with the PM gone the task is %s", got.Status)
	}
	if !activityHas(t, a, "the team has no PM") {
		t.Fatal("the release isn't in the activity")
	}
	// The PM's turn failed, or its reply could not be read.
	for _, runner := range []*scriptedRunner{
		{reviews: []string{pass, pass}, fail: []error{errors.New("usage limit")}},
		{reviews: []string{pass, pass}, pm: []string{"no idea", "still no idea"}},
	} {
		a, _, _, second := pmTeam(t, runner)
		step(t, a)
		if got := statusOf(t, a, second.ID); got.Status != core.TaskQueued {
			t.Fatalf("after the PM couldn't look the task is %s", got.Status)
		}
	}
}

func TestAProjectWithoutAPMNeverUsesTriage(t *testing.T) {
	t.Parallel()
	runner := &scriptedRunner{reviews: []string{pass, pass}}
	a, p, _ := loopApp(t, runner, "")
	task, err := a.QueueTask(context.Background(), p.ID, core.TaskInput{Objective: "Another note"}, core.LinkedByAssistant)
	if err != nil || task.Status != core.TaskQueued {
		t.Fatalf("task %s: %v", task.Status, err)
	}
}

func TestThePMAsksTheOwnerAboutATaskInTriageThroughTheLoop(t *testing.T) {
	t.Parallel()
	runner := &scriptedRunner{reviews: []string{pass, pass}}
	a, p, _, second := pmTeam(t, runner)
	ctx := context.Background()
	runner.pm = []string{fmt.Sprintf(`{"triage": [{"task": %q, "to": "owner", "question": "Which team is it for?"}], "order": [], "depends": [], "note": "", "questions": []}`, second.Ref)}
	step(t, a)
	got := statusOf(t, a, second.ID)
	if got.Status != core.TaskTriage || got.DecisionID == "" {
		t.Fatalf("task %s decision %q", got.Status, got.DecisionID)
	}
	snap, _ := a.Core.Snapshot(ctx)
	i := slices.IndexFunc(snap.Decisions, func(d core.Decision) bool { return d.ID == got.DecisionID })
	if i < 0 || snap.Decisions[i].Kind != core.DecisionPMQuestion || snap.Decisions[i].Context != "Which team is it for?" {
		t.Fatalf("decisions %+v", snap.Decisions)
	}
	// Stopping the task closes the question.
	if _, err := a.StopTask(ctx, p.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	snap, _ = a.Core.Snapshot(ctx)
	if d := snap.Decisions[i]; d.Status != core.DecisionDismissed {
		t.Fatalf("the question is %s", d.Status)
	}
}

func TestThePMIsToldWhatWaitsInTriage(t *testing.T) {
	t.Parallel()
	answer, _, err := parsePM(`{"triage": [{"task": " S-3 ", "to": " Research "}, {"task": "S-4", "to": "owner", "question": " Which? "}], "order": []}`)
	if err != nil || !slices.Equal(answer.Triage, []core.TriageRelease{{Task: "S-3", To: core.TriageToResearch}, {Task: "S-4", To: core.TriageToOwner, Question: "Which?"}}) {
		t.Fatalf("answer %+v: %v", answer.Triage, err)
	}
	p := core.Project{ID: "p", Title: "Site", Brief: core.Brief{Goal: "Ship"}}
	snap := core.Snapshot{Tasks: []core.Task{
		{ID: "a", Ref: "S-1", ProjectID: "p", Status: core.TaskQueued, Objective: "Search"},
		{ID: "c", Ref: "S-3", ProjectID: "p", Status: core.TaskTriage, Objective: "Shortcuts", Criteria: []string{"Works with a keyboard"}},
	}}
	prompt := pmPrompt(snap, p)
	for _, want := range []string{"In triage, oldest first:\n- S-3 (c): Shortcuts", "requirement: Works with a keyboard", "Tasks in triage are new work", "a task sent on waits for room in Triage", `"triage": [`} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the PM isn't told %q", want)
		}
	}
	if strings.Contains(prompt, "(triage)") {
		t.Error("a task in triage is listed with the queued work")
	}
	if prompt := pmPrompt(core.Snapshot{Tasks: snap.Tasks[:1]}, p); strings.Contains(prompt, "Tasks in triage are new work") {
		t.Error("the PM is told about triage with nothing in it")
	}
}

// The PM sees its latest answered questions on every look, not only the one
// the answer arrived for, so it doesn't ask again; another project's stay
// out, and so does a question still open.
func TestThePMKeepsSeeingTheOwnersAnswers(t *testing.T) {
	t.Parallel()
	p := core.Project{ID: "p1", Title: "Notes", Brief: core.Brief{Goal: "Notes"}}
	at := time.Date(2026, 9, 30, 18, 0, 0, 0, time.UTC)
	snap := core.Snapshot{Decisions: []core.Decision{
		{ProjectID: "p1", Kind: core.DecisionPMQuestion, Status: core.DecisionResolved, Context: "1. Has the daemon been restarted?", Answer: "Yes, it runs the new build.", CreatedAt: at},
		{ProjectID: "p1", Kind: core.DecisionPMQuestion, Status: core.DecisionResolved, Context: "1. Which first?", Answer: "Keep the order as it is", CreatedAt: at.Add(time.Hour)},
		{ProjectID: "p2", Kind: core.DecisionPMQuestion, Status: core.DecisionResolved, Context: "Elsewhere?", Answer: "Not yours", CreatedAt: at},
		{ProjectID: "p1", Kind: core.DecisionPMQuestion, Status: core.DecisionOpen, Context: "Still open?", CreatedAt: at},
	}}
	prompt := pmPrompt(snap, p)
	first, second := strings.Index(prompt, "Which first?"), strings.Index(prompt, "Has the daemon been restarted?")
	if first < 0 || second < first || !strings.Contains(prompt, "The owner answered: Yes, it runs the new build.") {
		t.Fatalf("the answers, newest first:\n%s", prompt)
	}
	for _, unwanted := range []string{"Not yours", "Still open?", "The owner told you"} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("the prompt carries %q:\n%s", unwanted, prompt)
		}
	}
}

func TestThePMTurnReordersAfterTheOwner(t *testing.T) {
	t.Parallel()
	runner := &scriptedRunner{}
	a, p, first, second := pmTeam(t, runner)
	ctx := context.Background()
	if _, err := a.Core.ApplyPM(ctx, p.ID, core.PMAnswer{Triage: []core.TriageRelease{{Task: second.ID, To: core.TriageToResearch}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Core.OrderTasks(ctx, p.ID, []string{second.ID, first.ID}, core.OrderedByOwner); err != nil {
		t.Fatal(err)
	}
	runner.pm = []string{fmt.Sprintf(`{"order": [%q, %q], "note": "first unblocks more", "questions": []}`, first.ID, second.ID)}
	seat, _ := p.PMSeat()
	if err := a.pmTurn(ctx, p.ID, seat); err != nil {
		t.Fatal(err)
	}
	snap, _ := a.Core.Snapshot(ctx)
	project, _ := findProject(snap, p.ID)
	var order []string
	for _, task := range snap.Tasks {
		if task.ProjectID == p.ID && task.Status == core.TaskQueued {
			order = append(order, task.ID)
		}
	}
	if !slices.Equal(order, []string{first.ID, second.ID}) || project.OrderedBy != core.OrderedByPM {
		t.Fatalf("order %v by %q", order, project.OrderedBy)
	}
	if !activityHas(t, a, "(was "+second.Ref+", "+first.Ref+"): first unblocks more") {
		t.Fatal("previous order missing")
	}
	for _, d := range snap.Decisions {
		if d.Kind == core.DecisionPMQuestion && d.Status == core.DecisionOpen {
			t.Fatal("reordering asked the owner")
		}
	}
}

func TestNoPMDoesNotLoopOnSentOnTasksWhenTodoIsFull(t *testing.T) {
	t.Parallel()
	runner := &scriptedRunner{}
	a, p, _, _ := pmTeam(t, runner)
	ctx := context.Background()
	if _, err := a.SetStageLimits(ctx, p.ID, map[string]int{core.StageTodo: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetSeat(ctx, p.ID, core.RolePM, ""); err != nil {
		t.Fatal(err)
	}
	snap, _ := a.Core.Snapshot(ctx)
	_, changed, err := a.managePM(ctx, snap, false)
	if err != nil || !changed {
		t.Fatalf("release: %v %v", changed, err)
	}
	snap, _ = a.Core.Snapshot(ctx)
	if snap.HasTriage(p.ID) {
		t.Fatal("sent-on task offered for triage again")
	}
	_, changed, err = a.managePM(ctx, snap, false)
	if err != nil || changed {
		t.Fatalf("repeated no-PM release: %v %v", changed, err)
	}
	if len(runner.seen) != 0 {
		t.Fatal("missing PM started a turn")
	}
}
