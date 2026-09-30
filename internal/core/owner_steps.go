package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/shhac/crew-assistant/internal/text"
)

// What the team can't finish goes on without holding the task back: the
// owner can accept a draft at its round limit and queue what the checkers
// still raise as a follow-up, and a requirement the team can't meet from its
// sandbox, such as a live run on the owner's machine, can become a step the
// owner checks after the change lands.

// Unreachable is a requirement the implementer said it can't meet from its
// sandbox, and why, on the draft it said so of.
type Unreachable struct {
	Criterion string `json:"criterion"`
	Why       string `json:"why"`
	Revision  int    `json:"revision"`
}

// OwnerStep is a requirement a decision proposes leaving to the owner: the
// requirement as the task states it, and the step the owner would check.
type OwnerStep struct {
	Criterion string `json:"criterion"`
	Step      string `json:"step"`
}

// Pending is what the implementer said it can't meet on draft n, still to
// be judged.
func (t Task) Pending(n int) []Unreachable {
	var out []Unreachable
	for _, u := range t.Unreachable {
		if u.Revision == n {
			out = append(out, u)
		}
	}
	return out
}

// SettleUnreachable takes criterion off what is still to be judged, within
// a change: the owner took it on, or left it with the team.
func (t *Task) SettleUnreachable(criterion string) {
	t.Unreachable = slices.DeleteFunc(t.Unreachable, func(u Unreachable) bool { return u.Criterion == criterion })
	if len(t.Unreachable) == 0 {
		t.Unreachable = nil
	}
}

// OwnersAlready is what the owner has taken on for this task: its steps, and
// the requirements they came from.
func (t Task) OwnersAlready() []string {
	return append(slices.Clone(t.OwnerSteps), t.OwnerTook...)
}

// OwnerChecklist is the steps the owner checks once the change lands, as a
// checklist, or empty when there are none.
func (t Task) OwnerChecklist() string {
	if len(t.OwnerSteps) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("After it lands, check:")
	for _, s := range t.OwnerSteps {
		b.WriteString("\n- [ ] " + s)
	}
	return b.String()
}

// AcceptWithFollowUp accepts a task's draft at the owner's choice and queues
// the follow-up the decision proposed, in one change. accept moves the task
// on as any approval does and says what happened. The follow-up is the
// owner's: it joins the to-do list directly, not triage, depends on the
// accepted task by a link only the owner or the assistant can take away, and
// starts right after it. A task that has finished is left alone.
func (s *Service) AcceptWithFollowUp(ctx context.Context, taskID, decisionID string, accept func(*Task) string) (Task, Task, error) {
	var out, follow Task
	err := s.store.update(ctx, func(v *Snapshot) error {
		i := slices.IndexFunc(v.Tasks, func(t Task) bool { return t.ID == taskID })
		if i < 0 {
			return ErrNotFound
		}
		t := &v.Tasks[i]
		d := decision(v, decisionID)
		switch {
		case d == nil || d.TaskID != t.ID || d.FollowUp == nil:
			return fmt.Errorf("the decision proposes no follow-up: %w", ErrNotFound)
		case t.Finished():
			out = *t
			return nil
		case !required(d.FollowUp.Objective):
			return errors.New("a follow-up needs an objective")
		}
		p := project(v, t.ProjectID)
		if p == nil {
			return ErrNotFound
		}
		now := s.now().UTC()
		activity := accept(t)
		t.UpdatedAt = now
		recordTask(v, now, t, "task."+t.Status, activity)
		follow = Task{ID: uid(), ProjectID: t.ProjectID, Objective: text.Clip(strings.TrimSpace(d.FollowUp.Objective), maxObjective), Status: TaskQueued, Stage: StageTodo, DependsOn: []string{t.ID}, Revisions: []Revision{}, Verdicts: []Verdict{}, CreatedAt: now, UpdatedAt: now}
		for _, c := range cleanList(d.FollowUp.Criteria) {
			follow.Criteria = append(follow.Criteria, text.Clip(c, maxCriterion))
		}
		mark(&follow, RelationDependsOn, t.ID, LinkedByOwner, now)
		numberTask(p, &follow)
		v.Tasks = slices.Insert(v.Tasks, i+1, follow)
		p.listChanged()
		accepted, queued := &v.Tasks[i], &v.Tasks[i+1]
		recordTask(v, now, queued, "task."+queued.Status, fmt.Sprintf("%s follows up %s", queued.Objective, accepted.Objective))
		derive(v, accepted)
		derive(v, queued)
		out, follow = *accepted, *queued
		return nil
	})
	return out, follow, err
}

// MakeOwnerStep leaves the requirement a decision proposed to the owner, at
// the owner's choice: the step joins the task's owner steps, and the
// requirement leaves the task's criteria, as the owner's edit, so every
// checker judges the draft again without it. A requirement that is not one
// of the task's own, such as the brief's, stays where it is, and the task
// goes on to be decided again. A task that has finished is left alone.
func (s *Service) MakeOwnerStep(ctx context.Context, taskID, decisionID string) (Task, error) {
	var out Task
	err := s.store.update(ctx, func(v *Snapshot) error {
		t, d := task(v, taskID), decision(v, decisionID)
		switch {
		case t == nil:
			return ErrNotFound
		case d == nil || d.TaskID != t.ID || d.OwnerStep == nil:
			return fmt.Errorf("the decision proposes no owner step: %w", ErrNotFound)
		case t.Finished():
			out = *t
			return nil
		}
		now := s.now().UTC()
		step := text.Clip(strings.TrimSpace(d.OwnerStep.Step), maxCriterion)
		if step == "" {
			step = d.OwnerStep.Criterion
		}
		// A step proposed for what is already the owner's would be the same
		// check twice, reworded.
		if !slices.Contains(t.OwnersAlready(), d.OwnerStep.Criterion) && !slices.Contains(t.OwnerSteps, step) {
			t.OwnerSteps = append(t.OwnerSteps, step)
		}
		if !slices.Contains(t.OwnerTook, d.OwnerStep.Criterion) {
			t.OwnerTook = append(t.OwnerTook, d.OwnerStep.Criterion)
		}
		t.SettleUnreachable(d.OwnerStep.Criterion)
		t.DecisionID, t.UpdatedAt = "", now
		t.Status, t.Detail = TaskDeciding, "Going on with the owner step"
		recordTask(v, now, t, "task.owner_step", fmt.Sprintf("%s: left to you after it lands: %s", t.Objective, step))
		if i := slices.Index(t.Criteria, d.OwnerStep.Criterion); i >= 0 {
			t.Status, t.Detail = TaskReviewing, "Checking again without the owner step"
			after := t.text()
			after.Criteria = slices.Delete(after.Criteria, i, i+1)
			return s.applyEdit(v, t, TaskEdit{By: FromOwner, Kind: FromOwner, After: after}, &out)
		}
		derive(v, t)
		out = *t
		return nil
	})
	return out, err
}
