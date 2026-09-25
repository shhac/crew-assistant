package work

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/media/gitrepo"
	"github.com/shhac/crew-assistant/internal/text"
)

// land takes the approved revision where the project's landing policy says:
// a new branch, a fast-forward push onto the target, or the delivery folder.
// It never forces anything: a moved target sends the task back to catch up.
func (lp *Loop) land(ctx context.Context, p core.Project, t core.Task, m medium) error {
	if asksFirst(p, t) && !approvalHolds(p, t) && !proposed(t) {
		return lp.setStatus(ctx, t.ID, core.TaskDeciding, "Checks are in")
	}
	playbook := taskPlaybook(p, t)
	if playbook.Land.Way() == core.LandPullRequest {
		gm, err := lp.gitMediumFor(ctx, p, playbook)
		if err != nil {
			return lp.roleFailed(ctx, t, "The workspace", err)
		}
		return lp.landPR(ctx, t, gm)
	}
	r := t.Revisions[len(t.Revisions)-1]
	if c, ok := m.(catcher); ok {
		done, err := c.alreadyLanded(ctx, t, r)
		if err != nil {
			return lp.landingFailed(ctx, t, r, err)
		}
		if done {
			return lp.cleanUp(ctx, t, r, m, lp.recordLanded(ctx, t, r, playbook.Land.Target, "it was already there"))
		}
	}
	c, l, err := lag(ctx, m, t)
	if err != nil {
		return lp.landingFailed(ctx, t, r, err)
	}
	if l != nil {
		return lp.catchUpRound(ctx, t, c, *l)
	}
	target, err := m.deliver(ctx, t, r)
	if errors.Is(err, gitrepo.ErrTargetMoved) {
		if c, l, lagErr := lag(ctx, m, t); lagErr == nil && l != nil {
			return lp.catchUpRound(ctx, t, c, *l)
		}
	}
	if err != nil {
		return lp.landingFailed(ctx, t, r, err)
	}
	return lp.cleanUp(ctx, t, r, m, lp.recordLanded(ctx, t, r, target, ""))
}

// proposed reports a task whose pull request is open: updates to it go out
// without asking again, unless they touch what runs or instructs.
func proposed(t core.Task) bool { return t.Proposal != nil && t.Proposal.Number > 0 }

func (lp *Loop) recordLanded(ctx context.Context, t core.Task, r core.Revision, target, note string) error {
	_, err := lp.updateOpen(ctx, t.ID, func(t *core.Task, p *core.Project) (string, error) {
		t.Status, t.DecisionID, t.DeliveredTo, t.CatchUps, t.LandingFailures = core.TaskDelivered, "", target, 0, nil
		// Where it went is said by the stage; the detail keeps only a note.
		t.Detail = note
		if r.Ref != "" {
			p.Landed = &core.Landing{TaskID: t.ID, Objective: t.Objective, Commit: r.Ref, Branch: target, At: time.Now().UTC()}
		}
		if t.Playbook != nil && t.Playbook.Land.Way() != core.LandBranch {
			t.Status = core.TaskLanded
			// Said only now that it is there, however the landing went.
			if pmApproved(*t) {
				return fmt.Sprintf("The PM landed %s on %s %s: %s", t.Objective, target, t.LandDecision.How(), t.LandDecision.Reason), nil
			}
			return fmt.Sprintf("%s landed on %s", t.Objective, target), nil
		}
		if target != "" {
			return fmt.Sprintf("%s delivered to %s", t.Objective, target), nil
		}
		return t.Objective + " approved", nil
	})
	if err != nil || r.Ref == "" {
		return err
	}
	return lp.supersedeStaleApprovals(ctx, t.ProjectID)
}

// landingFailed brings the owner a decision rather than retrying on a timer: a
// refused push needs something only they can change.
func (lp *Loop) landingFailed(ctx context.Context, t core.Task, r core.Revision, cause error) error {
	reason := cause.Error()
	target := "the target branch"
	if t.Playbook != nil && t.Playbook.Land.Target != "" {
		target = t.Playbook.Land.Target
	}
	switch {
	case errors.Is(cause, gitrepo.ErrCheckedOut):
		reason = fmt.Sprintf("%s is checked out in your repository, and git there refused to update it in place. Check out another branch, or check what is holding it, then choose Try again. Nothing was forced.", target)
	case errors.Is(cause, gitrepo.ErrDirtyCheckout):
		reason = fmt.Sprintf("Your checkout of %s has uncommitted changes, so git would not update it. Commit or stash them, then choose Try again. Nothing of yours was changed.", target)
	}
	if _, err := lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.ResumeStatus = core.TaskLanding
		return "", nil
	}); err != nil {
		return err
	}
	_, err := lp.Core.OpenTaskDecision(ctx, t.ID, core.DecisionFailure, core.DecisionInput{
		Title:          fmt.Sprintf("“%s” couldn't land", t.Objective),
		Context:        text.Clip(reason, 900),
		Recommendation: choiceTryAgain + " once the cause is fixed",
		Choices:        []string{choiceTryAgain, choiceStop},
	})
	return err
}

// LandTask lands a change that was delivered under an earlier landing policy,
// such as a branch approved before the project landed on main. Its approval
// stands. A change built on another that has not landed yet is refused, so
// the two land in the order they were built.
func (lp *Loop) LandTask(ctx context.Context, projectID, taskID string) (core.Task, error) {
	snap, err := lp.Core.Snapshot(ctx)
	if err != nil {
		return core.Task{}, err
	}
	p, ok := findProject(snap, projectID)
	if !ok {
		return core.Task{}, core.ErrNotFound
	}
	t, ok := findTask(snap, projectID, taskID)
	if !ok {
		return core.Task{}, core.ErrNotFound
	}
	// A signed-off change waiting on the PM's decision lands on the owner's
	// say-so instead, through the same landing.
	if t.Status == core.TaskDeciding {
		landing, err := lp.Core.LandAheadOfPM(ctx, projectID, taskID)
		lp.Nudge()
		return landing, err
	}
	if t.Status != core.TaskDelivered || len(t.Revisions) == 0 || t.Playbook == nil {
		return core.Task{}, fmt.Errorf("only a delivered change can be landed later: %w", core.ErrConflict)
	}
	if p.Playbook == nil || p.Playbook.Medium != core.MediumGit || p.Playbook.Land.Way() == core.LandBranch {
		return core.Task{}, fmt.Errorf("this project lands changes as new branches; set where they land first: %w", core.ErrConflict)
	}
	pinned := *t.Playbook
	pinned.Land = p.Playbook.Land
	m, err := lp.gitMediumFor(ctx, p, &pinned)
	if err != nil {
		return core.Task{}, err
	}
	tip := t.Revisions[len(t.Revisions)-1]
	for _, other := range snap.Tasks {
		if other.ID == t.ID || other.ProjectID != projectID || len(other.Revisions) == 0 || other.Status == core.TaskStopped {
			continue
		}
		theirs := other.Revisions[len(other.Revisions)-1]
		// Anything uncertain refuses: landing out of order would take the
		// other change with it.
		builtOn, err := m.repo.Contains(ctx, tip.Ref, theirs.Ref)
		if err != nil {
			return core.Task{}, fmt.Errorf("could not tell whether %q is built on %q: %w", t.Objective, other.Objective, err)
		}
		if !builtOn {
			continue
		}
		there, err := m.alreadyLanded(ctx, other, theirs)
		if err != nil {
			return core.Task{}, fmt.Errorf("could not tell whether %q has landed: %w", other.Objective, err)
		}
		if !there {
			return core.Task{}, fmt.Errorf("%q is built on %q, which has not landed on %s yet; land that first: %w", t.Objective, other.Objective, pinned.Land.Target, core.ErrConflict)
		}
	}
	landing, err := lp.Core.UpdateTask(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
		if t.Status != core.TaskDelivered {
			return "", core.ErrConflict
		}
		t.Playbook = &pinned
		t.Approved = t.Revisions[len(t.Revisions)-1].N
		t.MaxRounds = max(t.MaxRounds, t.Round+2)
		t.CatchUps = 0
		t.Status, t.Detail = core.TaskLanding, "Landing on "+pinned.Land.Target
		return fmt.Sprintf("Landing %s on %s", t.Objective, pinned.Land.Target), nil
	})
	lp.Nudge()
	return landing, err
}
