package work

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/integrations/github"
	"github.com/shhac/crew-assistant/internal/text"
)

// saysNothing is feedback with nothing in it to answer: the team's own
// posts, and a review with no words of its own. An approval asks for
// nothing, and a comment-only review is only the wrapper of its line
// comments, which arrive in their threads; GitHub makes one for every
// thread reply, the team's own included.
func saysNothing(f github.Feedback) bool {
	empty := strings.TrimSpace(f.Body) == ""
	return strings.Contains(f.Body, ownPost) || ((f.Kind == "review (approved)" || f.Kind == "review (commented)") && empty)
}

// untrusted is feedback since the team last looked from people the
// repository's owner didn't let in: shown, never acted on.
func untrusted(pr github.PR, prop core.Proposal) []github.Feedback {
	var out []github.Feedback
	for _, f := range pr.FeedbackSince(prop.Seen) {
		if !f.Trusted() && !saysNothing(f) {
			out = append(out, f)
		}
	}
	return out
}

// prFeedback is what arrived since the team last looked: reviews, comments
// and review-thread comments that ask for something, from the repository's
// owner, members and collaborators, and checks that failed on the latest
// revision.
func prFeedback(pr github.PR, prop core.Proposal, r core.Revision) []core.Verdict {
	var out []core.Verdict
	for _, f := range pr.FeedbackSince(prop.Seen) {
		if !f.Trusted() || saysNothing(f) {
			continue
		}
		out = append(out, core.Verdict{
			// A review names the head it was made on, which may be an older
			// draft; a comment is on no commit, and claims none.
			Ref:      f.Commit,
			Role:     "@" + f.Author + " on the pull request",
			Outcome:  core.VerdictRevise,
			Summary:  strings.ToUpper(f.Kind[:1]) + f.Kind[1:] + " on the pull request.",
			Outside:  true,
			Findings: []core.Finding{feedbackFinding(f)},
		})
	}
	if pr.CheckState() == "FAILURE" && pr.HeadRefOid == r.Ref && prop.ChecksFor != r.Ref {
		var findings []core.Finding
		for _, c := range pr.Checks {
			if c.Failed() {
				findings = append(findings, core.Finding{Criterion: c.Label(), Note: "failed on the pull request: " + c.Link()})
			}
		}
		out = append(out, core.Verdict{Ref: pr.HeadRefOid, Role: "CI", Outcome: core.VerdictRevise, Summary: "Checks failed on the pull request.", Findings: findings})
	}
	return out
}

// feedbackFinding is one piece of feedback as the implementer reads it; a
// thread comment names its place in the code and the thread to answer.
func feedbackFinding(f github.Feedback) core.Finding {
	finding := core.Finding{Note: text.Clip(f.Body, 1500)}
	if f.Thread != "" {
		finding.Criterion = fmt.Sprintf("%s:%d (thread %s)", f.Path, f.Line, f.Thread)
	}
	return finding
}

// answerPR gives the team another round with the pull request's feedback. It
// needs no approval: the owner approved the pull request, and its reviewers
// review what the team pushes next.
func (lp *Loop) answerPR(ctx context.Context, t core.Task, r core.Revision, pr github.PR, prop core.Proposal, feedback []core.Verdict) error {
	_, err := lp.updateOpen(ctx, t.ID, func(t *core.Task, p *core.Project) (string, error) {
		now := time.Now().UTC()
		for _, v := range feedback {
			// The feedback is answered in the round after the revision pushed
			// to the pull request; its Ref is what it was actually on.
			v.Revision, v.BriefVersion, v.At = r.N, p.Brief.Version, now
			t.Verdicts = append(t.Verdicts, v)
		}
		if t.Proposal == nil {
			t.Proposal = &core.Proposal{}
		}
		t.Proposal.Seen, t.Proposal.Answering = pr.Latest(), true
		if pr.CheckState() == "FAILURE" {
			t.Proposal.ChecksFor = r.Ref
		}
		t.NextRound()
		t.Status, t.Detail = core.TaskWriting, fmt.Sprintf("Answering pull request #%d", prop.Number)
		return fmt.Sprintf("Answering feedback on pull request #%d for %s", prop.Number, t.Objective), nil
	})
	return err
}
