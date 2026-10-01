package core

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
)

func taskLinearFixture(t *testing.T) (*Service, Project, Task, LinearRef) {
	t.Helper()
	s, p, _ := linearFixture(t)
	task, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	ref := LinearRef{LinearIssue: LinearIssue{ID: "22222222-2222-2222-2222-222222222222", Identifier: "EX-1", Title: "Work", URL: "https://linear.app/example/issue/EX-1"}, Kind: "issue", ConnectionID: "lin", Profile: "home"}
	return s, p, task, ref
}
func TestTaskLinearRetainsReceiptAndDeduplicates(t *testing.T) {
	s, p, task, ref := taskLinearFixture(t)
	out, err := s.AddTaskLinear(testContext, p.ID, task.ID, ref, LinkedByOwner)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.LinearLinks) != 1 || out.LinearLinks[0].By != LinkedByOwner || out.LinearLinks[0].At.IsZero() || out.LinearLinks[0].Title != ref.Title || out.LinearLinks[0].URL != ref.URL {
		t.Fatal(out)
	}
	before := stateVersion(t, s)
	duplicate, err := s.AddTaskLinear(testContext, p.ID, task.ID, ref, LinkedByAssistant)
	if err != nil || !reflect.DeepEqual(duplicate, out) || stateVersion(t, s) != before {
		t.Fatal("duplicate wrote state", err)
	}
	if _, err = s.RemoveTaskLinear(testContext, p.ID, task.ID, "issue", ref.ID, LinkedByOwner); err != nil {
		t.Fatal(err)
	}
	before = stateVersion(t, s)
	if _, err = s.RemoveTaskLinear(testContext, p.ID, task.ID, "issue", ref.ID, LinkedByOwner); !errors.Is(err, ErrNotFound) || stateVersion(t, s) != before {
		t.Fatal("missing removal changed state", err)
	}
	made, err := s.PickUpLinearIssue(testContext, p.ID, p.Linear.Version, ref.LinearIssue)
	if err != nil || made {
		t.Fatal("removed issue re-imported", made, err)
	}
	snap, _ := s.Snapshot(testContext)
	if len(snap.Tasks) != 1 || len(snap.Projects[0].LinearImported) != 1 {
		t.Fatal(snap)
	}
}
func TestTaskLinearProjectReplacementAndFinishedTasks(t *testing.T) {
	s, p, task, ref := taskLinearFixture(t)
	finish(t, s, task.ID, TaskStopped)
	ref.Kind = "project"
	ref.Identifier = "First"
	ref.Title = "First"
	if _, err := s.AddTaskLinear(testContext, p.ID, task.ID, ref, LinkedByAssistant); err != nil {
		t.Fatal(err)
	}
	ref.ID = "33333333-3333-3333-3333-333333333333"
	ref.Title = "Second"
	ref.Identifier = "Second"
	out, err := s.AddTaskLinear(testContext, p.ID, task.ID, ref, LinkedByOwner)
	if err != nil || len(out.LinearLinks) != 1 || out.LinearLinks[0].ID != ref.ID {
		t.Fatal(out, err)
	}
}
func TestTaskLinearValidationAndLimit(t *testing.T) {
	s, p, task, ref := taskLinearFixture(t)
	for _, change := range []func(*LinearRef){
		func(r *LinearRef) { r.Kind = "team" }, func(r *LinearRef) { r.ID = "bad" }, func(r *LinearRef) { r.URL = "https://evil.example/" },
		func(r *LinearRef) { r.URL = "http://linear.app/" }, func(r *LinearRef) { r.Profile = "gone" }, func(r *LinearRef) { r.Identifier = "-EX-1" },
		func(r *LinearRef) { r.Title = "bad\nvalue" }, func(r *LinearRef) { r.ConnectionID = "gone" },
	} {
		bad := ref
		change(&bad)
		before := stateVersion(t, s)
		if _, err := s.AddTaskLinear(testContext, p.ID, task.ID, bad, LinkedByOwner); err == nil || stateVersion(t, s) != before {
			t.Fatal("invalid ref accepted", bad, err)
		}
	}
	if _, err := s.AddTaskLinear(testContext, p.ID, "missing", ref, LinkedByOwner); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	for n := 0; n < 50; n++ {
		ref.ID = fmt.Sprintf("%08x-2222-2222-2222-222222222222", n)
		if _, err := s.AddTaskLinear(testContext, p.ID, task.ID, ref, LinkedByOwner); err != nil {
			t.Fatal(err)
		}
	}
	ref.ID = "ffffffff-2222-2222-2222-222222222222"
	if _, err := s.AddTaskLinear(testContext, p.ID, task.ID, ref, LinkedByOwner); err == nil {
		t.Fatal("limit ignored")
	}
}
func TestTaskLinearSourceCannotBeChanged(t *testing.T) {
	s, p, _, ref := taskLinearFixture(t)
	if _, err := s.PickUpLinearIssue(testContext, p.ID, p.Linear.Version, ref.LinearIssue); err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(testContext)
	source := snap.Tasks[1]
	before := stateVersion(t, s)
	out, err := s.AddTaskLinear(testContext, p.ID, source.ID, ref, LinkedByOwner)
	if err != nil || len(out.LinearLinks) != 0 || !reflect.DeepEqual(out.Linear, source.Linear) || stateVersion(t, s) != before {
		t.Fatal(out, err)
	}
	if _, err = s.RemoveTaskLinear(testContext, p.ID, source.ID, "issue", ref.ID, LinkedByOwner); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}
func TestTaskLinearConcurrentAddsAndPickUp(t *testing.T) {
	s, p, task, ref := taskLinearFixture(t)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			if _, err := s.AddTaskLinear(testContext, p.ID, task.ID, ref, LinkedByOwner); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Go(func() {
		if _, err := s.PickUpLinearIssue(testContext, p.ID, p.Linear.Version, ref.LinearIssue); err != nil {
			t.Error(err)
		}
	})
	wg.Wait()
	snap, _ := s.Snapshot(testContext)
	if len(snap.Projects[0].LinearImported) != 1 || len(snap.Tasks[0].LinearLinks) != 1 || len(snap.Tasks) > 2 {
		t.Fatal(snap)
	}
	// An import which wins first remains; a later link never deletes its task.
	count := 0
	for _, a := range snap.Activity {
		if a.Kind == "task.linear-linked" {
			count++
		}
	}
	if count != 1 {
		t.Fatal("duplicate link activity", count)
	}
}

func TestTaskLinearDoesNotSchedulePM(t *testing.T) {
	for _, by := range []string{LinkedByOwner, LinkedByAssistant} {
		t.Run(by, func(t *testing.T) {
			s, p, task, ref := taskLinearFixture(t)
			pb := *p.Playbook
			pb.Roles = append(pb.Roles, Role{Name: "PM", Kinds: []string{RolePM}, Engine: "claude"})
			if _, err := s.SetPlaybook(testContext, p.ID, pb); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ApplyPM(testContext, p.ID, PMAnswer{}); err != nil {
				t.Fatal(err)
			}
			assertNotDue := func() {
				t.Helper()
				snap, err := s.Snapshot(testContext)
				if err != nil {
					t.Fatal(err)
				}
				p, ok := findProjectByID(snap, p.ID)
				if !ok || p.PMDue {
					t.Fatal("Linear metadata scheduled a PM decision", p)
				}
			}
			assertNotDue()
			for _, kind := range []string{"issue", "project"} {
				ref.Kind = kind
				if _, err := s.AddTaskLinear(testContext, p.ID, task.ID, ref, by); err != nil {
					t.Fatal(err)
				}
				assertNotDue()
				if _, err := s.RemoveTaskLinear(testContext, p.ID, task.ID, kind, ref.ID, by); err != nil {
					t.Fatal(err)
				}
				assertNotDue()
			}
		})
	}
}
