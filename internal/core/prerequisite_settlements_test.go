package core

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
)

func researchAgain(t *testing.T, s *Service, id string) {
	t.Helper()
	_, err := s.UpdateTask(testContext, id, func(t *Task, _ *Project) (string, error) { t.Status = TaskResearching; return "", nil })
	if err != nil {
		t.Fatal(err)
	}
}

func TestMaterialWhitespaceIsNotDeterministicEquivalence(t *testing.T) {
	for _, settled := range []bool{false, true} {
		for _, quoted := range []bool{false, true} {
			t.Run(fmt.Sprintf("settled=%t/quoted=%t", settled, quoted), func(t *testing.T) {
				s, _ := fixture(t)
				p := plannedProject(t, s)
				own, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Diagnostics"})
				original, changed := "Access to /tmp/release  files", "Access to /tmp/release files"
				if quoted {
					original, changed = "Access to '/tmp/diagnostic  output'", "Access to '/tmp/diagnostic output'"
				}
				researchAgain(t, s, own.ID)
				if !settled {
					got, err := s.RecordPlan(testContext, own.ID, Plan{Summary: "Build"}, nil, []string{original, changed})
					if err != nil || len(got.Blockers) != 2 {
						t.Fatal(got, err)
					}
					return
				}
				got, err := s.RecordPlan(testContext, own.ID, Plan{Summary: "Build"}, nil, []string{original})
				if err != nil {
					t.Fatal(err)
				}
				snap, _ := s.Snapshot(testContext)
				if _, err := s.ChooseDecision(testContext, snap.Decisions[0].ID, ChoiceReadyPrerequisite, FromOwner); err != nil {
					t.Fatal(err)
				}
				calls := 0
				s.SetPrerequisiteComparer(func(_ context.Context, in string, question bool, _ []Prerequisite) (PrerequisiteComparison, error) {
					calls++
					if (!question && in != changed) || (question && in != "Is "+changed+" ready?") {
						t.Fatal("condition was normalized before comparison", in)
					}
					return PrerequisiteComparison{Result: "different"}, nil
				})
				researchAgain(t, s, own.ID)
				got, err = s.RecordPlan(testContext, own.ID, Plan{Summary: "Build", Questions: []string{"Is " + changed + " ready?"}}, nil, []string{changed})
				if err != nil || calls != 2 || len(got.Blockers) != 2 || got.Blockers[0].Description != original || got.Blockers[0].Outcome != "confirmed" {
					t.Fatal(got, calls, err)
				}
				snap, _ = s.Snapshot(testContext)
				if len(snap.Decisions) != 2 {
					t.Fatal(snap.Decisions)
				}
			})
		}
	}
}

func TestDelayedAssistantAnswerCannotReopenNewerSettlement(t *testing.T) {
	for _, choice := range []string{ChoiceReadyPrerequisite, ChoiceDropPrerequisite} {
		for _, restart := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/restart=%t", choice, restart), func(t *testing.T) {
				s, p, held, d := prerequisiteTask(t)
				if _, err := s.ChooseDecision(testContext, d.ID, choice, FromOwner); err != nil {
					t.Fatal(err)
				}
				b := taskByID(t, s, held.ID).Blockers[0]
				q, err := s.OpenTaskDecision(testContext, held.ID, DecisionQuestion, DecisionInput{Title: "Which format?", Context: "Choose the report format", Recommendation: "JSON", Choices: []string{"JSON", "CSV"}})
				if err != nil {
					t.Fatal(err)
				}
				answer := "Prerequisite " + b.ID + " no longer holds: " + b.Description
				queued, err := s.EnqueueChat(testContext, "delayed-owner-instruction", answer)
				if err != nil || queued.PrerequisiteInstruction == nil || queued.PrerequisiteInstruction.Settlement != PrerequisiteSettlementID(b) {
					t.Fatal(queued, err)
				}
				turn, err := s.StartNextChat(testContext, "codex")
				if err != nil {
					t.Fatal(err)
				}
				ctx := WithOwnerInstruction(testContext, turn.ID)
				if _, err := s.ReopenPrerequisite(testContext, p.ID, held.ID, b.ID, b.Description, "Owner revoked access", LinkedByOwner, PrerequisiteReopen{Source: "newer-owner-action", Settlement: PrerequisiteSettlementID(b)}); err != nil {
					t.Fatal(err)
				}
				snap, _ := s.Snapshot(testContext)
				for _, pending := range snap.Decisions {
					if pending.Kind == DecisionPrerequisite && pending.Status == DecisionOpen {
						if _, err := s.ChooseDecision(testContext, pending.ID, choice, FromOwner); err != nil {
							t.Fatal(err)
						}
					}
				}
				if restart {
					path := filepath.Join(s.StateDirectory(), "state.db")
					s.store.Close()
					st, err := Open(path)
					if err != nil {
						t.Fatal(err)
					}
					defer st.Close()
					s = NewService(st, config.Default())
				}
				before, _ := s.Snapshot(testContext)
				if _, err := s.AnswerDecision(ctx, q.ID, answer, FromAssistant); !errors.Is(err, ErrConflict) {
					t.Fatal("delayed custom answer reopened newer settlement", err)
				}
				current := taskByID(t, s, held.ID).Blockers[0]
				// Supplying a newer model-selected fence to the dedicated action
				// cannot upgrade the authority of the same stale owner message.
				if _, err := s.ReopenPrerequisite(ctx, p.ID, held.ID, b.ID, b.Description, answer, LinkedByAssistant, PrerequisiteReopen{Source: OwnerInstructionSource(ctx), Settlement: PrerequisiteSettlementID(current)}); !errors.Is(err, ErrConflict) {
					t.Fatal("dedicated action upgraded stale owner instruction", err)
				}
				after, _ := s.Snapshot(testContext)
				if !reflect.DeepEqual(before.Tasks, after.Tasks) || !reflect.DeepEqual(before.Decisions, after.Decisions) || !reflect.DeepEqual(before.Activity, after.Activity) {
					t.Fatal("stale answer did not roll back atomically")
				}
				if err := s.FinishChat(testContext, turn.ID, "interrupted", "", "Superseded"); err != nil {
					t.Fatal(err)
				}
				// A new explicit instruction observes the new settlement and can
				// use the existing decision-answer route without losing its wait.
				s.EnqueueChat(testContext, "fresh-owner-instruction", answer)
				fresh, err := s.StartNextChat(testContext, "codex")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.AnswerDecision(WithOwnerInstruction(testContext, fresh.ID), q.ID, answer, FromAssistant); err != nil {
					t.Fatal(err)
				}
				current = taskByID(t, s, held.ID).Blockers[0]
				if current.ClearedAt != nil || len(current.Settlements) != 4 || current.Settlements[3].Source != "chat:"+fresh.ID {
					t.Fatal(current)
				}
			})
		}
	}
}

func TestQueuedOwnerInstructionEditsPinCurrentSettlement(t *testing.T) {
	s, _, held, d := prerequisiteTask(t)
	if _, err := s.ChooseDecision(testContext, d.ID, ChoiceReadyPrerequisite, FromOwner); err != nil {
		t.Fatal(err)
	}
	b := taskByID(t, s, held.ID).Blockers[0]
	turn, err := s.EnqueueChat(testContext, "editable-owner-instruction", "Maybe the condition no longer holds")
	if err != nil || turn.PrerequisiteInstruction != nil {
		t.Fatal(turn, err)
	}
	answer := "Prerequisite " + b.ID + " no longer holds: " + b.Description
	turn, err = s.EditChatMessage(testContext, turn.ID, answer, turn.Revision)
	if err != nil || turn.PrerequisiteInstruction == nil || turn.PrerequisiteInstruction.Settlement != PrerequisiteSettlementID(b) {
		t.Fatal(turn, err)
	}
	turn, err = s.EditChatMessage(testContext, turn.ID, "Tell me about it instead", turn.Revision)
	if err != nil || turn.PrerequisiteInstruction != nil {
		t.Fatal(turn, err)
	}
}

func TestSettledPrerequisiteReconciliation(t *testing.T) {
	for _, choice := range []string{ChoiceReadyPrerequisite, ChoiceDropPrerequisite} {
		t.Run(choice, func(t *testing.T) {
			s, _, held, d := prerequisiteTask(t)
			if _, err := s.ChooseDecision(testContext, d.ID, choice, FromOwner); err != nil {
				t.Fatal(err)
			}
			canonical := taskByID(t, s, held.ID).Blockers[0]
			s.SetPrerequisiteComparer(func(_ context.Context, what string, question bool, records []Prerequisite) (PrerequisiteComparison, error) {
				if len(records) != 1 || records[0].Blocker != canonical.ID || records[0].Settlements[0].Answer != choice {
					t.Fatal(records)
				}
				if what == " LIB  v0.20.0 TAGGED " || what == "The v0.20.0 release of lib has a tag" || what == "Confirmed: lib v0.20.0 tagged. Do not request confirmation again." || what == "Can you confirm the v0.20.0 release of lib has a tag?" {
					return PrerequisiteComparison{"equivalent", canonical.ID, "same library, v0.20.0 and tag requirement"}, nil
				}
				return PrerequisiteComparison{Result: "different"}, nil
			})
			for _, what := range []string{" LIB  v0.20.0 TAGGED ", canonical.ID, "prerequisite " + canonical.ID, "The v0.20.0 release of lib has a tag", "Confirmed: lib v0.20.0 tagged. Do not request confirmation again."} {
				researchAgain(t, s, held.ID)
				got, err := s.RecordPlan(testContext, held.ID, Plan{Summary: "Build", Questions: []string{"Can you confirm the v0.20.0 release of lib has a tag?"}}, nil, []string{what})
				snap, _ := s.Snapshot(testContext)
				if err != nil || got.Status != TaskWriting || len(got.Plan.Questions) != 0 || len(got.Blockers) != 1 || len(snap.Decisions) != 1 {
					t.Fatalf("%+v %v", got, err)
				}
				b := got.Blockers[0]
				if b.ID != canonical.ID || b.Description != canonical.Description || b.Outcome != canonical.Outcome || !reflect.DeepEqual(b.Settlements, canonical.Settlements) {
					t.Fatal(b)
				}
				found := false
				for _, a := range snap.Activity {
					if a.Kind == "prerequisite.reused" && a.TaskID == held.ID && strings.Contains(a.Summary, what) && strings.Contains(a.Summary, b.ID) && strings.Contains(a.Summary, b.Outcome) {
						found = true
					}
				}
				if !found {
					t.Fatal("missing reuse activity")
				}
			}
			// Recorded aliases work even without inference; omission does not reopen.
			s.SetPrerequisiteComparer(nil)
			researchAgain(t, s, held.ID)
			got, err := s.RecordPlan(testContext, held.ID, Plan{Summary: "Unrelated rewrite"}, nil, []string{"The v0.20.0 release of lib has a tag"})
			if err != nil || got.Status != TaskWriting {
				t.Fatal(got, err)
			}
			researchAgain(t, s, held.ID)
			got, err = s.RecordPlan(testContext, held.ID, Plan{Summary: "Omit condition"}, nil, nil)
			if err != nil || got.Blockers[0].Outcome != canonical.Outcome {
				t.Fatal(got, err)
			}
		})
	}
}

func TestSettlementsRejectChangedConditionsAndForeignIdentities(t *testing.T) {
	for _, what := range []string{"lib v0.21.0 tagged", "lib v0.20.0 tagged in another repository", "lib v0.20.0 tagged with merge permission", "lib v0.20.0 tagged before tomorrow", "lib v0.20.0 tagged and signed", "unknown identity"} {
		t.Run(what, func(t *testing.T) {
			s, _, held, d := prerequisiteTask(t)
			s.ChooseDecision(testContext, d.ID, ChoiceReadyPrerequisite, FromOwner)
			s.SetPrerequisiteComparer(func(_ context.Context, in string, _ bool, _ []Prerequisite) (PrerequisiteComparison, error) {
				return PrerequisiteComparison{Result: "different", Details: "material requirements differ"}, nil
			})
			researchAgain(t, s, held.ID)
			got, err := s.RecordPlan(testContext, held.ID, Plan{Summary: "Build"}, nil, []string{what, held.Blockers[0].ID + ": " + what})
			snap, _ := s.Snapshot(testContext)
			if err != nil || got.Status != TaskQueued || len(got.Blockers) != 3 || len(snap.Decisions) != 3 || got.Blockers[0].Outcome != "confirmed" {
				t.Fatal(got, err)
			}
		})
	}
	s, p, held, d := prerequisiteTask(t)
	s.ChooseDecision(testContext, d.ID, ChoiceReadyPrerequisite, FromOwner)
	for _, project := range []Project{p, newProject(t, s)} {
		own, _ := s.QueueTask(testContext, project.ID, TaskInput{Objective: "Other task"})
		researchAgain(t, s, own.ID)
		got, err := s.RecordPlan(testContext, own.ID, Plan{Summary: "Build"}, nil, []string{held.Blockers[0].Description, held.Blockers[0].ID})
		if err != nil || got.Status != TaskQueued || len(got.Blockers) != 2 {
			t.Fatal(got, err)
		}
	}
}

func TestPrerequisiteReopeningKeepsHistoryAndAuthority(t *testing.T) {
	for _, choice := range []string{ChoiceReadyPrerequisite, ChoiceDropPrerequisite} {
		for _, method := range []string{"action", "answer"} {
			t.Run(choice+"/"+method, func(t *testing.T) {
				s, p, held, d := prerequisiteTask(t)
				s.ChooseDecision(testContext, d.ID, choice, FromOwner)
				b := taskByID(t, s, held.ID).Blockers[0]
				if _, err := reopenTestPrerequisite(t, s, testContext, p.ID, held.ID, b.ID, b.Description, "Reopen it", LinkedByPM); !errors.Is(err, ErrConflict) {
					t.Fatal(err)
				}
				if _, err := reopenTestPrerequisite(t, s, testContext, p.ID, held.ID, b.ID, "another condition", "Reopen it", LinkedByOwner); !errors.Is(err, ErrConflict) {
					t.Fatal(err)
				}
				for _, answer := range []string{"Maybe it is no longer ready", "Confirmed: no need to ask", "Do not wait"} {
					q, _ := s.OpenTaskDecision(testContext, held.ID, DecisionQuestion, DecisionInput{Title: "Another question", Context: "Unrelated", Recommendation: "Yes", Choices: []string{"Yes", "No"}})
					s.AnswerDecision(testContext, q.ID, answer, FromOwner)
					if taskByID(t, s, held.ID).Blockers[0].ClearedAt == nil {
						t.Fatal("ambiguous answer reopened")
					}
				}
				answer := "Prerequisite " + b.ID + " no longer holds: " + b.Description
				if method == "action" {
					if _, err := reopenTestPrerequisite(t, s, testContext, p.ID, held.ID, b.ID, b.Description, answer, LinkedByOwner); err != nil {
						t.Fatal(err)
					}
				} else {
					q, _ := s.OpenTaskDecision(testContext, held.ID, DecisionQuestion, DecisionInput{Title: "Which?", Context: "Unrelated", Recommendation: "Yes", Choices: []string{"Yes", "No"}})
					if _, err := s.AnswerDecision(testContext, q.ID, answer, FromOwner); err != nil {
						t.Fatal(err)
					}
				}
				got := taskByID(t, s, held.ID)
				if !holdsStart(got) || got.Blockers[0].Outcome != "" || len(got.Blockers[0].Settlements) != 2 || got.Blockers[0].Settlements[0].Answer != choice || got.Blockers[0].Settlements[1].Answer != answer {
					t.Fatal(got.Blockers)
				}
				reopenTestPrerequisite(t, s, testContext, p.ID, held.ID, b.ID, b.Description, answer, LinkedByOwner)
				snap, _ := s.Snapshot(testContext)
				count := 0
				for _, d := range snap.Decisions {
					if d.Kind == DecisionPrerequisite && d.Status == DecisionOpen {
						count++
					}
				}
				if count != 1 || len(taskByID(t, s, held.ID).Blockers[0].Settlements) != 2 {
					t.Fatal("duplicate reopening")
				}
			})
		}
	}
}

func TestSettlementPersistenceAndLegacyAnswers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewService(st, config.Default())
	p := plannedProject(t, s)
	held, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Publish diagnostics"})
	researchAgain(t, s, held.ID)
	s.RecordPlan(testContext, held.ID, Plan{Summary: "Build"}, nil, []string{"Someone can push a diagnostics PR to repository example"})
	snap, _ := s.Snapshot(testContext)
	d := snap.Decisions[0]
	s.ChooseDecision(testContext, d.ID, ChoiceReadyPrerequisite, FromOwner)
	restart := func() {
		t.Helper()
		st.Close()
		st, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		s = NewService(st, config.Default())
	}
	defer func() { st.Close() }()
	restart()
	b := taskByID(t, s, held.ID).Blockers[0]
	if b.Settlements[0].Answer != ChoiceReadyPrerequisite {
		t.Fatal(b)
	}
	q, _ := s.OpenTaskDecision(testContext, held.ID, DecisionQuestion, DecisionInput{Title: "Which colour?", Context: "Colour", Recommendation: "Blue", Choices: []string{"Blue", "Red"}})
	s.AnswerDecision(testContext, q.ID, "Use blue", FromOwner)
	restart()
	researchAgain(t, s, held.ID)
	s.SetPrerequisiteComparer(func(context.Context, string, bool, []Prerequisite) (PrerequisiteComparison, error) {
		return PrerequisiteComparison{"equivalent", b.ID, "same repository and scoped push capability"}, nil
	})
	alias := "A collaborator can publish the diagnostic PR to repository example"
	got, err := s.RecordPlan(testContext, held.ID, Plan{Summary: "Use blue", Questions: []string{"Can a collaborator publish the diagnostic PR to repository example?"}}, nil, []string{alias})
	if err != nil || got.Status != TaskWriting {
		t.Fatal(got, err)
	}
	restart()
	got = taskByID(t, s, held.ID)
	if len(got.Plan.Questions) != 0 || len(got.Blockers[0].Aliases) != 1 || got.Blockers[0].Aliases[0] != alias || got.Plan.Prerequisites[0].Settlements[0].Source != d.ID {
		t.Fatal(got.Plan)
	}
	researchAgain(t, s, held.ID)
	got, err = s.RecordPlan(testContext, held.ID, Plan{Summary: "Go again"}, nil, []string{alias})
	if err != nil || got.Status != TaskWriting {
		t.Fatal(got, err)
	}
	reopenTestPrerequisite(t, s, testContext, p.ID, held.ID, b.ID, b.Description, "Access was revoked", LinkedByOwner)
	restart()
	got = taskByID(t, s, held.ID)
	if !holdsStart(got) || len(got.Blockers[0].Settlements) != 2 || len(got.Blockers[0].Aliases) == 0 {
		t.Fatal(got)
	}
	researchAgain(t, s, held.ID)
	got, err = s.RecordPlan(testContext, held.ID, Plan{Summary: "Wait again"}, nil, []string{alias})
	if err != nil || got.Status != TaskQueued || len(got.Blockers) != 1 {
		t.Fatal(got, err)
	}
	// Legacy CA-58 settlement has an ordinary decision, no durable history yet.
	s.store.update(testContext, func(v *Snapshot) error {
		task(v, held.ID).Blockers[0] = b
		task(v, held.ID).Blockers[0].Settlements = nil
		return nil
	})
	snap, _ = s.Snapshot(testContext)
	got = taskByID(t, s, held.ID)
	pre := TaskPrerequisites(snap, got)
	if len(pre[0].Settlements) != 1 || pre[0].Settlements[0].Answer != ChoiceReadyPrerequisite {
		t.Fatal(pre)
	}
	// A historical task-page clear has no retained answer. Reopening must
	// preserve its known outcome without inventing the owner's words.
	if err := s.store.update(testContext, func(v *Snapshot) error {
		v.Decisions = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	snap, _ = s.Snapshot(testContext)
	pre = TaskPrerequisites(snap, taskByID(t, s, held.ID))
	if len(pre[0].Settlements) != 1 || pre[0].Settlements[0].Answer != "" || pre[0].Settlements[0].Outcome != "confirmed" {
		t.Fatal(pre)
	}
	got, err = reopenTestPrerequisite(t, s, testContext, p.ID, held.ID, b.ID, b.Description, "Owner revoked access", LinkedByOwner)
	if err != nil || len(got.Blockers[0].Settlements) != 2 || got.Blockers[0].Settlements[0].Outcome != "confirmed" || got.Blockers[0].Settlements[0].Answer != "" {
		t.Fatal(got, err)
	}
}

func TestPrerequisiteComparisonFailuresAndStaleState(t *testing.T) {
	for _, failure := range []string{"timeout", "malformed", "foreign match", "cancel", "stopped"} {
		t.Run(failure, func(t *testing.T) {
			s, _, held, d := prerequisiteTask(t)
			s.ChooseDecision(testContext, d.ID, ChoiceReadyPrerequisite, FromOwner)
			researchAgain(t, s, held.ID)
			before, _ := s.Snapshot(testContext)
			ctx, cancel := context.WithCancel(testContext)
			defer cancel()
			s.SetPrerequisiteComparer(func(_ context.Context, _ string, _ bool, _ []Prerequisite) (PrerequisiteComparison, error) {
				switch failure {
				case "timeout":
					return PrerequisiteComparison{}, context.DeadlineExceeded
				case "malformed":
					return PrerequisiteComparison{Result: "wrong"}, nil
				case "foreign match":
					return PrerequisiteComparison{"equivalent", "foreign", "same"}, nil
				case "cancel":
					cancel()
				case "stopped":
					s.UpdateTask(testContext, held.ID, func(t *Task, _ *Project) (string, error) { t.Status = TaskStopped; return "", nil })
				}
				return PrerequisiteComparison{"equivalent", held.Blockers[0].ID, "same condition"}, nil
			})
			_, err := s.RecordPlan(ctx, held.ID, Plan{Summary: "Build"}, nil, []string{"A reworded tag requirement"})
			if err == nil {
				t.Fatal("failure committed")
			}
			after, _ := s.Snapshot(testContext)
			if !reflect.DeepEqual(before.Tasks[0].Blockers, after.Tasks[0].Blockers) || len(after.Decisions) != len(before.Decisions) {
				t.Fatal("partial match committed")
			}
			for _, a := range after.Activity {
				if a.Kind == "prerequisite.reused" {
					t.Fatal("failure recorded reuse")
				}
			}
		})
	}
	// Reopening during inference invalidates the entire comparison.
	s, p, held, d := prerequisiteTask(t)
	s.ChooseDecision(testContext, d.ID, ChoiceDropPrerequisite, FromOwner)
	researchAgain(t, s, held.ID)
	calls := 0
	s.SetPrerequisiteComparer(func(_ context.Context, _ string, _ bool, _ []Prerequisite) (PrerequisiteComparison, error) {
		calls++
		reopenTestPrerequisite(t, s, testContext, p.ID, held.ID, held.Blockers[0].ID, held.Blockers[0].Description, "Tag withdrawn", LinkedByOwner)
		return PrerequisiteComparison{"equivalent", held.Blockers[0].ID, "same tag"}, nil
	})
	got, err := s.RecordPlan(testContext, held.ID, Plan{Summary: "Build"}, nil, []string{"A reworded tag requirement"})
	if !errors.Is(err, ErrConflict) || calls != 1 || taskByID(t, s, held.ID).Status != TaskQueued || taskByID(t, s, held.ID).Blockers[0].Outcome != "" {
		t.Fatal(got, calls, err)
	}
	snap, _ := s.Snapshot(testContext)
	for _, a := range snap.Activity {
		if a.Kind == "prerequisite.reused" {
			t.Fatal("stale match committed")
		}
	}
}

func TestPrerequisiteConcurrentPlanAndSettlement(t *testing.T) {
	s, _, held, d := prerequisiteTask(t)
	researchAgain(t, s, held.ID)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); s.ChooseDecision(testContext, d.ID, ChoiceReadyPrerequisite, FromOwner) }()
	go func() {
		defer wg.Done()
		s.RecordPlan(testContext, held.ID, Plan{Summary: "Build"}, nil, []string{held.Blockers[0].Description})
	}()
	wg.Wait()
	got := taskByID(t, s, held.ID)
	if len(got.Blockers) != 1 || len(got.Blockers[0].Settlements) != 1 {
		t.Fatal(got)
	}
	snap, _ := s.Snapshot(testContext)
	if len(snap.Decisions) != 1 || snap.Decisions[0].Status != DecisionResolved {
		t.Fatal(snap.Decisions)
	}
}

func TestPrerequisiteSQLFailureRollsBackMatchAndReopening(t *testing.T) {
	s, p, held, d := prerequisiteTask(t)
	s.ChooseDecision(testContext, d.ID, ChoiceReadyPrerequisite, FromOwner)
	researchAgain(t, s, held.ID)
	before, _ := s.Snapshot(testContext)
	// Store persists the snapshot in state; rejecting that write must also
	// discard activity and aliases produced by reconciliation.
	_, err := s.store.db.Exec("CREATE TRIGGER fail_state BEFORE UPDATE ON state BEGIN SELECT RAISE(ABORT, 'injected'); END")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.RecordPlan(testContext, held.ID, Plan{Summary: "Build"}, nil, []string{held.Blockers[0].ID})
	if err == nil {
		t.Fatal("write accepted")
	}
	_, err = reopenTestPrerequisite(t, s, testContext, p.ID, held.ID, held.Blockers[0].ID, held.Blockers[0].Description, "Reopen", LinkedByOwner)
	if err == nil {
		t.Fatal("reopen accepted")
	}
	after, _ := s.Snapshot(testContext)
	if !reflect.DeepEqual(before.Tasks, after.Tasks) || !reflect.DeepEqual(before.Activity, after.Activity) || !reflect.DeepEqual(before.Decisions, after.Decisions) {
		t.Fatal("partial write")
	}
}

func TestPrerequisiteMixedQuestionsUncertaintyAndDuplicatePlans(t *testing.T) {
	s, _, held, d := prerequisiteTask(t)
	s.ChooseDecision(testContext, d.ID, ChoiceReadyPrerequisite, FromOwner)
	researchAgain(t, s, held.ID)
	s.SetPrerequisiteComparer(func(_ context.Context, in string, question bool, records []Prerequisite) (PrerequisiteComparison, error) {
		if in == "Is lib v0.20.0 tagged ready and can it merge PRs?" {
			return PrerequisiteComparison{Result: "different", Details: "additional merge capability"}, nil
		}
		return PrerequisiteComparison{Result: "uncertain", Details: "missing material details"}, nil
	})
	got, err := s.RecordPlan(testContext, held.ID, Plan{Summary: "Build", Questions: []string{"Is lib v0.20.0 tagged ready?", "Is lib v0.20.0 tagged ready and can it merge PRs?", "Which format?"}}, nil, nil)
	if err != nil || len(got.Plan.Questions) != 2 || got.Blockers[0].Outcome != "confirmed" {
		t.Fatal(got, err)
	}
	researchAgain(t, s, held.ID)
	got, err = s.RecordPlan(testContext, held.ID, Plan{Summary: "Build"}, nil, []string{"Release available"})
	if err != nil || len(got.Blockers) != 2 || got.Blockers[0].Outcome != "confirmed" || got.Blockers[1].Outcome != "" {
		t.Fatal(got, err)
	}
	// Two simultaneous proposals of the same newly changed requirement produce
	// just one blocker and decision, even if both read the prior state.
	researchAgain(t, s, held.ID)
	var wg sync.WaitGroup
	wg.Add(2)
	for range 2 {
		go func() {
			defer wg.Done()
			s.RecordPlan(testContext, held.ID, Plan{Summary: "Build"}, nil, []string{"lib v0.21.0 tagged"})
		}()
	}
	wg.Wait()
	got = taskByID(t, s, held.ID)
	snap, _ := s.Snapshot(testContext)
	if len(got.Blockers) != 3 || len(snap.Decisions) != 3 {
		t.Fatal(got.Blockers, snap.Decisions)
	}
}

func TestReopeningBegunTaskPreservesDraftAndTeamCannotInvalidate(t *testing.T) {
	s, p, held, d := prerequisiteTask(t)
	s.ChooseDecision(testContext, d.ID, ChoiceReadyPrerequisite, FromOwner)
	s.UpdateTask(testContext, held.ID, func(t *Task, _ *Project) (string, error) {
		t.Status = TaskWriting
		t.Revisions = []Revision{{N: 1}}
		t.Branch = "draft"
		return "", nil
	})
	b := taskByID(t, s, held.ID).Blockers[0]
	answer := "Prerequisite " + b.ID + " no longer holds: " + b.Description
	q, err := s.OpenTaskDecision(testContext, held.ID, DecisionQuestion, DecisionInput{Title: "Question", Context: "Context", Recommendation: "Yes", Choices: []string{"Yes", "No"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AnswerDecision(testContext, q.ID, answer, LinkedByPM); err != nil {
		t.Fatal(err)
	}
	if taskByID(t, s, held.ID).Blockers[0].ClearedAt == nil {
		t.Fatal("team answer reopened")
	}
	s.UpdateTask(testContext, held.ID, func(t *Task, _ *Project) (string, error) { t.Status = TaskWriting; return "", nil })
	got, err := reopenTestPrerequisite(t, s, testContext, p.ID, held.ID, b.ID, b.Description, answer, LinkedByOwner)
	if err != nil || got.Status != TaskWriting || got.Branch != "draft" || len(got.Revisions) != 1 || !holdsLanding(got) {
		t.Fatal(got, err)
	}
	researchAgain(t, s, held.ID)
	got, err = s.RecordPlan(testContext, held.ID, Plan{Summary: "Continue"}, nil, []string{"Entirely new condition"})
	if err != nil || len(got.Blockers) != 1 || got.Status != TaskWriting {
		t.Fatal(got, err)
	}
}

func TestPrerequisiteFencedTurnAndEvaluationWriteRollback(t *testing.T) {
	s, p, held, d := prerequisiteTask(t)
	// A failed resolution rolls back the history, blocker, answer and activity.
	_, err := s.store.db.Exec("CREATE TRIGGER reject_eval BEFORE INSERT ON decision_evaluations BEGIN SELECT RAISE(ABORT, 'injected'); END")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ChooseDecision(testContext, d.ID, ChoiceReadyPrerequisite, FromOwner); err == nil {
		t.Fatal("resolution committed")
	}
	got := taskByID(t, s, held.ID)
	if got.Blockers[0].ClearedAt != nil || len(got.Blockers[0].Settlements) != 0 {
		t.Fatal(got.Blockers)
	}
	s.store.db.Exec("DROP TRIGGER reject_eval")
	s.ChooseDecision(testContext, d.ID, ChoiceReadyPrerequisite, FromOwner)
	researchAgain(t, s, held.ID)
	ctx := Fenced(testContext, held.ID, "superseded")
	if _, err = s.RecordPlan(ctx, held.ID, Plan{Summary: "Build"}, nil, []string{held.Blockers[0].ID}); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
	got = taskByID(t, s, held.ID)
	if len(got.Blockers[0].Aliases) != 0 {
		t.Fatal("stale alias")
	}
	// Only ordinary resolutions are offered to inference, not archive-only rows.
	_, err = s.store.db.Exec("INSERT INTO decision_evaluations(decision_id,resolved_at,payload) VALUES(?,?,?)", "archive-only", "2026-10-04", `{"answer":"EVALUATION_ONLY"}`)
	if err != nil {
		t.Fatal(err)
	}
	s.SetPrerequisiteComparer(func(_ context.Context, _ string, _ bool, records []Prerequisite) (PrerequisiteComparison, error) {
		for _, pre := range records {
			for _, settled := range pre.Settlements {
				if strings.Contains(settled.Answer, "EVALUATION_ONLY") {
					t.Fatal("archive entered comparison")
				}
			}
		}
		return PrerequisiteComparison{"equivalent", held.Blockers[0].ID, "same tag and version"}, nil
	})
	if _, err = s.RecordPlan(testContext, held.ID, Plan{Summary: "Build"}, nil, []string{"The library version has been tagged"}); err != nil {
		t.Fatal(err)
	}
	// A task-level clear preserves the exact reason without inventing a decision.
	reopenTestPrerequisite(t, s, testContext, p.ID, held.ID, held.Blockers[0].ID, held.Blockers[0].Description, "Wait again", LinkedByOwner)
	got, err = s.ClearBlocker(testContext, p.ID, held.ID, held.Blockers[0].ID, LinkedByOwner, "I checked the exact tag")
	if err != nil || got.Blockers[0].Settlements[2].Answer != "I checked the exact tag" {
		t.Fatal(got, err)
	}
}

func reopenTestPrerequisite(t *testing.T, s *Service, ctx context.Context, projectID, taskID, id, condition, answer, by string) (Task, error) {
	t.Helper()
	current := taskByID(t, s, taskID)
	var generation string
	for _, b := range current.Blockers {
		if b.ID == id {
			generation = PrerequisiteSettlementID(b)
			if generation == "" {
				for i := len(b.Settlements) - 1; i >= 0; i-- {
					if b.Settlements[i].Outcome != "reopened" {
						generation = b.Settlements[i].Source
						break
					}
				}
			}
		}
	}
	return s.ReopenPrerequisite(ctx, projectID, taskID, id, condition, answer, by, PrerequisiteReopen{Source: "test-action:" + id + ":" + answer, Settlement: generation})
}

func TestCompletePrerequisiteConditionsAndMaterialCase(t *testing.T) {
	prefix := strings.Repeat("The diagnostic capability must cover the complete synthetic scope. ", 7)
	for _, pair := range [][2]string{
		{prefix + "version v1.0.0 tagged", prefix + "version v2.0.0 tagged"},
		{prefix + "push PRs to example/one", prefix + "merge PRs to example/one"},
		{prefix + "repository example/one", prefix + "repository example/two"},
		{"lib v1.0.0-RC1 tagged", "lib v1.0.0-rc1 tagged"},
		{"Read access to /tmp/Scope is available", "Read access to /tmp/scope is available"},
	} {
		t.Run(pair[1], func(t *testing.T) {
			s, _ := fixture(t)
			p := plannedProject(t, s)
			own, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Build"})
			researchAgain(t, s, own.ID)
			got, err := s.RecordPlan(testContext, own.ID, Plan{Summary: "Build"}, nil, []string{pair[0]})
			if err != nil || len(got.Blockers) != 1 || got.Blockers[0].Description != pair[0] {
				t.Fatal(got, err)
			}
			snap, _ := s.Snapshot(testContext)
			s.ChooseDecision(testContext, snap.Decisions[0].ID, ChoiceReadyPrerequisite, FromOwner)
			calls := 0
			s.SetPrerequisiteComparer(func(_ context.Context, in string, _ bool, records []Prerequisite) (PrerequisiteComparison, error) {
				calls++
				if in != pair[1] || records[0].What != pair[0] {
					t.Fatal("truncated comparison", in, records)
				}
				return PrerequisiteComparison{Result: "different", Details: "material details differ"}, nil
			})
			researchAgain(t, s, own.ID)
			got, err = s.RecordPlan(testContext, own.ID, Plan{Summary: "Build"}, nil, []string{pair[1]})
			snap, _ = s.Snapshot(testContext)
			if err != nil || calls != 1 || len(got.Blockers) != 2 || got.Blockers[1].Description != pair[1] || len(snap.Decisions) != 2 {
				t.Fatal(got, calls, err)
			}
			researchAgain(t, s, own.ID)
			before, _ := s.Snapshot(testContext)
			if _, err = s.RecordPlan(testContext, own.ID, Plan{Summary: "Build"}, nil, []string{strings.Repeat("x", MaxPrerequisiteBytes+1)}); err == nil {
				t.Fatal("oversized condition accepted")
			}
			after, _ := s.Snapshot(testContext)
			if !reflect.DeepEqual(before.Tasks, after.Tasks) || !reflect.DeepEqual(before.Decisions, after.Decisions) {
				t.Fatal("oversized condition partially committed")
			}
		})
	}
}

func TestBegunReplanIgnoresConditionsButReconcilesQuestions(t *testing.T) {
	s, _, held, d := prerequisiteTask(t)
	s.ChooseDecision(testContext, d.ID, ChoiceReadyPrerequisite, FromOwner)
	s.UpdateTask(testContext, held.ID, func(t *Task, _ *Project) (string, error) {
		t.Status = TaskResearching
		t.Revisions = []Revision{{N: 1}}
		return "", nil
	})
	calls := 0
	s.SetPrerequisiteComparer(func(context.Context, string, bool, []Prerequisite) (PrerequisiteComparison, error) {
		calls++
		return PrerequisiteComparison{}, context.DeadlineExceeded
	})
	got, err := s.RecordPlan(testContext, held.ID, Plan{Summary: "Continue"}, nil, []string{"new prerequisite"})
	if err != nil || calls != 0 || got.Status != TaskWriting || len(got.Blockers) != 1 {
		t.Fatal(got, calls, err)
	}
	researchAgain(t, s, held.ID)
	if _, err = s.RecordPlan(testContext, held.ID, Plan{Summary: "Continue", Questions: []string{"Can you confirm that release?"}}, nil, []string{"new prerequisite"}); !errors.Is(err, ErrPrerequisiteComparison) || calls != 1 {
		t.Fatal(calls, err)
	}
}

func TestOwnerReopeningReplayAndSettlementFenceSurviveRestart(t *testing.T) {
	for _, choice := range []string{ChoiceReadyPrerequisite, ChoiceDropPrerequisite} {
		t.Run(choice, func(t *testing.T) {
			s, p, held, d := prerequisiteTask(t)
			s.ChooseDecision(testContext, d.ID, choice, FromOwner)
			b := taskByID(t, s, held.ID).Blockers[0]
			action := PrerequisiteReopen{Source: "owner-action-original", Settlement: PrerequisiteSettlementID(b)}
			answer := "Access revoked"
			if _, err := s.ReopenPrerequisite(testContext, p.ID, held.ID, b.ID, b.Description, answer, LinkedByOwner, action); err != nil {
				t.Fatal(err)
			}
			snap, _ := s.Snapshot(testContext)
			var pending Decision
			for _, d := range snap.Decisions {
				if d.Kind == DecisionPrerequisite && d.Status == DecisionOpen {
					pending = d
				}
			}
			if _, err := s.ChooseDecision(testContext, pending.ID, ChoiceReadyPrerequisite, FromOwner); err != nil {
				t.Fatal(err)
			}
			before, _ := s.Snapshot(testContext)
			// Close/reopen the database before a delayed duplicate returns.
			path := filepath.Join(s.StateDirectory(), "state.db")
			s.store.Close()
			st, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			s = NewService(st, config.Default())
			if _, err = s.ReopenPrerequisite(testContext, p.ID, held.ID, b.ID, b.Description, answer, LinkedByOwner, action); err != nil {
				t.Fatal(err)
			}
			after, _ := s.Snapshot(testContext)
			if !reflect.DeepEqual(before.Tasks, after.Tasks) || !reflect.DeepEqual(before.Decisions, after.Decisions) || !reflect.DeepEqual(before.Activity, after.Activity) {
				t.Fatal("replayed action changed newer settlement")
			}
			// A previously uncommitted action with an old fence cannot reopen either.
			action.Source = "delayed-other-action"
			if _, err = s.ReopenPrerequisite(testContext, p.ID, held.ID, b.ID, b.Description, answer, LinkedByOwner, action); !errors.Is(err, ErrConflict) {
				t.Fatal(err)
			}
			b = taskByID(t, s, held.ID).Blockers[0]
			action.Settlement = PrerequisiteSettlementID(b)
			var wg sync.WaitGroup
			wg.Add(2)
			for range 2 {
				go func() {
					defer wg.Done()
					s.ReopenPrerequisite(testContext, p.ID, held.ID, b.ID, b.Description, answer, LinkedByOwner, action)
				}()
			}
			wg.Wait()
			got := taskByID(t, s, held.ID)
			if len(got.Blockers[0].Settlements) != 4 {
				t.Fatal(got.Blockers)
			}
		})
	}
}
