package core

import (
	"context"
	"time"
)

// NextTask returns the task the loop should work on: the active task furthest
// along, or else the first queued task that waits for nothing, which it
// starts. The loop works on one task at a time; while every active task is
// waiting to retry, it waits with them rather than start another.
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
			if t.Status != TaskQueued || len(waitsFor(v, *t)) > 0 {
				continue
			}
			p := project(v, t.ProjectID)
			if p == nil || p.Playbook == nil {
				continue
			}
			now := s.now().UTC()
			pinned := *p.Playbook
			pinned.Roles = append([]Role(nil), p.Playbook.Roles...)
			pinned.Prepare = append([]string(nil), p.Playbook.Prepare...)
			t.Playbook = &pinned
			t.Roles = withLearnings(v, p.Playbook.Roles)
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
			out, found = *t, true
			record(v, now, t.ProjectID, "task.started", t.Objective)
			return nil
		}
		return nil
	})
	return out, found, err
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
		if t.Active() && (!found || ahead(t, best, now)) {
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
