package core

import (
	"slices"
	"strings"
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

// ask is a task the owner asked for, which the PM sent on from triage to
// the to-do list.
func ask(t *testing.T, s *Service, projectID, objective string) Task {
	t.Helper()
	out, err := s.QueueTask(testContext, projectID, TaskInput{Objective: objective})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyPM(testContext, projectID, PMAnswer{Triage: []TriageRelease{{Task: out.ID, To: TriageToResearch}}}); err != nil {
		t.Fatal(err)
	}
	return out
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
	b := ask(t, s, planned.ID, "b")
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
	a, b := ask(t, s, p.ID, "a"), ask(t, s, p.ID, "b")
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
	c := ask(t, s, p.ID, "c")
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

func TestThePMCanChangeAnOwnerOrAssistantOrder(t *testing.T) {
	for _, by := range []string{OrderedByOwner, OrderedByAssistant} {
		t.Run(by, func(t *testing.T) {
			s, _ := fixture(t)
			p := pmProject(t, s)
			a, b := ask(t, s, p.ID, "a"), ask(t, s, p.ID, "b")
			if _, err := s.OrderTasks(testContext, p.ID, []string{b.ID, a.ID}, by); err != nil {
				t.Fatal(err)
			}
			_, seen := queuedOrder(t, s, p.ID)
			if _, err := s.ApplyPM(testContext, p.ID, PMAnswer{SeenOrderedBy: seen.OrderedBy, SeenOrderedAt: seen.OrderedAt, Order: []string{a.Ref, b.Ref}, Note: "a unblocks more"}); err != nil {
				t.Fatal(err)
			}
			got, project := queuedOrder(t, s, p.ID)
			if !slices.Equal(got, []string{"a", "b"}) || project.OrderedBy != OrderedByPM {
				t.Fatalf("order %v by %q", got, project.OrderedBy)
			}
			snap, _ := s.Snapshot(testContext)
			want := "Changed the order to " + a.Ref + ", " + b.Ref + " (was " + b.Ref + ", " + a.Ref + "): a unblocks more"
			if !slices.ContainsFunc(snap.Activity, func(e Activity) bool { return e.Kind == "task.ordered" && e.Summary == want }) {
				t.Fatalf("activity %+v", snap.Activity)
			}
		})
	}
}

func TestThePMLeavesAnOrderSetDuringItsLook(t *testing.T) {
	for _, by := range []string{OrderedByOwner, OrderedByAssistant} {
		t.Run(by, func(t *testing.T) {
			s, _ := fixture(t)
			p := pmProject(t, s)
			a, b := ask(t, s, p.ID, "a"), ask(t, s, p.ID, "b")
			if _, err := s.OrderTasks(testContext, p.ID, []string{a.ID, b.ID}, by); err != nil {
				t.Fatal(err)
			}
			_, seen := queuedOrder(t, s, p.ID)
			in := PMAnswer{SeenOrderedBy: seen.OrderedBy, SeenOrderedAt: seen.OrderedAt, Order: []string{a.ID, b.ID}, Depends: map[string][]string{a.ID: {b.ID}}}
			at := s.now()
			s.now = func() time.Time { return at.Add(time.Hour) }
			if _, err := s.OrderTasks(testContext, p.ID, []string{b.ID, a.ID}, by); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ApplyPM(testContext, p.ID, in); err != nil {
				t.Fatal(err)
			}
			got, project := queuedOrder(t, s, p.ID)
			if !slices.Equal(got, []string{"b", "a"}) || project.OrderedBy != by {
				t.Fatalf("order %v by %q", got, project.OrderedBy)
			}
			snap, _ := s.Snapshot(testContext)
			if !slices.Equal(task(&snap, a.ID).DependsOn, []string{b.ID}) {
				t.Fatal("dependency was lost")
			}
			who := "you"
			if by == OrderedByAssistant {
				who = "the assistant"
			}
			if !slices.ContainsFunc(snap.Activity, func(e Activity) bool {
				return e.Kind == "task.ordered" && strings.Contains(e.Summary, "Left the order as "+who+" set it while the PM was looking")
			}) {
				t.Fatalf("activity %+v", snap.Activity)
			}
			// The next look reads the new stamp and can reorder.
			in.SeenOrderedBy, in.SeenOrderedAt = project.OrderedBy, project.OrderedAt
			if _, err := s.ApplyPM(testContext, p.ID, in); err != nil {
				t.Fatal(err)
			}
			if got, _ := queuedOrder(t, s, p.ID); !slices.Equal(got, []string{"a", "b"}) {
				t.Fatalf("fresh order %v", got)
			}
		})
	}
}

func TestThePMIgnoresInvalidOrUnchangedOrders(t *testing.T) {
	s, _ := fixture(t)
	p := pmProject(t, s)
	a, b := ask(t, s, p.ID, "a"), ask(t, s, p.ID, "b")
	for _, order := range [][]string{{a.ID, a.ID}, {a.ID}, {b.ID, a.ID, "extra"}, {a.ID, b.ID}} {
		if changed, err := s.ApplyPM(testContext, p.ID, PMAnswer{Order: order}); err != nil || changed != "" {
			t.Fatalf("order %v changed %q: %v", order, changed, err)
		}
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
