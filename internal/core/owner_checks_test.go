package core

import (
	"slices"
	"strings"
	"testing"
)

func TestOwnerChecksCreationAndChecklist(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	task, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Build", Criteria: []string{"Tests pass"}, OwnerChecks: []string{" Live check ", "Live check"}})
	if err != nil || !slices.Equal(task.OwnerChecks, []string{"Live check"}) || !slices.Equal(task.Criteria, []string{"Tests pass"}) {
		t.Fatalf("%+v %v", task, err)
	}
	task.OwnerSteps = []string{"Live check", "Tag release"}
	if got := task.OwnerChecklist(); got != "After it lands, check:\n- [ ] Live check\n- [ ] Tag release" {
		t.Fatal(got)
	}
	if !slices.Contains(task.OwnersAlready(), "Live check") {
		t.Fatal("missing owner check")
	}
	if _, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Build", Criteria: []string{"Tests pass"}, OwnerChecks: []string{"Tests pass"}}); err == nil {
		t.Fatal("accepted duplicate requirement")
	}
	long := strings.Repeat("x", maxCriterion+1)
	if _, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Build", Criteria: []string{long}, OwnerChecks: []string{long}}); err == nil {
		t.Fatal("clipping hid a duplicate requirement")
	}
}

func TestResearchMovesOwnerChecksAtomicallyAndUndoPreservesUnrelatedChecks(t *testing.T) {
	s, _ := fixture(t)
	p := plannedProject(t, s)
	task, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Build", Criteria: []string{"Tests pass", "CI green"}, OwnerChecks: []string{"Live check"}})
	older, err := s.EditTask(testContext, EditInput{Project: p.ID, Task: task.ID, By: "Researcher", Kind: RoleResearcher, Add: []string{"Docs"}})
	if err != nil {
		t.Fatal(err)
	}
	s.NextTask(testContext)
	moved, err := s.RecordPlan(testContext, task.ID, Plan{Role: "Researcher", Summary: "Build", OwnerChecks: []string{"CI green", "Demonstrated test evidence", "gone"}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(moved.Criteria, "CI green") || !slices.Contains(moved.OwnerChecks, "CI green") || !slices.Equal(moved.Plan.OwnerChecks, []string{"CI green"}) || len(moved.Edits) != 2 || moved.Edits[1].Kind != RoleResearcher {
		t.Fatalf("%+v", moved)
	}
	undone, err := s.UndoTaskEdit(testContext, p.ID, task.ID, moved.Edits[1].ID)
	if err != nil || !slices.Contains(undone.Criteria, "CI green") || slices.Contains(undone.OwnerChecks, "CI green") {
		t.Fatalf("%+v %v", undone, err)
	}
	undone, err = s.UndoTaskEdit(testContext, p.ID, task.ID, older.Edits[0].ID)
	if err != nil || !slices.Equal(undone.OwnerChecks, []string{"Live check"}) {
		t.Fatalf("%+v %v", undone, err)
	}
	if !strings.Contains(moved.OwnerChecklist(), "CI green") {
		t.Fatal("delivery loses moved check")
	}
}

func TestUndoOlderCriteriaEditKeepsLaterMovedCheck(t *testing.T) {
	s, _ := fixture(t)
	p := plannedProject(t, s)
	task, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Build", Criteria: []string{"CI green"}})
	edited, err := s.EditTask(testContext, EditInput{Project: p.ID, Task: task.ID, By: "Researcher", Kind: RoleResearcher, Add: []string{"Tests"}})
	if err != nil {
		t.Fatal(err)
	}
	s.NextTask(testContext)
	moved, err := s.RecordPlan(testContext, task.ID, Plan{Summary: "Build", Role: "Researcher", OwnerChecks: []string{"CI green"}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	undone, err := s.UndoTaskEdit(testContext, p.ID, task.ID, edited.Edits[0].ID)
	if err != nil || !slices.Equal(undone.OwnerChecks, moved.OwnerChecks) || slices.Contains(undone.Criteria, "CI green") {
		t.Fatalf("%+v %v", undone, err)
	}
}

func TestPlanRematchesCurrentCriteria(t *testing.T) {
	s, _ := fixture(t)
	p := plannedProject(t, s)
	task, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Build", Criteria: []string{"CI green"}})
	s.NextTask(testContext)
	if _, err := s.EditTask(testContext, EditInput{Project: p.ID, Task: task.ID, Kind: RoleResearcher, Criteria: []string{"Tests"}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.RecordPlan(testContext, task.ID, Plan{Summary: "Build", OwnerChecks: []string{"CI green"}}, nil, nil)
	if err != nil || len(got.OwnerChecks) != 0 || len(got.Plan.OwnerChecks) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestHeldBackPlanMovesNoOwnerChecks(t *testing.T) {
	s, _ := fixture(t)
	p := plannedProject(t, s)
	a, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "A", Criteria: []string{"CI green"}})
	b, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "B"})
	s.NextTask(testContext)
	a, err := s.RecordPlan(testContext, a.ID, Plan{Summary: "After B", OwnerChecks: []string{"CI green"}}, []string{b.ID}, nil)
	if err != nil || a.Plan != nil || len(a.OwnerChecks) != 0 || !slices.Contains(a.Criteria, "CI green") {
		t.Fatalf("%+v %v", a, err)
	}
}

func TestEditTaskKeepsOwnerChecksOutOfCriteria(t *testing.T) {
	for _, kind := range []string{RolePM, RoleResearcher, RoleImplementer} {
		t.Run(kind, func(t *testing.T) {
			s, _ := fixture(t)
			p := newProject(t, s)
			task, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Build", OwnerChecks: []string{"CI green"}})
			if err != nil {
				t.Fatal(err)
			}
			in := EditInput{Project: p.ID, Task: task.ID, Kind: kind, Add: []string{"CI green", "Tests"}}
			if Rewrites(kind) {
				in.Criteria = []string{"CI green"}
			}
			got, err := s.EditTask(testContext, in)
			if err != nil || slices.Contains(got.Criteria, "CI green") || !slices.Contains(got.Criteria, "Tests") || !slices.Contains(got.OwnerChecks, "CI green") {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
}
