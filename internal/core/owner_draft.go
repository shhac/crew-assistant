package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
)

// DraftByOwner marks a revision the owner made by hand.
const DraftByOwner = "owner"

// AdoptDraft records a change the owner made by hand as the task's next
// draft. It is checked like any other: reviewers and QA judge it, and the
// task goes on from their verdicts. approve takes it as approved by the
// owner, who has seen it: the reviewers' passes are theirs, but QA still
// runs, since a broken target costs more than one check. A decision the
// task waited on is closed, since the draft it asked about is superseded.
func (s *Service) AdoptDraft(ctx context.Context, taskID string, r Revision, approve bool) (Task, error) {
	var out Task
	err := s.store.update(ctx, func(v *Snapshot) error {
		t := task(v, taskID)
		if t == nil {
			return ErrNotFound
		}
		switch {
		case t.Finished():
			return fmt.Errorf("“%s” has finished: %w", t.Objective, ErrConflict)
		case len(t.Revisions) == 0:
			return fmt.Errorf("“%s” has no draft yet to change: %w", t.Objective, ErrConflict)
		case !slices.Contains([]string{TaskWriting, TaskReviewing, TaskDeciding, TaskWaiting}, t.Status):
			return fmt.Errorf("“%s” is %s; adopt a draft while it is being written, checked or waiting on you: %w", t.Objective, t.Status, ErrConflict)
		case r.Ref == "":
			return errors.New("a draft needs the commit it is")
		}
		p := project(v, t.ProjectID)
		if p == nil {
			return ErrNotFound
		}
		now := s.now().UTC()
		if d := decision(v, t.DecisionID); d != nil && d.Status == DecisionOpen {
			d.Status, d.Disposition, d.ResolvedAt = DecisionDismissed, DispositionDismissed, &now
			d.ResolutionReason = "The owner changed the draft by hand"
			record(v, now, d.ProjectID, "decision.dismissed", d.Title+": "+d.ResolutionReason)
		}
		r.N, r.BriefVersion, r.By, r.At = t.Revisions[len(t.Revisions)-1].N+1, p.Brief.Version, DraftByOwner, now
		t.Revisions = append(t.Revisions, r)
		if approve {
			t.Approved = r.N
			for _, reviewer := range t.RolesOf(RoleReviewer) {
				t.Verdicts = append(t.Verdicts, Verdict{Revision: r.N, Role: reviewer.Name, BriefVersion: p.Brief.Version, Outcome: VerdictPass, Summary: "The owner changed this draft by hand and approved it.", At: now})
			}
		}
		t.Status, t.DecisionID, t.ResumeStatus, t.Detail = TaskReviewing, "", "", "Checking the owner's draft"
		t.Failures, t.RetryAt, t.HeldFor = 0, time.Time{}, ""
		t.UpdatedAt = now
		record(v, now, t.ProjectID, "task.drafted", fmt.Sprintf("The owner changed %s by hand: draft %d", t.Objective, r.N))
		derive(v, t)
		out = *t
		return nil
	})
	return out, err
}
