package core

import "testing"

func TestUndeclaredWaitOnlyGuardsUnansweredInitialWork(t *testing.T) {
	plan := Plan{Summary: "Implementation waits for the library to be tagged"}
	for _, tc := range []struct {
		name string
		task Task
		want bool
	}{
		{"new work", Task{}, true},
		{"recorded resolved dependency", Task{DependsOn: []string{"landed"}}, false},
		{"begun", Task{Revisions: []Revision{{N: 1}}}, false},
		{"unanswered wait question", Task{Plan: &Plan{Questions: []string{UndeclaredWaitQuestion}}}, true},
		{"answered wait question", Task{Plan: &Plan{Answered: true, Questions: []string{UndeclaredWaitQuestion}}}, false},
		{"answered unrelated question", Task{Plan: &Plan{Answered: true, Questions: []string{"Which API?"}}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := UndeclaredPlanWait(plan, nil, tc.task); got != tc.want {
				t.Fatalf("undeclared wait = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestWaitAfterSentenceBoundary(t *testing.T) {
	for _, summary := range []string{"Use the new API. It waits for LIB-10 to land.", "Use the new API; it waits for LIB-10 to land.", "Use the new API. Waits for the library to be tagged."} {
		if !UndeclaredPlanWait(Plan{Summary: summary}, nil, Task{}) {
			t.Errorf("missed wait after a sentence: %s", summary)
		}
	}
	summary := "Use the API. It waits for a response; the next view is ready."
	// The bounded phrase must not cross punctuation to borrow readiness.
	if UndeclaredPlanWait(Plan{Summary: summary}, nil, Task{}) {
		t.Errorf("crossed a sentence: %s", summary)
	}

}

func TestRecordedDependencyLandingDoesNotQuestionReplan(t *testing.T) {
	s, _ := fixture(t)
	p, other := plannedProject(t, s), newProject(t, s)
	dep, _ := s.QueueTask(testContext, other.ID, TaskInput{Objective: "Publish library"})
	own, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Use library"})
	s.UpdateTask(testContext, own.ID, func(t *Task, _ *Project) (string, error) { t.Status = TaskResearching; return "", nil })
	held, err := s.RecordPlan(testContext, own.ID, Plan{Summary: "Implementation waits for the library"}, []string{dep.ID}, nil)
	if err != nil || held.Status != TaskQueued {
		t.Fatalf("hold %+v %v", held, err)
	}
	s.UpdateTask(testContext, dep.ID, func(t *Task, _ *Project) (string, error) { t.Status = TaskLanded; return "", nil })
	s.Schedule(testContext, anyone)
	current := taskByID(t, s, own.ID)
	snap, _ := s.Snapshot(testContext)
	if deps := PlanDependencies(snap, current, nil); len(deps) != 1 || deps[0] != dep.ID {
		t.Fatalf("lost recorded declaration: %v", deps)
	}
	got, err := s.RecordPlan(testContext, own.ID, Plan{Summary: "Implementation waits for the library, which has now landed"}, nil, nil)
	if err != nil || got.Status != TaskWriting || got.Plan == nil || len(got.Plan.Questions) != 0 {
		t.Fatalf("replan %+v %v", got, err)
	}
}

func TestModalWaitRequiresThisWorkAsItsSubject(t *testing.T) {
	for _, modal := range []string{"must", "should", "will"} {
		for _, subject := range []string{"Implementation", "This task", "The work on this", "Work on this task"} {
			if !saysImplementationWaits(subject + " " + modal + " wait until the library is ready") {
				t.Errorf("missed scheduling instruction: %s %s", subject, modal)
			}
		}
		if saysImplementationWaits("background work " + modal + " wait for the lock to be released") {
			t.Errorf("feature wording matched: %s", modal)
		}
	}
}

func TestRecordPlanRespectsAnsweredWaitQuestionWithoutReleasingConditions(t *testing.T) {
	for _, prerequisite := range []bool{false, true} {
		t.Run(map[bool]string{false: "start", true: "new prerequisite still holds"}[prerequisite], func(t *testing.T) {
			s, _ := fixture(t)
			p := plannedProject(t, s)
			queued, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Use release"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.UpdateTask(testContext, queued.ID, func(task *Task, _ *Project) (string, error) {
				task.Status = TaskResearching
				task.Plan = &Plan{Answered: true, Questions: []string{UndeclaredWaitQuestion}}
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			var conditions []string
			if prerequisite {
				conditions = []string{"library tagged"}
			}
			got, err := s.RecordPlan(testContext, queued.ID, Plan{Summary: "Implementation waits for the library"}, nil, conditions)
			if err != nil {
				t.Fatal(err)
			}
			if prerequisite {
				if got.Status != TaskQueued || !holdsStart(got) {
					t.Fatalf("lost prerequisite: %+v", got)
				}
			} else if got.Status != TaskWriting || got.Plan == nil || len(got.Plan.Questions) != 0 {
				t.Fatalf("asked the same question again: %+v", got)
			}
		})
	}
}
