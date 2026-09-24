package core

import (
	"errors"
	"slices"
	"testing"
)

func TestATaskSitsOnTheBoardWhereTheLoopHasGotTo(t *testing.T) {
	team := []Role{{Name: "Implementer", Kind: RoleImplementer}, {Name: "Reviewer", Kind: RoleReviewer}, {Name: "QA", Kind: RoleQA}}
	noQA := team[:2]
	drafted := []Revision{{N: 1}, {N: 2}}
	reviewed := []Verdict{{Revision: 2, Role: "Reviewer", Outcome: VerdictPass}}
	qaAsked := []Verdict{{Revision: 2, Role: "Reviewer", Outcome: VerdictPass}, {Revision: 2, Role: "QA", Outcome: VerdictQuestion, Question: "which tool?"}}
	decisions := []Decision{{ID: "delivery", Kind: "delivery"}, {ID: "failure", Kind: "failure"}, {ID: "question", Kind: "question"}, {ID: "escalation", Kind: "escalation"}}
	for name, tc := range map[string]struct {
		task Task
		want string
	}{
		"queued":                     {Task{Status: TaskQueued}, StageTodo},
		"writing":                    {Task{Status: TaskWriting, Roles: team}, StageImplementing},
		"reviewer still to judge":    {Task{Status: TaskReviewing, Roles: team, Revisions: drafted, Verdicts: []Verdict{{Revision: 1, Role: "Reviewer"}}}, StageReviewing},
		"reviewed, QA next":          {Task{Status: TaskReviewing, Roles: team, Revisions: drafted, Verdicts: reviewed}, StageQA},
		"reviewed, no QA":            {Task{Status: TaskDeciding, Roles: noQA, Revisions: drafted, Verdicts: reviewed}, StageReviewing},
		"every check in":             {Task{Status: TaskDeciding, Roles: team, Revisions: drafted, Verdicts: qaAsked}, StageQA},
		"awaiting approval":          {Task{Status: TaskWaiting, DecisionID: "delivery", Roles: team, Revisions: drafted}, StageReady},
		"QA asked a question":        {Task{Status: TaskWaiting, DecisionID: "question", Roles: team, Revisions: drafted, Verdicts: qaAsked}, StageQA},
		"reviewer asked a question":  {Task{Status: TaskWaiting, DecisionID: "question", Roles: team, Revisions: drafted, Verdicts: reviewed}, StageReviewing},
		"out of rounds":              {Task{Status: TaskWaiting, DecisionID: "escalation", Roles: team, Revisions: drafted}, StageQA},
		"failed while landing":       {Task{Status: TaskWaiting, DecisionID: "failure", ResumeStatus: TaskLanding, Roles: team}, StageReady},
		"failed with nowhere to go":  {Task{Status: TaskWaiting, DecisionID: "failure", Roles: team}, StageImplementing},
		"landing":                    {Task{Status: TaskLanding}, StageReady},
		"waiting on a pull request":  {Task{Status: TaskAwaiting}, StageReady},
		"delivered":                  {Task{Status: TaskDelivered}, StageDone},
		"landed":                     {Task{Status: TaskLanded}, StageDone},
		"stopped":                    {Task{Status: TaskStopped}, StageStopped},
		"reviewing with no revision": {Task{Status: TaskReviewing, Roles: team}, StageImplementing},
	} {
		if got := stageOf(&Snapshot{Decisions: decisions}, tc.task); got != tc.want {
			t.Errorf("%s: stage %q, want %q", name, got, tc.want)
		}
	}
}

func TestTheStageIsCurrentWhereverATaskIsRead(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	queued, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "A draft"})
	if err != nil || queued.Stage != StageTodo {
		t.Fatalf("queued %q %v", queued.Stage, err)
	}
	started, _, err := s.NextTask(testContext)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := s.UpdateTask(testContext, started.ID, func(t *Task, _ *Project) (string, error) {
		t.Status = TaskLanded
		return "", nil
	})
	if err != nil || updated.Stage != StageDone {
		t.Fatalf("returned stage %q %v", updated.Stage, err)
	}
	snap, _ := s.Snapshot(testContext)
	if snap.Tasks[0].Stage != StageDone {
		t.Fatalf("read stage %q", snap.Tasks[0].Stage)
	}
}

func TestTheToDoListIsReorderedOnlyWithinItsProject(t *testing.T) {
	s, _ := fixture(t)
	p, other := newProject(t, s), newProject(t, s)
	queue := func(project Project, objective string) Task {
		t.Helper()
		task, err := s.QueueTask(testContext, project.ID, TaskInput{Objective: objective})
		if err != nil {
			t.Fatal(err)
		}
		return task
	}
	a, x, b, c := queue(p, "a"), queue(other, "x"), queue(p, "b"), queue(p, "c")
	if _, err := s.OrderTasks(testContext, p.ID, []string{c.ID, a.ID, b.ID}); err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(testContext)
	var got []string
	for _, task := range snap.Tasks {
		got = append(got, task.Objective)
	}
	if want := []string{"c", "x", "a", "b"}; !slices.Equal(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
	next, _, _ := s.NextTask(testContext)
	if next.ID != c.ID {
		t.Fatalf("the loop started %q, not the first on the list", next.Objective)
	}
	// c has started, so a list that still names it is out of date.
	if _, err := s.OrderTasks(testContext, p.ID, []string{c.ID, b.ID, a.ID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a stale list was accepted: %v", err)
	}
	if _, err := s.OrderTasks(testContext, p.ID, []string{b.ID, x.ID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("another project's task was accepted: %v", err)
	}
	if _, err := s.OrderTasks(testContext, p.ID, []string{b.ID, b.ID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a repeated task was accepted: %v", err)
	}
}
