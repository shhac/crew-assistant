package work

import (
	"context"
	"errors"
	"strings"

	"github.com/shhac/crew-assistant/internal/core"
)

// The owner's and the assistant's changes that give the loop something to do.
// Each wakes the loop itself, so whether a change is noticed at once never
// depends on the caller remembering to.

// QueueTask asks for an outcome in a project.
func (lp *Loop) QueueTask(ctx context.Context, projectID string, in core.TaskInput) (core.Task, error) {
	t, err := lp.Core.QueueTask(ctx, projectID, in)
	lp.nudgeUnless(err)
	return t, err
}

// UpdateBrief changes what a project is for.
func (lp *Loop) UpdateBrief(ctx context.Context, projectID string, in core.BriefInput) (core.Project, error) {
	p, err := lp.Core.UpdateBrief(ctx, projectID, in)
	lp.nudgeUnless(err)
	return p, err
}

// ResolveDecision answers a decision with one of its choices, or with the
// owner's own words; never both, since words that spell a choice are not one.
func (lp *Loop) ResolveDecision(ctx context.Context, id, choice, answer string) (core.Decision, error) {
	if (strings.TrimSpace(choice) == "") == (strings.TrimSpace(answer) == "") {
		return core.Decision{}, errors.New("give either a choice or an answer")
	}
	resolve, with := lp.Core.ChooseDecision, choice
	if strings.TrimSpace(answer) != "" {
		resolve, with = lp.Core.AnswerDecision, answer
	}
	d, err := resolve(ctx, id, with)
	lp.nudgeUnless(err)
	return d, err
}

// DismissDecision closes a decision without an answer.
func (lp *Loop) DismissDecision(ctx context.Context, id, reason string) (core.Decision, error) {
	d, err := lp.Core.DismissDecision(ctx, id, reason)
	lp.nudgeUnless(err)
	return d, err
}

func (lp *Loop) nudgeUnless(err error) {
	if err == nil {
		lp.Nudge()
	}
}
