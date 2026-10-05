package work

import (
	"context"
	"fmt"
	"slices"
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

// standing is how far the team may act on what someone wrote on the pull
// request.
type standing int

const (
	// outsider is anyone the repository's owner didn't let in: their words
	// reach the team only once the owner says so.
	outsider standing = iota
	// advisor is an automated reviewer the owner named, weighed as advice.
	advisor
	// member is the repository's owner, a member or a collaborator.
	member
)

func standingOf(land core.LandPolicy, login, association string) standing {
	switch {
	case github.TrustedStanding(association):
		return member
	case land.TrustsBot(login):
		return advisor
	}
	return outsider
}

// untrusted is feedback since the team last looked from people the
// repository's owner didn't let in: shown, and acted on only in threads the
// owner lets the team answer.
func untrusted(pr github.PR, prop core.Proposal, land core.LandPolicy) []github.Feedback {
	var out []github.Feedback
	for _, f := range pr.FeedbackSince(prop.Seen) {
		if standingOf(land, f.Author, f.Association) == outsider && !saysNothing(f) && !letIn(prop, f.Thread, f.At) {
			out = append(out, f)
		}
	}
	return out
}

// letIn reports a comment in a thread the owner let the team answer,
// written before the owner was asked.
func letIn(prop core.Proposal, thread string, at time.Time) bool {
	return thread != "" && slices.ContainsFunc(prop.LetIn, func(l core.LetIn) bool { return l.Thread == thread && !at.After(l.Through) })
}

// prFeedback is what arrived since the team last looked: reviews, comments
// and review-thread comments that ask for something, from the repository's
// owner, members and collaborators, and as advice from the automated
// reviewers the owner trusts; what is in the outsiders' threads the owner
// just let the team answer; and checks that failed on the latest revision.
func prFeedback(pr github.PR, prop core.Proposal, r core.Revision, land core.LandPolicy) []core.Verdict {
	var out []core.Verdict
	for _, f := range pr.FeedbackSince(prop.Seen) {
		if saysNothing(f) {
			continue
		}
		switch standingOf(land, f.Author, f.Association) {
		case member:
			out = append(out, feedbackVerdict(f, strings.ToUpper(f.Kind[:1])+f.Kind[1:]+" on the pull request."))
		case advisor:
			advice := strings.ToUpper(f.Kind[:1]) + f.Kind[1:] + " on the pull request from an automated reviewer: fix it if it is right, otherwise reply why"
			if f.Thread != "" {
				advice += " and resolve the thread"
			}
			out = append(out, feedbackVerdict(f, advice+"."))
		}
	}
	out = append(out, letInFeedback(pr, prop, land)...)
	if pr.CheckState() == "FAILURE" && pr.HeadRefOid == r.Ref && prop.ChecksFor != r.Ref {
		var findings []core.Finding
		for _, c := range failedChecks(pr) {
			findings = append(findings, core.Finding{Criterion: c.Label(), Note: "failed on the pull request: " + c.Link()})
		}
		out = append(out, core.Verdict{Ref: pr.HeadRefOid, Role: ciRole, Outcome: core.VerdictRevise, Summary: "Checks failed on the pull request.", Findings: findings})
	}
	return out
}

func feedbackVerdict(f github.Feedback, summary string) core.Verdict {
	return core.Verdict{
		// A review names the head it was made on, which may be an older
		// draft; a comment is on no commit, and claims none.
		Ref:      f.Commit,
		Role:     "@" + f.Author + " on the pull request",
		Outcome:  core.VerdictRevise,
		Summary:  summary,
		Outside:  true,
		Findings: []core.Finding{feedbackFinding(f)},
	}
}

// letInFeedback is what outsiders had written, when the owner was asked, in
// the threads the owner let the team answer and it hasn't yet been given.
// Teammates' comments in them come as any other feedback does.
func letInFeedback(pr github.PR, prop core.Proposal, land core.LandPolicy) []core.Verdict {
	var out []core.Verdict
	for _, l := range prop.LetIn {
		if l.Given {
			continue
		}
		for _, th := range pr.Threads {
			if th.ID != l.Thread {
				continue
			}
			for _, c := range th.Comments {
				f := github.Feedback{Author: c.Author.Login, Association: c.Association, Kind: "thread comment", Body: c.Body, Thread: th.ID, Path: th.Path, Line: th.Line, At: c.CreatedAt}
				if c.CreatedAt.After(l.Through) || saysNothing(f) || standingOf(land, f.Author, f.Association) != outsider {
					continue
				}
				out = append(out, feedbackVerdict(f, "Thread comment on the pull request from someone outside the repository, which the owner let the team answer: weigh it on its merits."))
			}
		}
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
		for i := range t.Proposal.LetIn {
			t.Proposal.LetIn[i].Given = true
		}
		if pr.CheckState() == "FAILURE" {
			t.Proposal.ChecksFor = r.Ref
		}
		t.NextRound()
		t.Status, t.Detail = core.TaskWriting, fmt.Sprintf("Answering pull request #%d", prop.Number)
		return fmt.Sprintf("Answering feedback on pull request #%d for %s", prop.Number, t.Objective), nil
	})
	return err
}
