package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
)

func evaluations(t *testing.T, s *Service) []DecisionEvaluation {
	t.Helper()
	var b bytes.Buffer
	if err := s.ExportDecisionEvaluations(testContext, &b); err != nil {
		t.Fatal(err)
	}
	var out []DecisionEvaluation
	dec := json.NewDecoder(&b)
	for dec.More() {
		var e DecisionEvaluation
		if err := dec.Decode(&e); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

func TestEveryDecisionKindIsEvaluatedWithAnswerAndAttribution(t *testing.T) {
	for _, kind := range []string{"", "choice", "delivery", "update", "question", "escalation", "failure", "prerequisite", "run-recipe", "pr-flow", "pm-question", DecisionRelease, DecisionReleaseFailed, DecisionUpgradeAvailable} {
		for _, disposition := range []string{DispositionChoice, DispositionCustom} {
			t.Run(kind+"/"+disposition, func(t *testing.T) {
				s, _ := fixture(t)
				d := Decision{ID: "d", ProjectID: "p", TaskID: "t", Kind: kind, Title: "Title", Context: "Context", Choices: []string{"Yes", "No"}, Recommendation: "Yes", Status: DecisionOpen, CreatedAt: s.now()}
				if kind == DecisionUpgradeAvailable {
					d.Choices = []string{ChoiceUpgradeByHand, "Skip v2.0.0"}
					d.Recommendation = ChoiceUpgradeByHand
				}
				if err := s.store.update(testContext, func(v *Snapshot) error {
					if kind == DecisionUpgradeAvailable {
						v.Update.DecisionVersion = "v2.0.0"
					}
					v.Decisions = append(v.Decisions, d)
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				var resolved Decision
				var err error
				by, answer := FromOwner, d.Choices[0]
				if disposition == DispositionChoice {
					resolved, err = s.ChooseDecision(testContext, d.ID, answer, by)
				} else {
					by, answer = FromAssistant, "Custom answer"
					resolved, err = s.AnswerDecision(testContext, d.ID, answer, by)
				}
				if err != nil {
					t.Fatal(err)
				}
				got := evaluations(t, s)
				wantKind := kind
				if kind == "" {
					wantKind = "choice"
				}
				want := DecisionEvaluation{d.ID, d.ProjectID, d.TaskID, wantKind, d.Title, d.Context, d.Choices, d.Recommendation, answer, disposition, by, d.CreatedAt, *resolved.ResolvedAt}
				if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
					t.Fatalf("got %+v want %+v", got, want)
				}
				if _, err = s.AnswerDecision(testContext, d.ID, "Again", FromOwner); !errors.Is(err, ErrConflict) || len(evaluations(t, s)) != 1 {
					t.Fatalf("duplicate: %v", err)
				}
			})
		}
	}
}

func TestEvaluationDismissalRollbackAndDirectAnswer(t *testing.T) {
	s, _ := fixture(t)
	d, _ := s.CreateDecision(testContext, DecisionInput{Title: "Dismiss", Context: "Context", Recommendation: "Yes", Choices: []string{"Yes", "No"}})
	s.DismissDecision(testContext, d.ID, "Obsolete")
	if len(evaluations(t, s)) != 0 {
		t.Fatal("dismissal recorded")
	}
	d, _ = s.CreateDecision(testContext, DecisionInput{Title: "Rollback", Context: "Context", Recommendation: "Yes", Choices: []string{"Yes", "No"}})
	err := s.store.update(testContext, func(v *Snapshot) error {
		x := decision(v, d.ID)
		now := s.now()
		x.Status, x.Answer, x.ResolvedAt = DecisionResolved, "answer", &now
		return errors.New("abort")
	})
	if err == nil || len(evaluations(t, s)) != 0 {
		t.Fatal("rollback recorded")
	}
	snap, _ := s.Snapshot(testContext)
	if decision(&snap, d.ID).Status != DecisionOpen {
		t.Fatal("rollback resolved")
	}
	p := newProject(t, s)
	requested, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Build"})
	d, err = s.OpenTaskDecision(testContext, requested.ID, DecisionQuestion, DecisionInput{Title: "Which?", Context: "Colour", Recommendation: "Blue", Choices: []string{"Blue", "Red"}})
	if err != nil {
		t.Fatal(err)
	}
	err = s.store.update(testContext, func(v *Snapshot) error {
		return direct(v, task(v, requested.ID), &TeamMessage{From: FromAssistant, Text: "Green"}, s.now())
	})
	if err != nil {
		t.Fatal(err)
	}
	got := evaluations(t, s)
	if len(got) != 1 || got[0].AnsweredBy != FromAssistant || got[0].Answer != "Green" {
		t.Fatalf("%+v", got)
	}
}

func TestEvaluationBackfillAndResolvedOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err = st.update(testContext, func(v *Snapshot) error {
		v.Decisions = []Decision{{ID: "later", Status: DecisionResolved, Answer: "Later", ResolvedAt: &now}, {ID: "earlier", Status: DecisionResolved, Answer: "Earlier", ResolvedAt: ptrEarlier(now)}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = st.db.Exec("DROP TABLE decision_evaluations"); err != nil {
		t.Fatal(err)
	}
	st.Close()
	for range 2 {
		st, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		got := evaluations(t, NewService(st, config.Default()))
		if len(got) != 2 || got[0].DecisionID != "earlier" || got[0].AnsweredBy != "" {
			t.Fatalf("%+v", got)
		}
		st.Close()
	}
}

func ptrEarlier(t time.Time) *time.Time { t = t.Add(-time.Hour); return &t }

func TestEvaluationWriteFailureRollsBackResolution(t *testing.T) {
	s, _ := fixture(t)
	d, err := s.CreateDecision(testContext, DecisionInput{Title: "Which", Context: "Context", Recommendation: "Yes", Choices: []string{"Yes", "No"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.store.db.Exec(`CREATE TRIGGER refuse_evaluation BEFORE INSERT ON decision_evaluations BEGIN SELECT RAISE(ABORT, 'test failure'); END;`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ChooseDecision(testContext, d.ID, "Yes", FromOwner); err == nil {
		t.Fatal("resolution accepted without evaluation")
	}
	snap, err := s.Snapshot(testContext)
	if err != nil || decision(&snap, d.ID).Status != DecisionOpen || len(evaluations(t, s)) != 0 {
		t.Fatal("resolution was not rolled back")
	}
}

func TestEvaluationSurvivesDecisionRemovalOutsideSnapshot(t *testing.T) {
	s, _ := fixture(t)
	const marker = "EVALUATION_ONLY_CONTEXT_MARKER"
	d, err := s.CreateDecision(testContext, DecisionInput{Title: "Which", Context: marker, Recommendation: "Yes", Choices: []string{"Yes", "No"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ChooseDecision(testContext, d.ID, "Yes", FromOwner); err != nil {
		t.Fatal(err)
	}
	if err = s.store.update(testContext, func(v *Snapshot) error { v.Decisions = nil; v.Activity = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	snap, err := s.Snapshot(testContext)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(snap)
	if err != nil || bytes.Contains(data, []byte(marker)) {
		t.Fatal("evaluation entered snapshot")
	}
	if got := evaluations(t, s); len(got) != 1 || got[0].Context != marker {
		t.Fatalf("%+v", got)
	}
}

func TestEvaluationOrderingWithinOneSecond(t *testing.T) {
	s, _ := fixture(t)
	now := s.now().Truncate(time.Second)
	later := now.Add(time.Nanosecond)
	err := s.store.update(testContext, func(v *Snapshot) error {
		v.Decisions = []Decision{{ID: "later", Status: DecisionResolved, Answer: "Later", ResolvedAt: &later}, {ID: "earlier", Status: DecisionResolved, Answer: "Earlier", ResolvedAt: &now}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := evaluations(t, s); len(got) != 2 || got[0].DecisionID != "earlier" {
		t.Fatalf("%+v", got)
	}
}

func TestConcurrentDecisionAnswersKeepOneEvaluation(t *testing.T) {
	s, _ := fixture(t)
	d, err := s.CreateDecision(testContext, DecisionInput{Title: "Which", Context: "Context", Recommendation: "Yes", Choices: []string{"Yes", "No"}})
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, answer := range d.Choices {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.ChooseDecision(testContext, d.ID, answer, FromOwner)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 || len(evaluations(t, s)) != 1 {
		t.Fatal("concurrent answers did not settle exactly once")
	}
}

type evaluationWriterFunc func([]byte) (int, error)

func (f evaluationWriterFunc) Write(p []byte) (int, error) { return f(p) }

func TestExportSeesConsistentSnapshotWhileAnotherStoreResolves(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := NewService(st, config.Default())
	first, err := s.CreateDecision(testContext, DecisionInput{Title: "First", Context: "Context", Recommendation: "Yes", Choices: []string{"Yes", "No"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ChooseDecision(testContext, first.ID, "Yes", FromOwner); err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateDecision(testContext, DecisionInput{Title: "Second", Context: "Context", Recommendation: "Yes", Choices: []string{"Yes", "No"}})
	if err != nil {
		t.Fatal(err)
	}
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	another := NewService(other, config.Default())
	var b bytes.Buffer
	wrote := false
	err = s.ExportDecisionEvaluations(testContext, evaluationWriterFunc(func(p []byte) (int, error) {
		if !wrote {
			wrote = true
			if _, err := another.ChooseDecision(testContext, second.ID, "Yes", FromOwner); err != nil {
				return 0, err
			}
		}
		return b.Write(p)
	}))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b.Bytes(), []byte(second.ID)) || len(evaluations(t, s)) != 2 {
		t.Fatalf("inconsistent export: %s", b.String())
	}
}
