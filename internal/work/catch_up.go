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
func (lp *Loop) takeInLanded(ctx context.Context, t core.Task, m medium) (core.Task, string, *core.DraftCatchUp, error) {
	c, l, err := lag(ctx, m, t)
	if err != nil || l == nil {
		return t, "", nil, err
	}
	moved, commit, err := c.cleanMerge(ctx, t, *l)
	var conflicts []string
	switch {
	case err == nil && commit == "":
		moved, conflicts, err = c.conflictMerge(ctx, t, *l)
	case err == nil:
		// The implementer's next draft builds on the merge.
		err = c.resetTo(ctx, t, commit)
	}
	if err != nil {
		return t, "", nil, fmt.Errorf("catching up: %s: %w", l.What, err)
	}
	t.Base, t.From = moved.Base, moved.From
	return t, catchUpText(l.What, conflicts), &core.DraftCatchUp{Base: moved.Base, From: moved.From, What: l.What, Conflicts: conflicts}, nil
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
	_, err = lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
		dropLandingApproval(t)
		t.Status, t.DecisionID, t.Detail = core.TaskWriting, "", "Catching up: "+l.What
		return t.Objective + " conflicts with what landed: " + l.What + "; the implementer is resolving it", nil
	})
	return err
}

// recordCatchUp records a clean merge as a new revision without the
// implementer, handed over as a draft is. The task's own change is
// unchanged, so the reviewers' passes against the current brief carry over
// and an approval still stands; QA runs again on the merged result.
func (lp *Loop) recordCatchUp(ctx context.Context, moved core.Task, c catcher, commit string, l line) error {
	files, err := c.files(ctx, moved, commit)
	if err != nil {
		return lp.roleFailed(ctx, moved, "The workspace", err)
	}
	if len(moved.Revisions) == 0 {
		return nil
	}
	summary := "Merged in without conflicts: " + l.What + "."
	if l.Diverged {
		summary = "Replayed onto " + l.Name + " without conflicts, after its history was rewritten: " + l.What + "."
	}
	return lp.handOff(ctx, moved, c, core.Handoff{
		Revision: core.Revision{N: len(moved.Revisions) + 1, Files: files, Ref: commit, Summary: summary},
		CatchUp:  &core.CatchUp{Base: moved.Base, From: moved.From, Carry: !l.Foreign, Name: l.Name, What: l.What, Detail: fmt.Sprintf("Took in %s cleanly; checking it again", l.Name)},
	})
}

// carriedOver is the reviewers' passes on draft from, against the current
// brief, restated for draft to, which only merges from with landed work.
// reviewers are the reviewers' checker groups, which group names a seat's.
func carriedOver(verdicts []core.Verdict, from, to int, group func(seat string) string, reviewers map[string]bool, brief int, now time.Time) []core.Verdict {
	var out []core.Verdict
	carried := map[string]bool{}
	// Latest first: a reviewer asked again has its newest word carried, and
	// seats filled from one member count as one reviewer.
	for i := len(verdicts) - 1; i >= 0; i-- {
		v := verdicts[i]
		g := group(v.Role)
		if v.Revision != from || !reviewers[g] || carried[g] || v.BriefVersion != brief {
			continue
		}
		carried[g] = true
		if v.Outcome != core.VerdictPass {
			continue
		}
		v.Revision, v.At = to, now
		v.Summary = fmt.Sprintf("Carried over from draft %d, which this only merges with work that landed since: %s", from, v.Summary)
		out = append(out, v)
	}
	return out
}
