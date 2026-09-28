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
	Objective string   `json:"objective"`
	Criteria  []string `json:"criteria"`
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
	return TaskText{Objective: t.Objective, Criteria: slices.Clone(t.Criteria)}
}

func (a TaskText) same(b TaskText) bool {
	return a.Objective == b.Objective && slices.Equal(a.Criteria, b.Criteria)
}

// EditTask changes a task's objective or criteria as a team role may,
// keeping what it replaced. A finished task is left alone.
func (s *Service) EditTask(ctx context.Context, in EditInput) (Task, error) {
	if !Rewrites(in.Kind) && (strings.TrimSpace(in.Objective) != "" || in.Criteria != nil) {
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
		after := t.text()
		if objective := strings.TrimSpace(in.Objective); objective != "" {
			after.Objective = text.Clip(objective, maxObjective)
		}
		if in.Criteria != nil {
			after.Criteria = nil
			in.Add = append(slices.Clone(in.Criteria), in.Add...)
		}
		for _, c := range cleanList(in.Add) {
			if c = text.Clip(c, maxCriterion); !slices.Contains(after.Criteria, c) {
				after.Criteria = append(after.Criteria, c)
			}
		}
		if len(after.Criteria) > maxCriteria {
			return fmt.Errorf("a task keeps at most %d requirements", maxCriteria)
		}
		if after.Criteria == nil {
			after.Criteria = []string{}
		}
		return s.applyEdit(v, t, TaskEdit{By: in.By, Kind: in.Kind, After: after}, &out)
	})
	return out, err
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
		return s.applyEdit(v, t, TaskEdit{By: FromOwner, Kind: FromOwner, After: TaskText{Objective: before.Objective, Criteria: slices.Clone(before.Criteria)}, Undoes: editID}, &out)
	})
	return out, err
}

// applyEdit keeps e on t and puts its text in place, within a change.
func (s *Service) applyEdit(v *Snapshot, t *Task, e TaskEdit, out *Task) error {
	e.Before = t.text()
	if e.Before.same(e.After) {
		return errors.New("that changes nothing")
	}
	now := s.now().UTC()
	e.ID, e.At = uid(), now
	t.Objective, t.Criteria = e.After.Objective, e.After.Criteria
	t.Edits = append(t.Edits, e)
	// Every check made against the old text is asked for again.
	t.TextVersion++
	t.UpdatedAt = now
	what := "edited the requirements of"
	if e.Undoes != "" {
		what = "undid a change to"
	}
	recordTask(v, now, t, "task.edited", fmt.Sprintf("%s %s %s", e.By, what, t.Objective))
	derive(v, t)
	*out = *t
	return nil
}
