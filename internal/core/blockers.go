package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/text"
)

const (
	BlockerManual         = "manual"
	BlockerDaemonIncludes = "daemon_includes"
)

// Blocker keeps an external condition and its clearing history on the task.
type Blocker struct {
	ID          string     `json:"id"`
	Kind        string     `json:"kind"`
	Description string     `json:"description"`
	Task        string     `json:"task,omitempty"`
	LandingOnly bool       `json:"landing_only,omitempty"`
	By          string     `json:"by"`
	At          time.Time  `json:"at"`
	ClearedAt   *time.Time `json:"cleared_at,omitempty"`
	ClearedBy   string     `json:"cleared_by,omitempty"`
	Check       string     `json:"check,omitempty"`
}

type BlockerInput struct {
	Project, Task, Kind, Description, Other, By string
	LandingOnly                                 bool
}

func holdsStart(t Task) bool {
	for _, b := range t.Blockers {
		if b.ClearedAt == nil && !b.LandingOnly {
			return true
		}
	}
	return false
}
func holdsLanding(t Task) bool          { return len(BlockerReasons(t)) > 0 }
func heldBack(v *Snapshot, t Task) bool { return holdsStart(t) || len(waitsFor(v, t)) > 0 }

// BlockerReasons lists the conditions still holding delivery.
func BlockerReasons(t Task) []string {
	var out []string
	for _, b := range t.Blockers {
		if b.ClearedAt == nil {
			out = append(out, "held until: "+b.Description)
		}
	}
	return out
}

func (s *Service) SetBlocker(ctx context.Context, in BlockerInput) (Task, error) {
	return s.editTaskRecord(ctx, in.Project, in.Task, func(t *Task, v *Snapshot) error {
		if !overrules(in.By) && (t.Finished() || t.Delivering != nil) {
			return fmt.Errorf("the task has finished or is delivering: %w", ErrConflict)
		}
		if !in.LandingOnly && t.Delivering == nil {
			if err := mayWait(*t, in.By); err != nil {
				return err
			}
		}
		b := Blocker{ID: uid(), Kind: in.Kind, Description: text.Clip(strings.TrimSpace(in.Description), 300), LandingOnly: in.LandingOnly, By: in.By, At: s.now().UTC()}
		switch in.Kind {
		case BlockerManual:
			if b.Description == "" {
				return fmt.Errorf("describe the condition the task waits on")
			}
		case BlockerDaemonIncludes:
			other := task(v, strings.TrimSpace(in.Other))
			if other == nil || other.ProjectID != t.ProjectID {
				return ErrNotFound
			}
			if other.ID == t.ID {
				return fmt.Errorf("a task cannot wait on its own landing")
			}
			p := project(v, t.ProjectID)
			if p == nil || p.Playbook == nil || p.Playbook.Medium != MediumGit {
				return fmt.Errorf("daemon version conditions need a code project")
			}
			b.Task = other.ID
			if b.Description == "" {
				name := other.Ref
				if name == "" {
					name = text.Clip(other.Objective, 200)
				}
				b.Description = "the running daemon includes " + name
			}
		default:
			return fmt.Errorf("unknown blocker kind %q", in.Kind)
		}
		t.Blockers = append(t.Blockers, b)
		t.UpdatedAt = b.At
		recordTask(v, b.At, t, "task.blocked", b.Description)
		linksChanged(v, t.ProjectID, in.By)
		derive(v, t)
		return nil
	})
}

func (s *Service) ClearBlocker(ctx context.Context, projectID, taskID, id, by, why string) (Task, error) {
	return s.editTaskRecord(ctx, projectID, taskID, func(t *Task, v *Snapshot) error {
		for i := range t.Blockers {
			b := &t.Blockers[i]
			if b.ID != id {
				continue
			}
			if b.ClearedAt != nil {
				return fmt.Errorf("the condition is already cleared: %w", ErrConflict)
			}
			if !overrules(by) && (overrules(b.By) || t.Finished()) {
				return fmt.Errorf("only the owner or assistant can clear this condition: %w", ErrConflict)
			}
			s.clearBlocker(v, t, b, by, why)
			return nil
		}
		return ErrNotFound
	})
}

func (s *Service) clearBlocker(v *Snapshot, t *Task, b *Blocker, by, why string) {
	now := s.now().UTC()
	b.ClearedAt, b.ClearedBy, b.Check = &now, by, ""
	t.UpdatedAt = now
	recordTask(v, now, t, "task.unblocked", b.Description+": "+why)
	linksChanged(v, t.ProjectID, by)
	derive(v, t)
}

var errBlockerUnchanged = errors.New("external condition unchanged")

// ClearDaemonBlocker fences a successful observation against the current target.
func (s *Service) ClearDaemonBlocker(ctx context.Context, taskID, id, targetID string, revision int) error {
	err := s.store.update(ctx, func(v *Snapshot) error {
		t, target := task(v, taskID), task(v, targetID)
		if t == nil || t.Finished() || target == nil || target.ProjectID != t.ProjectID || target.Status != TaskLanded || len(target.Revisions) == 0 || target.Revisions[len(target.Revisions)-1].N != revision {
			return errBlockerUnchanged
		}
		for i := range t.Blockers {
			b := &t.Blockers[i]
			if b.ID == id && b.ClearedAt == nil && b.Kind == BlockerDaemonIncludes && b.Task == targetID {
				s.clearBlocker(v, t, b, "daemon", "the running daemon includes the landed change")
				return nil
			}
		}
		return errBlockerUnchanged
	})
	if errors.Is(err, errBlockerUnchanged) {
		return nil
	}
	return err
}

// CheckBlocker updates only a still-open condition's diagnostic.
func (s *Service) CheckBlocker(ctx context.Context, taskID, id, reason string) error {
	err := s.store.update(ctx, func(v *Snapshot) error {
		t := task(v, taskID)
		if t == nil || t.Finished() {
			return errBlockerUnchanged
		}
		for i := range t.Blockers {
			b := &t.Blockers[i]
			if b.ID == id && b.ClearedAt == nil && b.Check != reason {
				b.Check = reason
				t.UpdatedAt = s.now().UTC()
				return nil
			}
		}
		return errBlockerUnchanged
	})
	if errors.Is(err, errBlockerUnchanged) {
		return nil
	}
	return err
}
