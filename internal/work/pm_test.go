package work

import (
	"context"
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
}
