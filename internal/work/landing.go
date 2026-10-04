package work

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/media/gitrepo"
	"github.com/shhac/crew-assistant/internal/text"
)

// errDeliveryBlocked is a condition added after claim, before delivery began.
// The update rolls back and the next schedule pass shows the normal blocker wait.
var errDeliveryBlocked = errors.New("delivery waits on an external condition")

// land takes the approved revision where the project's landing policy says:
// a new branch, a fast-forward push onto the target, or the delivery folder.
// It never forces anything: a moved target sends the task back to catch up.
func (lp *Loop) land(ctx context.Context, p core.Project, t core.Task, m medium) error {
	if t.Delivering == nil && asksFirst(p, t) && !approvalHolds(p, t) && !proposed(t) {
		return lp.setStatus(ctx, t.ID, core.TaskDeciding, "Checks are in")
	}
	playbook := taskPlaybook(p, t)
	if playbook.Land.Way() == core.LandPullRequest {
		gm, err := lp.gitMediumFor(ctx, p, playbook)
		if err != nil {
			return lp.roleFailed(ctx, t, "The workspace", err)
		}
		return lp.landPR(ctx, p, t, gm)
	}
	r := mergeRevision(t, t.Revisions[len(t.Revisions)-1])
	if c, ok := m.(catcher); ok {
		target := playbook.Land.Target
		var done bool
		var err error
		if _, git := m.(gitMedium); git {
			// Branch delivery may have used a numbered name. Reconcile the
			// actual destination before releasing any outward-action intent.
			target, done, err = delivered(ctx, m, t, r)
		} else {
			done, err = c.alreadyLanded(ctx, t, r)
		}
		if err != nil {
			return lp.landingFailed(ctx, t, r, err)
		}
		if done {
			return lp.cleanUp(t, m, lp.recordLanded(ctx, t, r, target, "it was already there"))
		}
		if t.Delivering != nil {
			// The fresh destination read proves the recorded delivery did not land.
			// Release its fence before catch-up or restored asset integration.
			if err := lp.notDelivering(ctx, t.ID); err != nil {
				return err
			}
			t.Delivering = nil
			if t.NeedsAssetIntegration() || t.DirectionPending > 0 {
				return lp.setStatus(ctx, t.ID, core.TaskWriting, "Integrate assets and pending direction after delivery reconciliation")
			}
		}
	}
	c, l, err := lag(ctx, m, t)
	if err != nil {
		return lp.landingFailed(ctx, t, r, err)
	}
	if l != nil {
		return lp.catchUpRound(ctx, t, c, *l)
	}
	if held, err := lp.beginDelivering(ctx, t.ID, r); err != nil || len(held) > 0 {
		return err
	}
	target, err := m.deliver(context.WithoutCancel(ctx), t, r)
	if err != nil {
		if cleared := lp.notDelivering(ctx, t.ID); cleared != nil {
			return cleared
		}
	}
	if errors.Is(err, gitrepo.ErrTargetMoved) {
		if c, l, lagErr := lag(ctx, m, t); lagErr == nil && l != nil {
			return lp.catchUpRound(ctx, t, c, *l)
		}
	}
	if err != nil {
		return lp.landingFailed(ctx, t, r, err)
	}
	return lp.cleanUp(t, m, lp.recordLanded(ctx, t, r, target, ""))
}

// beginDelivering records that r is going out, in the change that checks
// nothing holds it, and says what holds it instead when something does. The
// intent is recorded before anything moves where the change lands, and the
// delivery, once begun, goes to its end: a task stopped or a daemon
// restarted meanwhile is settled from where the change went, never left half
// done or landed unrecorded.
func (lp *Loop) beginDelivering(ctx context.Context, taskID string, r core.Revision) ([]string, error) {
	var held []string
	_, err := lp.updateOpen(ctx, taskID, func(t *core.Task, p *core.Project) (string, error) {
		if t.PRMergePending() {
			return "", core.ErrConflict
		}
		if t.Delivering != nil {
			if t.Delivering.Revision != r.N {
				return "", core.ErrConflict
			}
			return "", nil
		}
		if why := core.LandingHeld(p, *t); len(why) > 0 && t.Delivering == nil {
			held = why
			return "", errDeliveryBlocked
		}
		t.Delivering = &core.Delivering{Revision: r.N, At: time.Now().UTC()}
		return "", nil
	})
	if errors.Is(err, errDeliveryBlocked) {
		return held, nil
	}
	return nil, err
}

// proposed reports a task whose pull request is open: updates to it go out
// without asking again, unless they touch what runs or instructs.
func proposed(t core.Task) bool { return t.Proposal != nil && t.Proposal.Number > 0 }

func (lp *Loop) recordLanded(ctx context.Context, t core.Task, r core.Revision, target, note string) error {
	_, err := lp.updateOpen(ctx, t.ID, func(t *core.Task, p *core.Project) (string, error) {
		return landedOn(t, p, r, target, note), nil
	})
	if err != nil || r.Ref == "" {
		return err
	}
	return lp.supersedeStaleApprovals(ctx, t.ProjectID)
}

// landedOn records a task's change as where it landed, within a change, and
// says so, with what the owner checks now that it has.
func landedOn(t *core.Task, p *core.Project, r core.Revision, target, note string) string {
	said := landedWords(t, p, r, target, note)
	if c := t.OwnerChecklist(); c != "" {
		said += "\n\n" + c
	}
	return said
}

// landedWords records the landing as landedOn does, saying where it went.
func landedWords(t *core.Task, p *core.Project, r core.Revision, target, note string) string {
	t.Status, t.DecisionID, t.DeliveredTo, t.CatchUps, t.LandingFailures = core.TaskDelivered, "", target, 0, nil
	t.Delivering = nil
	t.PRSwitchBy = ""
	if t.Proposal != nil {
		t.Proposal.MergeRequested = ""
	}
	// Where it went is said by the stage; the detail keeps only a note.
	t.Detail = note
	if r.Ref != "" {
		p.Landed = &core.Landing{TaskID: t.ID, Objective: t.Objective, Commit: r.Ref, Branch: target, At: time.Now().UTC()}
	}
	if t.Playbook != nil && t.Playbook.Land.Way() != core.LandBranch {
		t.Status = core.TaskLanded
		// Said only now that it is there, however the landing went.
		if pmApproved(*t) {
			return fmt.Sprintf("The PM landed %s on %s %s: %s", t.Objective, target, t.LandDecision.How(), t.LandDecision.Reason)
		}
		return fmt.Sprintf("%s landed on %s", t.Objective, target)
	}
	if target != "" {
		return fmt.Sprintf("%s delivered to %s", t.Objective, target)
	}
	return t.Objective + " approved"
}

// notDelivering clears a landing's intent once it is known to have gone
// nowhere.
func (lp *Loop) notDelivering(ctx context.Context, taskID string) error {
	_, err := lp.updateOpen(ctx, taskID, func(t *core.Task, _ *core.Project) (string, error) {
		t.Delivering = nil
		return "", nil
	})
	return err
}

// settleDeliveries settles the landings a stop or a restart cut off between
// starting to deliver and recording where the change went, once no step of
// the task is running: a change that landed is recorded as landed, however
// the task was stopped meanwhile, and one that didn't leaves the task as it
// stands. A task still landing needs nothing: its landing looks first for a
// change already there.
func (lp *Loop) settleDeliveries(ctx context.Context, snap core.Snapshot) error {
	var errs []error
	for _, t := range snap.Tasks {
		if snap.ProjectPaused(t.ProjectID) {
			continue
		}
		if t.Delivering == nil && !t.PRMergePending() || !t.Finished() || len(t.Claims) > 0 || lp.jobs.hasTask(t.ID) {
			continue
		}
		p, ok := findProject(snap, t.ProjectID)
		if !ok {
			continue
		}
		errs = append(errs, lp.settleDelivery(ctx, p, t))
	}
	return errors.Join(errs...)
}

func (lp *Loop) settleDelivery(ctx context.Context, p core.Project, t core.Task) error {
	r := mergeRevision(t, core.Revision{})
	i := slices.IndexFunc(t.Revisions, func(candidate core.Revision) bool { return candidate.N == r.N })
	if t.PROpen() {
		policy := taskPlaybook(p, t).Land
		pr, err := lp.github.View(ctx, policy.GitHub, t.Proposal.Number)
		if err != nil {
			return err
		}
		if pr.State == "OPEN" && pr.MergeInFlight {
			return nil
		}
		if pr.State == "MERGED" && i >= 0 {
			if pr.MergeCommit != nil {
				r.Ref = pr.MergeCommit.Oid
			}
			_, err = lp.Core.UpdateTask(ctx, t.ID, func(task *core.Task, p *core.Project) (string, error) {
				if task.Delivering == nil && !task.PRMergePending() {
					return "", nil
				}
				return landedOn(task, p, r, policy.Target, "It landed as you stopped it"), nil
			})
			if err != nil {
				return err
			}
			return lp.supersedeStaleApprovals(ctx, t.ProjectID)
		}
	}
	target, landed := "", false
	if i >= 0 {
		r := t.Revisions[i]
		m, err := lp.mediumFor(ctx, p, taskPlaybook(p, t))
		if err != nil {
			return err
		}
		if target, landed, err = delivered(ctx, m, t, r); err != nil {
			return err
		}
	}
	_, err := lp.Core.UpdateTask(ctx, t.ID, func(task *core.Task, p *core.Project) (string, error) {
		if task.Delivering == nil && !task.PRMergePending() {
			return "", nil
		}
		task.Delivering = nil
		if task.Proposal != nil {
			task.Proposal.MergeRequested = ""
		}
		if !landed {
			return "", nil
		}
		landedOn(task, p, t.Revisions[i], target, "It landed as you stopped it")
		return fmt.Sprintf("%s had already landed when it was stopped", task.Objective), nil
	})
	if err != nil || !landed {
		return err
	}
	return lp.supersedeStaleApprovals(ctx, t.ProjectID)
}

// delivered reports whether revision r of t already went where it lands,
// and where, without delivering anything.
func delivered(ctx context.Context, m medium, t core.Task, r core.Revision) (string, bool, error) {
	g, ok := m.(gitMedium)
	if !ok || r.Ref == "" {
		return "", false, nil
	}
	if g.playbook.Land.Way() == core.LandBranch {
		defer g.locked()()
		return g.repo.Delivered(ctx, r.Ref, g.branchName(t))
	}
	there, err := g.alreadyLanded(ctx, t, r)
	return g.playbook.Land.Target, there, err
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
	snap, p, t, err := lp.lookUp(ctx, projectID, taskID)
	if err != nil {
		return core.Task{}, err
	}
	if why := core.LandingHeld(&p, t); len(why) > 0 {
		return core.Task{}, fmt.Errorf("%s: %w", strings.Join(why, "; "), core.ErrConflict)
	}
	// A signed-off change waiting on the PM's decision lands on the owner's
	// say-so instead, through the same landing.
	if t.Status == core.TaskDeciding {
		landing, err := lp.Core.LandAheadOfPM(ctx, projectID, t.ID)
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
	landing, err := lp.Core.UpdateTask(ctx, t.ID, func(t *core.Task, p *core.Project) (string, error) {
		if why := core.LandingHeld(p, *t); len(why) > 0 {
			return "", fmt.Errorf("%s: %w", strings.Join(why, "; "), core.ErrConflict)
		}
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
