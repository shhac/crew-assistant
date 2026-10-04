package work

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
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
	beforeCatchUp := t
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
	spec, cleanup, err := lp.roleSpec(t, writer, m.workspace(t), true, m, writerPrompt(p, t, caughtUp, false)+prompt+guide, nil, core.RoleImplementer)
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
	spec.FreshReason = writerFreshReason(t, writer)
	switch t.WriterNext {
	case core.WriterFresh:
		spec.Resume = nil
		spec.FreshReason = core.FreshOwnerRequested
	case core.WriterCompact:
		spec.Compact = true
	}
	fresh := writerPrompt(p, t, caughtUp, true) + prompt + guide
	if len(spec.Resume) == 0 {
		spec.Prompt = fresh
	} else {
		spec.FreshPrompt = fresh
	}
	var result roles.Result
	for attempt := 0; attempt < 2; attempt++ {
		result, err = lp.runRole(ctx, spec)
		if err != nil {
			return lp.roleFailed(ctx, t, writer.Name, err)
		}
		in := parseWriterReply(result.Text, designsFor(t, writer))
		if in.productionError == nil || attempt == 1 {
			break
		}
		if err := m.reset(ctx, beforeCatchUp); err != nil {
			return lp.roleFailed(ctx, t, "The workspace", err)
		}
		t, caughtUp, integration, err = lp.takeInLanded(ctx, beforeCatchUp, m)
		if err != nil {
			return lp.roleFailed(ctx, t, "The workspace", err)
		}
		fresh = writerPrompt(p, t, caughtUp, true) + prompt + guide
		correction := "\n\nYour production request could not be used (" + in.productionError.Error() + "). Reply with only a valid production block."
		spec.Prompt = writerPrompt(p, t, caughtUp, false) + prompt + guide + correction
		spec.FreshPrompt = fresh + correction
		spec.Resume = result.Session
		spec.FreshReason = core.FreshNoThread
		spec.PreviousID, spec.RetryCause = result.AttemptID, "malformed_production"
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
	if in.productionError != nil {
		return lp.roleFailed(ctx, t, writer, in.productionError)
	}
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
		return lp.askDesignAssets(ctx, t, writer, in.question, "", in.assets, func(t *core.Task) {
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
	var kept []core.Unreachable
	h.Unreachable, kept = parseOwnerSteps(in.unmet, n, append(slices.Clone(t.Criteria), p.Brief.Criteria...), t.OwnersAlready(), t.TeamKept)
	revision.Summary = draftSummary(in.reply, kept)
	h.Revision = revision
	return lp.handOff(ctx, t, m, h)
}

// draftSummary leads with compact unmet requirements so even the delivery's
// 600-byte view can show a complete entry. Ordinary replies keep their limit.
func draftSummary(reply string, kept []core.Unreachable) string {
	if len(kept) == 0 {
		return text.Clip(reply, 2000)
	}
	summary := "Kept for the team, still not met from the sandbox:"
	for _, u := range kept {
		summary += "\n- " + text.Clip(u.Criterion, 300) + ": " + text.Clip(u.Why, 200)
	}
	summary += "\n\n" + reply
	limit := 2000
	if len(summary) > limit {
		limit -= len("…")
	}
	return text.Clip(summary, limit)
}

// writerReply is an implementer's reply taken apart: its words, and the
// blocks it may end with.
type writerReply struct {
	reply, learned, wake, unmet, prReply, pr, question, handOn string
	assets                                                     []core.WantedAsset
	productionError                                            error
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
		var production string
		hadProduction := strings.Contains(in.reply, "```production\n")
		in.reply, production = splitBlock(in.reply, "production")
		in.reply, in.question = splitBlock(in.reply, "design")
		if hadProduction {
			if in.question != "" {
				in.productionError = errors.New("ask with either a production block or a design block")
			} else {
				in.assets, in.question, in.productionError = parseProduction(production)
			}
		}
	}
	return in
}

// parseProduction reads named assets, followed by optional notes after a blank line.
func parseProduction(block string) ([]core.WantedAsset, string, error) {
	parts := strings.SplitN(strings.TrimSpace(block), "\n\n", 2)
	var assets []core.WantedAsset
	seen := map[string]bool{}
	for _, line := range strings.Split(parts[0], "\n") {
		name, want, ok := strings.Cut(line, ":")
		name, want = strings.TrimSpace(name), strings.TrimSpace(want)
		if !ok || name == "" || want == "" || seen[name] || len(name) > 120 || len(want) > 4000 {
			return nil, "", errors.New("production needs unique assets, one per line: name: what it is")
		}
		seen[name] = true
		assets = append(assets, core.WantedAsset{Name: name, Want: want})
	}
	if len(assets) > core.MaxProductionAssets {
		return nil, "", fmt.Errorf("production can request at most %d assets", core.MaxProductionAssets)
	}
	question := "Produce the named assets."
	if len(parts) > 1 {
		question += "\n" + parts[1]
	}
	return assets, question, nil
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

// writerFreshReason observes thread selection without changing Resumable.
func writerFreshReason(t core.Task, writer core.Role) string {
	if t.WriterNext == core.WriterFresh {
		return core.FreshOwnerRequested
	}
	th, ok := t.Thread(core.RoleImplementer, writer)
	if !ok {
		return core.FreshNoThread
	}
	if th.Engine != writer.Engine {
		return core.FreshEngineChanged
	}
	if th.Model != writer.Model {
		return core.FreshModelChanged
	}
	return core.FreshNoThread
}
