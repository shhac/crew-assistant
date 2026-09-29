package core

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/shhac/crew-assistant/internal/config"
)

// Thread is one team member's conversation on one task, in one kind of role.
// It belongs to the task, so nothing said in it reaches another task, and to
// the member, so another member's seat never takes it over: that seat starts
// its own from the task's record. What a member learns is kept on the member
// instead, and reaches every task.
type Thread struct {
	Kind string `json:"kind"`
	// Member is the team member whose conversation it is; a seat not copied
	// from a member keys its thread by Seat instead.
	Member string `json:"member,omitempty"`
	// Session resumes the conversation in its engine.
	Session json.RawMessage `json:"session"`
	// Engine and Model are what it ran on; a seat on anything else starts
	// afresh rather than carrying it on.
	Engine string `json:"engine"`
	Model  string `json:"model,omitempty"`
	// Seat is the seat that last used it.
	Seat string `json:"seat"`
}

// of reports whether the thread is the one this seat keeps for a kind of
// role: a member's seats share one, and a seat of no member has its own.
func (th Thread) of(kind string, r Role) bool {
	if th.Kind != kind || th.Member != r.Member {
		return false
	}
	return r.Member != "" || th.Seat == r.Name
}

// Thread is the conversation a seat keeps on this task for a kind of role,
// if it has one.
func (t Task) Thread(kind string, r Role) (Thread, bool) {
	for _, th := range t.Threads {
		if th.of(kind, r) && len(th.Session) > 0 {
			return th, true
		}
	}
	return Thread{}, false
}

// Writer is the implementer seat whose thread on the task carries on: the
// seat that last used it if it is still on the task, or else another seat
// of the same member. It reports false when no implementer seat has one.
func (t Task) Writer() (Role, bool) {
	var found Role
	ok := false
	for _, r := range t.RolesOf(RoleImplementer) {
		th, has := t.Thread(RoleImplementer, r)
		if !has {
			continue
		}
		if th.Seat == r.Name {
			return r, true
		}
		if !ok {
			found, ok = r, true
		}
	}
	return found, ok
}

// Resumable is the session a seat carries its thread on with, or nil when
// it has none or it ran on another engine or model and so starts afresh.
func (t Task) Resumable(kind string, r Role) json.RawMessage {
	th, ok := t.Thread(kind, r)
	if !ok || th.Engine != r.Engine || th.Model != r.Model {
		return nil
	}
	return th.Session
}

// KeepThread records where a seat's conversation for a kind of role got to,
// replacing what the seat's member had on this task and leaving every other
// member's thread as it was.
func (t *Task) KeepThread(kind string, r Role, session json.RawMessage) {
	th := Thread{Kind: kind, Member: r.Member, Session: session, Engine: r.Engine, Model: r.Model, Seat: r.Name}
	for i := range t.Threads {
		if t.Threads[i].of(kind, r) {
			t.Threads[i] = th
			return
		}
	}
	t.Threads = append(t.Threads, th)
}

// What to do with a task implementer's conversation at its next round.
const (
	// WriterCompact has the session compact its own context first. Only Codex
	// can: Claude Code offers no compaction to a session driven from outside.
	WriterCompact = "compact"
	// WriterFresh starts a new session. Nothing is lost that the round needs:
	// its prompt carries the brief, the latest draft and every review.
	WriterFresh = "new"
)

// SetWriterNext asks for a task implementer's conversation to be compacted or
// started afresh. The implementer's session is only ever used at the start of
// a round, so the request waits for the next one; a round already under way
// finishes as it is. Reviewers and QA start fresh every time and a project's
// manager is asked one question at a time, so the implementer is the only
// team member with a conversation to act on.
func (s *Service) SetWriterNext(ctx context.Context, projectID, taskID, next string) (Task, error) {
	if next != WriterCompact && next != WriterFresh {
		return Task{}, fmt.Errorf("the implementer's conversation can be compacted or started afresh: %w", ErrChatValidation)
	}
	return s.UpdateTask(ctx, taskID, func(t *Task, _ *Project) (string, error) {
		if t.ProjectID != projectID {
			return "", ErrNotFound
		}
		if t.Finished() {
			return "", fmt.Errorf("the task is finished; its implementer won't run again: %w", ErrConflict)
		}
		// The request is for the thread an implementer seat now on the task
		// would carry on; one another member left on the task isn't its to
		// act on.
		writer, started := t.Writer()
		if !started {
			return "", fmt.Errorf("the implementer has no conversation yet; its first round starts one: %w", ErrConflict)
		}
		if next == WriterCompact && !config.Supports(writer.Engine, config.UseCompact) {
			return "", fmt.Errorf("%s runs on %s, which can't be compacted from outside; start its conversation afresh instead, which loses nothing its next round needs: %w", writer.Name, writer.Engine, ErrConflict)
		}
		t.WriterNext = next
		t.WriterRequest++
		if next == WriterCompact {
			return fmt.Sprintf("%s will compact its conversation at its next round of %s", writer.Name, t.Objective), nil
		}
		return fmt.Sprintf("%s will start afresh at its next round of %s", writer.Name, t.Objective), nil
	})
}
