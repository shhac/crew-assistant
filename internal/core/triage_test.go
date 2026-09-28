package core

import (
	"slices"
	"strings"
	"testing"
)

// triageProject is a project whose team has a researcher and Pim as its PM.
func triageProject(t *testing.T, s *Service) Project {
	t.Helper()
	p := plannedProject(t, s)
	playbook := *p.Playbook
	playbook.Roles = append(slices.Clone(playbook.Roles), Role{Name: "Pim", Kinds: []string{RolePM}, Engine: "claude"})
	p, err := s.SetPlaybook(testContext, p.ID, playbook)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func taskNow(t *testing.T, s *Service, id string) Task {
	t.Helper()
	snap, err := s.Snapshot(testContext)
	if err != nil {
		t.Fatal(err)
	}
	out, ok := snap.FindTask(id)
	if !ok {
		t.Fatalf("no task %s", id)
	}
	return out
}

func TestWorkTheOwnerOrAssistantAsksForGoesToThePMFirst(t *testing.T) {
	s, _ := fixture(t)
	withPM, plain := triageProject(t, s), plannedProject(t, s)
	for _, by := range []string{LinkedByOwner, LinkedByAssistant} {
		task, err := s.QueueTaskAs(testContext, withPM.ID, TaskInput{Objective: "asked by " + by}, by)
		if err != nil {
			t.Fatal(err)
		}
		if task.Status != TaskTriage || task.Stage != StageTriage {
			t.Fatalf("%s's task is %s in %s", by, task.Status, task.Stage)
		}
		if got := taskNow(t, s, task.ID); got.Status != TaskTriage || got.Stage != StageTriage || got.Checking != "Pim" {
			t.Fatalf("%s's task is stored as %s in %s with %q", by, got.Status, got.Stage, got.Checking)
		}
		// Without a PM, nothing waits on one.
		direct, err := s.QueueTaskAs(testContext, plain.ID, TaskInput{Objective: "asked by " + by}, by)
		if err != nil || direct.Status != TaskQueued || direct.Stage != StageTodo {
			t.Fatalf("%s's task without a PM is %s: %v", by, direct.Status, err)
		}
	}
	split, err := s.QueueTaskAs(testContext, withPM.ID, TaskInput{Objective: "split off by the PM"}, LinkedByPM)
	if err != nil || split.Status != TaskQueued {
		t.Fatalf("the PM's own task is %s: %v", split.Status, err)
	}
	snap, _ := s.Snapshot(testContext)
	if !slices.ContainsFunc(snap.Activity, func(e Activity) bool { return e.Kind == "task.triage" && e.Summary == "asked by owner" }) {
		t.Fatal("the task's arrival in triage isn't in the activity")
	}
	if p, _ := findProjectByID(snap, withPM.ID); !p.PMDue {
		t.Fatal("work in triage didn't bring the PM")
	}
}

func TestATaskInTriageNeverStartsUntilThePMSendsItOn(t *testing.T) {
	s, _ := fixture(t)
	p := triageProject(t, s)
	task, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "search"})
	if _, found, err := s.NextTask(testContext); err != nil || found {
		t.Fatalf("a task in triage started: %v", err)
	}
	if got := taskNow(t, s, task.ID); got.Status != TaskTriage || got.Active() || got.Finished() {
		t.Fatalf("task %s", got.Status)
	}
	changed, err := s.ApplyPM(testContext, p.ID, PMAnswer{Triage: []TriageRelease{{Task: task.ID, To: TriageToResearch}}})
	if err != nil || !strings.Contains(changed, "search") {
		t.Fatalf("changed %q: %v", changed, err)
	}
	if got := taskNow(t, s, task.ID); got.Status != TaskQueued || got.Stage != StageTodo {
		t.Fatalf("sent on as %s in %s", got.Status, got.Stage)
	}
	started, found, err := s.NextTask(testContext)
	if err != nil || !found || started.ID != task.ID || started.Status != TaskResearching {
		t.Fatalf("started %+v %v: %v", started.Status, found, err)
	}
}

func TestThePMPlacesWhatItSendsOnInTheSameLook(t *testing.T) {
	s, _ := fixture(t)
	p := triageProject(t, s)
	first := ask(t, s, p.ID, "first")
	second, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "second"})
	if _, err := s.ApplyPM(testContext, p.ID, PMAnswer{Triage: []TriageRelease{{Task: second.Ref, To: TriageToResearch}}, Order: []string{second.ID, first.ID}}); err != nil {
		t.Fatal(err)
	}
	if got, project := queuedOrder(t, s, p.ID); !slices.Equal(got, []string{"second", "first"}) || project.OrderedBy != OrderedByPM {
		t.Fatalf("order %v by %q", got, project.OrderedBy)
	}
}

func TestThePMOnlySendsOnWhatIsInTriage(t *testing.T) {
	s, _ := fixture(t)
	p := triageProject(t, s)
	queued, _ := s.QueueTaskAs(testContext, p.ID, TaskInput{Objective: "queued"}, LinkedByPM)
	stopped, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "stopped"})
	s.UpdateTask(testContext, stopped.ID, func(t *Task, _ *Project) (string, error) {
		t.Status = TaskStopped
		return "", nil
	})
	other := triageProject(t, s)
	elsewhere, _ := s.QueueTask(testContext, other.ID, TaskInput{Objective: "elsewhere"})
	triaged, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "triaged"})
	changed, err := s.ApplyPM(testContext, p.ID, PMAnswer{Triage: []TriageRelease{
		{Task: queued.ID, To: TriageToOwner, Question: "Really?"},
		{Task: stopped.ID, To: TriageToResearch},
		{Task: elsewhere.ID, To: TriageToResearch},
		{Task: "not-a-task", To: TriageToResearch},
		{Task: triaged.ID, To: "somewhere"},
		{Task: triaged.ID, To: TriageToOwner},
	}})
	if err != nil || changed != "" {
		t.Fatalf("changed %q: %v", changed, err)
	}
	snap, _ := s.Snapshot(testContext)
	if len(snap.Decisions) != 0 {
		t.Fatalf("decisions %+v", snap.Decisions)
	}
	for id, want := range map[string]string{queued.ID: TaskQueued, stopped.ID: TaskStopped, elsewhere.ID: TaskTriage, triaged.ID: TaskTriage} {
		if got := taskNow(t, s, id).Status; got != want {
			t.Fatalf("%s is %s, want %s", taskNow(t, s, id).Objective, got, want)
		}
	}
}

func TestThePMCanAskTheOwnerAboutATaskInTriage(t *testing.T) {
	s, _ := fixture(t)
	p := triageProject(t, s)
	task, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "search"})
	changed, err := s.ApplyPM(testContext, p.ID, PMAnswer{Triage: []TriageRelease{{Task: task.ID, To: TriageToOwner, Question: "Search titles or bodies too?"}}})
	if err != nil || !strings.Contains(changed, "Asked you") {
		t.Fatalf("changed %q: %v", changed, err)
	}
	snap, _ := s.Snapshot(testContext)
	i := slices.IndexFunc(snap.Decisions, func(d Decision) bool { return d.TaskID == task.ID })
	if i < 0 {
		t.Fatal("no question for the owner")
	}
	d := snap.Decisions[i]
	if d.Kind != DecisionPMQuestion || d.Status != DecisionOpen || d.Context != "Search titles or bodies too?" || !strings.Contains(d.Title, "Pim") {
		t.Fatalf("decision %+v", d)
	}
	got := taskNow(t, s, task.ID)
	if got.Status != TaskTriage || got.DecisionID != d.ID || got.Answered {
		t.Fatalf("task %s decision %q answered %v", got.Status, got.DecisionID, got.Answered)
	}
	// While the question is open, the PM doesn't ask it again.
	if changed, _ := s.ApplyPM(testContext, p.ID, PMAnswer{Triage: []TriageRelease{{Task: task.ID, To: TriageToOwner, Question: "Again?"}}}); changed != "" {
		t.Fatalf("asked again: %q", changed)
	}
	if _, err := s.AnswerDecision(testContext, d.ID, "Bodies too"); err != nil {
		t.Fatal(err)
	}
	got = taskNow(t, s, task.ID)
	if got.Status != TaskTriage || !got.Answered {
		t.Fatalf("answered task %s answered %v", got.Status, got.Answered)
	}
	_, project := queuedOrder(t, s, p.ID)
	if !project.PMDue || !strings.Contains(project.PMDirection, "Search titles or bodies too?") || !strings.Contains(project.PMDirection, "Bodies too") || !strings.Contains(project.PMDirection, "search") {
		t.Fatalf("the PM isn't brought back with the answer: %+v", project)
	}
	if _, err := s.ApplyPM(testContext, p.ID, PMAnswer{Triage: []TriageRelease{{Task: task.ID, To: TriageToResearch}}}); err != nil {
		t.Fatal(err)
	}
	if got := taskNow(t, s, task.ID); got.Status != TaskQueued || got.DecisionID != "" || got.Answered {
		t.Fatalf("sent on as %s, decision %q", got.Status, got.DecisionID)
	}
}

func TestAnswersToThePMBeforeItLooksAreAllKept(t *testing.T) {
	s, _ := fixture(t)
	p := triageProject(t, s)
	a, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "a"})
	b, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "b"})
	s.ApplyPM(testContext, p.ID, PMAnswer{Triage: []TriageRelease{{Task: a.ID, To: TriageToOwner, Question: "Why a?"}, {Task: b.ID, To: TriageToOwner, Question: "Why b?"}}})
	for _, id := range []string{a.ID, b.ID} {
		if _, err := s.AnswerDecision(testContext, taskNow(t, s, id).DecisionID, "Because "+id); err != nil {
			t.Fatal(err)
		}
	}
	_, project := queuedOrder(t, s, p.ID)
	if !strings.Contains(project.PMDirection, "Because "+a.ID) || !strings.Contains(project.PMDirection, "Because "+b.ID) {
		t.Fatalf("direction %q", project.PMDirection)
	}
}

func TestReleaseTriageSendsEverythingOnAndClosesItsQuestions(t *testing.T) {
	s, _ := fixture(t)
	p := triageProject(t, s)
	a, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "a"})
	b, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "b"})
	other := triageProject(t, s)
	elsewhere, _ := s.QueueTask(testContext, other.ID, TaskInput{Objective: "elsewhere"})
	s.ApplyPM(testContext, p.ID, PMAnswer{Triage: []TriageRelease{{Task: b.ID, To: TriageToOwner, Question: "Why b?"}}})
	question := taskNow(t, s, b.ID).DecisionID
	if err := s.ReleaseTriage(testContext, p.ID, "the team has no PM"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a.ID, b.ID} {
		if got := taskNow(t, s, id); got.Status != TaskQueued || got.DecisionID != "" {
			t.Fatalf("%s is %s", got.Objective, got.Status)
		}
	}
	if got := taskNow(t, s, elsewhere.ID); got.Status != TaskTriage {
		t.Fatalf("another project's task is %s", got.Status)
	}
	snap, _ := s.Snapshot(testContext)
	if d := decision(&snap, question); d.Status != DecisionDismissed {
		t.Fatalf("the question is %s", d.Status)
	}
	lines := 0
	for _, e := range snap.Activity {
		if e.Kind == "task.triaged" && strings.Contains(e.Summary, "the team has no PM") {
			lines++
		}
	}
	if lines != 1 {
		t.Fatalf("%d activity lines for the release", lines)
	}
	if !snap.HasTriage(other.ID) || snap.HasTriage(p.ID) {
		t.Fatal("HasTriage disagrees with the tasks")
	}
}

func TestThePMShapesATaskInTriage(t *testing.T) {
	s, _ := fixture(t)
	p := triageProject(t, s)
	dep := ask(t, s, p.ID, "dependency")
	task, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "messy ask", Criteria: []string{"do it"}})
	edited, err := s.EditTask(testContext, EditInput{Project: p.ID, Task: task.ID, By: "Pim", Kind: RolePM, Objective: "Tidy ask", Criteria: []string{"Do the tidy thing"}})
	if err != nil || edited.Objective != "Tidy ask" || edited.Status != TaskTriage || len(edited.Edits) != 1 {
		t.Fatalf("edit %+v: %v", edited, err)
	}
	if _, err := s.UndoTaskEdit(testContext, p.ID, task.ID, edited.Edits[0].ID); err != nil {
		t.Fatal(err)
	}
	if got := taskNow(t, s, task.ID); got.Objective != "messy ask" {
		t.Fatalf("undo left %q", got.Objective)
	}
	if _, err := s.LinkTasks(testContext, Link{Project: p.ID, Task: task.ID, Relation: RelationDependsOn, Other: dep.ID, By: LinkedByPM}); err != nil {
		t.Fatalf("the PM couldn't make a task in triage wait: %v", err)
	}
	if got := taskNow(t, s, task.ID); !slices.Equal(got.DependsOn, []string{dep.ID}) {
		t.Fatalf("waits for %v", got.DependsOn)
	}
}
