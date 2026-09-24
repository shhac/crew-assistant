package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/media/gitrepo"
)

// maxCatchUps bounds how often landing goes back to catch up with a target
// that keeps moving before the owner is asked what to do.
const maxCatchUps = 4

// approve records the owner's approval of the latest revision and moves the
// task on to landing.
func (a *App) approve(ctx context.Context, t core.Task) error {
	_, err := a.Core.UpdateTask(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
		if t.Finished() {
			return "", nil
		}
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
func (a *App) resumeLanding(ctx context.Context, t core.Task) error {
	_, err := a.Core.UpdateTask(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
		if t.Finished() {
			return "", nil
		}
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
func (a *App) land(ctx context.Context, p core.Project, t core.Task, m medium) error {
	if len(t.Revisions) == 0 {
		return a.setStatus(ctx, t.ID, core.TaskWriting, "")
	}
	if taskPlaybook(p, t).Land.AsksFirst() && !approvalStands(t) && !proposed(t) {
		return a.setStatus(ctx, t.ID, core.TaskDeciding, "Checks are in")
	}
	if gm, ok := m.(gitMedium); ok && gm.playbook.Land.Way() == core.LandPullRequest {
		return a.landPR(ctx, p, t, gm)
	}
	r := t.Revisions[len(t.Revisions)-1]
	done, err := m.alreadyLanded(ctx, t, r)
	if err != nil {
		return a.landingFailed(ctx, t, r, err)
	}
	if done {
		return a.recordLanded(ctx, t, r, taskPlaybook(p, t).Land.Target, "it was already there")
	}
	l, err := m.behind(ctx, t)
	if err != nil {
		return a.landingFailed(ctx, t, r, err)
	}
	if l != nil {
		return a.catchUpRound(ctx, t, *l)
	}
	target, err := m.deliver(ctx, t, r)
	if errors.Is(err, gitrepo.ErrTargetMoved) {
		if l, lineErr := m.behind(ctx, t); lineErr == nil && l != nil {
			return a.catchUpRound(ctx, t, *l)
		}
	}
	if err != nil {
		return a.landingFailed(ctx, t, r, err)
	}
	return a.recordLanded(ctx, t, r, target, "")
}

// proposed reports a task whose pull request is open: updates to it go out
// without asking again, unless they touch what runs or instructs.
func proposed(t core.Task) bool { return t.Proposal != nil && t.Proposal.Number > 0 }

func (a *App) recordLanded(ctx context.Context, t core.Task, r core.Revision, target, note string) error {
	_, err := a.Core.UpdateTask(ctx, t.ID, func(t *core.Task, p *core.Project) (string, error) {
		if t.Finished() {
			return "", nil
		}
		t.Status, t.DecisionID, t.DeliveredTo, t.CatchUps = core.TaskDelivered, "", target, 0
		t.Detail = fmt.Sprintf("Draft %d approved", r.N)
		if target != "" {
			t.Detail += " and delivered to " + target
		}
		if t.Playbook != nil && t.Playbook.Land.Way() != core.LandBranch {
			t.Status, t.Detail = core.TaskLanded, fmt.Sprintf("Draft %d landed on %s", r.N, target)
		}
		if note != "" {
			t.Detail += " (" + note + ")"
		}
		if r.Ref != "" {
			p.Landed = &core.Landing{TaskID: t.ID, Objective: t.Objective, Commit: r.Ref, Branch: target, At: time.Now().UTC()}
		}
		return t.Objective + ": " + t.Detail, nil
	})
	if err != nil || r.Ref == "" {
		return err
	}
	return a.supersedeStaleApprovals(ctx, t.ProjectID)
}

// landingFailed brings the owner a decision rather than retrying on a timer: a
// refused push needs something only they can change.
func (a *App) landingFailed(ctx context.Context, t core.Task, r core.Revision, cause error) error {
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
	if _, err := a.Core.UpdateTask(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
		if t.Finished() {
			return "", nil
		}
		t.ResumeStatus = core.TaskLanding
		return "", nil
	}); err != nil {
		return err
	}
	_, err := a.Core.OpenTaskDecision(ctx, t.ID, decisionFailure, core.DecisionInput{
		Title:          fmt.Sprintf("Draft %d of %s couldn't land", r.N, t.Objective),
		Context:        clip(reason, 900),
		Recommendation: choiceTryAgain + " once the cause is fixed",
		Choices:        []string{choiceTryAgain, choiceStop},
	})
	return err
}

// catchUpRound sends a task back to take in work that landed after it
// started. A clean merge is recorded by the daemon; only conflicts need the
// implementer. A target that keeps moving is brought to the owner.
func (a *App) catchUpRound(ctx context.Context, t core.Task, l line) error {
	tooMany := false
	_, err := a.Core.UpdateTask(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
		if t.Finished() {
			return "", nil
		}
		t.CatchUps++
		if t.CatchUps > maxCatchUps {
			tooMany = true
			t.ResumeStatus = core.TaskLanding
			return "", nil
		}
		t.Status, t.DecisionID, t.CatchUp, t.Detail = core.TaskWriting, "", true, "Catching up: "+l.What
		return t.Objective + " is catching up: " + l.What, nil
	})
	if err != nil || !tooMany {
		return err
	}
	_, err = a.Core.OpenTaskDecision(ctx, t.ID, decisionFailure, core.DecisionInput{
		Title:          fmt.Sprintf("%s keeps having to catch up", t.Objective),
		Context:        fmt.Sprintf("It caught up %d times and the target moved again each time: %s. Nothing was forced.", maxCatchUps, l.What),
		Recommendation: choiceTryAgain + " once the target is quiet",
		Choices:        []string{choiceTryAgain, choiceStop},
	})
	return err
}

// recordCatchUp records a clean merge as a new revision without the
// implementer. The task's own change is unchanged, so the reviewers' passes
// against the current brief carry over and an approval still stands; QA runs
// again on the merged result.
func (a *App) recordCatchUp(ctx context.Context, moved core.Task, m medium, commit string, l line) error {
	files, err := m.files(ctx, moved, commit)
	if err != nil {
		return a.roleFailed(ctx, moved, "The workspace", err)
	}
	reviewers := map[string]bool{}
	for _, r := range roleOf(moved, core.RoleReviewer) {
		reviewers[r.Name] = true
	}
	_, err = a.Core.UpdateTask(ctx, moved.ID, func(t *core.Task, p *core.Project) (string, error) {
		if t.Finished() || len(t.Revisions) == 0 {
			return "", nil
		}
		prev := t.Revisions[len(t.Revisions)-1]
		n := prev.N + 1
		now := time.Now().UTC()
		if !l.Foreign {
			t.Base, t.From = moved.Base, moved.From
		}
		revision := core.Revision{N: n, BriefVersion: p.Brief.Version, Files: files, Ref: commit, CleanMergeOf: prev.N, Summary: "Merged in without conflicts: " + l.What + ".", At: now}
		if l.Foreign {
			// Someone else's commits are new work: nothing carries over.
			revision.CleanMergeOf = 0
		}
		t.Revisions = append(t.Revisions, revision)
		for _, v := range t.Verdicts {
			if l.Foreign {
				break
			}
			if v.Revision != prev.N || !reviewers[v.Role] || v.Outcome != core.VerdictPass || v.BriefVersion != p.Brief.Version {
				continue
			}
			v.Revision, v.At = n, now
			v.Summary = fmt.Sprintf("Carried over from draft %d, which this only merges with work that landed since: %s", prev.N, v.Summary)
			t.Verdicts = append(t.Verdicts, v)
		}
		t.CatchUp, t.Failures, t.RetryAt = false, 0, time.Time{}
		t.Status, t.Detail = core.TaskReviewing, fmt.Sprintf("Draft %d merges in %s cleanly; checking it again", n, l.Name)
		return fmt.Sprintf("%s caught up cleanly: %s", t.Objective, l.What), nil
	})
	return err
}

// supersedeStaleApprovals replaces any approval another task in the project
// is waiting on with a catch-up, so the owner is never asked to approve work
// that is out of date.
func (a *App) supersedeStaleApprovals(ctx context.Context, projectID string) error {
	snap, err := a.Core.Snapshot(ctx)
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
		if !ok || d.Status != "open" || (d.Kind != decisionDelivery && d.Kind != decisionEscalation) {
			continue
		}
		m, err := a.mediumFor(ctx, p, taskPlaybook(p, t))
		if err != nil {
			return err
		}
		l, err := m.behind(ctx, t)
		if err != nil {
			return err
		}
		if l == nil {
			continue
		}
		if err = a.catchUpRound(ctx, t, *l); err != nil {
			return err
		}
		if _, err = a.Core.DismissDecision(ctx, d.ID, "Out of date: "+l.What+". It is catching up and will ask again."); err != nil && !errors.Is(err, core.ErrConflict) {
			return err
		}
	}
	return nil
}

// LandTask lands a change that was delivered under an earlier landing policy,
// such as a branch approved before the project landed on main. Its approval
// stands. A change built on another that has not landed yet is refused, so
// the two land in the order they were built.
func (a *App) LandTask(ctx context.Context, projectID, taskID string) (core.Task, error) {
	snap, err := a.Core.Snapshot(ctx)
	if err != nil {
		return core.Task{}, err
	}
	p, ok := findProject(snap, projectID)
	if !ok {
		return core.Task{}, core.ErrNotFound
	}
	var t core.Task
	for _, candidate := range snap.Tasks {
		if candidate.ID == taskID && candidate.ProjectID == projectID {
			t, ok = candidate, true
		}
	}
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
	m, err := a.mediumFor(ctx, p, &pinned)
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
		builtOn, err := m.(gitMedium).repo.Contains(ctx, tip.Ref, theirs.Ref)
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
	landing, err := a.Core.UpdateTask(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
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
	a.nudgeLoop()
	return landing, err
}
