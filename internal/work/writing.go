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

// write runs the implementer seat that claimed this round and records what
// it produced. The workspace is first reset to the last revision, so nothing
// a crashed or failed turn left behind is ever mistaken for a draft.
func (lp *Loop) write(ctx context.Context, p core.Project, t core.Task, m medium, writer core.Role) error {
	if writer.Name == "" {
		return lp.stopTask(ctx, t, "This task's team has no writer")
	}
	t, err := lp.prepareWorkspace(ctx, t, m)
	if err != nil {
		return lp.roleFailed(ctx, t, "The workspace", err)
	}
	if held, err := lp.holdForUsage(ctx, t, writer); held || err != nil {
		return err
	}
	t, caughtUp, integration, err := lp.takeInLanded(ctx, t, m)
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
	guide := learnedGuide(writer, false) + handOnGuide(t, core.RoleImplementer, "")
	spec, cleanup, err := lp.roleSpec(t, writer, m.workspace(t), true, m, writerPrompt(p, t, caughtUp, false)+prompt+guide)
	if err != nil {
		return lp.roleFailed(ctx, t, "The workspace", err)
	}
	defer cleanup()
	spec.Loopback = checkLoopback(taskPlaybook(p, t), writer)
	// The implementer carries on its member's conversation on this task,
	// whichever of the member's seats had it, and never another task's or
	// another member's. Starting afresh, whether asked to or because it
	// can't carry it on, it gets the task's record in its place.
	spec.Resume = t.Resumable(core.RoleImplementer, writer)
	switch t.WriterNext {
	case core.WriterFresh:
		spec.Resume = nil
	case core.WriterCompact:
		spec.Compact = true
	}
	fresh := writerPrompt(p, t, caughtUp, true) + prompt + guide
	if len(spec.Resume) == 0 {
		spec.Prompt = fresh
	} else {
		spec.FreshPrompt = fresh
	}
	result, err := lp.runRole(ctx, spec)
	if err != nil {
		return lp.roleFailed(ctx, t, writer.Name, err)
	}
	return lp.recordDraft(ctx, p, t, m, writer.Name, result, seen, integration)
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
func (lp *Loop) recordDraft(ctx context.Context, p core.Project, t core.Task, m medium, writer string, result roles.Result, seen int, integration *core.DraftCatchUp) error {
	r, ok := t.Role(writer)
	in := parseWriterReply(result.Text, ok && designsFor(t, r))
	why, handProblems := parseHandOn(in.handOn)
	wakeErrors := lp.applyWakeBlock(ctx, p, t, in.wake)
	wakeErrors = append(wakeErrors, handProblems...)
	prText, problem := parsePRText(in.pr)
	if problem != "" {
		wakeErrors = append(wakeErrors, problem)
	}
	// Replies with a new draft wait for it to be pushed; without one, for
	// the draft the pull request has.
	n := len(t.Revisions) + 1
	posts, handTo, problems := parsePRReply(in.prReply, writer, t.PROpen(), n)
	wakeErrors = append(wakeErrors, problems...)
	if handTo != nil {
		if _, err := lp.Core.AskAboutPR(ctx, t.ID, writer, handTo.To, handTo.Question); err != nil {
			wakeErrors = append(wakeErrors, "handing the pull request to "+handTo.To+": "+err.Error())
		}
	}
	if ok {
		lp.recordLearned(ctx, p, t, r, m, in.learned)
	}
	h := core.Handoff{HandOnWhy: why, DraftCatchUp: integration, Writer: writer, Session: result.Session, Seen: seen, Reply: in.reply, Request: t.WriterRequest, WakeErrors: wakeErrors, PR: prText, Posts: posts}
	if ok {
		r.Learnings = nil
		h.Seat = &r
	}
	// Asking for design input ends the turn without a draft: whatever it
	// changed is set aside when the workspace is next reset, and its session
	// resumes with the answer.
	if in.question != "" {
		return lp.askDesign(ctx, t, writer, in.question, func(t *core.Task) {
			t.WakeErrors = wakeErrors
			if why != "" {
				t.WakeErrors = append(t.WakeErrors, "hand-on ignored during a design question; ask again when this round finishes")
			}
			if t.WriterRequest == h.Request {
				t.WriterNext = ""
			}
			if h.Seat != nil {
				t.KeepThread(core.RoleImplementer, *h.Seat, h.Session)
			}
		})
	}
	revision, err := m.snapshot(ctx, t, n)
	if errors.Is(err, gitrepo.ErrNoChange) && proposed(t) {
		_, err = lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
			return noChangeNeeded(t, h), nil
		})
		return err
	}
	if err != nil {
		if errors.Is(err, gitrepo.ErrConflictMarkers) {
			_, logErr := lp.updateOpen(ctx, t.ID, func(task *core.Task, _ *core.Project) (string, error) {
				return fmt.Sprintf("%s left %s", writer, err), nil
			})
			if logErr != nil {
				return logErr
			}
		}
		return lp.roleFailed(ctx, t, writer, fmt.Errorf("the work could not be recorded: %w", err))
	}
	// The draft counts only once the project's records hold it; the handoff
	// carries the round's whole outcome until then.
	revision.Summary = text.Clip(in.reply, 2000)
	h.Revision = revision
	h.Unreachable = parseOwnerSteps(in.unmet, n, t.Criteria, t.OwnersAlready())
	return lp.handOff(ctx, t, m, h)
}

// writerReply is an implementer's reply taken apart: its words, and the
// blocks it may end with.
type writerReply struct {
	reply, learned, wake, unmet, prReply, pr, question, handOn string
}

// parseWriterReply takes an implementer's reply apart; designs is whether
// it may ask the designer, with a design block.
func parseWriterReply(text string, designs bool) writerReply {
	var in writerReply
	text, in.handOn = splitHandOn(text)
	in.reply, in.learned = splitBlock(text, "learned")
	in.reply, in.wake = splitBlock(in.reply, "wake")
	in.reply, in.unmet = splitBlock(in.reply, "owner-step")
	in.reply, in.prReply = splitBlock(in.reply, "pr-reply")
	in.reply, in.pr = splitBlock(in.reply, "pr")
	if designs {
		in.reply, in.question = splitBlock(in.reply, "design")
	}
	return in
}

// noChangeNeeded records, within a change, an implementer's round on an
// open pull request that changed nothing: its replies go with the draft the
// pull request has, and the task lands again, or revises at once with any
// direction the round didn't see.
func noChangeNeeded(t *core.Task, h core.Handoff) string {
	for i := range h.Posts {
		h.Posts[i].Revision = len(t.Revisions)
	}
	tookTurn(t, h)
	t.AnswerDirection(h.Seen, 0, "No change needed: "+h.Reply, time.Now().UTC())
	if t.DirectionPending > 0 {
		t.ReviseWithDirection()
		return fmt.Sprintf("%s: no change needed for the pull request; revising with your note", t.Objective)
	}
	t.Status, t.Detail = core.TaskLanding, "No change needed: "+text.Clip(h.Reply, 300)
	return fmt.Sprintf("%s: no change needed for the pull request's feedback", t.Objective)
}
