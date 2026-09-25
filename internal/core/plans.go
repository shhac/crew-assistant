package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Plan is what a researcher worked out about a task before anything was
// written: what already exists, what will change, what stays out, and what
// is unclear. What the task waits for is kept on the task itself.
type Plan struct {
	Summary    string    `json:"summary"`
	Exists     []string  `json:"exists,omitempty"`
	Changes    []string  `json:"changes,omitempty"`
	OutOfScope []string  `json:"out_of_scope,omitempty"`
	Questions  []string  `json:"questions,omitempty"`
	Role       string    `json:"role"`
	At         time.Time `json:"at"`
}

// unfinished reports a task that has not landed, been delivered or stopped.
func unfinished(v *Snapshot, id string) *Task {
	t := task(v, id)
	if t == nil || t.Finished() {
		return nil
	}
	return t
}

// waitsFor is what a task still waits for: the objectives of the tasks it
// depends on that have not finished. One that no longer exists, or that was
// stopped, no longer holds it back.
func waitsFor(v *Snapshot, t Task) []string {
	var out []string
	for _, id := range t.DependsOn {
		if dep := unfinished(v, id); dep != nil {
			out = append(out, dep.Objective)
		}
	}
	return out
}

// dependencies checks what a task says it depends on: tasks in its own
// project, not itself, and never a loop, which would leave both waiting
// forever. Ids of tasks that have already finished are kept but hold nothing
// back; a task may finish between being read and being named.
func dependencies(v *Snapshot, t Task, ids []string) ([]string, error) {
	var out []string
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || slices.Contains(out, id) {
			continue
		}
		dep := task(v, id)
		switch {
		case id == t.ID:
			return nil, errors.New("a task cannot wait for itself")
		case dep == nil || dep.ProjectID != t.ProjectID:
			return nil, fmt.Errorf("there is no task %q in this project", id)
		case reaches(v, id, t.ID, map[string]bool{}):
			return nil, fmt.Errorf("“%s” already waits for this task, so this task cannot wait for it", dep.Objective)
		}
		out = append(out, id)
	}
	return out, nil
}

// possibleDependencies is what a role says a task waits for, less any task
// dependencies would refuse: a role naming one impossible task keeps the
// rest of its list, so the task still waits for what it can.
func possibleDependencies(v *Snapshot, t Task, ids []string) []string {
	var out []string
	for _, id := range ids {
		if deps, err := dependencies(v, t, append(slices.Clone(out), id)); err == nil {
			out = deps
		}
	}
	return out
}

// reaches reports whether from depends on to, directly or through others.
func reaches(v *Snapshot, from, to string, seen map[string]bool) bool {
	if from == to {
		return true
	}
	if seen[from] {
		return false
	}
	seen[from] = true
	t := task(v, from)
	if t == nil {
		return false
	}
	for _, next := range t.DependsOn {
		if reaches(v, next, to, seen) {
			return true
		}
	}
	return false
}

// RecordPlan keeps a researcher's plan on its task and moves the task on.
// What it waits for comes first: a task with unlanded work to wait for goes
// back to the queue, keeping neither this plan nor its branch point, so it is
// researched again on top of the landed work. Then the researcher's
// questions, which the loop brings to the owner. Otherwise the implementer
// starts.
func (s *Service) RecordPlan(ctx context.Context, taskID string, plan Plan, dependsOn []string) (Task, error) {
	var out Task
	err := s.store.update(ctx, func(v *Snapshot) error {
		t := task(v, taskID)
		if t == nil {
			return ErrNotFound
		}
		// A task stopped while the researcher worked stays stopped.
		if t.Status != TaskResearching {
			return fmt.Errorf("the task is no longer being researched: %w", ErrConflict)
		}
		deps := possibleDependencies(v, *t, append(slices.Clone(t.DependsOn), dependsOn...))
		now := s.now().UTC()
		t.DependsOn, t.UpdatedAt = deps, now
		markAll(t, deps, researcherLinker(*t), now)
		plan.At = now
		waiting := waitsFor(v, *t)
		switch {
		case len(waiting) > 0:
			t.Status, t.Plan, t.Detail = TaskQueued, nil, ""
			t.Base, t.From, t.Branch = "", "", ""
			record(v, now, t.ProjectID, "task.queued", fmt.Sprintf("%s waits for %s", t.Objective, strings.Join(waiting, ", ")))
		case len(plan.Questions) > 0:
			t.Plan = &plan
		default:
			t.Plan = &plan
			t.Status, t.Detail = TaskWriting, ""
			record(v, now, t.ProjectID, "task.planned", fmt.Sprintf("Planned %s", t.Objective))
		}
		if p := project(v, t.ProjectID); p != nil {
			p.listChanged()
		}
		derive(v, t)
		out = *t
		return nil
	})
	return out, err
}

// researcherLinker is how the links a task's researcher adds are marked.
func researcherLinker(t Task) string {
	researcher, _ := t.Researcher()
	return TeamLinker(researcher.Member, RoleResearcher)
}
