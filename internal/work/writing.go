package work

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	guide := learnedGuide(writer, false)
	spec, cleanup, err := lp.roleSpec(t, writer, m.workspace(t), true, m, writerPrompt(p, t, caughtUp, false)+prompt+guide)
	if err != nil {
		return lp.roleFailed(ctx, t, "The workspace", err)
	}
	defer cleanup()
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
	reply, learned := splitBlock(result.Text, "learned")
	reply, block := splitBlock(reply, "wake")
	reply, unmet := splitBlock(reply, "owner-step")
	reply, answered := splitBlock(reply, "pr-reply")
	reply, described := splitBlock(reply, "pr")
	wakeErrors := lp.applyWakeBlock(ctx, p, t, block)
	prText, problem := parsePRText(described)
	if problem != "" {
		wakeErrors = append(wakeErrors, problem)
	}
	posts, handTo, problems := parsePRReply(answered, writer, t.PROpen())
	wakeErrors = append(wakeErrors, problems...)
	if handTo != nil {
		if _, err := lp.Core.AskAboutPR(ctx, t.ID, writer, handTo.To, handTo.Question); err != nil {
			wakeErrors = append(wakeErrors, "handing the pull request to "+handTo.To+": "+err.Error())
		}
	}
	r, ok := t.Role(writer)
	if ok {
		lp.recordLearned(ctx, p, t, r, m, learned)
	}
	var question string
	if ok && designsFor(t, r) {
		reply, question = splitBlock(reply, "design")
	}
	// The round carried out the request it started with; one made while it
	// ran, even for the same thing, waits for the next round. Where its
	// conversation got to is kept in the same change as what the round
	// produced, so a restart carries on from the last round recorded.
	applied := t.WriterRequest
	took := func(t *core.Task) {
		if t.WriterRequest == applied {
			t.WriterNext = ""
		}
		if ok {
			t.KeepThread(core.RoleImplementer, r, result.Session)
		}
	}
	// Asking for design input ends the turn without a draft: whatever it
	// changed is set aside when the workspace is next reset, and its session
	// resumes with the answer.
	if question != "" {
		return lp.askDesign(ctx, t, writer, question, func(t *core.Task) {
			t.WakeErrors = wakeErrors
			took(t)
		})
	}
	n := len(t.Revisions) + 1
	revision, err := m.snapshot(ctx, t, n)
	if errors.Is(err, gitrepo.ErrNoChange) && proposed(t) {
		_, err = lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
			t.WakeErrors, t.Failures, t.RetryAt = wakeErrors, 0, time.Time{}
			took(t)
			if prText != nil {
				t.Describe(*prText)
			}
			t.Post(posts...)
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
	revision.Summary = text.Clip(reply, 2000)
	h := core.Handoff{DraftCatchUp: integration, Revision: revision, Writer: writer, Session: result.Session, Seen: seen, Reply: reply, Request: applied, WakeErrors: wakeErrors, PR: prText, Posts: posts, Unreachable: parseOwnerSteps(unmet, n, t.Criteria, t.OwnersAlready())}
	if ok {
		r.Learnings = nil
		h.Seat = &r
	}
	return lp.handOff(ctx, t, m, h)
}

// parsePRText reads the implementer's ```pr block: the title and description
// its pull request opens with, or is rewritten to. A problem with it is told
// to the implementer in its next round, as a wake block's is.
func parsePRText(block string) (*core.PRText, string) {
	if block == "" {
		return nil, ""
	}
	var in core.PRText
	if err := json.Unmarshal([]byte(block), &in); err != nil {
		return nil, "the pr block was not valid JSON: " + err.Error()
	}
	in.Title = strings.Join(strings.Fields(in.Title), " ")
	in.Body = strings.TrimSpace(in.Body)
	if in.Title == "" || in.Body == "" {
		return nil, `the pr block needs a "title" and a "body"`
	}
	in.Title, in.Body = text.Clip(in.Title, 120), text.Clip(in.Body, 6000)
	return &in, ""
}

// prHandTo is the teammate an implementer hands a pull request's question
// to, and what it asks them.
type prHandTo struct {
	To       string `json:"to"`
	Question string `json:"question"`
}

// parsePRReply reads the implementer's ```pr-reply block: replies on the
// pull request, in a thread or its conversation, threads its pushed draft
// resolves, and a teammate to hand a question to. Replies wait to be posted
// until the draft they came with is pushed.
func parsePRReply(block, by string, open bool) ([]core.PRPost, *prHandTo, []string) {
	if block == "" {
		return nil, nil, nil
	}
	if !open {
		return nil, nil, []string{"the pr-reply block was for a pull request that isn't open"}
	}
	var in struct {
		Replies []struct {
			Thread string `json:"thread"`
			Body   string `json:"body"`
		} `json:"replies"`
		Resolve []string  `json:"resolve"`
		HandTo  *prHandTo `json:"hand_to"`
	}
	if err := json.Unmarshal([]byte(block), &in); err != nil {
		return nil, nil, []string{"the pr-reply block was not valid JSON: " + err.Error()}
	}
	var posts []core.PRPost
	var problems []string
	for _, r := range in.Replies {
		body := strings.TrimSpace(r.Body)
		if body == "" {
			problems = append(problems, "a pr-reply reply had no body")
			continue
		}
		posts = append(posts, core.PRPost{Thread: strings.TrimSpace(r.Thread), Body: text.Clip(body, 4000), By: by})
	}
	for _, thread := range in.Resolve {
		if thread = strings.TrimSpace(thread); thread != "" {
			posts = append(posts, core.PRPost{Thread: thread, Resolve: true, By: by})
		}
	}
	if h := in.HandTo; h != nil && (strings.TrimSpace(h.To) == "" || strings.TrimSpace(h.Question) == "") {
		problems = append(problems, `a pr-reply hand_to needs "to" and "question"`)
		in.HandTo = nil
	}
	return posts, in.HandTo, problems
}
