package core

import (
	"context"
	"fmt"
	"time"
)

// NextTask returns the one task to work on when tasks are worked one at a
// time: the active task furthest along, or else the first queued task that
// waits for nothing, which it starts; while every active task is waiting to
// retry, it waits with them rather than start another. The loop itself
// schedules several steps at once; see Schedule.
func (s *Service) NextTask(ctx context.Context) (Task, bool, error) {
	var out Task
	found := false
	err := s.store.update(ctx, func(v *Snapshot) error {
		if t, ok := furthestAlong(v.Tasks, s.now()); ok {
			out, found = t, true
			return nil
		}
		for i := range v.Tasks {
			t := &v.Tasks[i]
			if t.Status != TaskQueued || heldBack(v, *t) {
				continue
			}
			p := project(v, t.ProjectID)
			if p == nil || p.Playbook == nil {
				continue
			}
			startTask(v, p, t, s.now().UTC())
			out, found = *t, true
			return nil
		}
		return nil
	})
	return out, found, err
}

// startTask takes a queued task off the to-do list: it pins the playbook,
// copies the roles with their members' learnings and starts round 1, with
// the researcher if the team has one and the task has no plan yet.
func startTask(v *Snapshot, p *Project, t *Task, now time.Time) {
	pinTeam(v, p, t)
	t.MaxRounds = p.Playbook.MaxRounds
	t.Round = 1
	t.Status = TaskWriting
	if _, researches := t.Researcher(); researches && t.Plan == nil {
		t.Status = TaskResearching
	}
	t.Detail = ""
	t.UpdatedAt = now
	if t.StartedAt.IsZero() {
		t.StartedAt = now
	}
	recordTask(v, now, t, "task.started", t.Objective)
}

// progress is how far along each active status is, from working out what a
// task needs to landing it. A status is active exactly when it is here.
var progress = map[string]int{TaskResearching: 0, TaskDesigning: 1, TaskWriting: 2, TaskReviewing: 3, TaskDeciding: 4, TaskLanding: 5}

// furthestAlong is the active task to carry on with. Finishing started work
// first keeps the time each task spends between leaving the to-do list and
// landing as short as it can be.
func furthestAlong(tasks []Task, now time.Time) (Task, bool) {
	var best Task
	found := false
	for _, t := range tasks {
		if t.Active() && !(t.Status == TaskLanding && holdsLanding(t) && t.Delivering == nil) && (!found || ahead(t, best, now)) {
			best, found = t, true
		}
	}
	return best, found
}

// ahead reports whether a should carry on before b: work that can move now
// before work waiting to retry, then the one further along, then the one with
// more drafts done, then the one started longest ago.
func ahead(a, b Task, now time.Time) bool {
	if waitingA, waitingB := a.RetryAt.After(now), b.RetryAt.After(now); waitingA != waitingB {
		return waitingB
	}
	if progress[a.Status] != progress[b.Status] {
		return progress[a.Status] > progress[b.Status]
	}
	if len(a.Revisions) != len(b.Revisions) {
		return len(a.Revisions) > len(b.Revisions)
	}
	return a.started().Before(b.started())
}

// started is when the task left the to-do list; tasks started before that
// was recorded count from when they were asked for.
func (t Task) started() time.Time {
	if !t.StartedAt.IsZero() {
		return t.StartedAt
	}
	return t.CreatedAt
}

// pinTeam gives a task the team its project has now, each seat with its
// member's learnings.
func pinTeam(v *Snapshot, p *Project, t *Task) {
	pinned := *p.Playbook
	pinned.Roles = append([]Role(nil), p.Playbook.Roles...)
	pinned.Prepare = append([]string(nil), p.Playbook.Prepare...)
	t.Playbook = &pinned
	t.Roles = withLearnings(v, p.Playbook.Roles)
}

// UseProjectTeam moves a task that waits for the owner, or waits in the queue
// after it started, onto the team its project has now. A task keeps the team
// it started with, so a change to the team, such as a QA that can run the
// check or seats moved off a paused engine, reaches it only this way. No one
// may be working on it, and the kind of work stays the same. Checks the new
// seats haven't made are made afresh.
func (s *Service) UseProjectTeam(ctx context.Context, projectID, taskID string) (Task, error) {
	return s.updateTask(ctx, taskID, func(v *Snapshot, t *Task, p *Project) (string, error) {
		switch {
		case t.ProjectID != projectID:
			return "", ErrNotFound
		case (t.Status != TaskWaiting && t.Status != TaskQueued) || len(t.Claims) > 0:
			return "", fmt.Errorf("a request takes on the project's team only while it waits, for you or in the queue, and no one is working on it: %w", ErrConflict)
		case t.Status == TaskQueued && t.Playbook == nil:
			// Not started yet: it takes on the project's team when it starts.
			return "", nil
		case p.Playbook == nil || t.Playbook == nil || p.Playbook.Medium != t.Playbook.Medium:
			return "", fmt.Errorf("the project's team does a different kind of work: %w", ErrConflict)
		}
		pinTeam(v, p, t)
		t.MaxRounds = max(t.MaxRounds, p.Playbook.MaxRounds)
		return t.Objective + " goes on with the project's current team: " + playbookSummary(*p.Playbook), nil
	})
}
