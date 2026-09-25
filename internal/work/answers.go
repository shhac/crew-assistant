package work

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/diagnostics"
	"github.com/shhac/crew-assistant/internal/roles"
	"github.com/shhac/crew-assistant/internal/text"
)

// roleFailed retries a failing role a couple of times with growing waits,
// then brings the owner one decision. A sandbox or login problem will not
// clear by itself and goes to the owner at once.
func (lp *Loop) roleFailed(ctx context.Context, t core.Task, role string, cause error) error {
	permanent := roles.Permanent(cause)
	var failures int
	updated, err := lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.Failures++
		failures = t.Failures
		if permanent || t.Failures > roleRetries {
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
	if _, err = lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.ResumeStatus = t.Status
		return "", nil
	}); err != nil {
		return err
	}
	_, err = lp.Core.OpenTaskDecision(ctx, t.ID, core.DecisionFailure, core.DecisionInput{
		Title:          fmt.Sprintf("%s couldn't work on “%s”", role, t.Objective),
		Context:        text.Clip(cause.Error(), 600),
		Recommendation: choiceTryAgain + " once the cause is fixed",
		Choices:        []string{choiceTryAgain, choiceStop},
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
	case d.Approves() && chose(choiceApprove),
		d.Kind == core.DecisionEscalation && chose(choiceAcceptDraft):
		return lp.approve(ctx, t)
	case d.Kind == core.DecisionFailure && chose(choiceTryAgain):
		_, err := lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
			t.Status, t.ResumeStatus = t.ResumeStatus, ""
			if t.Status == "" {
				t.Status = core.TaskWriting
			}
			// The owner's retry starts the count of catch-ups afresh.
			t.Failures, t.RetryAt, t.DecisionID, t.Detail, t.CatchUps = 0, time.Time{}, "", "Trying again", 0
			return "Trying " + t.Objective + " again", nil
		})
		return err
	}
	// Anything else is direction for another round: the owner asked for
	// changes, answered a reviewer's question or wants one more attempt.
	_, err := lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
		if d.Kind == core.DecisionQuestion || (!chose(choiceAnotherRound) && !chose(choiceChanges)) {
			t.AddDirection(&d, answer)
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

// StopTask ends a task at the owner's request. A turn already running
// finishes, but nothing it reports can restart the task, and any decision the
// task was waiting on is closed as no longer needed.
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
	if decisionID != "" {
		if _, dismissErr := lp.Core.DismissDecision(ctx, decisionID, "The task was stopped"); dismissErr != nil && !errors.Is(dismissErr, core.ErrConflict) {
			return stopped, dismissErr
		}
	}
	lp.Nudge()
	return stopped, nil
}
