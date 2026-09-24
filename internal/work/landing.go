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

// maxCatchUps bounds how often landing goes back to catch up with a target
// that keeps moving before the owner is asked what to do.
const maxCatchUps = 4

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

// land takes the approved revision where the project's landing policy says:
// a new branch, a fast-forward push onto the target, or the delivery folder.
// It never forces anything: a moved target sends the task back to catch up.
func (lp *Loop) land(ctx context.Context, p core.Project, t core.Task, m medium) error {
	if len(t.Revisions) == 0 {
		return lp.setStatus(ctx, t.ID, core.TaskWriting, "")
	}
	if t.DirectionPending > 0 {
		return lp.takeDirection(ctx, t)
	}
	if taskPlaybook(p, t).Land.AsksFirst() && !approvalStands(t) && !proposed(t) {
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
			return lp.recordLanded(ctx, t, r, playbook.Land.Target, "it was already there")
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
	return lp.recordLanded(ctx, t, r, target, "")
}

// proposed reports a task whose pull request is open: updates to it go out
// without asking again, unless they touch what runs or instructs.
func proposed(t core.Task) bool { return t.Proposal != nil && t.Proposal.Number > 0 }

func (lp *Loop) recordLanded(ctx context.Context, t core.Task, r core.Revision, target, note string) error {
	_, err := lp.updateOpen(ctx, t.ID, func(t *core.Task, p *core.Project) (string, error) {
		t.Status, t.DecisionID, t.DeliveredTo, t.CatchUps = core.TaskDelivered, "", target, 0
		// Where it went is said by the stage; the detail keeps only a note.
		t.Detail = note
		if r.Ref != "" {
			p.Landed = &core.Landing{TaskID: t.ID, Objective: t.Objective, Commit: r.Ref, Branch: target, At: time.Now().UTC()}
		}
		if t.Playbook != nil && t.Playbook.Land.Way() != core.LandBranch {
			t.Status = core.TaskLanded
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
	_, err := lp.Core.OpenTaskDecision(ctx, t.ID, decisionFailure, core.DecisionInput{
		Title:          fmt.Sprintf("“%s” couldn't land", t.Objective),
		Context:        text.Clip(reason, 900),
		Recommendation: choiceTryAgain + " once the cause is fixed",
		Choices:        []string{choiceTryAgain, choiceStop},
	})
	return err
}

// catchUpRound sends a task back to take in work that landed after it
// started. A clean merge is recorded by the daemon; only conflicts need the
// implementer. A target that keeps moving is brought to the owner.
func (lp *Loop) catchUpRound(ctx context.Context, t core.Task, c catcher, l line) error {
	tooMany := false
	t, err := lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.CatchUps++
		if t.CatchUps > maxCatchUps {
			tooMany = true
			t.ResumeStatus = core.TaskLanding
		}
		return "", nil
	})
	if err != nil {
		return err
	}
	if tooMany {
		_, err = lp.Core.OpenTaskDecision(ctx, t.ID, decisionFailure, core.DecisionInput{
			Title:          fmt.Sprintf("“%s” keeps having to catch up", t.Objective),
			Context:        fmt.Sprintf("It caught up %d times and the target moved again each time: %s. Nothing was forced.", maxCatchUps, l.What),
			Recommendation: choiceTryAgain + " once the target is quiet",
			Choices:        []string{choiceTryAgain, choiceStop},
		})
		return err
	}
	moved, commit, err := c.cleanMerge(ctx, t, l)
	if err != nil {
		return lp.roleFailed(ctx, t, "The workspace", fmt.Errorf("catching up: %s: %w", l.What, err))
	}
	if commit != "" {
		return lp.recordCatchUp(ctx, moved, c, commit, l)
	}
	// A conflict is the implementer's to resolve, in a round of its own.
	_, err = lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.Status, t.DecisionID, t.Detail = core.TaskWriting, "", "Catching up: "+l.What
		return t.Objective + " is catching up: " + l.What, nil
	})
	return err
}

// recordCatchUp records a clean merge as a new revision without the
// implementer. The task's own change is unchanged, so the reviewers' passes
// against the current brief carry over and an approval still stands; QA runs
// again on the merged result.
func (lp *Loop) recordCatchUp(ctx context.Context, moved core.Task, c catcher, commit string, l line) error {
	files, err := c.files(ctx, moved, commit)
	if err != nil {
		return lp.roleFailed(ctx, moved, "The workspace", err)
	}
	reviewers := map[string]bool{}
	for _, r := range roleOf(moved, core.RoleReviewer) {
		reviewers[r.Name] = true
	}
	_, err = lp.updateOpen(ctx, moved.ID, func(t *core.Task, p *core.Project) (string, error) {
		if len(t.Revisions) == 0 {
			return "", nil
		}
		prev := t.Revisions[len(t.Revisions)-1]
		n := prev.N + 1
		now := time.Now().UTC()
		revision := core.Revision{N: n, BriefVersion: p.Brief.Version, Files: files, Ref: commit, Summary: "Merged in without conflicts: " + l.What + ".", At: now}
		// Someone else's commits are new work: nothing carries over from them.
		if !l.Foreign {
			t.Base, t.From = moved.Base, moved.From
			revision.CleanMergeOf = prev.N
			t.Verdicts = append(t.Verdicts, carriedOver(t.Verdicts, prev.N, n, reviewers, p.Brief.Version, now)...)
		}
		t.Revisions = append(t.Revisions, revision)
		t.DecisionID, t.Failures, t.RetryAt = "", 0, time.Time{}
		t.Status, t.Detail = core.TaskReviewing, fmt.Sprintf("Took in %s cleanly; checking it again", l.Name)
		return fmt.Sprintf("%s caught up cleanly: %s", t.Objective, l.What), nil
	})
	return err
}

// carriedOver is the reviewers' passes on draft from, against the current
// brief, restated for draft to, which only merges from with landed work.
func carriedOver(verdicts []core.Verdict, from, to int, reviewers map[string]bool, brief int, now time.Time) []core.Verdict {
	var out []core.Verdict
	carried := map[string]bool{}
	// Latest first: a reviewer asked again has its newest word carried.
	for i := len(verdicts) - 1; i >= 0; i-- {
		v := verdicts[i]
		if v.Revision != from || !reviewers[v.Role] || carried[v.Role] || v.BriefVersion != brief {
			continue
		}
		carried[v.Role] = true
		if v.Outcome != core.VerdictPass {
			continue
		}
		v.Revision, v.At = to, now
		v.Summary = fmt.Sprintf("Carried over from draft %d, which this only merges with work that landed since: %s", from, v.Summary)
		out = append(out, v)
	}
	return out
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
		if !ok || d.Status != "open" || (d.Kind != decisionDelivery && d.Kind != decisionUpdate && d.Kind != decisionEscalation) {
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
		if _, err = lp.Core.DismissDecision(ctx, d.ID, "Out of date: "+l.What+". It is catching up and will ask again."); err != nil && !errors.Is(err, core.ErrConflict) {
			return err
		}
	}
	return nil
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
