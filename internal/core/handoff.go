package core

import "encoding/json"

// Handoff is a revision on its way to being recorded. A commit made in a
// task's own clone exists only there, so the revision is recorded only once
// the project's clone holds it under Name. The intent is stored before that
// ref is written, and the revision is appended from it once the ref is
// verified; a daemon stopped in between finds it on start and finishes or
// drops it. It carries everything the turn's outcome changes on the task, so
// finishing it never runs the turn again.
type Handoff struct {
	// Name is the ref the revision is kept under, unique to this attempt.
	Name string `json:"name"`
	// Revision is what is appended, with Ref the commit being handed over.
	Revision Revision `json:"revision"`
	// Writer is the implementer seat whose draft it is, and Seat the seat as
	// it ran, if it is on the team, with where its conversation got to, how
	// much of the owner's direction its prompt carried, its reply, and the
	// request about its thread it carried out.
	Writer     string          `json:"writer,omitempty"`
	Seat       *Role           `json:"seat,omitempty"`
	Session    json.RawMessage `json:"session,omitempty"`
	Seen       int             `json:"seen,omitempty"`
	Reply      string          `json:"reply,omitempty"`
	Request    int             `json:"request,omitempty"`
	WakeErrors []string        `json:"wake_errors,omitempty"`
	// CatchUp is set for a clean merge with landed work the daemon made
	// itself, with no implementer.
	CatchUp *CatchUp `json:"catch_up,omitempty"`
}

// CatchUp is what a clean catch-up changes besides adding its revision.
type CatchUp struct {
	// Base and From are where the task stands on the line it took in.
	Base string `json:"base,omitempty"`
	From string `json:"from,omitempty"`
	// Carry keeps the reviewers' passes on the revision it merged: not so
	// for commits someone outside the team pushed.
	Carry  bool   `json:"carry,omitempty"`
	Name   string `json:"name"`
	What   string `json:"what"`
	Detail string `json:"detail"`
}
