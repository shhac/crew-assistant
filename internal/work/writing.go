package work

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/media/gitrepo"
	"github.com/shhac/crew-assistant/internal/roles"
	"github.com/shhac/crew-assistant/internal/text"
)

// write runs the implementer for this round and records what it produced.
// The workspace is first reset to the last revision, so nothing a crashed or
// failed turn left behind is ever mistaken for a draft.
func (lp *Loop) write(ctx context.Context, p core.Project, t core.Task, m medium) error {
	writers := t.RolesOf(core.RoleImplementer)
	if len(writers) != 1 {
		return lp.stopTask(ctx, t, "This task's team has no writer")
	}
	t, err := lp.prepareWorkspace(ctx, t, m)
	if err != nil {
		return lp.roleFailed(ctx, t, "The workspace", err)
	}
	if held, err := lp.holdForUsage(ctx, t, writers[0]); held || err != nil {
		return err
	}
	t, caughtUp, err := lp.takeInLanded(ctx, t, m)
	if err != nil {
		return lp.roleFailed(ctx, t, "The workspace", err)
	}
	woken, err := lp.Core.TakeTaskWakes(ctx, t.ID, core.WakeTask)
	if err != nil {
		return err
	}
	prompt, err := lp.wakePrompt(ctx, t, woken)
	if err != nil {
		return err
	}
	seen := len(t.Direction)
	spec, cleanup, err := lp.roleSpec(t, writers[0], m.workspace(), true, m, writerPrompt(p, t, caughtUp)+prompt+learnedGuide(writers[0], false))
	if err != nil {
		return lp.roleFailed(ctx, t, "The workspace", err)
	}
	defer cleanup()
	spec.Resume = t.WriterSession
	switch t.WriterNext {
	case core.WriterFresh:
		spec.Resume = nil
	case core.WriterCompact:
		spec.Compact = true
	}
	result, err := lp.runner.Run(ctx, spec)
	if err != nil {
		return lp.roleFailed(ctx, t, writers[0].Name, err)
	}
	return lp.recordDraft(ctx, p, t, m, writers[0].Name, result, seen)
}

// prepareWorkspace starts the task's workspace on its first round, then puts
// it back at the last revision.
func (lp *Loop) prepareWorkspace(ctx context.Context, t core.Task, m medium) (core.Task, error) {
	if len(t.Revisions) == 0 {
		started, err := m.begin(ctx, t)
		if err != nil {
			return t, err
		}
		if started.Base != t.Base || started.Branch != t.Branch {
			if t, err = lp.updateOpen(ctx, t.ID, func(task *core.Task, _ *core.Project) (string, error) {
				task.Base, task.From, task.Branch = started.Base, started.From, started.Branch
				return "", nil
			}); err != nil {
				return t, err
			}
		}
	}
	return t, m.reset(ctx, t)
}

// recordDraft records what the implementer's round produced: a new draft for
// review, or, answering a pull request, the team's word that nothing needed
// to change, or its hand-off to the designer for design input. seen is how
// much of the owner's direction its prompt carried.
func (lp *Loop) recordDraft(ctx context.Context, p core.Project, t core.Task, m medium, writer string, result roles.Result, seen int) error {
	reply, learned := splitBlock(result.Text, "learned")
	reply, block := splitBlock(reply, "wake")
	wakeErrors := lp.applyWakeBlock(ctx, p, t, block)
	r, ok := t.Role(writer)
	if ok {
		lp.recordLearned(ctx, p, t, r, m, learned)
	}
	var question string
	if ok && designsFor(t, r) {
		reply, question = splitBlock(reply, "design")
	}
	// The round carried out the request it started with; one made while it
	// ran, even for the same thing, waits for the next round.
	applied := t.WriterRequest
	took := func(t *core.Task) {
		if t.WriterRequest == applied {
			t.WriterNext = ""
		}
	}
	// Asking for design input ends the turn without a draft: whatever it
	// changed is set aside when the workspace is next reset, and its session
	// resumes with the answer.
	if question != "" {
		return lp.askDesign(ctx, t, writer, question, func(t *core.Task) {
			t.WriterSession, t.WakeErrors = result.Session, wakeErrors
			took(t)
		})
	}
	n := len(t.Revisions) + 1
	revision, err := m.snapshot(ctx, t, n)
	if errors.Is(err, gitrepo.ErrNoChange) && proposed(t) {
		_, err = lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
			t.WriterSession, t.WakeErrors, t.Failures, t.RetryAt = result.Session, wakeErrors, 0, time.Time{}
			took(t)
			t.AnswerDirection(seen, 0, "No change needed: "+reply, time.Now().UTC())
			if t.DirectionPending > 0 {
				t.ReviseWithDirection()
				return fmt.Sprintf("%s: no change needed for the pull request; revising with your note", t.Objective), nil
			}
			t.Status, t.Detail = core.TaskLanding, "No change needed: "+text.Clip(reply, 300)
			return fmt.Sprintf("%s: no change needed for the pull request's feedback", t.Objective), nil
		})
		return err
	}
	if err != nil {
		return lp.roleFailed(ctx, t, writer, fmt.Errorf("the work could not be recorded: %w", err))
	}
	_, err = lp.updateOpen(ctx, t.ID, func(t *core.Task, p *core.Project) (string, error) {
		revision.BriefVersion, revision.Summary, revision.At = p.Brief.Version, text.Clip(reply, 2000), time.Now().UTC()
		t.Revisions = append(t.Revisions, revision)
		t.AnswerDirection(seen, n, reply, revision.At)
		t.WriterSession, t.WakeErrors = result.Session, wakeErrors
		took(t)
		t.Failures, t.RetryAt = 0, time.Time{}
		t.Status, t.Detail = core.TaskReviewing, ""
		return fmt.Sprintf("%s finished version %d of %s", writer, n, t.Objective), nil
	})
	return err
}
