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
	switch {
	case err == nil && commit == "":
		moved, conflicts, err = c.conflictMerge(ctx, t, *l)
	case err == nil:
		// The implementer's next draft builds on the merge.
		err = c.resetTo(ctx, t, commit)
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
	// A conflict with work another task of the project landed while this
	// one was built beside it comes to the owner, never forced or resolved
	// unasked; letting the implementer resolve it is what they are
	// recommended.
	if sibling, ok, err := lp.landedBeside(ctx, t); err != nil || ok {
		if err != nil {
			return err
		}
		if _, err = lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
			landingFailure(t, "Catching up conflicted: "+l.What)
			t.ResumeStatus, t.Detail = core.TaskWriting, "Catching up: "+l.What
			return "", nil
		}); err != nil {
			return err
		}
		_, err = lp.Core.OpenTaskDecision(ctx, t.ID, core.DecisionFailure, core.DecisionInput{
			Title:          fmt.Sprintf("“%s” conflicts with “%s”, which landed while both were under way", t.Objective, sibling.Objective),
			Context:        fmt.Sprintf("Catching up with %s conflicted. Nothing was forced or resolved.", l.What),
			Recommendation: choiceResolve,
			Choices:        []string{choiceResolve, choiceStop},
		})
		return err
	}
	// Any other conflict is the implementer's to resolve, in a round of its
	// own.
	_, err = lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
		landingFailure(t, "Catching up conflicted: "+l.What)
		t.Status, t.DecisionID, t.Detail = core.TaskWriting, "", "Catching up: "+l.What
		return t.Objective + " is catching up: " + l.What, nil
	})
	return err
}

// landedBeside is the task of the same project built beside t, under way at
// the same time as it, that landed last while t was under way, when the
// project works on several tasks at once. One started only once t waited,
// or finished before t started, was built after or before it, not beside
// it, and its conflict is the implementer's as ever; so is every conflict
// while the project works on one task at a time.
func (lp *Loop) landedBeside(ctx context.Context, t core.Task) (core.Task, bool, error) {
	snap, err := lp.Core.Snapshot(ctx)
	if err != nil {
		return core.Task{}, false, err
	}
	p, ok := findProject(snap, t.ProjectID)
	if !ok || p.Playbook == nil || p.Playbook.ActiveCap() <= 1 || t.StartedAt.IsZero() {
		return core.Task{}, false, nil
	}
	var sibling core.Task
	found := false
	for _, o := range snap.Tasks {
		if o.ProjectID != t.ProjectID || o.ID == t.ID || (o.Status != core.TaskLanded && o.Status != core.TaskDelivered) || !o.UpdatedAt.After(t.StartedAt) || !t.BuiltBeside(o) {
			continue
		}
		if !found || o.UpdatedAt.After(sibling.UpdatedAt) {
			sibling, found = o, true
		}
	}
	return sibling, found, nil
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
