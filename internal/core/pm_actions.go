package core

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

// PMAction records a checked policy response and its reversible effects.
type PMAction struct {
	ID       string       `json:"id"`
	Policy   string       `json:"policy"`
	Key      string       `json:"key"`
	By       string       `json:"by"`
	Reason   string       `json:"reason"`
	Evidence []LoopSource `json:"evidence,omitempty"`
	Effects  PMEffects    `json:"effects"`
	Status   string       `json:"status"`
	At       time.Time    `json:"at"`
	UndoneAt *time.Time   `json:"undone_at,omitempty"`
}
type PMEffects struct {
	PriorTextVersion int    `json:"prior_text_version"`
	ClosedDetail     string `json:"closed_detail"`
	DuplicateLabel   string `json:"duplicate_label,omitempty"`
	DuplicateOf      string `json:"duplicate_of,omitempty"`
	PriorStatus      string `json:"prior_status"`
	PriorDetail      string `json:"prior_detail,omitempty"`
	PriorIndex       int    `json:"prior_index"`
}

// CloseDuplicate closes only an untouched queued request. seen is captured at
// turn admission; a changed record requires a new look.
func (s *Service) CloseDuplicate(ctx context.Context, projectID, taskID, duplicateOf, reason, by string, seen Task) (PMAction, error) {
	var out PMAction
	err := s.store.update(ctx, func(v *Snapshot) error {
		t, target := task(v, taskID), task(v, duplicateOf)
		if t == nil || t.ProjectID != projectID || target == nil || target.ProjectID != projectID {
			return ErrNotFound
		}
		if t.ID == target.ID {
			return fmt.Errorf("a task cannot duplicate itself: %w", ErrConflict)
		}
		key := "dup:" + t.ID + ":" + target.ID
		return applyPMAction(v, t, "close_duplicate", key, &out, func() error {
			if target.Finished() || target.DuplicateOf != "" {
				return fmt.Errorf("the duplicate target must be unfinished and must not itself be a duplicate: %w", ErrConflict)
			}
			if slices.ContainsFunc(t.Messages, func(m TeamMessage) bool { return m.Open() }) {
				return fmt.Errorf("this task has open team messages: %w", ErrConflict)
			}
			for _, w := range v.Wakes {
				if w.TaskID == t.ID && (w.Status == WakeWaiting || w.Status == WakeFired) {
					return fmt.Errorf("this task has a pending wake: %w", ErrConflict)
				}
			}
			if slices.ContainsFunc(t.Roles, func(r Role) bool { return len(r.Learnings) > 0 }) {
				return fmt.Errorf("this task has prior role learnings: %w", ErrConflict)
			}
			if strings.TrimSpace(reason) == "" || strings.TrimSpace(by) == "" {
				return fmt.Errorf("a duplicate closure needs a reason and PM seat")
			}
			if seen.ID != t.ID || seen.Status != TaskQueued || !seen.UpdatedAt.Equal(t.UpdatedAt) || seen.TextVersion != t.TextVersion {
				return fmt.Errorf("the task changed since this look: %w", ErrConflict)
			}
			if t.Status != TaskQueued || !t.StartedAt.IsZero() || t.Attempt != 0 || t.Round != 0 || len(t.Revisions) > 0 || t.Plan != nil || t.DecisionID != "" || len(t.Claims) > 0 || t.Handoff != nil || len(t.Verdicts) > 0 || len(t.Design) > 0 || len(t.Research) > 0 || len(t.Threads) > 0 || t.Branch != "" || t.Delivering != nil || t.Proposal != nil || len(t.Direction) > 0 || len(t.FirstSeats) > 0 || t.Failures > 0 {
				return fmt.Errorf("only a queued task whose work never started can close as a duplicate: %w", ErrConflict)
			}
			for _, d := range v.Decisions {
				if d.TaskID == t.ID {
					return fmt.Errorf("this task has decision history: %w", ErrConflict)
				}
			}
			for _, m := range t.LinkedBy {
				if overrules(m.By) {
					return fmt.Errorf("an owner link overrides this policy: %w", ErrConflict)
				}
			}
			for _, b := range t.Blockers {
				if b.ClearedAt == nil && overrules(b.By) {
					return fmt.Errorf("an owner hold overrides this policy: %w", ErrConflict)
				}
			}
			for _, q := range v.Tasks {
				// A carrier must stay open even when the requests it carries are
				// already stopped. Otherwise closing the carrier would create a chain.
				if q.DuplicateOf == t.ID {
					return fmt.Errorf("this task carries another duplicate request; keep its work open: %w", ErrConflict)
				}
				if q.Finished() || q.ID == t.ID {
					continue
				}
				waits := slices.Contains(q.DependsOn, t.ID) || q.StacksOn == t.ID
				for _, b := range q.Blockers {
					waits = waits || (b.Task == t.ID && b.ClearedAt == nil)
				}
				if waits {
					return fmt.Errorf("unfinished task %s waits on this task: %w", q.Label(), ErrConflict)
				}
			}
			if riskyLoopText(t.Objective + " " + strings.Join(t.Criteria, " ") + " " + target.Objective + " " + strings.Join(target.Criteria, " ") + " " + reason) {
				return fmt.Errorf("P1, security, data loss or irreversible work stays with the owner: %w", ErrConflict)
			}
			index := 0
			for _, q := range v.Tasks {
				if q.ID == t.ID {
					break
				}
				if q.ProjectID == projectID && q.Status == TaskQueued {
					index++
				}
			}
			now := s.now().UTC()
			out = PMAction{ID: uid(), Policy: "close_duplicate", Key: key, By: by, Reason: strings.TrimSpace(reason), Status: "performed", At: now, Evidence: []LoopSource{{Kind: "task", ID: t.ID, At: t.UpdatedAt, Text: "Never-started queued duplicate"}, {Kind: "task", ID: target.ID, At: target.UpdatedAt, Text: target.Objective}}, Effects: PMEffects{DuplicateOf: target.ID, DuplicateLabel: pmTaskLabel(target), PriorStatus: t.Status, PriorDetail: t.Detail, PriorIndex: index, PriorTextVersion: t.TextVersion}}
			t.DuplicateOf = target.ID
			t.Status, t.Detail, t.UpdatedAt = TaskStopped, fmt.Sprintf("Duplicate of %s: %s", out.Effects.DuplicateLabel, out.Reason), now
			out.Effects.ClosedDetail = t.Detail
			t.Notes = append(t.Notes, Note{ID: uid(), By: by, Kind: RolePM, Text: t.Detail, At: now})
			if p := project(v, projectID); p != nil {
				p.listChanged()
			}
			return nil
		})
	})
	return out, err
}

// applyPMAction is the shared admission and ledger boundary. Call it only
// inside store.update; change validates policy-specific state and effects.
// Neither replay nor an undone key invokes change. All effects, the action and
// Activity commit together, or the enclosing transaction rolls everything back.
func applyPMAction(v *Snapshot, t *Task, policy, key string, out *PMAction, change func() error) error {
	if p := project(v, t.ProjectID); p == nil || p.Paused {
		return fmt.Errorf("an owner project hold overrides this policy: %w", ErrConflict)
	}
	if !PMPolicyOn(*v, t.ProjectID, policy) {
		return fmt.Errorf("the owner disabled %s: %w", policy, ErrConflict)
	}
	// An owner undo vetoes duplicate closure for this task, not just one
	// proposed target. Repeated looks must not shop for another target.
	for _, a := range t.PMActions {
		if policy == "close_duplicate" && a.Policy == policy && a.Status == "undone" {
			return fmt.Errorf("the owner undid a duplicate closure on this task; owner override refuses re-closing: %w", ErrConflict)
		}
	}
	for _, a := range t.PMActions {
		if a.Key == key {
			if a.Status == "undone" {
				return fmt.Errorf("the owner undid this action; replay refused: %w", ErrConflict)
			}
			*out = a
			return nil
		}
	}
	if err := change(); err != nil {
		return err
	}
	if out.ID == "" || strings.TrimSpace(out.By) == "" || strings.TrimSpace(out.Reason) == "" {
		return fmt.Errorf("a PM action needs an id, seat and reason")
	}
	out.Policy, out.Key, out.Status = policy, key, "performed"
	t.PMActions = append(t.PMActions, *out)
	what := "updated " + pmTaskLabel(t)
	if policy == "close_duplicate" {
		what = "closed " + pmTaskLabel(t) + " as a duplicate of " + out.Effects.DuplicateLabel
	}
	recordTask(v, out.At, t, "pm.action", out.By+" "+what+": "+out.Reason)
	derive(v, t)
	return nil
}

// UndoPMAction is owner-only and never changes policy authority. Undone keys
// permanently fence replay. Every reversal and its audit entry commit together.
func (s *Service) UndoPMAction(ctx context.Context, projectID, taskID, actionID, by string) (Task, error) {
	var out Task
	if by != FromOwner {
		return out, fmt.Errorf("only the owner can undo a PM action: %w", ErrConflict)
	}
	err := s.store.update(ctx, func(v *Snapshot) error {
		t := task(v, taskID)
		if t == nil || t.ProjectID != projectID {
			return ErrNotFound
		}
		i := slices.IndexFunc(t.PMActions, func(a PMAction) bool { return a.ID == actionID })
		if i < 0 {
			return ErrNotFound
		}
		a := &t.PMActions[i]
		if a.Status != "performed" {
			return fmt.Errorf("this action was already undone: %w", ErrConflict)
		}
		if a.Policy != "close_duplicate" {
			return fmt.Errorf("this policy has no checked undo")
		}
		if t.Status != TaskStopped || t.DuplicateOf != a.Effects.DuplicateOf || !t.StartedAt.IsZero() || t.Attempt != 0 || t.Plan != nil || len(t.Revisions) > 0 || t.TextVersion != a.Effects.PriorTextVersion || t.Detail != a.Effects.ClosedDetail {
			return fmt.Errorf("the task changed after this closure; undo refused: %w", ErrConflict)
		}
		now := s.now().UTC()
		t.Status, t.Detail, t.DuplicateOf = a.Effects.PriorStatus, a.Effects.PriorDetail, ""
		a.Status, a.UndoneAt = "undone", &now
		prior := a.Effects.PriorIndex
		t.UpdatedAt = now
		if p := project(v, projectID); p != nil {
			p.listChanged()
		}
		recordTask(v, now, t, "pm.action.undone", "Owner undid a duplicate closure of "+pmTaskLabel(t))
		derive(v, t)
		out = *t
		var ids []string
		for _, q := range v.Tasks {
			if q.ProjectID == projectID && q.Status == TaskQueued && q.ID != t.ID {
				ids = append(ids, q.ID)
			}
		}
		ids = slices.Insert(ids, min(prior, len(ids)), t.ID)
		_, err := reorder(v, projectID, ids)
		return err
	})
	return out, err
}

// pmTaskLabel is for owner-facing notes and activity, unlike Task.Label which
// includes canonical IDs for tools.
func pmTaskLabel(t *Task) string {
	if t.Ref != "" {
		return t.Ref
	}
	return t.Objective
}
