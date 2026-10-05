//go:build !windows

package work

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/roles"
	"github.com/shhac/lib-agent-harness/sandbox"
)

func TestEscalationKeepsTheRequirementsJudgedByPM(t *testing.T) {
	t.Parallel()
	for _, choice := range []string{choiceAcceptDraft, choiceAcceptFollowUp} {
		for _, brief := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/brief=%t", choice, brief), func(t *testing.T) {
				t.Parallel()
				ctx := context.Background()
				runner := &scriptedRunner{reviews: []string{revise}, escalate: []string{judgement(true, false, false, false, false)}}
				a, p, task := roundLimited(t, runner, true)
				task = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Status == core.TaskDeciding })
				a.runner = checkingRunner{next: runner, before: func(_ context.Context, spec roles.Spec) {
					if strings.Contains(spec.Prompt, "Judge what remains at the round limit") {
						editAcceptedRequirements(t, a, p.ID, task.ID, brief)
					}
				}}
				task, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
					t.Claims = []core.Claim{{Token: "judging", Step: core.TaskDeciding}}
					return "", nil
				})
				if err != nil {
					t.Fatal(err)
				}
				if err := a.escalate(core.Fenced(ctx, task.ID, "judging"), p, task, task.Verdicts, task.Verdicts); !errors.Is(err, core.ErrStale) {
					t.Fatal("old PM judgement stamped new requirements", err)
				}
				current := taskByID(t, a, task.ID)
				if current.DecisionID != "" || current.Acceptance != nil || len(followUps(t, a, task.ID)) != 0 {
					t.Fatal("stale judgement offered acceptance", current)
				}
				// Even a restored resolved answer carrying the judged versions cannot
				// accept the edited work or create a follow-up.
				d, err := a.Core.OpenTaskDecision(ctx, task.ID, core.DecisionEscalation, core.DecisionInput{Title: "Old judgement", Context: "Old checks", Recommendation: choice, Choices: escalationChoices, FollowUp: followUpFrom(task, remaining(task.Verdicts))})
				if err != nil {
					t.Fatal(err)
				}
				_, err = a.Core.UpdateTaskWithDecision(ctx, task.ID, func(_ *core.Task, _ *core.Project, d *core.Decision) (string, error) {
					d.BriefVersion, d.TextVersion = p.Brief.Version, task.TextVersion
					return "", nil
				})
				if err != nil {
					t.Fatal(err)
				}
				d, err = a.Core.ChooseDecision(ctx, d.ID, choice, core.FromOwner)
				if err != nil {
					t.Fatal(err)
				}
				if err = a.applyAnswer(ctx, taskByID(t, a, task.ID), d); err != nil {
					t.Fatal(err)
				}
				current = taskByID(t, a, task.ID)
				if current.Status != core.TaskReviewing || current.Acceptance != nil || len(followUps(t, a, task.ID)) != 0 {
					t.Fatal("stale acceptance applied", current)
				}
			})
		}
	}
}

func TestCleanupNoteSurvivesWrappedEndedObserver(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	lp, p, task := loopApp(t, &scriptedRunner{}, "")
	watch := lp.watchTurn(task, core.RoleQA, core.Role{Name: "QA"}, "", false)
	watch.Started()
	watch.Ended()
	wrapped := &screenshots{next: browserFallbackObserver{Observer: watch}}
	lp.commandCleanupFor(wrapped)(&sandbox.CommandError{Code: sandbox.CommandCleanupUnknown})
	steps, err := lp.Core.TurnSteps(ctx, p.ID, task.ID, "QA")
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range steps {
		if step.Kind == core.StepNote && strings.Contains(step.Text, sandbox.CommandCleanupUnknown) {
			return
		}
	}
	t.Fatal("cleanup note lost after wrapped turn ended", steps)
}
