package work

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/roles"
)

// review runs each checking role that has not yet judged the latest revision
// against the current brief, one per step: reviewers first, then QA.
func (lp *Loop) review(ctx context.Context, p core.Project, t core.Task, m medium) error {
	r := t.Revisions[len(t.Revisions)-1]
	for _, checker := range t.Checkers() {
		if t.Judged(checker.Name, r.N, p.Brief.Version) {
			continue
		}
		if held, err := lp.holdForUsage(ctx, t, checker); held || err != nil {
			return err
		}
		verdict, err := lp.runChecker(ctx, p, t, r, checker, m, "")
		if err != nil {
			return lp.roleFailed(ctx, t, checker.Name, err)
		}
		_, err = lp.updateOpen(ctx, t.ID, func(t *core.Task, p *core.Project) (string, error) {
			verdict.Revision, verdict.Role, verdict.BriefVersion, verdict.At = r.N, checker.Name, p.Brief.Version, time.Now().UTC()
			t.Verdicts = append(t.Verdicts, verdict)
			t.Failures, t.RetryAt = 0, time.Time{}
			return fmt.Sprintf("%s checked version %d of %s: %s", checker.Name, r.N, t.Objective, outcomeWords[verdict.Outcome]), nil
		})
		return err
	}
	return lp.setStatus(ctx, t.ID, core.TaskDeciding, "Checks are in")
}

// runChecker gives a fresh checking session the revision to judge. Only QA
// may write, to run the check; the medium discards whatever it wrote. A reply
// that is not a usable verdict gets one plain retry.
func (lp *Loop) runChecker(ctx context.Context, p core.Project, t core.Task, r core.Revision, checker core.Role, m medium, note string) (core.Verdict, error) {
	dir, cleanup, err := m.checkDir(ctx, t, r)
	if err != nil {
		return core.Verdict{}, err
	}
	defer cleanup()
	playbook := taskPlaybook(p, t)
	base := checkerPrompt(p, t, r, checker, playbook) + note + learnedGuide(checker, true)
	spec, cleanupLearnings, err := lp.roleSpec(t, checker, dir, checker.Holds(core.RoleQA), m, base)
	if err != nil {
		return core.Verdict{}, err
	}
	defer cleanupLearnings()
	var verdict core.Verdict
	_, learned, parseErr, err := lp.askForJSON(ctx, spec, func(reply string) (err error) {
		verdict, err = parseVerdict(reply)
		return err
	})
	if err != nil {
		return core.Verdict{}, err
	}
	if parseErr != nil {
		return core.Verdict{}, parseErr
	}
	lp.recordLearned(ctx, p, t, checker, m, learned)
	return verdict, nil
}

// askForJSON runs a role and reads its reply with parse, asking once more,
// with the reason, when the reply can't be read. It gives the reply it
// settled on, the one parse accepted or else the last, which a caller may
// still use as written, and that reply's learned block, so what a role
// learned is recorded once. parseErr is why the last reply couldn't be read;
// runErr is the role failing to run at all.
func (lp *Loop) askForJSON(ctx context.Context, spec roles.Spec, parse func(reply string) error) (reply, learned string, parseErr, runErr error) {
	base := spec.Prompt
	for attempt := 0; attempt < 2; attempt++ {
		result, err := lp.runner.Run(ctx, spec)
		if err != nil {
			return reply, learned, parseErr, err
		}
		reply, learned = splitBlock(result.Text, "learned")
		if parseErr = parse(reply); parseErr == nil {
			return reply, learned, nil, nil
		}
		spec.Prompt = base + "\n\nYour previous reply could not be used (" + parseErr.Error() + "). Reply with only the JSON object."
	}
	return reply, learned, parseErr, nil
}

// decide turns the latest reviews into the next step. Deterministic: revise
// until the round limit, bring the owner questions, a limit reached, or a
// draft every reviewer passed.
func (lp *Loop) decide(ctx context.Context, p core.Project, t core.Task) error {
	r := t.Revisions[len(t.Revisions)-1]
	var current []core.Verdict
	for _, v := range t.Verdicts {
		if v.Revision == r.N && v.BriefVersion == p.Brief.Version {
			current = append(current, v)
		}
	}
	for _, checker := range t.Checkers() {
		if !t.Judged(checker.Name, r.N, p.Brief.Version) {
			// The brief changed after some checks: judge again against it.
			return lp.setStatus(ctx, t.ID, core.TaskReviewing, "Checking again against the updated brief")
		}
	}
	var questions, changes []core.Verdict
	for _, v := range current {
		switch v.Outcome {
		case core.VerdictQuestion:
			questions = append(questions, v)
		case core.VerdictRevise:
			changes = append(changes, v)
		}
	}
	switch {
	// A draft written for an older brief that passes against the current one
	// is still a pass.
	case len(questions) == 0 && len(changes) == 0:
		return lp.askForDelivery(ctx, p, t, r)
	case len(questions) > 0:
		q := questions[0]
		_, err := lp.Core.OpenTaskDecision(ctx, t.ID, core.DecisionQuestion, core.DecisionInput{
			Title:          fmt.Sprintf("%s has a question about “%s”", q.Role, t.Objective),
			Context:        q.Question,
			Recommendation: "Answer it, or let the team decide",
			Choices:        []string{"Use your judgment", choiceStop},
		})
		return err
	case t.Round >= t.MaxRounds:
		_, err := lp.Core.OpenTaskDecision(ctx, t.ID, core.DecisionEscalation, core.DecisionInput{
			Title:          fmt.Sprintf("“%s” still has review points after %d rounds", t.Objective, t.Round),
			Context:        reviewDigest(changes),
			Recommendation: "Another round if these points matter; otherwise accept it as it is",
			Choices:        []string{choiceAnotherRound, choiceAcceptDraft, choiceStop},
		})
		return err
	default:
		_, err := lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
			t.NextRound()
			t.Status, t.Detail = core.TaskWriting, ""
			return fmt.Sprintf("Round %d of %s: revising after review", t.Round, t.Objective), nil
		})
		return err
	}
}

func reviewDigest(verdicts []core.Verdict) string {
	var b strings.Builder
	for _, v := range verdicts {
		fmt.Fprintf(&b, "%s: %s\n", v.Role, v.Summary)
		for _, f := range v.Findings {
			fmt.Fprintf(&b, "- %s\n", f.Note)
		}
	}
	return strings.TrimSpace(b.String())
}

var outcomeWords = map[string]string{core.VerdictPass: "passed", core.VerdictRevise: "asked for changes", core.VerdictQuestion: "asked a question"}
