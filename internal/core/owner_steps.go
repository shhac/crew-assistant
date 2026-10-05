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
	ID            string `json:"id,omitempty"`
	BriefVersion  int    `json:"brief_version,omitempty"`
	Source        string `json:"source,omitempty"`
	Finding       string `json:"finding,omitempty"`
	Bound         string `json:"bound,omitempty"`
	TextVersion   int    `json:"text_version,omitempty"`
	AssetCreation *bool  `json:"asset_creation,omitempty"`
	Routed        string `json:"routed,omitempty"`
	Criterion     string `json:"criterion"`
	Why           string `json:"why"`
	Revision      int    `json:"revision"`
}

// NeedsAssetReply includes confirmed assets left pending by a partial spec,
// as well as an explicit route awaiting production or classification.
func (u Unreachable) NeedsAssetReply() bool {
	return u.Routed != "" || u.AssetCreation != nil && *u.AssetCreation || u.Source == "landing" && u.AssetCreation == nil
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
		if u.Revision == n && u.Source != "review" && !(u.Source == "landing" && u.AssetCreation != nil && !*u.AssetCreation) {
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
	return append(append(slices.Clone(t.OwnerChecks), t.OwnerSteps...), t.OwnerTook...)
}

// OwnerChecklist is the steps the owner checks once the change lands, as a
// checklist, or empty when there are none.
func (t Task) OwnerChecklist() string {
	if len(t.OwnerChecks)+len(t.OwnerSteps) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("After it lands, check:")
	seen := map[string]bool{}
	for _, s := range append(slices.Clone(t.OwnerChecks), t.OwnerSteps...) {
		if seen[s] {
			continue
		}
		seen[s] = true
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
	return s.AcceptDraft(ctx, taskID, decisionID, true, accept)
}

// AcceptDraft applies either explicit acceptance choice exactly once.
func (s *Service) AcceptDraft(ctx context.Context, taskID, decisionID string, followUp bool, accept func(*Task) string) (Task, Task, error) {
	var out, follow Task
	err := s.store.update(ctx, func(v *Snapshot) error {
		i := slices.IndexFunc(v.Tasks, func(t Task) bool { return t.ID == taskID })
		if i < 0 {
			return ErrNotFound
		}
		t := &v.Tasks[i]
		d := decision(v, decisionID)
		choice := ChoiceAcceptDraft
		if followUp {
			choice = ChoiceAcceptFollowUp
		}
		if d != nil && (d.Status != DecisionResolved || d.Kind != DecisionEscalation || d.Disposition != DispositionChoice || d.Answer != choice) {
			return ErrConflict
		}
		switch {
		case d == nil || d.TaskID != t.ID || (followUp && d.FollowUp == nil):
			return fmt.Errorf("the decision proposes no follow-up: %w", ErrNotFound)
		case d.Applied:
			out = *t
			if queued := task(v, d.FollowUpID); queued != nil {
				follow = *queued
			}
			return nil
		case t.Finished():
			out = *t
			return nil
		case followUp && !required(d.FollowUp.Objective):
			return errors.New("a follow-up needs an objective")
		}
		p := project(v, t.ProjectID)
		if p == nil {
			return ErrNotFound
		}
		if t.DecisionID != d.ID || t.Status != TaskWaiting {
			return ErrConflict
		}
		if len(t.Revisions) == 0 || !d.matchesWork(t, p.Brief.Version) || t.DirectionPending > 0 {
			if t.Proposal != nil {
				t.Proposal.MergeApproved = 0
			}
			t.DecisionID, t.ResumeStatus = "", ""
			t.Acceptance, t.MergeValidation, t.Approved = nil, nil, 0
			t.Status = TaskReviewing
			if len(t.Revisions) == 0 || t.DirectionPending > 0 {
				t.Status = TaskWriting
			}
			t.Detail = "Checking again because the work changed after the acceptance decision"
			t.UpdatedAt = s.now().UTC()
			recordTask(v, t.UpdatedAt, t, "task.acceptance_stale", t.Detail)
			derive(v, t)
			out = *t
			return nil
		}
		now := s.now().UTC()
		t.Acceptance = &DraftAcceptance{Revision: t.Revisions[len(t.Revisions)-1].N, Decision: d.ID, BriefVersion: p.Brief.Version, TextVersion: t.TextVersion}
		t.MergeValidation = nil
		d.Applied = true
		activity := accept(t)
		t.UpdatedAt = now
		recordTask(v, now, t, "task."+t.Status, activity)
		if !followUp {
			derive(v, t)
			out = *t
			return nil
		}
		follow = Task{ID: uid(), ProjectID: t.ProjectID, Objective: text.Clip(strings.TrimSpace(d.FollowUp.Objective), maxObjective), Status: TaskQueued, Stage: StageTodo, DependsOn: []string{t.ID}, Revisions: []Revision{}, Verdicts: []Verdict{}, CreatedAt: now, UpdatedAt: now}
		for _, c := range cleanList(d.FollowUp.Criteria) {
			follow.Criteria = append(follow.Criteria, text.Clip(c, maxCriterion))
		}
		d.FollowUpID = follow.ID
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
		if !slices.Contains(t.OwnersAlready(), d.OwnerStep.Criterion) && !slices.Contains(t.OwnerSteps, step) && !slices.Contains(t.OwnerChecks, step) {
			t.OwnerSteps = append(t.OwnerSteps, step)
		}
		if !slices.Contains(t.OwnerTook, d.OwnerStep.Criterion) {
			t.OwnerTook = append(t.OwnerTook, d.OwnerStep.Criterion)
		}
		settled := slices.DeleteFunc(slices.Clone(t.Unreachable), func(u Unreachable) bool { return u.Criterion != d.OwnerStep.Criterion })
		t.DecisionID, t.UpdatedAt = "", now
		t.Status, t.Detail = TaskDeciding, "Going on with the owner step"
		recordTask(v, now, t, "task.owner_step", fmt.Sprintf("%s: left to you after it lands: %s", t.Objective, step))
		if i := slices.Index(t.Criteria, d.OwnerStep.Criterion); i >= 0 {
			t.Status, t.Detail = TaskReviewing, "Checking again without the owner step"
			after := t.text()
			after.Criteria = slices.Delete(after.Criteria, i, i+1)
			return s.applyEdit(v, t, TaskEdit{By: FromOwner, Kind: FromOwner, After: after, Settled: settled}, &out)
		}
		t.SettleUnreachable(d.OwnerStep.Criterion)
		derive(v, t)
		out = *t
		return nil
	})
	return out, err
}

// ChoiceSplit asks the owner for the team and after-landing parts of a requirement.
const ChoiceSplit = "Split it"

// OwnerSplit keeps part of a requirement with the team and part with the owner.
type OwnerSplit struct {
	Team  string `json:"team"`
	Owner string `json:"owner"`
}

// SplitOwnerStep applies a resolved split as one undoable owner edit.
func (s *Service) SplitOwnerStep(ctx context.Context, taskID, decisionID string) (Task, error) {
	var out Task
	err := s.store.update(ctx, func(v *Snapshot) error {
		t, d := task(v, taskID), decision(v, decisionID)
		if t == nil {
			return ErrNotFound
		}
		if t.Finished() {
			out = *t
			return nil
		}
		if d == nil || d.TaskID != taskID || d.OwnerStep == nil || d.Split == nil {
			return ErrNotFound
		}
		if t.DecisionID != decisionID {
			out = *t
			return nil
		}
		// The owner can add a check while a resolved split awaits this pass.
		// Keep that newer choice and settle the rest without blocking the loop.
		ownerAlready := d.Split.Team == d.Split.Owner || slices.Contains(t.OwnerChecks, d.Split.Team)
		original := d.OwnerStep.Criterion
		after := t.text()
		if i := slices.Index(after.Criteria, original); i >= 0 {
			if ownerAlready {
				after.Criteria = slices.Delete(after.Criteria, i, i+1)
			} else {
				after.Criteria[i] = d.Split.Team
			}
		} else {
			if !ownerAlready && !slices.Contains(after.Criteria, d.Split.Team) {
				after.Criteria = append(after.Criteria, d.Split.Team)
			}
		}
		unique := make([]string, 0, len(after.Criteria))
		for _, c := range after.Criteria {
			if !slices.Contains(unique, c) {
				unique = append(unique, c)
			}
		}
		after.Criteria = unique
		if !slices.Contains(t.OwnerTook, original) {
			t.OwnerTook = append(t.OwnerTook, original)
		}
		if !slices.Contains(after.OwnerChecks, d.Split.Owner) {
			after.OwnerChecks = append(after.OwnerChecks, d.Split.Owner)
		}
		if ownerAlready {
			t.TeamKept = slices.DeleteFunc(t.TeamKept, func(c string) bool { return c == d.Split.Team })
			if len(t.TeamKept) == 0 {
				t.TeamKept = nil
			}
		} else if !slices.Contains(t.TeamKept, d.Split.Team) {
			t.TeamKept = append(t.TeamKept, d.Split.Team)
		}
		settled := slices.DeleteFunc(slices.Clone(t.Unreachable), func(u Unreachable) bool { return u.Criterion != original })
		t.DecisionID, t.Status, t.Detail = "", TaskReviewing, "Checking again with the split requirement"
		t.UpdatedAt = s.now().UTC()
		after.Criteria = slices.DeleteFunc(after.Criteria, func(c string) bool { return slices.Contains(after.OwnerChecks, c) })
		activity := fmt.Sprintf("%s: split: %s stays with the team; %s after it lands", t.Objective, d.Split.Team, d.Split.Owner)
		if ownerAlready {
			activity = fmt.Sprintf("%s: split: %s is already an owner check; %s after it lands", t.Objective, d.Split.Team, d.Split.Owner)
		}
		recordTask(v, t.UpdatedAt, t, "task.owner_split", activity)
		if t.text().same(after) {
			t.SettleUnreachable(original)
			t.Status, t.Detail = TaskDeciding, "Going on with the split requirement"
			derive(v, t)
			out = *t
			return nil
		}
		return s.applyEdit(v, t, TaskEdit{By: FromOwner, Kind: FromOwner, After: after, Settled: settled}, &out)
	})
	return out, err
}
