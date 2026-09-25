package work

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
)

// pmTeam is a writing project whose team has Pim as its PM, with the first
// task queued before Pim joined and a second queued after.
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
	runner := &scriptedRunner{reviews: []string{pass, pass}}
	a, p, first, second := pmTeam(t, runner)
	runner.pm = []string{fmt.Sprintf(`{"order": [%q, %q], "depends": [], "note": "the second is smaller", "questions": []}`, second.ID, first.ID)}
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
	answer, questions, err := parsePM("```json\n{\"order\": [\"b\", \"a\"], \"depends\": [{\"task\": \" a \", \"on\": [\"b\"]}, {\"task\": \"c\", \"on\": null}], \"note\": \"b first\", \"questions\": [\" \", \"Why c?\"]}\n```")
	if err != nil || !slices.Equal(answer.Order, []string{"b", "a"}) || !slices.Equal(answer.Depends["a"], []string{"b"}) || answer.Depends["c"] != nil || !slices.Equal(questions, []string{"Why c?"}) {
		t.Fatalf("answer %+v questions %v: %v", answer, questions, err)
	}
	p := core.Project{ID: "p", Title: "Site", OrderedBy: core.OrderedByOwner, PMDirection: "Docs first", Brief: core.Brief{Goal: "Ship"}}
	snap := core.Snapshot{Tasks: []core.Task{
		{ID: "a", ProjectID: "p", Status: core.TaskQueued, Objective: "Search", Plan: &core.Plan{Summary: "Add a box", Changes: []string{"search.go"}}},
		{ID: "b", ProjectID: "p", Status: core.TaskQueued, Objective: "Docs", DependsOn: []string{"a"}},
		{ID: "x", ProjectID: "other", Status: core.TaskQueued, Objective: "Elsewhere"},
	}}
	prompt := pmPrompt(snap, p)
	for _, want := range []string{"The owner set the current order", "The owner told you: Docs first", "plan: Add a box", "changes: search.go", "waits for: a"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the PM isn't told %q", want)
		}
	}
	if strings.Contains(prompt, "Elsewhere") {
		t.Error("the PM is told about another project's work")
	}
}

func TestAPMThatLeftOrFailedNeverHoldsUpTheWork(t *testing.T) {
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
