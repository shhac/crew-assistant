package core

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
)

func sentOnTasks(t *testing.T, s *Service, p Project, n int, pm bool) []Task {
	t.Helper()
	var tasks []Task
	var releases []TriageRelease
	for i := 0; i < n; i++ {
		task, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: fmt.Sprintf("Request %d", i)})
		if err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, task)
		releases = append(releases, TriageRelease{Task: task.ID, To: TriageToResearch})
	}
	if pm {
		if _, err := s.ApplyPM(testContext, p.ID, PMAnswer{Triage: releases}); err != nil {
			t.Fatal(err)
		}
	} else if err := s.ReleaseTriage(testContext, p.ID, "missing PM"); err != nil {
		t.Fatal(err)
	}
	return tasks
}

func TestTriageAndTodoUnlimitedByDefault(t *testing.T) {
	for _, pm := range []bool{false, true} {
		t.Run(fmt.Sprint(pm), func(t *testing.T) {
			s, _ := fixture(t)
			p := pmProject(t, s)
			tasks := sentOnTasks(t, s, p, 25, pm)
			snap, _ := s.Snapshot(testContext)
			if snap.HasTriage(p.ID) {
				t.Fatal("triage remained")
			}
			for _, task := range tasks {
				if got := taskNow(t, s, task.ID); got.Status != TaskQueued || got.SentOn {
					t.Fatalf("not released: %+v", got)
				}
			}
		})
	}
}

func TestTodoCapacityGatesOnlyTriageAndRefillsInSamePass(t *testing.T) {
	for _, pm := range []bool{false, true} {
		t.Run(fmt.Sprint(pm), func(t *testing.T) {
			s, _ := fixture(t)
			p := pmProject(t, s)
			book := *p.Playbook
			book.StageLimits = map[string]int{StageTodo: 2}
			p, err := s.SetPlaybook(testContext, p.ID, book)
			if err != nil {
				t.Fatal(err)
			}
			tasks := sentOnTasks(t, s, p, 4, pm)
			snap, _ := s.Snapshot(testContext)
			if snap.HasTriage(p.ID) {
				t.Fatal("sent-on tasks must not be triaged again")
			}
			for _, task := range tasks[2:] {
				got := taskNow(t, s, task.ID)
				want := Wait{Kind: WaitStage, Stage: StageTodo, From: StageTriage, Count: 2, Limit: 2}
				if got.Status != TaskTriage || !got.SentOn || got.Waiting == nil || *got.Waiting != want {
					t.Fatalf("held triage: %+v", got)
				}
			}
			claimed(t, s)
			if got := taskNow(t, s, tasks[2].ID); got.Status != TaskQueued || got.SentOn || got.Waiting != nil {
				t.Fatalf("same-pass refill: %+v", got)
			}
			if got := taskNow(t, s, tasks[3].ID); got.Status != TaskTriage || !got.SentOn {
				t.Fatalf("overfilled: %+v", got)
			}
			// Clearing the limit pulls the remainder even though the writer is busy.
			book.StageLimits = nil
			if _, err := s.SetPlaybook(testContext, p.ID, book); err != nil {
				t.Fatal(err)
			}
			claimed(t, s)
			if got := taskNow(t, s, tasks[3].ID); got.Status != TaskQueued {
				t.Fatalf("unlimited: %+v", got)
			}
		})
	}
}

func TestNoPMReleaseSummaryNamesTasksWaitingForTodoRoom(t *testing.T) {
	s, _ := fixture(t)
	p := pmProject(t, s)
	book := *p.Playbook
	book.StageLimits = map[string]int{StageTodo: 1}
	p, err := s.SetPlaybook(testContext, p.ID, book)
	if err != nil {
		t.Fatal(err)
	}
	sentOnTasks(t, s, p, 3, false)
	snap, _ := s.Snapshot(testContext)
	if !slices.ContainsFunc(snap.Activity, func(e Activity) bool {
		return e.Kind == "task.triaged" && strings.Contains(e.Summary, "Sent on without triage (3; 2 waiting for room in To do)")
	}) {
		t.Fatalf("summary: %+v", snap.Activity)
	}
}

func TestSentOnSurvivesRestartAndPause(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewService(st, config.Default())
	p := pmProject(t, s)
	book := *p.Playbook
	book.StageLimits = map[string]int{StageTodo: 1, StageQA: 4}
	p, err = s.SetPlaybook(testContext, p.ID, book)
	if err != nil {
		t.Fatal(err)
	}
	tasks := sentOnTasks(t, s, p, 2, true)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s = NewService(st, config.Default())
	snap, _ := s.Snapshot(testContext)
	loaded := project(&snap, p.ID).Playbook
	if loaded.StageLimits[StageQA] != 4 || loaded.StageLimit(StageReviewing) != 10 {
		t.Fatalf("stored capacities: %+v", loaded)
	}
	if got := taskNow(t, s, tasks[1].ID); !got.SentOn || got.Status != TaskTriage || snap.HasTriage(p.ID) {
		t.Fatalf("restart: %+v", got)
	}
	if _, err := s.SetProjectPaused(testContext, p.ID, true); err != nil {
		t.Fatal(err)
	}
	claimed(t, s)
	if got := taskNow(t, s, tasks[1].ID); got.Status != TaskTriage || got.Waiting == nil || got.Waiting.Stage != StageTodo || got.Waiting.Count != 1 || got.Waiting.Limit != 1 {
		t.Fatalf("paused triage lost its place or capacity wait: %+v", got)
	}
	if _, err := s.SetProjectPaused(testContext, p.ID, false); err != nil {
		t.Fatal(err)
	}
	claimed(t, s)
	if got := taskNow(t, s, tasks[1].ID); got.Status != TaskQueued || got.SentOn {
		t.Fatalf("resumed: %+v", got)
	}
}

func TestReadyOwnerWaitsRespectOptionalCapacity(t *testing.T) {
	for _, capacity := range []int{0, 10} {
		t.Run(fmt.Sprint(capacity), func(t *testing.T) {
			s, _ := fixture(t)
			p := staged(t, s, newProject(t, s), 0, map[string]int{StageReady: capacity})
			tasks := queueAll(t, s, p, "next")
			if err := s.store.update(testContext, func(v *Snapshot) error {
				for i := 0; i < 10; i++ {
					id := fmt.Sprintf("approval-%d", i)
					v.Decisions = append(v.Decisions, Decision{ID: id, Kind: DecisionDelivery, ProjectID: p.ID, Status: DecisionOpen})
					v.Tasks = append(v.Tasks, Task{ID: id, ProjectID: p.ID, Status: TaskWaiting, Place: StageReady, DecisionID: id})
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			claimed(t, s)
			finish(t, s, tasks[0].ID, TaskReviewing)
			claimed(t, s)
			judge(t, s, tasks[0].ID, "Reviewer", VerdictPass)
			judge(t, s, tasks[0].ID, "QA", VerdictPass)
			claimed(t, s)
			if capacity > 0 {
				if w := waiting(t, s, tasks[0].ID); w == nil || w.Stage != StageReady || w.Count != 10 || w.Limit != capacity {
					t.Fatalf("approval capacity: %+v", w)
				}
			} else if got := taskNow(t, s, tasks[0].ID); got.Place != StageReady || got.Waiting != nil {
				t.Fatalf("unlimited Ready blocked: %+v", got)
			}
		})
	}
}

func TestCapacityValidation(t *testing.T) {
	for _, stage := range append([]string{StageTodo}, limitStages...) {
		for _, n := range []int{0, 4, 11, 500, 1000} {
			book := Templates["draft"]
			book.StageLimits = map[string]int{stage: n}
			if err := book.Validate(); err != nil {
				t.Fatalf("%s=%d: %v", stage, n, err)
			}
		}
	}
	for _, limits := range []map[string]int{{StageQA: -1}, {StageQA: 1001}, {StageTriage: 0}, {StageTriage: 10}} {
		book := Templates["draft"]
		book.StageLimits = limits
		if book.Validate() == nil {
			t.Fatalf("accepted %+v", limits)
		}
	}
}

func TestCustomQACapacityAndLoweringDoesNotEvict(t *testing.T) {
	s, _ := fixture(t)
	p := staged(t, s, newProject(t, s), 0, map[string]int{StageQA: 4})
	tasks := queueAll(t, s, p, "A", "B", "C", "D", "E")
	claimed(t, s)
	for _, task := range tasks {
		finish(t, s, task.ID, TaskReviewing)
		claimed(t, s)
		judge(t, s, task.ID, "Reviewer", VerdictPass)
		claimed(t, s)
	}
	if w := waiting(t, s, tasks[4].ID); w == nil || w.Stage != StageQA || w.Count != 4 || w.Limit != 4 {
		t.Fatalf("custom QA capacity: %+v", w)
	}
	book := *p.Playbook
	book.StageLimits = map[string]int{StageQA: 2}
	if _, err := s.SetPlaybook(testContext, p.ID, book); err != nil {
		t.Fatal(err)
	}
	claimed(t, s)
	for _, task := range tasks[:4] {
		if got := taskNow(t, s, task.ID); got.Place != StageQA {
			t.Fatalf("evicted: %+v", got)
		}
	}
	if w := waiting(t, s, tasks[4].ID); w == nil || w.Count != 4 || w.Limit != 2 {
		t.Fatalf("lowered capacity: %+v", w)
	}
}

func TestTodoOverCapacityAcceptsDirectWorkAndIgnoresStoppedTriage(t *testing.T) {
	s, _ := fixture(t)
	p := pmProject(t, s)
	book := *p.Playbook
	book.StageLimits = map[string]int{StageTodo: 1}
	p, err := s.SetPlaybook(testContext, p.ID, book)
	if err != nil {
		t.Fatal(err)
	}
	tasks := sentOnTasks(t, s, p, 3, true)
	direct, err := s.QueueTaskAs(testContext, p.ID, TaskInput{Objective: "split off"}, LinkedByPM)
	if err != nil || direct.Status != TaskQueued {
		t.Fatalf("direct work refused: %+v %v", direct, err)
	}
	finish(t, s, tasks[1].ID, TaskStopped)
	claimed(t, s)
	if got := taskNow(t, s, tasks[1].ID); !got.Finished() || got.Waiting != nil {
		t.Fatalf("stopped triage: %+v", got)
	}
	if w := waiting(t, s, tasks[2].ID); w == nil || w.Count != 1 || w.Limit != 1 {
		t.Fatalf("lowered queue: %+v", w)
	}
	// No eviction after lowering below the queue length; only starts free room.
	if got := taskNow(t, s, direct.ID); got.Status != TaskQueued {
		t.Fatalf("direct queue changed: %+v", got)
	}
}

func TestSentOnWaitDoesNotChangeTaskTimestamp(t *testing.T) {
	s, _ := fixture(t)
	p := pmProject(t, s)
	book := *p.Playbook
	book.StageLimits = map[string]int{StageTodo: 1}
	p, err := s.SetPlaybook(testContext, p.ID, book)
	if err != nil {
		t.Fatal(err)
	}
	tasks := sentOnTasks(t, s, p, 3, true)
	// Starting the queued task leaves one sent-on task held behind the refill.
	claimed(t, s)
	before := taskNow(t, s, tasks[2].ID)
	if !before.SentOn {
		t.Fatal("task was not held")
	}
	for i := 1; i <= 2; i++ {
		at := before.UpdatedAt.Add(time.Duration(i) * time.Hour)
		s.now = func() time.Time { return at }
		claimed(t, s)
		got := taskNow(t, s, tasks[2].ID)
		if !got.UpdatedAt.Equal(before.UpdatedAt) || got.Detail != before.Detail || got.Waiting == nil {
			t.Fatalf("idle handoff changed task: before=%+v after=%+v", before, got)
		}
	}
}
