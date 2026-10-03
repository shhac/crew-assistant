package core

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func splitFixture(t *testing.T, criterion string, own bool) (*Service, Task, Decision) {
	t.Helper()
	s, _ := fixture(t)
	p := newProject(t, s)
	in := TaskInput{Objective: "Update README"}
	if own {
		in.Criteria = []string{"First", criterion, "Last"}
	} else {
		criterion = p.Brief.Criteria[0]
	}
	task, err := s.QueueTask(testContext, p.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
		t.Unreachable = []Unreachable{{Criterion: criterion, Revision: 1}}
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.OpenTaskDecision(testContext, task.ID, DecisionEscalation, DecisionInput{Title: "Split?", Context: "Blocked", Recommendation: "Split", Choices: []string{"Split it", "Keep it for the team"}, OwnerStep: &OwnerStep{Criterion: criterion, Step: criterion}})
	if err != nil {
		t.Fatal(err)
	}
	return s, task, d
}

func TestSplitOwnerStepIsOneUndoableEdit(t *testing.T) {
	s, before, d := splitFixture(t, "README; CI; release", true)
	resolved, err := s.ChooseDecision(testContext, d.ID, "Split it", FromOwner, &OwnerSplit{Team: " README ", Owner: " CI and release "})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Split == nil || *resolved.Split != (OwnerSplit{Team: "README", Owner: "CI and release"}) {
		t.Fatalf("resolved: %+v", resolved)
	}
	if _, err = s.ChooseDecision(testContext, d.ID, "Split it", FromOwner, resolved.Split); !errors.Is(err, ErrConflict) {
		t.Fatalf("second resolve: %v", err)
	}
	got, err := s.SplitOwnerStep(testContext, before.ID, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Criteria, []string{"First", "README", "Last"}) || !slices.Equal(got.OwnerChecks, []string{"CI and release"}) || !slices.Equal(got.TeamKept, []string{"README"}) {
		t.Fatalf("lists: %+v", got)
	}
	if !slices.Contains(got.OwnerTook, d.OwnerStep.Criterion) {
		t.Fatal("split original not settled")
	}
	if got.TextVersion != before.TextVersion+1 || len(got.Edits) != 1 || got.Edits[0].By != FromOwner || got.Status != TaskReviewing || got.DecisionID != "" || len(got.Unreachable) != 0 {
		t.Fatalf("edit: %+v", got)
	}
	again, err := s.SplitOwnerStep(testContext, before.ID, d.ID)
	if err != nil || !reflect.DeepEqual(got, again) {
		t.Fatalf("second application changed task: %+v %v", again, err)
	}
	snap, err := s.Snapshot(testContext)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, a := range snap.Activity {
		if a.TaskID == got.ID && a.Kind == "task.owner_split" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("split activity recorded %d times", count)
	}
	undone, err := s.UndoTaskEdit(testContext, got.ProjectID, got.ID, got.Edits[0].ID)
	if err != nil || !slices.Equal(undone.Criteria, before.Criteria) || len(undone.OwnerChecks) != 0 {
		t.Fatalf("undo: %+v %v", undone, err)
	}
}

func TestSplitOwnerStepBriefEditedFinishedAndNoOp(t *testing.T) {
	for _, kind := range []string{"brief", "edited", "finished", "no-op"} {
		t.Run(kind, func(t *testing.T) {
			s, task, d := splitFixture(t, "README; CI; release", kind != "brief")
			if _, err := s.ChooseDecision(testContext, d.ID, "Split it", FromOwner, &OwnerSplit{Team: "README", Owner: "CI"}); err != nil {
				t.Fatal(err)
			}
			before, err := s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
				switch kind {
				case "edited":
					t.Criteria = []string{"Changed meanwhile"}
				case "finished":
					t.Status = TaskStopped
				case "no-op":
					t.Criteria = []string{"README"}
					t.OwnerChecks = []string{"CI"}
				}
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			got, err := s.SplitOwnerStep(testContext, task.ID, d.ID)
			if err != nil {
				t.Fatal(err)
			}
			if kind != "finished" {
				snap, err := s.Snapshot(testContext)
				if err != nil {
					t.Fatal(err)
				}
				if !slices.ContainsFunc(snap.Activity, func(a Activity) bool {
					return a.TaskID == task.ID && a.Kind == "task.owner_split" && strings.Contains(a.Summary, "README stays with the team; CI after it lands")
				}) {
					t.Fatal("split decision missing from activity")
				}
			}
			if kind == "finished" {
				if !reflect.DeepEqual(before, got) {
					t.Fatal("finished task changed")
				}
				return
			}
			if !slices.Contains(got.Criteria, "README") || !slices.Contains(got.OwnerChecks, "CI") || !slices.Contains(got.TeamKept, "README") {
				t.Fatalf("lists: %+v", got)
			}
			if kind == "no-op" {
				if got.Status != TaskDeciding || got.Detail != "Going on with the split requirement" {
					t.Fatalf("no-op falsely promises recheck: %+v", got)
				}
				if len(got.Edits) != 0 || got.TextVersion != before.TextVersion {
					t.Fatal("no-op made an edit")
				}
				return
			}
			if kind == "brief" && !slices.Contains(got.OwnerTook, d.OwnerStep.Criterion) {
				t.Fatal("original not settled")
			}
			if kind == "edited" && !slices.Equal(got.Criteria, []string{"Changed meanwhile", "README"}) {
				t.Fatal("overwrote edited criterion")
			}
			if kind == "brief" {
				snap, _ := s.Snapshot(testContext)
				if !slices.Contains(snap.Projects[0].Brief.Criteria, d.OwnerStep.Criterion) {
					t.Fatal("brief changed")
				}
			}
		})
	}
}

func TestInvalidSplitLeavesDecisionOpen(t *testing.T) {
	for _, kind := range []string{"missing", "empty team", "empty owner", "no owner step", "other choice", "identical", "identical after clipping", "existing owner check"} {
		t.Run(kind, func(t *testing.T) {
			s, _, d := splitFixture(t, "README; CI", true)
			split := &OwnerSplit{Team: "README", Owner: "CI"}
			choice := "Split it"
			switch kind {
			case "identical":
				split.Team = " CI "
				split.Owner = "CI"
			case "identical after clipping":
				split.Team = strings.Repeat("x", maxCriterion) + "team"
				split.Owner = strings.Repeat("x", maxCriterion) + "owner"
			case "existing owner check":
				_, err := s.UpdateTask(testContext, d.TaskID, func(t *Task, _ *Project) (string, error) { t.OwnerChecks = []string{"README"}; return "", nil })
				if err != nil {
					t.Fatal(err)
				}
			case "missing":
				split = nil
			case "empty team":
				split.Team = " "
			case "empty owner":
				split.Owner = " "
			case "other choice":
				choice = "Keep it for the team"
			case "no owner step":
				err := s.store.update(testContext, func(v *Snapshot) error { decision(v, d.ID).OwnerStep = nil; return nil })
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.ChooseDecision(testContext, d.ID, choice, FromOwner, split); err == nil {
				t.Fatal("invalid split accepted")
			}
			snap, err := s.Snapshot(testContext)
			if err != nil {
				t.Fatal(err)
			}
			for _, got := range snap.Decisions {
				if got.ID == d.ID && (got.Status != DecisionOpen || got.Split != nil) {
					t.Fatalf("decision changed: %+v", got)
				}
			}
		})
	}
}

func TestSplitReplacementDoesNotDuplicateExistingCriterion(t *testing.T) {
	s, task, d := splitFixture(t, "README; CI", true)
	if _, err := s.ChooseDecision(testContext, d.ID, ChoiceSplit, FromOwner, &OwnerSplit{Team: "First", Owner: "CI"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.SplitOwnerStep(testContext, task.ID, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Criteria, []string{"First", "Last"}) {
		t.Fatalf("duplicate criteria: %v", got.Criteria)
	}
}

func TestSplitSettlesOwnerCheckAddedWhileWaiting(t *testing.T) {
	s, task, d := splitFixture(t, "README; CI", true)
	if _, err := s.ChooseDecision(testContext, d.ID, ChoiceSplit, FromOwner, &OwnerSplit{Team: "README", Owner: "CI"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
		t.OwnerChecks = []string{"README"}
		t.TeamKept = []string{"README"}
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.SplitOwnerStep(testContext, task.ID, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Criteria, []string{"First", "Last"}) || !slices.Equal(got.OwnerChecks, []string{"README", "CI"}) || slices.Contains(got.TeamKept, "README") || !slices.Contains(got.OwnerTook, "README; CI") || len(got.Unreachable) != 0 || got.DecisionID != "" || got.Status == TaskWaiting {
		t.Fatalf("collision not settled: %+v", got)
	}
	again, err := s.SplitOwnerStep(testContext, task.ID, d.ID)
	if err != nil || !reflect.DeepEqual(got, again) {
		t.Fatalf("split repeated: %+v %v", again, err)
	}
	snap, err := s.Snapshot(testContext)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(snap.Activity, func(a Activity) bool {
		return a.TaskID == task.ID && a.Kind == "task.owner_split" && strings.Contains(a.Summary, "already an owner check")
	}) {
		t.Fatal("collision decision missing from activity")
	}
}
