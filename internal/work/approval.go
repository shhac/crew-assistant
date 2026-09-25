package work

import (
	"context"
	"errors"
	"fmt"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/text"
)

// approve records the owner's approval of the latest revision and moves the
// task on to landing.
func (lp *Loop) approve(ctx context.Context, t core.Task) error {
	_, err := lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
		if len(t.Revisions) > 0 {
			t.Approved = t.Revisions[len(t.Revisions)-1].N
		}
		t.Status, t.DecisionID, t.ResumeStatus, t.Detail = core.TaskLanding, "", "", "Landing"
		return "Landing " + t.Objective, nil
	})
	return err
}

// resumeLanding moves a task whose approval still stands, or that needs none,
// back on to landing. Its catch-ups keep counting.
func (lp *Loop) resumeLanding(ctx context.Context, t core.Task) error {
	_, err := lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.Status, t.DecisionID, t.Detail = core.TaskLanding, "", "Landing"
		return "Landing " + t.Objective, nil
	})
	return err
}

// approvalStands reports whether the latest revision is the one the owner
// approved, or only merges it cleanly with work that landed since.
func approvalStands(t core.Task) bool {
	if t.Approved == 0 || len(t.Revisions) == 0 {
		return false
	}
	byN := map[int]core.Revision{}
	for _, r := range t.Revisions {
		byN[r.N] = r
	}
	seen := map[int]bool{}
	for r := t.Revisions[len(t.Revisions)-1]; !seen[r.N]; {
		if r.N == t.Approved {
			return true
		}
		seen[r.N] = true
		prev, ok := byN[r.CleanMergeOf]
		if r.CleanMergeOf == 0 || !ok {
			return false
		}
		r = prev
	}
	return false
}

// supersedeStaleApprovals replaces any approval another task in the project
// is waiting on with a catch-up, so the owner is never asked to approve work
// that is out of date.
func (lp *Loop) supersedeStaleApprovals(ctx context.Context, projectID string) error {
	snap, err := lp.Core.Snapshot(ctx)
	if err != nil {
		return err
	}
	p, ok := findProject(snap, projectID)
	if !ok {
		return nil
	}
	for _, t := range snap.Tasks {
		if t.ProjectID != projectID || t.Status != core.TaskWaiting || t.DecisionID == "" {
			continue
		}
		d, ok := findDecision(snap, t.DecisionID)
		if !ok || d.Status != core.DecisionOpen || !(d.Approves() || d.Kind == core.DecisionEscalation) {
			continue
		}
		m, err := lp.mediumFor(ctx, p, taskPlaybook(p, t))
		if err != nil {
			return err
		}
		c, l, err := lag(ctx, m, t)
		if err != nil {
			return err
		}
		if l == nil {
			continue
		}
		if err = lp.catchUpRound(ctx, t, c, *l); err != nil {
			return err
		}
		// A catch-up that failed leaves the task on this approval, to retry;
		// dismissing it then would read as the owner closing the task.
		after, err := lp.Core.Snapshot(ctx)
		if err != nil {
			return err
		}
		if now, ok := findTask(after, projectID, t.ID); ok && now.DecisionID == d.ID {
			continue
		}
		if _, err = lp.Core.DismissDecision(ctx, d.ID, "Out of date: "+l.What+". It is catching up and will ask again."); err != nil && !errors.Is(err, core.ErrConflict) {
			return err
		}
	}
	return nil
}

func (lp *Loop) askForDelivery(ctx context.Context, p core.Project, t core.Task, r core.Revision) error {
	m, err := lp.mediumFor(ctx, p, taskPlaybook(p, t))
	if err != nil {
		return lp.roleFailed(ctx, t, "The workspace", err)
	}
	c, l, err := lag(ctx, m, t)
	if err != nil {
		return lp.roleFailed(ctx, t, "The workspace", err)
	}
	if l != nil {
		return lp.catchUpRound(ctx, t, c, *l)
	}
	if approvalStands(t) || !taskPlaybook(p, t).Land.AsksFirst() || proposed(t) {
		return lp.resumeLanding(ctx, t)
	}
	_, err = lp.Core.OpenTaskDecision(ctx, t.ID, core.DecisionDelivery, core.DecisionInput{
		Title:          approvalTitle(t, taskPlaybook(p, t)),
		Context:        text.Clip(r.Summary, 600) + "\n\n" + m.deliveryNote(t),
		Recommendation: choiceApprove,
		Choices:        []string{choiceApprove, choiceChanges},
	})
	return err
}

// approvalTitle says what approving does.
func approvalTitle(t core.Task, playbook *core.Playbook) string {
	if playbook == nil || playbook.Medium != core.MediumGit {
		return fmt.Sprintf("Approve “%s”", t.Objective)
	}
	switch playbook.Land.Way() {
	case core.LandPush:
		return fmt.Sprintf("Land “%s” on %s", t.Objective, playbook.Land.Target)
	case core.LandPullRequest:
		return fmt.Sprintf("Open a pull request for “%s”", t.Objective)
	}
	return fmt.Sprintf("Create a branch for “%s”", t.Objective)
}
