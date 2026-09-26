package core

import (
	"errors"
	"testing"
)

// Only a task with a draft that is being written, checked or waiting on the
// owner takes a draft by hand; unapproved, it is checked by everyone.
func TestADraftByHandIsCheckedLikeAnyOther(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	task, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Add Feature", Criteria: []string{"Works"}})
	if _, err := s.AdoptDraft(testContext, task.ID, Revision{Ref: "abc"}, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("a task with no draft took one: %v", err)
	}
	s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
		t.Status, t.Revisions = TaskReviewing, []Revision{{N: 1, Ref: "first"}}
		return "", nil
	})
	got, err := s.AdoptDraft(testContext, task.ID, Revision{Ref: "second", Summary: "By hand"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if r := got.Revisions[1]; r.N != 2 || r.By != DraftByOwner || got.Approved != 0 || len(got.Verdicts) != 0 || got.Status != TaskReviewing {
		t.Fatalf("%+v", got)
	}
	s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
		t.Status = TaskLanding
		return "", nil
	})
	if _, err := s.AdoptDraft(testContext, task.ID, Revision{Ref: "third"}, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("a landing task took a draft: %v", err)
	}
}
