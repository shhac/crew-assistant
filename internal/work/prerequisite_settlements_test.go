package work

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
)

func TestDiagnosticsPRSettlementSurvivesUnrelatedAnswerAndRewordedReplan(t *testing.T) {
	for _, choice := range []string{core.ChoiceReadyPrerequisite, core.ChoiceDropPrerequisite} {
		t.Run(choice, func(t *testing.T) {
			condition := "Someone with GitHub access can push a diagnostics PR to example/diagnostics"
			repeat := "Confirmed: someone with GitHub access can push a diagnostics PR to example/diagnostics. Do not request confirmation again."
			question := "Can a collaborator publish the diagnostic pull request to example/diagnostics?"
			lp, runner, p := plannedCode(t, 6,
				fmt.Sprintf(`{"summary":"Build diagnostics","prerequisites":[%q]}`, condition),
				`{"summary":"Build diagnostics","questions":["Which diagnostic format?"]}`,
				fmt.Sprintf(`{"summary":"Build diagnostics as JSON","prerequisites":[%q],"questions":[%q]}`, repeat, question))
			ctx := context.Background()
			own, err := lp.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Build diagnostics"})
			if err != nil {
				t.Fatal(err)
			}
			held := taskNow(t, lp, own.ID)
			snap, _ := lp.Core.Snapshot(ctx)
			var prerequisite core.Decision
			for _, d := range snap.Decisions {
				if d.Kind == core.DecisionPrerequisite {
					prerequisite = d
				}
			}
			if prerequisite.ID == "" || runner.edits != 0 {
				t.Fatal("missing initial prerequisite")
			}
			if _, err = lp.Core.ChooseDecision(ctx, prerequisite.ID, choice, core.FromOwner); err != nil {
				t.Fatal(err)
			}
			lp.Core.SetPrerequisiteComparer(func(_ context.Context, in string, isQuestion bool, settled []core.Prerequisite) (core.PrerequisiteComparison, error) {
				if len(settled) != 1 || settled[0].Blocker != held.Blockers[0].ID || settled[0].Settlements[0].Answer != choice {
					t.Fatal(settled)
				}
				if in == repeat || in == question {
					return core.PrerequisiteComparison{Result: "equivalent", Blocker: settled[0].Blocker, Details: "same actor's push capability, diagnostics PR and repository"}, nil
				}
				return core.PrerequisiteComparison{Result: "different"}, nil
			})
			waiting := taskNow(t, lp, own.ID)
			unrelated := openDecision(t, lp, waiting)
			if unrelated.Kind != core.DecisionQuestion {
				t.Fatal(unrelated)
			}
			if _, err = lp.Core.AnswerDecision(ctx, unrelated.ID, "Use JSON", core.FromOwner); err != nil {
				t.Fatal(err)
			}
			got := taskNow(t, lp, own.ID)
			if runner.edits != 1 || got.Plan == nil || len(got.Plan.Questions) != 0 || len(got.Blockers) != 1 || got.Blockers[0].ID != held.Blockers[0].ID {
				t.Fatalf("%+v edits %d", got, runner.edits)
			}
			snap, _ = lp.Core.Snapshot(ctx)
			for _, d := range snap.Decisions {
				if d.Kind == core.DecisionPrerequisite && d.ID != prerequisite.ID {
					t.Fatal("asked prerequisite again")
				}
				if d.Kind == core.DecisionQuestion && d.ID != unrelated.ID {
					t.Fatal("asked confirmation again")
				}
			}
			plans := turns(&runner.scriptedRunner, "Plan this task before anything is written")
			if len(plans) != 3 {
				t.Fatal(len(plans))
			}
			for _, prompt := range []string{plans[1].Prompt, plans[2].Prompt, taskBrief(snap, p.ID, got, "")} {
				for _, want := range []string{condition, held.Blockers[0].ID, choice} {
					if !strings.Contains(prompt, want) {
						t.Fatalf("missing %q in %s", want, prompt)
					}
				}
			}
			if !strings.Contains(plans[2].Prompt, "Do not declare it as a prerequisite or ask about it again") {
				t.Fatal(plans[2].Prompt)
			}
		})
	}
}

func TestTeamToolsCannotReopenSettledPrerequisites(t *testing.T) {
	lp, _, p := plannedCode(t, 6)
	own, _ := lp.Core.QueueTask(context.Background(), p.ID, core.TaskInput{Objective: "Build"})
	snap, _ := lp.Core.Snapshot(context.Background())
	task, _ := findTask(snap, p.ID, own.ID)
	for _, r := range task.Roles {
		tools := lp.toolsFor(task, r.Working(), r)
		if tools.offers("reopen_prerequisite") {
			t.Fatal("team reopening tool exposed")
		}
	}
}

func TestReopeningPreservesPlanningAnswerRoutingAcrossRestart(t *testing.T) {
	for _, method := range []string{"direct", "planning answer"} {
		t.Run(method, func(t *testing.T) {
			ctx := context.Background()
			third := `{"summary":"Build as JSON"}`
			if method == "planning answer" {
				third = `{"summary":"Need the format","questions":["Which diagnostic format?"]}`
			}
			lp, runner, p := plannedCode(t, 6,
				`{"summary":"Build","prerequisites":["Library v1 tagged"]}`,
				`{"summary":"Build","questions":["Which diagnostic format?"]}`,
				third, `{"summary":"Build as JSON"}`)
			own, err := lp.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Build diagnostic report"})
			if err != nil {
				t.Fatal(err)
			}
			held := taskNow(t, lp, own.ID)
			snap, _ := lp.Core.Snapshot(ctx)
			for _, d := range snap.Decisions {
				if d.TaskID == own.ID && d.Kind == core.DecisionPrerequisite && d.Status == core.DecisionOpen {
					lp.Core.ChooseDecision(ctx, d.ID, core.ChoiceReadyPrerequisite, core.FromOwner)
				}
			}
			waiting := taskNow(t, lp, own.ID)
			q := openDecision(t, lp, waiting)
			b := waiting.Blockers[0]
			instruction := "Prerequisite " + b.ID + " no longer holds: " + b.Description
			if method == "direct" {
				_, err = lp.Core.ReopenPrerequisite(ctx, p.ID, own.ID, b.ID, b.Description, instruction, core.LinkedByOwner, core.PrerequisiteReopen{Source: "owner-reopen", Settlement: core.PrerequisiteSettlementID(b)})
			} else {
				_, err = lp.Core.AnswerDecision(ctx, q.ID, instruction, core.FromOwner)
			}
			if err != nil {
				t.Fatal(err)
			}
			snap, _ = lp.Core.Snapshot(ctx)
			got, _ := snap.FindTask(own.ID)
			if got.Status != core.TaskWaiting || got.DecisionID != q.ID || got.Asker == nil || runner.edits != 0 {
				t.Fatal(got)
			}
			// Reconstruct the service and answer routing from persisted SQLite state.
			st, err := core.Open(filepath.Join(lp.Core.StateDirectory(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			lp.Core = core.NewService(st, lp.Config())
			snap, _ = lp.Core.Snapshot(ctx)
			for _, d := range snap.Decisions {
				if d.TaskID == own.ID && d.Kind == core.DecisionPrerequisite && d.Status == core.DecisionOpen {
					if _, err = lp.Core.ChooseDecision(ctx, d.ID, core.ChoiceReadyPrerequisite, core.FromOwner); err != nil {
						t.Fatal(err)
					}
				}
			}
			if method == "direct" {
				got = taskNow(t, lp, own.ID)
				if got.Status != core.TaskWaiting || got.DecisionID != q.ID || runner.edits != 0 {
					t.Fatal("unanswered question bypassed", got)
				}
				if _, err = lp.Core.AnswerDecision(ctx, q.ID, "Use JSON", core.FromOwner); err != nil {
					t.Fatal(err)
				}
			} else {
				got = taskNow(t, lp, own.ID)
				if runner.edits != 0 || got.Status != core.TaskWaiting {
					t.Fatal("planning answer bypassed", got)
				}
				newQuestion := openDecision(t, lp, got)
				if newQuestion.ID == q.ID {
					t.Fatal("old answer not processed")
				}
				plans := turns(&runner.scriptedRunner, "Plan this task before anything is written")
				if len(plans) != 3 || !strings.Contains(plans[2].Prompt, instruction) {
					t.Fatal("answer lost", plans)
				}
				if _, err = lp.Core.AnswerDecision(ctx, newQuestion.ID, "Use JSON", core.FromOwner); err != nil {
					t.Fatal(err)
				}
			}
			got = taskNow(t, lp, own.ID)
			if runner.edits != 1 || len(got.Revisions) != 1 || got.Blockers[0].ID != held.Blockers[0].ID {
				t.Fatal(got, runner.edits)
			}
			plans := turns(&runner.scriptedRunner, "Plan this task before anything is written")
			if !strings.Contains(plans[len(plans)-1].Prompt, "Use JSON") {
				t.Fatal("unrelated answer lost")
			}
		})
	}
}

func TestParsePlanPreservesCompleteConditionsAndRejectsOverflow(t *testing.T) {
	long := strings.Repeat("Scoped diagnostics permission is required. ", 12) + "merge PRs using v2.0.0-RC1"
	raw := fmt.Sprintf(`{"summary":"Build","prerequisites":[%q]}`, long)
	plan, _, _, _, err := parsePlan(raw, false, false, nil)
	if err != nil || len(plan.Prerequisites) != 1 || plan.Prerequisites[0].What != long {
		t.Fatal(plan, err)
	}
	for _, field := range []string{"prerequisites", "questions"} {
		raw = fmt.Sprintf(`{"summary":"Build",%q:[%q]}`, field, strings.Repeat("x", core.MaxPrerequisiteBytes+1))
		if _, _, _, _, err = parsePlan(raw, false, false, nil); err == nil {
			t.Fatal("oversized input silently truncated", field)
		}
	}
}

func TestOversizedResearchProposalsCannotStartImplementation(t *testing.T) {
	for _, field := range []string{"prerequisites", "questions"} {
		t.Run(field, func(t *testing.T) {
			reply := fmt.Sprintf(`{"summary":"Build diagnostics",%q:[%q]}`, field, strings.Repeat("x", core.MaxPrerequisiteBytes+1))
			lp, runner, p := plannedCode(t, 6, reply, reply)
			ctx := context.Background()
			own, err := lp.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Build diagnostics"})
			if err != nil {
				t.Fatal(err)
			}
			got := taskNow(t, lp, own.ID)
			if got.Status == core.TaskWriting || len(got.Revisions) != 0 || runner.edits != 0 || got.Plan != nil {
				t.Fatalf("invalid proposal started implementation: %+v edits %d", got, runner.edits)
			}
			plans := turns(&runner.scriptedRunner, "Plan this task before anything is written")
			if len(plans) != 2 || !strings.Contains(plans[1].Prompt, "3000 bytes") {
				t.Fatal("must exhaust the correction attempt")
			}
			if got.Failures != 1 || got.RetryAt.IsZero() || got.Status != core.TaskResearching {
				t.Fatalf("validation failure bypassed role retry: %+v", got)
			}
			snap, _ := lp.Core.Snapshot(ctx)
			for _, decision := range snap.Decisions {
				if decision.TaskID == own.ID {
					t.Fatalf("validation failure manufactured a decision: %+v", decision)
				}
			}
		})
	}
}

func TestSavedPlanQuestionsReuseLaterSettlements(t *testing.T) {
	for _, outcome := range []string{core.ChoiceReadyPrerequisite, core.ChoiceDropPrerequisite} {
		for _, restart := range []bool{false, true} {
			for _, unrelated := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/restart=%t/unrelated=%t", outcome, restart, unrelated), func(t *testing.T) {
					ctx := context.Background()
					lp, runner, p := plannedCode(t, 6, `{"summary":"Build","prerequisites":["Library v1 tagged"]}`)
					own, err := lp.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Build diagnostics"})
					if err != nil {
						t.Fatal(err)
					}
					held := taskNow(t, lp, own.ID)
					snap, _ := lp.Core.Snapshot(ctx)
					for _, d := range snap.Decisions {
						if d.Kind == core.DecisionPrerequisite && d.Status == core.DecisionOpen {
							if _, err := lp.Core.ChooseDecision(ctx, d.ID, core.ChoiceReadyPrerequisite, core.FromOwner); err != nil {
								t.Fatal(err)
							}
						}
					}
					drafted := taskNow(t, lp, own.ID)
					if len(drafted.Revisions) != 1 {
						t.Fatal(drafted)
					}
					b := drafted.Blockers[0]
					if _, err := lp.Core.ReopenPrerequisite(ctx, p.ID, own.ID, b.ID, b.Description, "Owner revoked access", core.LinkedByOwner, core.PrerequisiteReopen{Source: "owner-reopen", Settlement: core.PrerequisiteSettlementID(b)}); err != nil {
						t.Fatal(err)
					}
					if _, err := lp.Core.UpdateTask(ctx, own.ID, func(t *core.Task, _ *core.Project) (string, error) {
						t.Status, t.DecisionID, t.Asker = core.TaskResearching, "", nil
						return "", nil
					}); err != nil {
						t.Fatal(err)
					}
					question := "Is Library v1 tagged ready?"
					questions := []string{question}
					if unrelated {
						questions = append(questions, "Which diagnostic format?")
					}
					if _, err := lp.Core.RecordPlan(ctx, own.ID, core.Plan{Role: "Researcher", Summary: "Continue diagnostics", Questions: questions}, nil, nil); err != nil {
						t.Fatal(err)
					}
					snap, _ = lp.Core.Snapshot(ctx)
					for _, d := range snap.Decisions {
						if d.Kind == core.DecisionPrerequisite && d.Status == core.DecisionOpen {
							if _, err := lp.Core.ChooseDecision(ctx, d.ID, outcome, core.FromOwner); err != nil {
								t.Fatal(err)
							}
						}
					}
					if restart {
						st, err := core.Open(filepath.Join(lp.Core.StateDirectory(), "state.db"))
						if err != nil {
							t.Fatal(err)
						}
						defer st.Close()
						lp.Core = core.NewService(st, lp.Config())
					}
					snap, _ = lp.Core.Snapshot(ctx)
					current, _ := snap.FindTask(own.ID)
					if !planned(current) {
						t.Fatal("saved plan did not resume")
					}
					// Use the actual saved-plan branch, which must not pay for research again.
					researcher, _ := current.Researcher()
					if err := lp.researchTask(ctx, p, current, nil, researcher); err != nil {
						t.Fatal(err)
					}
					snap, _ = lp.Core.Snapshot(ctx)
					got, _ := snap.FindTask(own.ID)
					if len(got.Revisions) != 1 || got.Revisions[0].Ref != drafted.Revisions[0].Ref || got.Blockers[0].ID != held.Blockers[0].ID || runner.edits != 1 {
						t.Fatal("saved-plan reconciliation discarded or rewrote the draft", got)
					}
					if unrelated {
						d := openDecision(t, lp, got)
						if got.Status != core.TaskWaiting || d.Context != "1. Which diagnostic format?" || len(got.Plan.Questions) != 1 {
							t.Fatal("unrelated question was lost or confirmation persisted", d, got)
						}
					} else if got.Status != core.TaskWriting || len(got.Plan.Questions) != 0 {
						t.Fatal("settlement opened another owner wait", got)
					}
					found := false
					for _, a := range snap.Activity {
						if a.TaskID == own.ID && a.Kind == "prerequisite.reused" && strings.Contains(a.Summary, question) && strings.Contains(a.Summary, b.ID) && strings.Contains(a.Summary, got.Blockers[0].Outcome) {
							found = true
						}
					}
					if !found {
						t.Fatal("saved-question reuse was not recorded")
					}
				})
			}
		}
	}
}
