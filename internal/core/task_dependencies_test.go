package core

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestCrossProjectCreationWaitsAndReleases(t *testing.T) {
	for _, readable := range []bool{false, true} {
		t.Run(map[bool]string{false: "canonical", true: "readable"}[readable], func(t *testing.T) {
			s, _ := fixture(t)
			a, b := plannedProject(t, s), newProject(t, s)
			b, _ = s.SetProjectTitle(testContext, b.ID, "Library")
			dep, _ := s.QueueTask(testContext, b.ID, TaskInput{Objective: "Publish library"})
			id := dep.ID
			if readable {
				id = dep.Ref
			}
			waiting, err := s.QueueTask(testContext, a.ID, TaskInput{Objective: "Use library", DependsOn: []string{id}})
			if err != nil || !slices.Equal(waiting.DependsOn, []string{dep.ID}) || waiting.LinkedBy[linkKey(RelationDependsOn, dep.ID)].By != LinkedByOwner {
				t.Fatalf("creation %+v %v", waiting, err)
			}

			if len(waiting.WaitingOn) != 1 || waiting.WaitingOn[0].Project != "Library" || waiting.WaitingOn[0].Ref != dep.Ref || !strings.Contains(waiting.WaitsFor[0], "Library") {
				t.Fatalf("derived %+v", waiting)
			}
			s.Schedule(testContext, anyone)
			if got := taskByID(t, s, waiting.ID); got.Status != TaskQueued {
				t.Fatalf("started early: %s", got.Status)
			}
			s.UpdateTask(testContext, dep.ID, func(t *Task, _ *Project) (string, error) { t.Status = TaskLanded; t.Claims = nil; return "", nil })
			s.Schedule(testContext, anyone)
			if got := taskByID(t, s, waiting.ID); got.Status != TaskResearching || len(got.WaitingOn) != 0 {
				t.Fatalf("not released: %+v", got)
			}
		})
	}
}

func TestRequestedCrossProjectDependenciesAreAtomic(t *testing.T) {
	for _, status := range []string{"missing", TaskLanded, TaskStopped, TaskDelivered} {
		t.Run(status, func(t *testing.T) {
			s, _ := fixture(t)
			a, b := plannedProject(t, s), newProject(t, s)
			good, _ := s.QueueTask(testContext, b.ID, TaskInput{Objective: "Good"})
			bad, _ := s.QueueTask(testContext, b.ID, TaskInput{Objective: "Finished"})
			id := "UNKNOWN-999"
			if status != "missing" {
				s.UpdateTask(testContext, bad.ID, func(t *Task, _ *Project) (string, error) { t.Status = status; return "", nil })
				id = bad.Ref
			}
			before, _ := s.Snapshot(testContext)
			_, err := s.QueueTask(testContext, a.ID, TaskInput{Objective: "Use", DependsOn: []string{good.ID, id}})
			if err == nil {
				t.Fatal("accepted invalid dependency")
			}
			if status == "missing" {
				if !strings.Contains(err.Error(), id) || strings.Contains(err.Error(), "in this project") {
					t.Fatal(err)
				}
			} else if !strings.Contains(err.Error(), "has finished") {
				t.Fatal(err)
			}
			after, _ := s.Snapshot(testContext)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("failed request changed state")
			}
		})
	}
}
func TestAssistantMayRequestCrossProjectDependency(t *testing.T) {
	s, _ := fixture(t)
	a, b := plannedProject(t, s), newProject(t, s)
	dep, _ := s.QueueTask(testContext, b.ID, TaskInput{Objective: "Dependency"})
	got, err := s.QueueTaskAs(testContext, a.ID, TaskInput{Objective: "Use", DependsOn: []string{dep.Ref}}, LinkedByAssistant)
	if err != nil || got.LinkedBy[linkKey(RelationDependsOn, dep.ID)].By != LinkedByAssistant {
		t.Fatalf("%+v %v", got, err)
	}
}
func TestRequestedDependenciesRefuseCrossProjectCycle(t *testing.T) {
	v := Snapshot{Projects: []Project{{ID: "a"}, {ID: "b"}}, Tasks: []Task{{ID: "remote", ProjectID: "b", DependsOn: []string{"new"}}}}
	if _, err := requestedDependencies(&v, Task{ID: "new", ProjectID: "a"}, []string{"remote"}, true); err == nil || !strings.Contains(err.Error(), "already waits") {
		t.Fatal(err)
	}
}

func TestOwnerDependencySurvivesPMTriage(t *testing.T) {
	s, _ := fixture(t)
	a, b := pmProject(t, s), newProject(t, s)
	dep, _ := s.QueueTask(testContext, b.ID, TaskInput{Objective: "Dependency"})
	got, err := s.QueueTask(testContext, a.ID, TaskInput{Objective: "Use", DependsOn: []string{dep.Ref}})
	if err != nil || got.Status != TaskTriage {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := s.ApplyPM(testContext, a.ID, PMAnswer{Triage: []TriageRelease{{Task: got.ID, To: TriageToResearch}}}); err != nil {
		t.Fatal(err)
	}
	s.Schedule(testContext, anyone)
	got = taskByID(t, s, got.ID)
	if got.Status != TaskQueued || !slices.Equal(got.DependsOn, []string{dep.ID}) {
		t.Fatal(got)
	}
}

func TestFinishedLocalDependencyStillCreatesAnInertLink(t *testing.T) {
	s, _ := fixture(t)
	p := plannedProject(t, s)
	dep, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Done"})
	s.UpdateTask(testContext, dep.ID, func(t *Task, _ *Project) (string, error) { t.Status = TaskLanded; return "", nil })
	got, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Use", DependsOn: []string{dep.Ref}})
	if err != nil || !slices.Equal(got.DependsOn, []string{dep.ID}) || len(got.WaitingOn) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}
