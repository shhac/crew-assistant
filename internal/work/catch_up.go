package work

import (
	"context"
	"fmt"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
)

// takeInLanded merges what landed since into the workspace, so the
// implementer works on top of it: cleanly if it can be, otherwise with the
// conflicts left for it to resolve. It says what happened, for the prompt.
func (lp *Loop) takeInLanded(ctx context.Context, t core.Task, m medium) (core.Task, string, error) {
	c, l, err := lag(ctx, m, t)
	if err != nil || l == nil {
		return t, "", err
	}
	moved, commit, err := c.cleanMerge(ctx, t, *l)
	var conflicts []string
	if err == nil && commit == "" {
		moved, conflicts, err = c.conflictMerge(ctx, t, *l)
	}
	if err != nil {
		return t, "", fmt.Errorf("catching up: %s: %w", l.What, err)
	}
	t, err = lp.updateOpen(ctx, t.ID, func(task *core.Task, _ *core.Project) (string, error) {
		task.Base, task.From = moved.Base, moved.From
		return "", nil
	})
	return t, catchUpText(l.What, conflicts), err
}

// maxCatchUps bounds how often landing goes back to catch up with a target
// that keeps moving before the owner is asked what to do.
const maxCatchUps = 4

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
		_, err = lp.Core.OpenTaskDecision(ctx, t.ID, core.DecisionFailure, core.DecisionInput{
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
	for _, r := range moved.RolesOf(core.RoleReviewer) {
		reviewers[r.Name] = true
	}
	_, err = lp.updateOpen(ctx, moved.ID, func(t *core.Task, p *core.Project) (string, error) {
		if len(t.Revisions) == 0 {
			return "", nil
		}
		prev := t.Revisions[len(t.Revisions)-1]
		n := prev.N + 1
		now := time.Now().UTC()
		summary := "Merged in without conflicts: " + l.What + "."
		if l.Diverged {
			summary = "Replayed onto " + l.Name + " without conflicts, after its history was rewritten: " + l.What + "."
		}
		revision := core.Revision{N: n, BriefVersion: p.Brief.Version, Files: files, Ref: commit, Summary: summary, At: now}
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
