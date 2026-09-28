package core

import "encoding/json"

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
