package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// How one task relates to another. Depending on a task holds this one back
// until that one finishes; blocking is the same link seen from the other
// end. Relating is only a pointer, for whoever works on either to look at.
const (
	RelationDependsOn = "depends_on"
	RelationBlocks    = "blocks"
	RelationRelatesTo = "relates_to"
)

// Who set a link. The owner's and the assistant's links hold against the
// team: the PM or a role may add links and take back its own, but never one
// the owner or assistant set. Team members are "member:<id>", and a seat
// with no member is "role:<kind>".
const (
	LinkedByOwner     = "owner"
	LinkedByAssistant = "assistant"
	LinkedByPM        = "pm"
)

// LinkMark records who set a link and when. A link with no mark was set
// before links were marked, by the researcher or the PM, and counts as the
// team's.
type LinkMark struct {
	By string    `json:"by"`
	At time.Time `json:"at"`
}

// TeamLinker is how a team member's links are marked.
func TeamLinker(memberID, kind string) string {
	if memberID != "" {
		return "member:" + memberID
	}
	return "role:" + kind
}

func linkKey(relation, id string) string { return relation + ":" + id }

// overrules says whether by speaks for the owner, whose links the team
// leaves alone.
func overrules(by string) bool { return by == LinkedByOwner || by == LinkedByAssistant }

// HeldByOwner says whether the owner or the assistant set t's link to id,
// which the team leaves alone.
func (t Task) HeldByOwner(relation, id string) bool {
	return overrules(t.LinkedBy[linkKey(relation, id)].By)
}

func mark(t *Task, relation, id, by string, now time.Time) {
	if t.LinkedBy == nil {
		t.LinkedBy = map[string]LinkMark{}
	}
	t.LinkedBy[linkKey(relation, id)] = LinkMark{By: by, At: now}
}

func unmark(t *Task, relation, id string) {
	delete(t.LinkedBy, linkKey(relation, id))
	if len(t.LinkedBy) == 0 {
		t.LinkedBy = nil
	}
}

// markAll marks each of ids t depends on that has no mark yet.
func markAll(t *Task, ids []string, by string, now time.Time) {
	for _, id := range ids {
		if _, ok := t.LinkedBy[linkKey(RelationDependsOn, id)]; !ok {
			mark(t, RelationDependsOn, id, by, now)
		}
	}
}

// linked says how a and b are already linked, if at all: one relation per
// pair keeps what the owner sees unambiguous.
func linked(a, b Task) string {
	switch {
	case slices.Contains(a.DependsOn, b.ID):
		return RelationDependsOn
	case slices.Contains(b.DependsOn, a.ID):
		return RelationBlocks
	case slices.Contains(a.RelatesTo, b.ID):
		return RelationRelatesTo
	}
	return ""
}

// Link is one change to how two tasks of a project relate.
type Link struct {
	Project, Task, Other string
	// Relation is what Task is to Other: it depends on it, blocks it, or
	// relates to it. Unlinking takes away whichever it is.
	Relation string
	// By is who asks, which decides who may undo it.
	By string
	// Relations, when set, are the only relations By may make or take
	// away, as a team role's are.
	Relations []string
	// While, when set, is the status Task must still be in: a role's turn
	// changes nothing once its task has moved on without it.
	While string
}

// LinkTasks relates two tasks. A team member may make a task depend on
// another only while it has not begun its work, and never change a finished
// task.
func (s *Service) LinkTasks(ctx context.Context, l Link) (Task, error) {
	var out Task
	err := s.store.update(ctx, func(v *Snapshot) error {
		t, other, err := pair(v, l)
		if err != nil {
			return err
		}
		if existing := linked(*t, *other); existing != "" {
			return fmt.Errorf("“%s” and “%s” are already linked (%s); unlink them first: %w", t.Objective, other.Objective, relationWords(existing), ErrConflict)
		}
		if err := l.allows(l.Relation); err != nil {
			return err
		}
		now := s.now().UTC()
		switch l.Relation {
		case RelationDependsOn, RelationBlocks:
			dependent, dep := orient(l.Relation, t, other)
			if err := mayWait(*dependent, l.By); err != nil {
				return err
			}
			deps, err := dependencies(v, *dependent, append(slices.Clone(dependent.DependsOn), dep.ID))
			if err != nil {
				return err
			}
			dependent.DependsOn = deps
			mark(dependent, RelationDependsOn, dep.ID, l.By, now)
			record(v, now, t.ProjectID, "task.linked", fmt.Sprintf("%s waits for %s", dependent.Objective, dep.Objective))
		case RelationRelatesTo:
			t.RelatesTo = append(t.RelatesTo, other.ID)
			other.RelatesTo = append(other.RelatesTo, t.ID)
			mark(t, RelationRelatesTo, other.ID, l.By, now)
			mark(other, RelationRelatesTo, t.ID, l.By, now)
			record(v, now, t.ProjectID, "task.linked", fmt.Sprintf("%s relates to %s", t.Objective, other.Objective))
		default:
			return fmt.Errorf("a task depends on, blocks or relates to another, not %q", l.Relation)
		}
		t.UpdatedAt, other.UpdatedAt = now, now
		linksChanged(v, t.ProjectID, l.By)
		derive(v, t)
		out = *t
		return nil
	})
	return out, err
}

// UnlinkTasks takes away whatever links two tasks. The team may take away
// only links the team set.
func (s *Service) UnlinkTasks(ctx context.Context, l Link) (Task, error) {
	var out Task
	err := s.store.update(ctx, func(v *Snapshot) error {
		t, other, err := pair(v, l)
		if err != nil {
			return err
		}
		relation := linked(*t, *other)
		if relation == "" {
			return fmt.Errorf("“%s” and “%s” are not linked: %w", t.Objective, other.Objective, ErrNotFound)
		}
		if err := l.allows(relation); err != nil {
			return err
		}
		if relation == RelationRelatesTo {
			if !overrules(l.By) && t.HeldByOwner(RelationRelatesTo, other.ID) {
				return ownersLink(t, other)
			}
			t.RelatesTo = slices.DeleteFunc(t.RelatesTo, func(id string) bool { return id == other.ID })
			other.RelatesTo = slices.DeleteFunc(other.RelatesTo, func(id string) bool { return id == t.ID })
			unmark(t, RelationRelatesTo, other.ID)
			unmark(other, RelationRelatesTo, t.ID)
		} else {
			dependent, dep := orient(relation, t, other)
			if !overrules(l.By) && dependent.HeldByOwner(RelationDependsOn, dep.ID) {
				return ownersLink(t, other)
			}
			dependent.DependsOn = slices.DeleteFunc(dependent.DependsOn, func(id string) bool { return id == dep.ID })
			unmark(dependent, RelationDependsOn, dep.ID)
		}
		now := s.now().UTC()
		t.UpdatedAt, other.UpdatedAt = now, now
		record(v, now, t.ProjectID, "task.unlinked", fmt.Sprintf("%s and %s are no longer linked", t.Objective, other.Objective))
		linksChanged(v, t.ProjectID, l.By)
		derive(v, t)
		out = *t
		return nil
	})
	return out, err
}

// allows refuses a relation l's asker may not make or take away.
func (l Link) allows(relation string) error {
	if l.Relations != nil && !slices.Contains(l.Relations, relation) {
		return fmt.Errorf("you may link or unlink only as %s: %w", strings.Join(l.Relations, ", "), ErrConflict)
	}
	return nil
}

// orient is which of two linked tasks waits for which: blocking is
// depending seen from the other end.
func orient(relation string, t, other *Task) (dependent, dep *Task) {
	if relation == RelationBlocks {
		return other, t
	}
	return t, other
}

// pair is the two tasks a link joins, both in l's project, provided the
// asker may still change the first.
func pair(v *Snapshot, l Link) (*Task, *Task, error) {
	t, other := task(v, strings.TrimSpace(l.Task)), task(v, strings.TrimSpace(l.Other))
	switch {
	case t == nil || t.ProjectID != l.Project:
		return nil, nil, ErrNotFound
	case other == nil || other.ProjectID != t.ProjectID:
		return nil, nil, fmt.Errorf("there is no task %q in this project: %w", l.Other, ErrNotFound)
	case other.ID == t.ID:
		return nil, nil, errors.New("a task cannot be linked to itself")
	case l.While != "" && t.Status != l.While:
		return nil, nil, fmt.Errorf("the task has moved on, so this changes nothing more: %w", ErrConflict)
	case !overrules(l.By) && t.Finished():
		return nil, nil, fmt.Errorf("“%s” has finished, so the team no longer changes it: %w", t.Objective, ErrConflict)
	}
	return t, other, nil
}

// mayWait refuses to hold back a task that cannot wait any more: one that
// has finished, and for the team, one whose work has begun, since the team
// would be stopping work the owner already has under way.
func mayWait(t Task, by string) error {
	if t.Finished() {
		return fmt.Errorf("“%s” has finished, so it waits for nothing: %w", t.Objective, ErrConflict)
	}
	if !overrules(by) && t.Status != TaskQueued && t.Status != TaskResearching {
		return fmt.Errorf("“%s” has begun its work; only the owner or assistant can make it wait now: %w", t.Objective, ErrConflict)
	}
	return nil
}

func ownersLink(t, other *Task) error {
	return fmt.Errorf("the owner linked “%s” and “%s”; only the owner or assistant can unlink them: %w", t.Objective, other.Objective, ErrConflict)
}

// linksChanged asks the PM to look at the list again, unless the PM made
// the change: its own links must not bring it back to look forever.
func linksChanged(v *Snapshot, projectID, by string) {
	if by == LinkedByPM {
		return
	}
	if p := project(v, projectID); p != nil {
		p.listChanged()
	}
}

func relationWords(relation string) string {
	switch relation {
	case RelationDependsOn:
		return "the first waits for the second"
	case RelationBlocks:
		return "the second waits for the first"
	}
	return "they relate"
}

// pmSetsDepends makes proposed the team's part of what t waits for, keeping
// what the owner or assistant set, and says whether that changed anything.
// A PM that got the whole list wrong changes nothing, rather than releasing
// the task; and once t has begun its work, the PM can take the team's
// dependencies away but not add any, as a role cannot.
func pmSetsDepends(v *Snapshot, t *Task, proposed []string, now time.Time) bool {
	team, owners := teamDependsOn(*t)
	proposed = slices.DeleteFunc(slices.Clone(proposed), func(dep string) bool { return slices.Contains(owners, dep) })
	if mayWait(*t, LinkedByPM) != nil {
		proposed = slices.DeleteFunc(proposed, func(dep string) bool { return !slices.Contains(team, dep) })
	}
	deps := possibleDependencies(v, Task{ID: t.ID, ProjectID: t.ProjectID}, proposed)
	if (len(deps) == 0 && len(proposed) > 0) || slices.Equal(deps, team) {
		return false
	}
	for _, dep := range team {
		if !slices.Contains(deps, dep) {
			unmark(t, RelationDependsOn, dep)
		}
	}
	t.DependsOn = append(slices.Clone(owners), deps...)
	markAll(t, deps, LinkedByPM, now)
	return true
}

// teamDependsOn is the part of t's dependencies the team set, which the PM
// may replace; the owner's and the assistant's are kept.
func teamDependsOn(t Task) (team, owners []string) {
	for _, id := range t.DependsOn {
		if t.HeldByOwner(RelationDependsOn, id) {
			owners = append(owners, id)
		} else {
			team = append(team, id)
		}
	}
	return team, owners
}

// blocking is, for each task, the unfinished and finished tasks that depend
// on it, worked out once for the whole snapshot rather than per task.
func blocking(v *Snapshot) map[string][]string {
	out := map[string][]string{}
	for _, t := range v.Tasks {
		for _, id := range t.DependsOn {
			out[id] = append(out[id], t.ID)
		}
	}
	return out
}
