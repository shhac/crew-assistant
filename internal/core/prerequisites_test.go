package core

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestCrossProjectPlanWaitsAndReleases(t *testing.T) {
	for _, readable := range []bool{false, true} {
		t.Run(map[bool]string{false: "canonical", true: "readable"}[readable], func(t *testing.T) {
			s, _ := fixture(t)
			a, b := plannedProject(t, s), newProject(t, s)
			b, _ = s.SetProjectTitle(testContext, b.ID, "Library")
			dep, _ := s.QueueTask(testContext, b.ID, TaskInput{Objective: "Publish library"})
			waiting, _ := s.QueueTask(testContext, a.ID, TaskInput{Objective: "Use library"})
			s.UpdateTask(testContext, waiting.ID, func(t *Task, _ *Project) (string, error) { t.Status = TaskResearching; t.Base = "old"; return "", nil })
			id := dep.ID
			if readable {
				id = dep.Ref
			}
			waiting, err := s.RecordPlan(testContext, waiting.ID, Plan{Summary: "Build on library"}, []string{id}, nil)
			if err != nil || waiting.Status != TaskQueued || waiting.Plan != nil || waiting.Base != "" || !slices.Equal(waiting.DependsOn, []string{dep.ID}) {
				t.Fatalf("wait %+v %v", waiting, err)
			}
			if len(waiting.WaitingOn) != 1 || waiting.WaitingOn[0].Project != "Library" || waiting.WaitingOn[0].Ref != dep.Ref || !strings.Contains(waiting.WaitsFor[0], "Library") {
				t.Fatalf("derived %+v", waiting)
			}
			snap, _ := s.Snapshot(testContext)
			if !slices.ContainsFunc(snap.Activity, func(e Activity) bool { return e.Kind == "task.queued" && strings.Contains(e.Summary, "Library") }) {
				t.Fatal("queued event did not name Library")
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

func prerequisiteTask(t *testing.T) (*Service, Project, Task, Decision) {
	t.Helper()
	s, _ := fixture(t)
	p := plannedProject(t, s)
	queued, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Use release"})
	started, _, _ := s.NextTask(testContext)
	if started.ID != queued.ID {
		t.Fatal(started)
	}
	held, err := s.RecordPlan(testContext, started.ID, Plan{Summary: "Use release"}, nil, []string{"lib v0.20.0 tagged", " LIB  v0.20.0 TAGGED "})
	snap, _ := s.Snapshot(testContext)
	if err != nil || held.Status != TaskQueued || held.Plan != nil || len(held.Blockers) != 1 || len(snap.Decisions) != 1 {
		t.Fatalf("held %+v decisions %+v %v", held, snap.Decisions, err)
	}
	d := snap.Decisions[0]
	if !held.Blockers[0].AnswerPending || !strings.Contains(d.Context, held.Objective) || !strings.Contains(d.Context, ChoiceReadyPrerequisite) || !strings.Contains(d.Context, ChoiceDropPrerequisite) {
		t.Fatalf("decision context %s blocker %+v", d.Context, held.Blockers[0])
	}
	stored, _ := json.Marshal(withoutRefs([]Task{held}))
	if strings.Contains(string(stored), "answer_pending") {
		t.Fatal(string(stored))
	}
	if d.Title != "Is lib v0.20.0 tagged ready?" || d.Kind != DecisionPrerequisite || d.BlockerID != held.Blockers[0].ID {
		t.Fatal(d)
	}
	s.Schedule(testContext, anyone)
	if got := taskByID(t, s, held.ID); got.Status != TaskQueued {
		t.Fatal("started before answer")
	}
	return s, p, held, d
}

func TestPrerequisitesConfirmedAndDropped(t *testing.T) {
	for _, choice := range []string{ChoiceReadyPrerequisite, ChoiceDropPrerequisite} {
		t.Run(choice, func(t *testing.T) {
			s, _, held, d := prerequisiteTask(t)
			if _, err := s.ChooseDecision(testContext, d.ID, choice); err != nil {
				t.Fatal(err)
			}
			outcome := "confirmed"
			if choice == ChoiceDropPrerequisite {
				outcome = "dropped"
			}
			got := taskByID(t, s, held.ID)
			if got.Blockers[0].Outcome != outcome || got.Blockers[0].ClearedBy != LinkedByOwner || holdsStart(got) {
				t.Fatal(got.Blockers)
			}
			s.Schedule(testContext, anyone)
			got = taskByID(t, s, held.ID)
			if got.Status != TaskResearching {
				t.Fatal(got.Status)
			}
			got, err := s.RecordPlan(testContext, got.ID, Plan{Summary: "Go"}, nil, []string{" LIB  v0.20.0 TAGGED "})
			snap, _ := s.Snapshot(testContext)
			if err != nil || got.Plan == nil || got.Plan.Prerequisites[0].Outcome != outcome || len(snap.Decisions) != 1 {
				t.Fatalf("repeat %+v %v", got, err)
			}
		})
	}
}

func TestPrerequisiteAnswerDismissAndClearAuthority(t *testing.T) {
	for _, typed := range []bool{false, true} {
		t.Run(map[bool]string{false: "dismiss", true: "typed"}[typed], func(t *testing.T) {
			s, p, held, d := prerequisiteTask(t)
			var err error
			if typed {
				_, err = s.AnswerDecision(testContext, d.ID, ChoiceReadyPrerequisite)
			} else {
				_, err = s.DismissDecision(testContext, d.ID, "Later")
			}
			if err != nil {
				t.Fatal(err)
			}
			s.Schedule(testContext, anyone)
			if got := taskByID(t, s, held.ID); got.Status != TaskQueued || !holdsStart(got) || got.Blockers[0].AnswerPending {
				t.Fatal(got)
			}
			if _, err := s.ClearBlocker(testContext, p.ID, held.ID, held.Blockers[0].ID, researcherLinker(held), "ready"); !errors.Is(err, ErrConflict) {
				t.Fatalf("team cleared prerequisite: %v", err)
			}
			got, err := s.ClearBlocker(testContext, p.ID, held.ID, held.Blockers[0].ID, LinkedByOwner, "ready")
			if err != nil || holdsStart(got) || got.Blockers[0].Outcome != "confirmed" {
				t.Fatalf("owner clear %+v %v", got, err)
			}
		})
	}
	s, p, held, d := prerequisiteTask(t)
	s.ClearBlocker(testContext, p.ID, held.ID, held.Blockers[0].ID, LinkedByAssistant, "ready")
	snap, _ := s.Snapshot(testContext)
	if snap.Decisions[0].Status != DecisionDismissed || snap.Decisions[0].ResolutionReason != "Cleared on the task" {
		t.Fatal(snap.Decisions)
	}
	if _, err := s.ChooseDecision(testContext, d.ID, ChoiceDropPrerequisite); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}

func TestStoppingTaskDismissesPrerequisite(t *testing.T) {
	s, _, held, d := prerequisiteTask(t)
	s.UpdateTask(testContext, held.ID, func(t *Task, _ *Project) (string, error) { t.Status = TaskStopped; return "", nil })
	if _, err := s.ChooseDecision(testContext, d.ID, ChoiceReadyPrerequisite); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(testContext)
	if snap.Decisions[0].ResolutionReason != "The task was stopped" {
		t.Fatal(snap.Decisions)
	}
}

func TestCrossProjectLinksKeepOwnerControlAndRejectCycles(t *testing.T) {
	s, _ := fixture(t)
	a, b := plannedProject(t, s), plannedProject(t, s)
	x, _ := s.QueueTask(testContext, a.ID, TaskInput{Objective: "A"})
	y, _ := s.QueueTask(testContext, b.ID, TaskInput{Objective: "B"})
	if _, err := s.LinkTasks(testContext, Link{Project: a.ID, Task: x.ID, Other: y.ID, Relation: RelationDependsOn, By: LinkedByOwner}); err == nil {
		t.Fatal("cross-project linking allowed")
	}
	s.store.update(testContext, func(v *Snapshot) error {
		task(v, x.ID).DependsOn = []string{y.ID}
		mark(task(v, x.ID), RelationDependsOn, y.ID, LinkedByPM, s.now())
		return nil
	})
	s.store.update(testContext, func(v *Snapshot) error {
		pmSetsDepends(v, task(v, x.ID), nil, s.now())
		if !slices.Equal(task(v, x.ID).DependsOn, []string{y.ID}) {
			t.Fatal("PM dropped cross-project wait")
		}
		if deps := possibleDependencies(v, *task(v, y.ID), []string{x.ID}, true); len(deps) != 0 {
			t.Fatal("cycle accepted")
		}
		return nil
	})
	if got := taskByID(t, s, y.ID); len(got.Blocks) != 0 {
		t.Fatal("cross-project reverse index", got.Blocks)
	}
	local, _ := s.QueueTask(testContext, a.ID, TaskInput{Objective: "Local prerequisite"})
	if got, err := s.LinkTasks(testContext, Link{Project: a.ID, Task: x.ID, Other: local.ID, Relation: RelationDependsOn, By: LinkedByOwner}); err != nil || !slices.Contains(got.DependsOn, y.ID) {
		t.Fatalf("same-project link lost the cross-project wait: %+v %v", got, err)
	}
	if _, err := s.UnlinkTasks(testContext, Link{Project: a.ID, Task: x.ID, Other: y.ID, By: LinkedByPM}); err == nil {
		t.Fatal("team unlinked cross-project wait")
	}
	got, err := s.UnlinkTasks(testContext, Link{Project: a.ID, Task: x.ID, Other: y.ID, By: LinkedByOwner})
	if err != nil || !slices.Equal(got.DependsOn, []string{local.ID}) {
		t.Fatalf("unlink %+v %v", got, err)
	}
}

func TestOldSameProjectWaitState(t *testing.T) {
	var v Snapshot
	err := json.Unmarshal([]byte(`{"projects":[{"id":"p","title":"Old","prefix":"OLD"}],"tasks":[{"id":"a","project_id":"p","number":1,"objective":"First","status":"queued"},{"id":"b","project_id":"p","number":2,"objective":"Second","status":"queued","depends_on":["a"],"linked_by":{"depends_on:a":{"by":"owner"}},"blockers":[{"id":"manual","kind":"manual","description":"Ready","by":"owner"}]}]}`), &v)
	if err != nil {
		t.Fatal(err)
	}
	deriveStages(&v)
	b := v.Tasks[1]
	if !slices.Equal(b.WaitsFor, []string{"First"}) || len(b.WaitingOn) != 1 || b.WaitingOn[0].ProjectID != "p" || !heldBack(&v, b) || !b.HeldByOwner(RelationDependsOn, "a") {
		t.Fatal(b)
	}
	stored, _ := json.Marshal(withoutRefs(v.Tasks))
	if strings.Contains(string(stored), "waiting_on") || strings.Contains(string(stored), "prerequisites") || strings.Contains(string(stored), "outcome") {
		t.Fatal(string(stored))
	}
	v.Tasks[0].Status = TaskLanded
	if !heldBack(&v, b) {
		t.Fatal("manual blocker stopped holding")
	}
	now := slices.Clone(v.Tasks)[0].UpdatedAt
	b.Blockers[0].ClearedAt = &now
	if heldBack(&v, b) {
		t.Fatal("old wait did not release")
	}
}

func TestPrerequisiteAnswerRacesTaskPageClear(t *testing.T) {
	s, p, held, d := prerequisiteTask(t)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); s.ChooseDecision(testContext, d.ID, ChoiceDropPrerequisite) }()
	go func() {
		defer wg.Done()
		s.ClearBlocker(testContext, p.ID, held.ID, held.Blockers[0].ID, LinkedByOwner, "ready")
	}()
	wg.Wait()
	got := taskByID(t, s, held.ID)
	if got.Blockers[0].ClearedAt == nil || (got.Blockers[0].Outcome != "confirmed" && got.Blockers[0].Outcome != "dropped") {
		t.Fatal(got.Blockers)
	}
	snap, _ := s.Snapshot(testContext)
	if snap.Decisions[0].Status == DecisionOpen {
		t.Fatal("decision left open")
	}
	unblocked := 0
	for _, a := range snap.Activity {
		if a.TaskID == held.ID && a.Kind == "task.unblocked" {
			unblocked++
		}
	}
	if unblocked != 1 {
		t.Fatalf("recorded %d outcomes", unblocked)
	}
}

func TestCrossProjectWaitReleasesForStoppedOrMissingWorkAndHonorsPause(t *testing.T) {
	for _, release := range []string{"stopped", "deleted", "project removed", "landed while paused"} {
		t.Run(release, func(t *testing.T) {
			s, _ := fixture(t)
			a, b := plannedProject(t, s), newProject(t, s)
			dep, _ := s.QueueTask(testContext, b.ID, TaskInput{Objective: "Library"})
			held, _ := s.QueueTask(testContext, a.ID, TaskInput{Objective: "Use library"})
			s.store.update(testContext, func(v *Snapshot) error { task(v, held.ID).Status = TaskResearching; return nil })
			s.RecordPlan(testContext, held.ID, Plan{Summary: "Wait for library"}, []string{dep.ID}, nil)
			if release == "landed while paused" {
				s.SetProjectPaused(testContext, a.ID, true)
			}
			s.store.update(testContext, func(v *Snapshot) error {
				switch release {
				case "stopped":
					task(v, dep.ID).Status = TaskStopped
				case "deleted":
					v.Tasks = slices.DeleteFunc(v.Tasks, func(t Task) bool { return t.ID == dep.ID })
				case "project removed":
					v.Projects = slices.DeleteFunc(v.Projects, func(p Project) bool { return p.ID == b.ID })
				default:
					task(v, dep.ID).Status = TaskLanded
				}
				return nil
			})
			s.Schedule(testContext, anyone)
			if release == "landed while paused" {
				if got := taskByID(t, s, held.ID); got.Status != TaskQueued || len(got.WaitingOn) != 0 {
					t.Fatal(got)
				}
				s.SetProjectPaused(testContext, a.ID, false)
				s.Schedule(testContext, anyone)
			}
			if got := taskByID(t, s, held.ID); got.Status != TaskResearching {
				t.Fatalf("not released: %+v", got)
			}
		})
	}
}

func TestBegunTaskCannotAcquirePlanPrerequisites(t *testing.T) {
	s, _ := fixture(t)
	p := plannedProject(t, s)
	x, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Already writing"})
	s.UpdateTask(testContext, x.ID, func(t *Task, _ *Project) (string, error) {
		t.Status = TaskResearching
		t.Revisions = []Revision{{N: 1}}
		return "", nil
	})
	got, err := s.RecordPlan(testContext, x.ID, Plan{Summary: "Continue"}, nil, []string{"new prerequisite"})
	snap, _ := s.Snapshot(testContext)
	if err != nil || len(got.Blockers) != 0 || len(snap.Decisions) != 0 || got.Status != TaskWriting {
		t.Fatalf("begun task %+v %v", got, err)
	}
}

func TestRecordPlanGuardsRefusedAndFinishedDependencies(t *testing.T) {
	for _, reason := range []string{"missing", "self", "cycle", "finished"} {
		t.Run(reason, func(t *testing.T) {
			s, _ := fixture(t)
			p := plannedProject(t, s)
			own, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Use work"})
			dep, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Other work"})
			id := dep.ID
			s.store.update(testContext, func(v *Snapshot) error {
				task(v, own.ID).Status = TaskResearching
				switch reason {
				case "missing":
					id = "unknown"
				case "self":
					id = own.ID
				case "cycle":
					task(v, dep.ID).DependsOn = []string{own.ID}
				case "finished":
					task(v, dep.ID).Status = TaskLanded
				}
				return nil
			})
			snap, _ := s.Snapshot(testContext)
			if deps := PlanDependencies(snap, own, []string{id}); len(deps) != 0 {
				t.Fatal(deps)
			}
			got, err := s.RecordPlan(testContext, own.ID, Plan{Summary: "Implementation waits for work to land"}, []string{id}, nil)
			if err != nil || got.Status == TaskWriting || got.Plan == nil || !slices.Contains(got.Plan.Questions, UndeclaredWaitQuestion) {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
}
