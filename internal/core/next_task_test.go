package core

import (
	"testing"
	"time"
)

func TestTheFurthestAlongWorkCarriesOnFirst(t *testing.T) {
	s, _ := fixture(t)
	start := s.now()
	p := newProject(t, s)
	queue := func(objective string, at time.Duration) Task {
		s.now = func() time.Time { return start.Add(at) }
		task, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: objective})
		started, _, _ := s.NextTask(testContext)
		if started.ID != task.ID || started.StartedAt.IsZero() {
			t.Fatalf("%s didn't start: %+v", objective, started)
		}
		// Park it so the next one starts too.
		s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
			t.Status = TaskWaiting
			return "", nil
		})
		return task
	}
	writing, reviewing, older, newer := queue("writing", 0), queue("reviewing", time.Minute), queue("landing, started first", 2*time.Minute), queue("landing, started later", 3*time.Minute)
	set := func(task Task, status string, drafts int) {
		s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
			t.Status, t.Revisions = status, make([]Revision, drafts)
			return "", nil
		})
	}
	set(writing, TaskWriting, 3)
	set(reviewing, TaskReviewing, 1)
	set(newer, TaskLanding, 1)
	set(older, TaskLanding, 1)
	next := func() string {
		task, _, _ := s.NextTask(testContext)
		return task.Objective
	}
	if got := next(); got != "landing, started first" {
		t.Fatalf("carried on with %q", got)
	}
	// More drafts done is further along than an earlier start.
	set(newer, TaskLanding, 2)
	if got := next(); got != "landing, started later" {
		t.Fatalf("carried on with %q", got)
	}
	// Work waiting to retry is passed over while other work can move.
	s.UpdateTask(testContext, newer.ID, func(t *Task, _ *Project) (string, error) {
		t.RetryAt = start.Add(time.Hour)
		return "", nil
	})
	set(older, TaskWaiting, 1)
	if got := next(); got != "reviewing" {
		t.Fatalf("carried on with %q", got)
	}
	// While all started work waits to retry, nothing new starts.
	for _, task := range []Task{writing, reviewing} {
		s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
			t.RetryAt = start.Add(time.Hour)
			return "", nil
		})
	}
	queued, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "queued"})
	if task, _, _ := s.NextTask(testContext); !task.RetryAt.After(start) {
		t.Fatalf("carried on with %q", task.Objective)
	}
	snap, _ := s.Snapshot(testContext)
	if task(&snap, queued.ID).Status != TaskQueued {
		t.Fatal("new work started while started work waited to retry")
	}
}
