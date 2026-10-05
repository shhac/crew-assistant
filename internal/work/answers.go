package work

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/diagnostics"
	"github.com/shhac/crew-assistant/internal/media/gitrepo"
	"github.com/shhac/crew-assistant/internal/roles"
	"github.com/shhac/crew-assistant/internal/text"
	"github.com/shhac/lib-agent-harness/session"
)

// roleFailed retries a failing role a couple of times with growing waits,
// then brings the owner one decision. A sandbox or login problem will not
// clear by itself and goes to the owner at once.
func (lp *Loop) roleFailed(ctx context.Context, t core.Task, role string, cause error) error {
	var accounting *teamAccountingFailure
	if errors.As(cause, &accounting) {
		if _, scheduled := ctx.Value(accountingFailureKey{}).(*accountingFailureState); scheduled {
			return cause // the scheduler holds this claim instead of replaying inference
		}
	}
	if accounting == nil && roles.KeychainLocked(cause) {
		return lp.awaitKeychain(ctx, t)
	}
	permanent := roles.Permanent(cause) || accounting != nil
	var failures int
	updated, err := lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.Failures++
		failures = t.Failures
		if permanent || t.Failures > roleRetries {
			t.RetryAt = time.Time{}
			return "", nil
		}
		t.RetryAt, t.HeldFor = time.Now().Add(time.Duration(t.Failures*t.Failures)*time.Minute), ""
		t.Detail = fmt.Sprintf("%s hit a problem; trying again shortly", role)
		return "", nil
	})
	if err != nil {
		return err
	}
	lp.Diagnostics.Failure(diagnostics.Event{Component: "daemon", Stage: "task_role", ProjectID: updated.ProjectID}, cause)
	if !permanent && failures <= roleRetries {
		return nil
	}
	if _, err = lp.Core.UpdateTaskWithDecision(ctx, t.ID, func(t *core.Task, _ *core.Project, d *core.Decision) (string, error) {
		if t.Finished() {
			return "", nil
		}
		if t.Status == core.TaskWaiting && d != nil && d.Status != core.DecisionOpen {
			return "", core.ErrStale
		}
		t.ResumeStatus = t.Status
		if t.Status == core.TaskWaiting {
			// The failed workspace operation was catching up for delivery, rather
			// than waiting for an answer that this failure decision replaces.
			if d != nil && (d.Approves() || d.Kind == core.DecisionEscalation) {
				t.ResumeStatus = core.TaskLanding
			} else if t.ResumeStatus == core.TaskWaiting {
				t.ResumeStatus = core.TaskWriting
			}
		}
		return "", nil
	}); err != nil {
		return err
	}
	title := fmt.Sprintf("%s couldn't work on “%s”", role, t.Objective)
	if errors.Is(cause, gitrepo.ErrConflictMarkers) {
		title = fmt.Sprintf("“%s” couldn't resolve its conflict with what landed", t.Objective)
	}
	failureContext := text.Clip(cause.Error(), 600)
	var capability *session.CapabilityError
	if errors.As(cause, &capability) && capability.Phase == session.BeforeLaunch && sandboxProofCode(capability.Code) {
		failureContext = fmt.Sprintf("The sandbox for %s’s commands couldn’t be proved on this computer (%s); nothing ran.\n\n%s", role, capability.Code, text.Clip(cause.Error(), 600))
	}
	_, err = lp.Core.OpenTaskDecision(ctx, t.ID, core.DecisionFailure, core.DecisionInput{
		Title:          title,
		Context:        failureContext,
		Recommendation: choiceTryAgain + " once the cause is fixed",
		Choices:        []string{choiceTryAgain, choiceStop},
	})
	return err
}

// awaitKeychain holds a task whose role could not start while the login
// keychain is locked. Starting anything would raise an unlock prompt, so it
// checks again in a minute, and it is not counted as a failure: the owner
// unlocking the Mac is the fix, not a decision.
func (lp *Loop) awaitKeychain(ctx context.Context, t core.Task) error {
	_, err := lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.RetryAt, t.HeldFor = time.Now().Add(time.Minute), ""
		t.Detail = "Waiting for the login keychain to be unlocked"
		return "", nil
	})
	return err
}

func (lp *Loop) setStatus(ctx context.Context, id, status, detail string) error {
	_, err := lp.updateOpen(ctx, id, func(t *core.Task, _ *core.Project) (string, error) {
		t.Status, t.Detail = status, detail
		return "", nil
	})
	return err
}

func (lp *Loop) stopTask(ctx context.Context, t core.Task, reason string) error {
	_, err := lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.Status, t.Detail = core.TaskStopped, reason
		return t.Objective + " stopped", nil
	})
	return err
}

// settleAnswers applies the owner's answers to decisions tasks are waiting on.
func (lp *Loop) settleAnswers(ctx context.Context, snap core.Snapshot) (bool, error) {
	for _, t := range snap.Tasks {
		if snap.ProjectPaused(t.ProjectID) {
			continue
		}
		if t.Status != core.TaskWaiting || t.DecisionID == "" {
			continue
		}
		d, ok := findDecision(snap, t.DecisionID)
		if !ok || d.Status == core.DecisionOpen {
			continue
		}
		return true, lp.applyAnswer(ctx, t, d)
	}
	return false, nil
}

func (lp *Loop) applyAnswer(ctx context.Context, t core.Task, d core.Decision) error {
	if d.Status == core.DecisionDismissed {
		return lp.stopTask(ctx, t, "You closed it")
	}
	answer := strings.TrimSpace(d.Answer)
	// Only a choice the owner picked acts on the task. Their own words are
	// direction, even when they spell "approve" or "stop".
	chose := func(choice string) bool {
		return d.Disposition == core.DispositionChoice && answer == choice
	}
	switch {
	case chose(choiceStop):
		return lp.stopTask(ctx, t, "You stopped it")
	case d.Kind == core.DecisionDelivery && t.PROpen() && chose(choiceApprove):
		return lp.approveMerge(ctx, t)
	case d.Kind == core.DecisionReadyForReview && (chose(choiceMarkReady) || chose(choiceKeepDraft)):
		return lp.chooseDraft(ctx, t, d, chose(choiceMarkReady))
	case d.Kind == core.DecisionOutsideThreads && (chose(choiceLetTeam) || chose(choiceLeaveToMe)):
		return lp.chooseOutsideThreads(ctx, t, d, chose(choiceLetTeam))
	case d.Approves() && chose(choiceApprove):
		return lp.approve(ctx, t)
	case d.Kind == core.DecisionEscalation && chose(choiceAcceptDraft):
		_, _, err := lp.Core.AcceptDraft(ctx, t.ID, d.ID, false, approveLatest)
		return err
	// The follow-up queued is the one the owner was shown.
	case d.Kind == core.DecisionEscalation && d.FollowUp != nil && chose(choiceAcceptFollowUp):
		_, _, err := lp.Core.AcceptWithFollowUp(ctx, t.ID, d.ID, approveLatest)
		return err
	case d.OwnerStep != nil && d.Split != nil && chose(choiceSplit):
		_, err := lp.Core.SplitOwnerStep(ctx, t.ID, d.ID)
		return err
	case d.OwnerStep != nil && chose(choiceOwnerStep):
		_, err := lp.Core.MakeOwnerStep(ctx, t.ID, d.ID)
		return err
	case d.OwnerStep != nil && chose(choiceKeepForTeam):
		return lp.keepForTeam(ctx, t, d.OwnerStep.Criterion, fmt.Sprintf("You kept “%s” for the team", text.Clip(d.OwnerStep.Criterion, 200)), true)
	case d.Kind == core.DecisionUnstack && chose(core.ChoiceUnstack):
		_, err := lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
			t.Unstack()
			t.Status, t.DecisionID, t.Detail = core.TaskLanding, "", "Rebasing onto the target"
			return t.Objective + " no longer builds on the work it was stacked on, and is rebased onto the target", nil
		})
		return err
	case d.Kind == core.DecisionFailure && (chose(choiceTryAgain) || chose(choiceResolve)):
		_, err := lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
			t.Status, t.ResumeStatus = t.ResumeStatus, ""
			if t.Status == "" || t.Status == core.TaskWaiting {
				t.Status = core.TaskWriting
			}
			// The owner's retry starts the count of catch-ups afresh.
			t.Failures, t.RetryAt, t.DecisionID, t.Detail, t.CatchUps = 0, time.Time{}, "", "Trying again", 0
			t.LandingFailures = nil
			if t.MergeValidation != nil && t.Acceptance != nil && t.Status == core.TaskReviewing {
				// Superseded turns retain their scheduling hold until they end,
				// but may no longer write results into this new attempt.
				for i := range t.Claims {
					t.Claims[i].Revoked = true
				}
				validation := *t.MergeValidation
				validation.Checked, validation.Failure, validation.Evidence = false, "", ""
				t.MergeValidation = &validation
				for i := range t.Verdicts {
					if t.Verdicts[i].Revision == t.MergeValidation.Revision {
						t.Verdicts[i].Answered = true
					}
				}
			}
			if chose(choiceResolve) {
				t.Detail = "Resolving the conflict"
				return t.Objective + " is resolving its conflict with what landed", nil
			}
			return "Trying " + t.Objective + " again", nil
		})
		return err
	}
	// Anything else is direction for another round: the owner asked for
	// changes, answered a question or wants one more attempt.
	_, err := lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
		if t.Acceptance != nil {
			for i := range t.Claims {
				t.Claims[i].Revoked = true
			}
			t.Approved = 0
			if t.Proposal != nil {
				t.Proposal.MergeApproved = 0
			}
		}
		t.Acceptance, t.MergeValidation = nil, nil
		// The owner stepped in: landings the PM approved count afresh.
		t.LandingFailures = nil
		if d.Kind == core.DecisionQuestion || (!chose(choiceAnotherRound) && !chose(choiceChanges)) {
			t.AddDirection(&d, answer)
		}
		// A role's question goes back to whoever asked it, in the same round,
		// with the owner's answer in its direction: the researcher plans
		// again, a checker judges the draft again, and either decides where
		// the task goes next.
		if a := t.BackToAsker(d.ID); a != nil {
			t.Detail = "Going on with your answer"
			return fmt.Sprintf("%s goes on with your answer about %s", a.From, t.Objective), nil
		}
		// A design question goes back to the step that asked it, in the same
		// round, with the owner's answer in its direction.
		if r := t.DesignDecision(d.ID); r != nil {
			if r.Open() {
				r.AnsweredAt = time.Now().UTC()
			}
			t.Status, t.DecisionID, t.Detail = r.Step, "", "Going on with your answer"
			return fmt.Sprintf("%s goes on with your answer about the design of %s", r.From, t.Objective), nil
		}
		// A designer that failed is asked again within the same round.
		if d.Kind == core.DecisionFailure && t.ResumeStatus == core.TaskDesigning {
			t.Status, t.ResumeStatus, t.DecisionID, t.Detail = core.TaskDesigning, "", "", "Asking for design input again with your answer"
			return fmt.Sprintf("Asking again for design input on %s", t.Objective), nil
		}
		t.NextRound()
		status, detail, activity := core.TaskWriting, "Revising with your answer", fmt.Sprintf("Revising %s with your direction", t.Objective)
		if len(t.Revisions) == 0 {
			detail, activity = "Starting with your answer", fmt.Sprintf("Starting %s with your answer", t.Objective)
		}
		// A researcher that failed researches again, with the owner's words to
		// go on.
		if d.Kind == core.DecisionFailure && t.ResumeStatus == core.TaskResearching {
			status, detail, t.ResumeStatus = core.TaskResearching, "Researching again with your answer", ""
		}
		t.Status, t.DecisionID, t.Detail = status, "", detail
		return activity, nil
	})
	return err
}

// StopTask ends a task at the owner's request. Its steps running now are
// cancelled and their seats freed; nothing they report can restart the task
// or be recorded, and any decision the task was waiting on is closed as no
// longer needed. Other tasks' steps go on.
func (lp *Loop) StopTask(ctx context.Context, projectID, taskID string) (core.Task, error) {
	var decisionID string
	stopped, err := lp.Core.UpdateTask(ctx, taskID, func(t *core.Task, _ *core.Project) (string, error) {
		if t.ProjectID != projectID {
			return "", core.ErrNotFound
		}
		if t.Finished() {
			return "", fmt.Errorf("this request has already finished: %w", core.ErrConflict)
		}
		decisionID = t.DecisionID
		t.Status, t.DecisionID, t.Detail = core.TaskStopped, "", "You stopped it"
		return t.Objective + " stopped", nil
	})
	if err != nil {
		return stopped, err
	}
	lp.jobs.cancelTask(stopped.ID)
	if decisionID != "" {
		if _, dismissErr := lp.Core.DismissDecision(ctx, decisionID, "The task was stopped"); dismissErr != nil && !errors.Is(dismissErr, core.ErrConflict) {
			return stopped, dismissErr
		}
	}
	lp.Nudge()
	return stopped, nil
}

// Only failed sandbox proofs warrant saying that the sandbox could not be proved.
func sandboxProofCode(code string) bool {
	switch code {
	case session.CapabilitySandboxNotEnforced, session.CapabilitySandboxUnavailable, session.CapabilityProbeTimeout, session.CapabilitySandboxToolMissing:
		return true
	}
	return false
}
