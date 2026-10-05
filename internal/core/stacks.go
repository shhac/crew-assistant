package core

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Stacking lets a task build on another task's open pull request instead of
// waiting for it to land: when the target branch is frozen, or when the next
// piece of work needs the one under review. The stacked task starts from the
// pull request's branch, opens its own pull request onto that branch, and
// merges only after it, replayed onto the target once the one below has
// merged. One task stacks on one other at most, in the same project, and
// only where the project lands by pull request with stacking on.
const (
	// RelationStacksOn is a task stacked on another's pull request.
	RelationStacksOn = "stacks_on"
	// RelationCarries is stacking seen from the other end. It is never made,
	// only taken away.
	RelationCarries = "carries"
)

// DecisionUnstack asks the owner what becomes of a stacked task whose
// parent will not merge: its pull request was closed, or it was stopped.
const (
	DecisionUnstack = "unstack"
	ChoiceUnstack   = "Rebase it onto the target"
)

// StackParent is the task a stacked task builds on, as it is now: its
// status, and its pull request's branch, the head last pushed there and
// number.
type StackParent struct {
	Task      string `json:"task"`
	Ref       string `json:"ref,omitempty"`
	Objective string `json:"objective"`
	Status    string `json:"status"`
	Branch    string `json:"branch,omitempty"`
	Head      string `json:"head,omitempty"`
	Number    int    `json:"number,omitempty"`
}

// Merged reports a parent whose pull request merged.
func (s StackParent) Merged() bool { return s.Status == TaskLanded }

// Finished reports a parent that will do nothing more.
func (s StackParent) Finished() bool {
	return s.Status == TaskLanded || s.Status == TaskDelivered || s.Status == TaskStopped
}

// Open reports a parent whose pull request is open with a head pushed to
// build on.
func (s StackParent) Open() bool { return !s.Finished() && s.Number > 0 && s.Head != "" }

// OnParent reports a stacked task that sits on its parent's pull request
// branch: it started there, or caught up with it last, and has not yet
// been replayed onto the target.
func (t Task) OnParent() bool {
	return t.Stack != nil && t.Stack.Branch != "" && t.From == t.Stack.Branch
}

// StackBroken says why a task that sits on its parent's branch can't go on
// there, or "" when it can: the parent was stopped, or its pull request
// was closed without merging.
func (t Task) StackBroken() string {
	if !t.OnParent() || t.Stack.Merged() {
		return ""
	}
	switch {
	case t.Stack.Finished():
		return fmt.Sprintf("“%s”, which it is stacked on, was stopped", t.Stack.Objective)
	case t.Stack.Number == 0:
		return fmt.Sprintf("the pull request of “%s”, which it is stacked on, was closed without merging", t.Stack.Objective)
	}
	return ""
}

// stackHold is why a task stacked on another can't merge yet: it still sits
// on its parent's branch.
func stackHold(t Task) string {
	switch {
	case !t.OnParent():
		return ""
	case t.Stack.Merged():
		return fmt.Sprintf("“%s”, which it is stacked on, merged, and it is still to be replayed onto the target", t.Stack.Objective)
	}
	return fmt.Sprintf("it is stacked on “%s”, whose pull request has not merged", t.Stack.Objective)
}

// Unstack takes the task off the task it is stacked on. One that sat on
// its parent's branch goes on from the target, replaying only its own
// change onto it.
func (t *Task) Unstack() {
	if t.StacksOn == "" {
		return
	}
	unmark(t, RelationStacksOn, t.StacksOn)
	t.StacksOn, t.Stack = "", nil
}

// wakeStackedOn has the tasks stacked on t, waiting on their pull requests,
// look again now that t has finished, within the change that finished it:
// they go on from where it merged, or their owner decides.
func wakeStackedOn(v *Snapshot, t *Task) {
	for i := range v.Tasks {
		c := &v.Tasks[i]
		if c.StacksOn == t.ID && c.Status == TaskAwaiting && c.UsesPRs() {
			c.Status, c.Detail = TaskLanding, fmt.Sprintf("“%s”, which it is stacked on, is %s", t.Objective, t.Status)
			derive(v, c)
		}
	}
}

func stackParent(v *Snapshot, t Task) *StackParent {
	if t.StacksOn == "" {
		return nil
	}
	parent := task(v, t.StacksOn)
	if parent == nil {
		return nil
	}
	out := &StackParent{Task: parent.ID, Objective: parent.Objective, Status: parent.Status}
	if p := project(v, parent.ProjectID); p != nil {
		out.Ref = p.TaskRef(parent.Number)
	}
	if prop := parent.Proposal; prop != nil {
		out.Branch, out.Head, out.Number = prop.Branch, prop.Pushed, prop.Number
	}
	return out
}

// stackWait is the parent a stacked task waits for before it starts: one
// that has not finished and has no open pull request yet to build on.
func stackWait(v *Snapshot, t Task) *Task {
	s := stackParent(v, t)
	if s == nil || t.Base != "" || s.Finished() || s.Open() {
		return nil
	}
	return task(v, s.Task)
}

// stackOn stacks t on the task id names, within a change, for by.
func stackOn(v *Snapshot, t *Task, id, by string, now time.Time) error {
	parent := task(v, strings.TrimSpace(id))
	p := project(v, t.ProjectID)
	switch {
	case parent == nil || parent.ProjectID != t.ProjectID:
		return fmt.Errorf("there is no task %q in this project: %w", id, ErrNotFound)
	case parent.ID == t.ID:
		return errors.New("a task cannot be stacked on itself")
	case p == nil || p.Playbook == nil || !p.Playbook.Land.PullRequests || !p.Playbook.Land.Stack:
		return fmt.Errorf("stacking is off for this project; turn it on in its landing settings, with pull requests, first: %w", ErrConflict)
	case t.StacksOn == parent.ID:
		return nil
	case t.StacksOn != "":
		return fmt.Errorf("“%s” is already stacked on another task, and a task stacks on one at most; unstack it first: %w", t.Objective, ErrConflict)
	case t.Finished():
		return fmt.Errorf("“%s” has finished, so it stacks on nothing: %w", t.Objective, ErrConflict)
	case t.Base != "":
		return fmt.Errorf("“%s” has begun its work from its own starting point; only a task that has not begun can be stacked: %w", t.Objective, ErrConflict)
	case parent.Finished():
		return fmt.Errorf("“%s” has finished, so there is nothing to stack on: %w", parent.Objective, ErrConflict)
	case parent.Playbook != nil && !parent.UsesPRs():
		return fmt.Errorf("“%s” lands without a pull request, so nothing can be stacked on it: %w", parent.Objective, ErrConflict)
	case reaches(v, parent.ID, t.ID, map[string]bool{}):
		return fmt.Errorf("“%s” already builds on this task, so this task cannot be stacked on it: %w", parent.Objective, ErrConflict)
	}
	t.StacksOn = parent.ID
	mark(t, RelationStacksOn, parent.ID, by, now)
	return nil
}

// unstack takes the stacking link between child and parent away, for by. The
// team may take away only its own, and only before the child has begun.
func unstack(child, parent *Task, by string) error {
	if !overrules(by) && child.HeldByOwner(RelationStacksOn, parent.ID) {
		return ownersLink(child, parent)
	}
	if !overrules(by) && child.Base != "" {
		return fmt.Errorf("“%s” has begun its work on “%s”; only the owner or assistant can unstack it now: %w", child.Objective, parent.Objective, ErrConflict)
	}
	child.Unstack()
	return nil
}
