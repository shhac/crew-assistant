package core

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// DecisionPMQuestion is a question the team's PM asked the owner about the
// order of work. The owner's answer goes to the PM's next look.
const DecisionPMQuestion = "pm-question"

// PMAnswer is what the team's PM decided about the to-do list: the queued
// tasks in order, and the full list of what a task waits for, for any task
// it changes.
type PMAnswer struct {
	Order   []string
	Depends map[string][]string
	Note    string
}

// ApplyPM puts the PM's decision into effect and marks the list looked at.
// What each task waits for is checked like any other dependency: the same
// project, and never a loop. The order applies only to exactly the queued
// tasks. The owner's or the assistant's order stands: while it is newer than
// every task queued since, the PM changes only what waits for what, and once
// new work arrives it may place the new tasks but keeps their order for the
// rest. It reports what it changed.
func (s *Service) ApplyPM(ctx context.Context, projectID string, in PMAnswer) (string, error) {
	var changed []string
	err := s.store.update(ctx, func(v *Snapshot) error {
		p := project(v, projectID)
		if p == nil {
			return ErrNotFound
		}
		p.PMDue, p.PMDirection = false, ""
		now := s.now().UTC()
		ids := make([]string, 0, len(in.Depends))
		for id := range in.Depends {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			t := unfinished(v, id)
			if t == nil || t.ProjectID != projectID {
				continue
			}
			deps, err := dependencies(v, Task{ID: t.ID, ProjectID: t.ProjectID}, in.Depends[id])
			if err != nil || slices.Equal(deps, t.DependsOn) {
				continue
			}
			t.DependsOn = deps
			changed = append(changed, "what “"+t.Objective+"” waits for")
		}
		if order := s.pmOrder(v, p, in.Order); order != nil {
			if _, err := reorder(v, projectID, order); err == nil {
				p.OrderedBy, p.OrderedAt = OrderedByPM, now
				changed = append(changed, "the order")
			}
		}
		summary := "The to-do list stays as it is"
		if len(changed) > 0 {
			summary = "Changed " + strings.Join(changed, " and ")
		}
		if note := strings.TrimSpace(in.Note); note != "" {
			summary += ": " + note
		}
		record(v, now, projectID, "task.ordered", summary)
		deriveStages(v)
		return nil
	})
	return strings.Join(changed, ", "), err
}

// pmOrder is the order the PM may set, or nil to leave it. It must name
// exactly the queued tasks and change something. When the owner or the
// assistant ordered the list, their order holds for the tasks that were
// there then; the PM only places tasks queued since.
func (s *Service) pmOrder(v *Snapshot, p *Project, order []string) []string {
	var current []string
	for _, t := range v.Tasks {
		if t.ProjectID == p.ID && t.Status == TaskQueued {
			current = append(current, t.ID)
		}
	}
	if len(order) != len(current) || slices.Equal(order, current) {
		return nil
	}
	for _, id := range order {
		if !slices.Contains(current, id) {
			return nil
		}
	}
	if p.OrderedBy != OrderedByOwner && p.OrderedBy != OrderedByAssistant {
		return order
	}
	var kept []string
	for _, id := range current {
		if !task(v, id).CreatedAt.After(p.OrderedAt) {
			kept = append(kept, id)
		}
	}
	if len(kept) == len(current) {
		return nil
	}
	out := make([]string, 0, len(order))
	next := 0
	for _, id := range order {
		if slices.Contains(kept, id) {
			out = append(out, kept[next])
			next++
			continue
		}
		out = append(out, id)
	}
	if slices.Equal(out, current) {
		return nil
	}
	return out
}

// PMSeat is the seat on a project's team that keeps its to-do list.
func (p Project) PMSeat() (Role, bool) {
	if p.Playbook == nil {
		return Role{}, false
	}
	i := slices.IndexFunc(p.Playbook.Roles, func(r Role) bool { return r.Holds(RolePM) })
	if i < 0 {
		return Role{}, false
	}
	return p.Playbook.Roles[i], true
}

// listChanged asks the project's PM, if it has one, to look at the to-do
// list again.
func (p *Project) listChanged() {
	if _, ok := p.PMSeat(); ok {
		p.PMDue = true
	}
}

// SkipPM marks the list looked at without the PM, when the project has no
// PM or its PM could not look, saying why.
func (s *Service) SkipPM(ctx context.Context, projectID, why string) error {
	return s.store.update(ctx, func(v *Snapshot) error {
		p := project(v, projectID)
		if p == nil {
			return ErrNotFound
		}
		p.PMDue = false
		if why != "" {
			record(v, s.now().UTC(), projectID, "task.ordered", fmt.Sprintf("The PM couldn't look at the to-do list: %s", why))
		}
		return nil
	})
}
