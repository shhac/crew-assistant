package core

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func savedQuestionTask(t *testing.T) (*Service, Project, Task, string) {
	t.Helper()
	s, p, held, d := prerequisiteTask(t)
	if _, err := s.UpdateTask(testContext, held.ID, func(t *Task, _ *Project) (string, error) {
		t.Status, t.Revisions = TaskResearching, []Revision{{N: 1, Ref: "draft"}}
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
	question := "Can the v0.20.0 release of lib now be used?"
	got, err := s.RecordPlan(testContext, held.ID, Plan{Role: "Researcher", Summary: "Continue", Questions: []string{question}}, nil, nil)
	if err != nil || got.Status != TaskResearching {
		t.Fatal(got, err)
	}
	if _, err := s.ChooseDecision(testContext, d.ID, ChoiceReadyPrerequisite, FromOwner); err != nil {
		t.Fatal(err)
	}
	return s, p, taskByID(t, s, held.ID), question
}

func TestSavedQuestionComparisonRechecksConcurrentReopening(t *testing.T) {
	s, p, held, question := savedQuestionTask(t)
	b := held.Blockers[0]
	calls := 0
	s.SetPrerequisiteComparer(func(_ context.Context, incoming string, isQuestion bool, settled []Prerequisite) (PrerequisiteComparison, error) {
		calls++
		if !isQuestion || incoming != question || len(settled) != 1 {
			t.Fatal(incoming, settled)
		}
		if _, err := s.ReopenPrerequisite(testContext, p.ID, held.ID, b.ID, b.Description, "Owner revoked access", LinkedByOwner, PrerequisiteReopen{Source: "concurrent-owner", Settlement: PrerequisiteSettlementID(b)}); err != nil {
			t.Fatal(err)
		}
		return PrerequisiteComparison{Result: "equivalent", Blocker: b.ID, Details: "same release and tag"}, nil
	})
	got, err := s.AskPlanQuestions(testContext, held.ID)
	if err != nil || calls != 1 || got.Status != TaskWaiting || len(got.Plan.Questions) != 1 || got.Blockers[0].ClearedAt != nil {
		t.Fatal(got, calls, err)
	}
	snap, _ := s.Snapshot(testContext)
	for _, a := range snap.Activity {
		if a.Kind == "prerequisite.reused" {
			t.Fatal("stale match was recorded", a)
		}
	}
	before, _ := s.Snapshot(testContext)
	if _, err := s.AskPlanQuestions(testContext, held.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate question request accepted", err)
	}
	after, _ := s.Snapshot(testContext)
	if !reflect.DeepEqual(before.Decisions, after.Decisions) {
		t.Fatal("duplicate decision opened")
	}
}

func TestSavedQuestionComparisonFailureCommitsNothing(t *testing.T) {
	s, _, held, _ := savedQuestionTask(t)
	before, _ := s.Snapshot(testContext)
	s.SetPrerequisiteComparer(func(context.Context, string, bool, []Prerequisite) (PrerequisiteComparison, error) {
		return PrerequisiteComparison{}, context.DeadlineExceeded
	})
	if _, err := s.AskPlanQuestions(testContext, held.ID); !errors.Is(err, ErrPrerequisiteComparison) {
		t.Fatal(err)
	}
	after, _ := s.Snapshot(testContext)
	if !reflect.DeepEqual(before.Tasks, after.Tasks) || !reflect.DeepEqual(before.Decisions, after.Decisions) || !reflect.DeepEqual(before.Activity, after.Activity) {
		t.Fatal("failed saved-question comparison committed changes")
	}
}

func TestSavedQuestionComparisonRechecksChangedPlan(t *testing.T) {
	s, _, held, _ := savedQuestionTask(t)
	calls := 0
	s.SetPrerequisiteComparer(func(_ context.Context, incoming string, _ bool, _ []Prerequisite) (PrerequisiteComparison, error) {
		calls++
		if calls == 1 {
			if _, err := s.UpdateTask(testContext, held.ID, func(t *Task, _ *Project) (string, error) {
				t.Plan.Questions = []string{"Which diagnostic format?"}
				return "", nil
			}); err != nil {
				t.Fatal(err)
			}
			return PrerequisiteComparison{Result: "equivalent", Blocker: held.Blockers[0].ID, Details: "same release"}, nil
		}
		if incoming != "Which diagnostic format?" {
			t.Fatal("did not reread the saved plan", incoming)
		}
		return PrerequisiteComparison{Result: "different"}, nil
	})
	got, err := s.AskPlanQuestions(testContext, held.ID)
	if err != nil || calls != 2 || got.Status != TaskWaiting {
		t.Fatal(got, calls, err)
	}
	snap, _ := s.Snapshot(testContext)
	if len(snap.Decisions) != 2 || snap.Decisions[1].Context != "1. Which diagnostic format?" {
		t.Fatal("opened a decision from the stale plan", snap.Decisions)
	}
}

func TestSavedQuestionComparisonRechecksConcurrentSettlement(t *testing.T) {
	for _, outcome := range []string{ChoiceReadyPrerequisite, ChoiceDropPrerequisite} {
		t.Run(outcome, func(t *testing.T) {
			s, _, held, pending := prerequisiteTask(t)
			researchAgain(t, s, held.ID)
			if _, err := s.RecordPlan(testContext, held.ID, Plan{Summary: "Build"}, nil, []string{"Another external condition"}); err != nil {
				t.Fatal(err)
			}
			snap, _ := s.Snapshot(testContext)
			if _, err := s.ChooseDecision(testContext, snap.Decisions[1].ID, ChoiceReadyPrerequisite, FromOwner); err != nil {
				t.Fatal(err)
			}
			question := "Is lib v0.20.0 tagged ready?"
			s.SetPrerequisiteComparer(func(context.Context, string, bool, []Prerequisite) (PrerequisiteComparison, error) {
				return PrerequisiteComparison{Result: "different"}, nil
			})
			if _, err := s.UpdateTask(testContext, held.ID, func(t *Task, _ *Project) (string, error) {
				t.Status, t.Revisions = TaskResearching, []Revision{{N: 1, Ref: "draft"}}
				return "", nil
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.RecordPlan(testContext, held.ID, Plan{Role: "Researcher", Summary: "Continue", Questions: []string{question}}, nil, nil); err != nil {
				t.Fatal(err)
			}
			calls := 0
			s.SetPrerequisiteComparer(func(context.Context, string, bool, []Prerequisite) (PrerequisiteComparison, error) {
				calls++
				if _, err := s.ChooseDecision(testContext, pending.ID, outcome, FromOwner); err != nil {
					t.Fatal(err)
				}
				return PrerequisiteComparison{Result: "different"}, nil
			})
			got, err := s.AskPlanQuestions(testContext, held.ID)
			if err != nil || calls != 1 || got.Status != TaskWriting || len(got.Plan.Questions) != 0 {
				t.Fatal("asked a condition settled during comparison", got, calls, err)
			}
			snap, _ = s.Snapshot(testContext)
			if len(snap.Decisions) != 2 {
				t.Fatal("opened another decision", snap.Decisions)
			}
		})
	}
}

func TestSavedQuestionFilteringReturnsToChecker(t *testing.T) {
	s, _, held, question := savedQuestionTask(t)
	if _, err := s.UpdateTask(testContext, held.ID, func(t *Task, _ *Project) (string, error) {
		t.Research = []ResearchRequest{{ID: "research", From: "Reviewer", Revision: 1}}
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
	s.SetPrerequisiteComparer(func(context.Context, string, bool, []Prerequisite) (PrerequisiteComparison, error) {
		return PrerequisiteComparison{Result: "equivalent", Blocker: held.Blockers[0].ID, Details: "same release and tag"}, nil
	})
	got, err := s.AskPlanQuestions(testContext, held.ID)
	if err != nil || got.Status != TaskReviewing || got.OpenResearch() != nil || len(got.Plan.Questions) != 0 || got.Research[0].Researcher != "Researcher" {
		t.Fatal(got, err)
	}
	snap, _ := s.Snapshot(testContext)
	if len(snap.Decisions) != 1 {
		t.Fatal(snap.Decisions)
	}
	found := false
	for _, a := range snap.Activity {
		if a.Kind == "prerequisite.reused" && strings.Contains(a.Summary, question) {
			found = true
		}
	}
	if !found {
		t.Fatal("missing reuse activity")
	}
}
