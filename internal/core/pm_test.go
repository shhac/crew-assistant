package core

import (
	"slices"
	"testing"
	"time"
)

// pmProject is a writing project whose team has a PM keeping its list.
func pmProject(t *testing.T, s *Service) Project {
	t.Helper()
	p := newProject(t, s)
	playbook := *p.Playbook
	playbook.Roles = append(append([]Role(nil), playbook.Roles...), Role{Name: "Pim", Kinds: []string{RolePM}, Engine: "claude"})
	p, err := s.SetPlaybook(testContext, p.ID, playbook)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func queuedOrder(t *testing.T, s *Service, projectID string) ([]string, Project) {
	t.Helper()
	snap, _ := s.Snapshot(testContext)
	var got []string
	for _, task := range snap.Tasks {
		if task.ProjectID == projectID && task.Status == TaskQueued {
			got = append(got, task.Objective)
		}
	}
	p, _ := findProjectByID(snap, projectID)
	return got, p
}

func findProjectByID(snap Snapshot, id string) (Project, bool) {
	i := slices.IndexFunc(snap.Projects, func(p Project) bool { return p.ID == id })
	if i < 0 {
		return Project{}, false
	}
	return snap.Projects[i], true
}

func TestOnlyATeamWithAPMIsAskedToLookAtTheList(t *testing.T) {
	s, _ := fixture(t)
	plain, withPM := newProject(t, s), pmProject(t, s)
	for _, p := range []Project{plain, withPM} {
		if _, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "a"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, p := queuedOrder(t, s, plain.ID); p.PMDue {
		t.Fatal("a team without a PM was asked to order its list")
	}
	_, p := queuedOrder(t, s, withPM.ID)
	if !p.PMDue {
		t.Fatal("queuing work didn't bring the PM")
	}
	if _, err := s.ApplyPM(testContext, p.ID, PMAnswer{}); err != nil {
		t.Fatal(err)
	}
	if _, p := queuedOrder(t, s, p.ID); p.PMDue || p.OrderedBy != "" {
		t.Fatalf("an empty answer changed the list: %+v", p)
	}
}

func TestThePMLooksAgainWhenWorkIsPlannedOrFinishes(t *testing.T) {
	s, _ := fixture(t)
	p := pmProject(t, s)
	a, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "a"})
	due := func() bool {
		_, project := queuedOrder(t, s, p.ID)
		return project.PMDue
	}
	finish := func() {
		s.UpdateTask(testContext, a.ID, func(t *Task, _ *Project) (string, error) {
			t.Status = TaskStopped
			return "", nil
		})
	}
	s.ApplyPM(testContext, p.ID, PMAnswer{})
	finish()
	if !due() {
		t.Fatal("finished work didn't bring the PM")
	}
	s.ApplyPM(testContext, p.ID, PMAnswer{})
	finish()
	if due() {
		t.Fatal("an update to finished work brought the PM again")
	}
	planned := plannedProject(t, s)
	playbook := *planned.Playbook
	playbook.Roles = append(playbook.Roles, Role{Name: "Pim", Kinds: []string{RolePM}, Engine: "claude"})
	planned, _ = s.SetPlaybook(testContext, planned.ID, playbook)
	b, _ := s.QueueTask(testContext, planned.ID, TaskInput{Objective: "b"})
	s.NextTask(testContext)
	s.ApplyPM(testContext, planned.ID, PMAnswer{})
	if _, err := s.RecordPlan(testContext, b.ID, Plan{Summary: "Do b"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, project := queuedOrder(t, s, planned.ID); !project.PMDue {
		t.Fatal("a new plan didn't bring the PM")
	}
}

func TestThePMOrdersTheListAndSetsWhatWaitsForWhat(t *testing.T) {
	s, _ := fixture(t)
	p := pmProject(t, s)
	a, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "a"})
	b, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "b"})
	changed, err := s.ApplyPM(testContext, p.ID, PMAnswer{Order: []string{b.ID, a.ID}, Depends: map[string][]string{a.ID: {b.ID}}, Note: "b unblocks a"})
	if err != nil || changed == "" {
		t.Fatalf("changed %q, %v", changed, err)
	}
	got, project := queuedOrder(t, s, p.ID)
	if !slices.Equal(got, []string{"b", "a"}) || project.OrderedBy != OrderedByPM {
		t.Fatalf("order %v by %q", got, project.OrderedBy)
	}
	snap, _ := s.Snapshot(testContext)
	if waiting := task(&snap, a.ID); !slices.Equal(waiting.DependsOn, []string{b.ID}) {
		t.Fatalf("a waits for %v", waiting.DependsOn)
	}
	// A loop, or a list missing a task, is ignored.
	if changed, _ := s.ApplyPM(testContext, p.ID, PMAnswer{Order: []string{a.ID}, Depends: map[string][]string{b.ID: {a.ID}}}); changed != "" {
		t.Fatalf("a bad answer changed %s", changed)
	}
	// An impossible id beside a good one keeps the good one; an empty list
	// releases the task.
	c, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "c"})
	if _, err := s.ApplyPM(testContext, p.ID, PMAnswer{Depends: map[string][]string{c.ID: {"not-a-task", b.ID}, a.ID: {}}}); err != nil {
		t.Fatal(err)
	}
	snap, _ = s.Snapshot(testContext)
	if !slices.Equal(task(&snap, c.ID).DependsOn, []string{b.ID}) || len(task(&snap, a.ID).DependsOn) != 0 {
		t.Fatalf("c waits for %v, a for %v", task(&snap, c.ID).DependsOn, task(&snap, a.ID).DependsOn)
	}
	if got, _ := queuedOrder(t, s, p.ID); !slices.Equal(got, []string{"b", "a", "c"}) {
		t.Fatalf("order %v", got)
	}
}

func TestTheOwnersOrderStandsOverThePM(t *testing.T) {
	s, _ := fixture(t)
	start := s.now()
	p := pmProject(t, s)
	a, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "a"})
	b, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "b"})
	if _, err := s.OrderTasks(testContext, p.ID, []string{b.ID, a.ID}, OrderedByOwner); err != nil {
		t.Fatal(err)
	}
	if changed, _ := s.ApplyPM(testContext, p.ID, PMAnswer{Order: []string{a.ID, b.ID}}); changed != "" {
		t.Fatalf("the PM overruled the owner: %s", changed)
	}
	s.now = func() time.Time { return start.Add(time.Hour) }
	c, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "c"})
	// A reply naming one task for every place changes nothing.
	if changed, err := s.ApplyPM(testContext, p.ID, PMAnswer{Order: []string{a.ID, a.ID, a.ID}}); err != nil || changed != "" {
		t.Fatalf("a repeated task changed %q: %v", changed, err)
	}
	// The PM may place the new task, but not swap the owner's two.
	if _, err := s.ApplyPM(testContext, p.ID, PMAnswer{Order: []string{c.ID, a.ID, b.ID}}); err != nil {
		t.Fatal(err)
	}
	got, project := queuedOrder(t, s, p.ID)
	if !slices.Equal(got, []string{"c", "b", "a"}) || project.OrderedBy != OrderedByPM {
		t.Fatalf("order %v by %q", got, project.OrderedBy)
	}
}

func TestTheOwnersAnswerToThePMBringsItBack(t *testing.T) {
	s, _ := fixture(t)
	p := pmProject(t, s)
	if _, err := s.ApplyPM(testContext, p.ID, PMAnswer{}); err != nil {
		t.Fatal(err)
	}
	d, err := s.AskForPM(testContext, p.ID, DecisionInput{Title: "Which first?", Context: "1. Search or shortcuts?", Recommendation: "Answer", Choices: []string{"Use your judgment", "Keep the order as it is"}})
	if err != nil || d.Kind != DecisionPMQuestion {
		t.Fatalf("decision %+v %v", d, err)
	}
	if _, err := s.ChooseDecision(testContext, d.ID, "Keep the order as it is"); err != nil {
		t.Fatal(err)
	}
	if _, project := queuedOrder(t, s, p.ID); !project.PMDue || project.PMDirection != "Keep the order as it is" {
		t.Fatalf("project %+v", project)
	}
}
