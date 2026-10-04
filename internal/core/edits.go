package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/text"
)

// The team keeps a task's objective and criteria in shape as it learns
// more: the researcher and the PM may reword or remove anything, the owner's
// words included; every other role may only add criteria it finds missing.
// Every change is kept, with what it replaced, and the owner can undo any of
// them.

// TaskText is a task's objective and criteria at one moment.
type TaskText struct {
	Objective   string   `json:"objective"`
	Criteria    []string `json:"criteria"`
	OwnerChecks []string `json:"owner_checks,omitempty"`
}

// TaskEdit is one change to a task's objective or criteria: who made it, as
// which role, and the text before and after. An owner's undo is an edit too,
// naming the edit it undoes.
type TaskEdit struct {
	ID     string    `json:"id"`
	By     string    `json:"by"`
	Kind   string    `json:"kind"`
	Before TaskText  `json:"before"`
	After  TaskText  `json:"after"`
	Undoes string    `json:"undoes,omitempty"`
	At     time.Time `json:"at"`
	// Pending evidence changed by this edit. Undo merges only these entries
	// into the current draft, keeping unrelated and newer evidence intact.
	Settled  []Unreachable `json:"settled,omitempty"`
	Restored []Unreachable `json:"restored,omitempty"`
}

// EditInput is one role's change to a task. An empty Objective keeps it;
// nil Criteria keeps them, and otherwise replaces them; Add adds to them.
type EditInput struct {
	Project, Task string
	// By is who edits, as they are named on the team; Kind is the role
	// they edit as, which decides what they may change.
	By, Kind string
	// While, when set, is the status the task must still be in: a role's
	// turn changes nothing once its task has moved on without it.
	While     string
	Objective string
	Criteria  []string
	Add       []string
	// OwnerChecks adds exact owner undertakings to the after-landing list.
	// Only the PM and researcher may add these.
	OwnerChecks []string
	// TextVersion fences replacements made while folding owner answers.
	// Additive edits need no version and merge into the current task.
	TextVersion *int
}

// Limits on what an edit keeps.
const (
	maxObjective = 1000
	maxCriterion = 500
	maxCriteria  = 40
)

// Rewrites reports whether a role may reword and remove, not only add.
func Rewrites(kind string) bool { return kind == RoleResearcher || kind == RolePM }

func (t Task) text() TaskText {
	return TaskText{Objective: t.Objective, Criteria: slices.Clone(t.Criteria), OwnerChecks: slices.Clone(t.OwnerChecks)}
}

func (a TaskText) same(b TaskText) bool {
	return a.Objective == b.Objective && slices.Equal(a.Criteria, b.Criteria) && slices.Equal(a.OwnerChecks, b.OwnerChecks)
}

// EditTask changes a task's objective or criteria as a team role may,
// keeping what it replaced. A finished task is left alone.
func (s *Service) EditTask(ctx context.Context, in EditInput) (Task, error) {
	if !Rewrites(in.Kind) && (strings.TrimSpace(in.Objective) != "" || in.Criteria != nil || len(in.OwnerChecks) > 0) {
		return Task{}, fmt.Errorf("as the %s you may only add requirements: %w", in.Kind, ErrConflict)
	}
	var out Task
	err := s.store.update(ctx, func(v *Snapshot) error {
		t := task(v, strings.TrimSpace(in.Task))
		switch {
		case t == nil || t.ProjectID != in.Project:
			return ErrNotFound
		case in.While != "" && t.Status != in.While:
			return fmt.Errorf("the task has moved on, so this changes nothing more: %w", ErrConflict)
		case t.Finished():
			return fmt.Errorf("“%s” has finished, so the team no longer changes it: %w", t.Objective, ErrConflict)
		}
		if in.TextVersion != nil && *in.TextVersion != t.TextVersion {
			return fmt.Errorf("task text changed; read_task before retrying: %w", ErrConflict)
		}
		if in.Criteria != nil && len(in.OwnerChecks) > 0 && in.TextVersion == nil {
			return fmt.Errorf("owner-answer replacement requires text_version: %w", ErrConflict)
		}
		after := t.text()
		p := project(v, t.ProjectID)
		if objective := strings.TrimSpace(in.Objective); objective != "" {
			after.Objective = text.Clip(objective, maxObjective)
		}
		if in.Criteria != nil {
			after.Criteria = nil
			in.Add = append(slices.Clone(in.Criteria), in.Add...)
		}
		for _, c := range cleanList(in.Add) {
			// Existing owner-entered criteria can exceed the team input
			// limit. Copy them verbatim when replacing or replaying them.
			if !slices.Contains(t.Criteria, c) {
				c = text.Clip(c, maxCriterion)
			}
			if !slices.Contains(after.Criteria, c) {
				after.Criteria = append(after.Criteria, c)
			}
		}
		for _, c := range cleanList(in.OwnerChecks) {
			if slices.Contains(after.OwnerChecks, c) || slices.Contains(t.OwnerSteps, c) {
				continue
			}
			// Transfer only exact pre-edit stored criteria. New input in this
			// request cannot manufacture an exemption from the input limit.
			stored := slices.Contains(t.Criteria, c) || (p != nil && slices.Contains(p.Brief.Criteria, c))
			if len([]rune(c)) > maxCriterion && !stored {
				return fmt.Errorf("owner check exceeds %d characters", maxCriterion)
			}
			after.OwnerChecks = append(after.OwnerChecks, c)
		}
		// Creation and other owner-check paths permit longer lists. Keep
		// existing lists usable for team edits and duplicate replays.
		if len(after.OwnerChecks) > maxCriteria && len(after.OwnerChecks) > len(t.OwnerChecks) {
			return fmt.Errorf("a task keeps at most %d owner checks", maxCriteria)
		}
		if len(in.OwnerChecks) > 0 {
			after.Criteria = slices.DeleteFunc(after.Criteria, func(c string) bool {
				return slices.Contains(after.OwnerChecks, c) || (slices.Contains(in.OwnerChecks, c) && slices.Contains(t.OwnerSteps, c))
			})
			if t.text().same(after) {
				out = *t
				return nil
			}
		}
		if len(after.Criteria) > maxCriteria && len(after.Criteria) > len(t.Criteria) {
			return fmt.Errorf("a task keeps at most %d requirements", maxCriteria)
		}
		if after.Criteria == nil {
			after.Criteria = []string{}
		}
		return s.applyEdit(v, t, TaskEdit{By: in.By, Kind: in.Kind, After: after}, &out)
	})
	return out, err
}

// KeepSettledEvidence retains a late report on the edit that transferred its
// requirement. Handoff commit and restart recovery share this path. Undo can
// then restore current-draft evidence under the same fences as an ordinary fold.
func (t *Task) KeepSettledEvidence(u Unreachable) {
	for i := len(t.Edits) - 1; i >= 0; i-- {
		e := &t.Edits[i]
		removed := slices.Contains(e.Before.Criteria, u.Criterion) && !slices.Contains(e.After.Criteria, u.Criterion)
		owned := !slices.Contains(e.Before.OwnerChecks, u.Criterion) && slices.Contains(e.After.OwnerChecks, u.Criterion)
		if removed || owned {
			if !slices.Contains(e.Settled, u) {
				e.Settled = append(e.Settled, u)
			}
			return
		}
	}
}

// UndoTaskEdit puts a task's objective and criteria back as they were
// before one edit, for the owner. The undo is kept as an edit too, so it
// can be undone in turn.
func (s *Service) UndoTaskEdit(ctx context.Context, projectID, taskID, editID string) (Task, error) {
	var out Task
	err := s.store.update(ctx, func(v *Snapshot) error {
		t := task(v, taskID)
		if t == nil || t.ProjectID != projectID {
			return ErrNotFound
		}
		i := slices.IndexFunc(t.Edits, func(e TaskEdit) bool { return e.ID == editID })
		if i < 0 {
			return fmt.Errorf("there is no such change to undo: %w", ErrNotFound)
		}
		if t.Finished() {
			return fmt.Errorf("this request has finished: %w", ErrConflict)
		}
		before := t.Edits[i].Before
		checks := slices.Clone(t.OwnerChecks)
		if !slices.Equal(before.OwnerChecks, t.Edits[i].After.OwnerChecks) {
			checks = slices.Clone(before.OwnerChecks)
		}
		restore := []Unreachable{}
		p := project(v, t.ProjectID)
		// Undo restores a whole text snapshot, potentially reactivating work
		// transferred by later edits. Late handoffs may also have retained
		// evidence on a redo rather than the original transfer. Recover the
		// newest retained evidence for all reactivated criteria; applyEdit
		// still fences by draft and preserves evidence already on the task.
		for j := len(t.Edits) - 1; j >= 0; j-- {
			for _, u := range t.Edits[j].Settled {
				if teamRequirement(p, before.Criteria, checks, t.OwnerSteps, t.OwnerTook, u.Criterion) &&
					!teamRequirement(p, t.Criteria, t.OwnerChecks, t.OwnerSteps, t.OwnerTook, u.Criterion) {
					restore = append(restore, u)
				}
			}
		}
		return s.applyEdit(v, t, TaskEdit{By: FromOwner, Kind: FromOwner, After: TaskText{Objective: before.Objective, Criteria: slices.Clone(before.Criteria), OwnerChecks: checks}, Undoes: editID, Restored: restore, Settled: slices.Clone(t.Edits[i].Restored)}, &out)
	})
	return out, err
}

// Explicit task criteria can put previously owner-assigned work back on the
// team. Inherited criteria remain assigned to the owner under their original
// wording even when the checklist step has been reworded.
func teamRequirement(p *Project, criteria, checks, steps, took []string, c string) bool {
	if slices.Contains(checks, c) || slices.Contains(steps, c) {
		return false
	}
	return slices.Contains(criteria, c) || (p != nil && slices.Contains(p.Brief.Criteria, c) && !slices.Contains(took, c))
}

// applyEdit keeps e on t and puts its text in place, within a change.
func (s *Service) applyEdit(v *Snapshot, t *Task, e TaskEdit, out *Task) error {
	e.After.Criteria = slices.DeleteFunc(slices.Clone(e.After.Criteria), func(c string) bool { return slices.Contains(e.After.OwnerChecks, c) })
	e.Before = t.text()
	if e.Before.same(e.After) {
		return errors.New("that changes nothing")
	}
	now := s.now().UTC()
	e.ID, e.At = uid(), now
	pendingBefore := slices.Clone(t.Unreachable)
	restore, settle := e.Restored, e.Settled
	t.Objective, t.Criteria, t.OwnerChecks = e.After.Objective, e.After.Criteria, slices.Clone(e.After.OwnerChecks)
	// Planning moves and answer folds share this atomic boundary. The
	// current draft must not re-escalate a requirement assigned to the owner.
	for _, criterion := range append(slices.Clone(t.OwnerChecks), t.OwnerSteps...) {
		t.SettleUnreachable(criterion)
	}
	// A mixed replacement may quote a new owner undertaking instead of
	// the old criterion. A removed task criterion is no longer pending;
	// pending brief criteria and retained task criteria remain untouched.
	p := project(v, t.ProjectID)
	for _, criterion := range e.Before.Criteria {
		if !slices.Contains(t.Criteria, criterion) && (p == nil || !slices.Contains(p.Brief.Criteria, criterion)) {
			t.SettleUnreachable(criterion)
		}
	}
	t.Unreachable = slices.DeleteFunc(t.Unreachable, func(u Unreachable) bool { return slices.Contains(settle, u) })
	for _, u := range restore {
		if len(t.Revisions) == 0 || u.Revision != t.Revisions[len(t.Revisions)-1].N {
			continue
		}
		if !teamRequirement(p, t.Criteria, t.OwnerChecks, t.OwnerSteps, t.OwnerTook, u.Criterion) {
			continue
		}
		if !slices.ContainsFunc(t.Unreachable, func(current Unreachable) bool {
			return ReportKey(current) == ReportKey(u) && current.Revision == u.Revision
		}) {
			t.Unreachable = append(t.Unreachable, u)
		}
	}
	e.Settled, e.Restored = nil, nil
	for _, u := range pendingBefore {
		if !slices.Contains(t.Unreachable, u) {
			e.Settled = append(e.Settled, u)
		}
	}
	for _, u := range t.Unreachable {
		if !slices.Contains(pendingBefore, u) {
			e.Restored = append(e.Restored, u)
		}
	}
	t.Edits = append(t.Edits, e)
	// Every check made against the old text is asked for again.
	t.TextVersion++
	t.UpdatedAt = now
	what := "edited the requirements of"
	if e.Undoes != "" {
		what = "undid a change to"
	}
	recordTask(v, now, t, "task.edited", fmt.Sprintf("%s %s %s", e.By, what, t.Objective))
	reconcileAssetIntegration(t, p)
	derive(v, t)
	*out = *t
	return nil
}
