package work

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/integrations/github"
	"github.com/shhac/crew-assistant/internal/text"
)

const (
	choiceMarkReady = "Mark it ready"
	choiceKeepDraft = "Keep it a draft"
	choiceLetTeam   = "Let the team answer them"
	choiceLeaveToMe = "Leave them to me"
	// maxThreadsShown keeps the owner's decision readable on a pull request
	// many outsiders commented on.
	maxThreadsShown = 10
)

// openThreads are a pull request's unresolved review threads by who can
// settle them: the team, for its members' threads, the automated reviewers'
// it trusts and the outsiders' the owner let it answer; the owner, for the
// outsiders' they kept; or no one yet, for outsiders' the owner hasn't been
// asked about.
type openThreads struct{ team, owner, unasked []github.Thread }

func sortThreads(pr github.PR, prop core.Proposal, land core.LandPolicy) openThreads {
	var out openThreads
	for _, th := range pr.Threads {
		if th.Resolved {
			continue
		}
		opener := th.Opener()
		switch {
		case standingOf(land, opener.Author.Login, opener.Association) != outsider,
			slices.ContainsFunc(prop.LetIn, func(l core.LetIn) bool { return l.Thread == th.ID }):
			out.team = append(out.team, th)
		case slices.Contains(prop.OwnerThreads, th.ID):
			out.owner = append(out.owner, th)
		default:
			out.unasked = append(out.unasked, th)
		}
	}
	return out
}

// heldOnlyByThreads reports a pull request that only threads the team may
// not answer keep from being ready: to land, or for a draft, for review.
func heldOnlyByThreads(pr github.PR, o core.Observed, threads openThreads) bool {
	if o.Checks == "PENDING" || len(threads.team) > 0 {
		return false
	}
	if pr.IsDraft {
		return pr.ReadyForReview()
	}
	return pr.ReadyButThreads()
}

// askAboutOutsideThreads asks the owner who answers the outsiders' threads
// that are all that holds the pull request. Their words never reach the
// team unless the owner lets them, since anyone can write anything there.
func (lp *Loop) askAboutOutsideThreads(ctx context.Context, p core.Project, t core.Task, land core.LandPolicy, r core.Revision, pr github.PR, unasked []github.Thread) error {
	// Only threads the owner is shown are decided; the rest are asked
	// about next.
	shown := unasked[:min(len(unasked), maxThreadsShown)]
	ids := make([]string, 0, len(shown))
	for _, th := range shown {
		ids = append(ids, th.ID)
	}
	if err := lp.editProposal(ctx, t.ID, "", func(p *core.Proposal) { p.OutsideAsked = ids }); err != nil {
		return err
	}
	if err := lp.watchPR(ctx, t, land.GitHub, pr); err != nil {
		return err
	}
	_, err := lp.Core.OpenTaskDecision(ctx, t.ID, core.DecisionOutsideThreads, core.DecisionInput{
		Against:        &core.DecisionVersions{Revision: r.N, Brief: p.Brief.Version, Text: t.TextVersion},
		Title:          fmt.Sprintf("Who answers the outside review threads on pull request #%d?", pr.Number),
		Context:        outsideThreadsText(t, pr, unasked),
		Recommendation: choiceLeaveToMe + " unless you've read them and want the team to act on them",
		Choices:        []string{choiceLetTeam, choiceLeaveToMe},
	})
	return err
}

func outsideThreadsText(t core.Task, pr github.PR, unasked []github.Thread) string {
	var b strings.Builder
	ready := "ready to land"
	if pr.IsDraft {
		ready = "ready for review"
	}
	fmt.Fprintf(&b, "Pull request #%d for “%s” is otherwise %s, but people outside the repository left %d unresolved review thread(s) on it. The team never acts on what outsiders write unless you say so. What they wrote, as written:\n", pr.Number, t.Objective, ready, len(unasked))
	for i, th := range unasked {
		if i == maxThreadsShown {
			fmt.Fprintf(&b, "\n…and %d more on the pull request, which you're asked about next.\n", len(unasked)-i)
			break
		}
		opener := th.Opener()
		link := opener.URL
		if link == "" {
			link = pr.URL
		}
		fmt.Fprintf(&b, "\n- @%s on %s:%d: “%s”\n  %s\n", opener.Author.Login, th.Path, th.Line, text.Clip(strings.Join(strings.Fields(opener.Body), " "), 200), link)
	}
	b.WriteString("\n" + choiceLetTeam + " gives the implementer what is in the threads shown now, as requests to weigh on their merits; anything written in them later isn't passed on. " + choiceLeaveToMe + " keeps them from the team")
	if pr.IsDraft {
		b.WriteString(": you resolve them on GitHub, and the draft can still be marked ready for review meanwhile.")
	} else {
		b.WriteString(": the pull request waits until you resolve them on GitHub.")
	}
	return b.String()
}

// chooseOutsideThreads records who answers the outsiders' threads the owner
// was asked about. The team is given only what was written in them by the
// time the owner was asked, which is what the owner saw.
func (lp *Loop) chooseOutsideThreads(ctx context.Context, t core.Task, d core.Decision, team bool) error {
	_, err := lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
		if t.DecisionID != d.ID || t.Proposal == nil {
			return "", nil
		}
		prop := t.Proposal
		asked := prop.OutsideAsked
		prop.OutsideAsked = nil
		t.Status, t.DecisionID = core.TaskLanding, ""
		if !team {
			for _, id := range asked {
				if !slices.Contains(prop.OwnerThreads, id) {
					prop.OwnerThreads = append(prop.OwnerThreads, id)
				}
			}
			t.Detail = "Leaving the outside review threads to you"
			return fmt.Sprintf("You kept the outside review threads on pull request #%d for %s", prop.Number, t.Objective), nil
		}
		for _, id := range asked {
			if !slices.ContainsFunc(prop.LetIn, func(l core.LetIn) bool { return l.Thread == id }) {
				prop.LetIn = append(prop.LetIn, core.LetIn{Thread: id, Through: d.CreatedAt})
			}
		}
		t.Detail = "Answering the outside review threads you let in"
		return fmt.Sprintf("You let the team answer the outside review threads on pull request #%d for %s", prop.Number, t.Objective), nil
	})
	return err
}

// awaitKeptThreads waits on a pull request held only by outsiders' threads
// the owner kept, saying so, since nothing the team does will move it.
func (lp *Loop) awaitKeptThreads(ctx context.Context, t core.Task, land core.LandPolicy, pr github.PR, threads openThreads) error {
	if err := lp.watchPR(ctx, t, land.GitHub, pr); err != nil {
		return err
	}
	return lp.setStatus(ctx, t.ID, core.TaskAwaiting, fmt.Sprintf("Pull request #%d waits on you to resolve the %d outside review thread(s) you kept", pr.Number, len(threads.owner)))
}

// offerReadyForReview asks the owner whether a draft with nothing left for
// the team is marked ready for review, once for each head: kept a draft, it
// is asked about again only after the team pushes again.
func (lp *Loop) offerReadyForReview(ctx context.Context, p core.Project, t core.Task, land core.LandPolicy, r core.Revision, prop core.Proposal, pr github.PR, threads openThreads) error {
	if !pr.ReadyForReview() || prop.Observed.Checks == "PENDING" || len(threads.team) > 0 {
		return lp.awaitPR(ctx, t, land.GitHub, pr)
	}
	if err := lp.watchPR(ctx, t, land.GitHub, pr); err != nil {
		return err
	}
	if prop.KeptDraft == pr.HeadRefOid {
		return lp.setStatus(ctx, t.ID, core.TaskAwaiting, fmt.Sprintf("Pull request #%d stays a draft, as you chose, until the team pushes again", pr.Number))
	}
	about := fmt.Sprintf("Pull request #%d for “%s” is a draft, and nothing is left for the team: its checks are green, it merges cleanly with its base, and no review thread waits on the team.", pr.Number, t.Objective)
	if n := len(threads.owner); n > 0 {
		about += fmt.Sprintf(" %d outside review thread(s) you kept are still open.", n)
	}
	about += " The team's own review and QA passed on this exact commit. Marking it ready asks other people to review it, and starts any checks that only run once a pull request is ready, so mark it ready only if you're confident it will pass someone else's scrutiny. Kept a draft, it is asked about again after the team next pushes.\n\n" + pr.URL
	_, err := lp.Core.OpenTaskDecision(ctx, t.ID, core.DecisionReadyForReview, core.DecisionInput{
		Against:        &core.DecisionVersions{Revision: r.N, Brief: p.Brief.Version, Text: t.TextVersion},
		Title:          fmt.Sprintf("Mark pull request #%d ready for review", pr.Number),
		Context:        about,
		Recommendation: choiceMarkReady + " only if you're confident it will pass someone else's review; otherwise keep it a draft, or answer with what should change",
		Choices:        []string{choiceMarkReady, choiceKeepDraft},
	})
	return err
}

// chooseDraft records the owner's choice about a draft, for the head they
// were shown.
func (lp *Loop) chooseDraft(ctx context.Context, t core.Task, d core.Decision, ready bool) error {
	_, err := lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
		if t.DecisionID != d.ID || t.Proposal == nil {
			return "", nil
		}
		t.Status, t.DecisionID = core.TaskLanding, ""
		if ready {
			t.Proposal.ReadyFor, t.Detail = t.Proposal.Pushed, "Marking the pull request ready for review"
			return fmt.Sprintf("You chose to mark pull request #%d for %s ready for review", t.Proposal.Number, t.Objective), nil
		}
		t.Proposal.KeptDraft, t.Detail = t.Proposal.Pushed, "Keeping the pull request a draft"
		return fmt.Sprintf("You kept pull request #%d for %s a draft", t.Proposal.Number, t.Objective), nil
	})
	return err
}

// markReady marks the draft ready for review once the owner chose to, and
// only at the head they were shown: a later push is asked about again.
func (lp *Loop) markReady(ctx context.Context, t core.Task, land core.LandPolicy, r core.Revision, prop core.Proposal, pr github.PR) (bool, error) {
	if prop.ReadyFor == "" {
		return false, nil
	}
	if !pr.IsDraft || prop.ReadyFor != pr.HeadRefOid {
		return false, lp.editProposal(ctx, t.ID, "", func(p *core.Proposal) { p.ReadyFor = "" })
	}
	if err := lp.github.MarkReady(ctx, land.GitHub, pr.Number); err != nil {
		return true, lp.landingFailed(ctx, t, r, err)
	}
	if err := lp.editProposal(ctx, t.ID, fmt.Sprintf("Marked pull request #%d ready for review", pr.Number), func(p *core.Proposal) { p.ReadyFor = "" }); err != nil {
		return true, err
	}
	if err := lp.watchPR(ctx, t, land.GitHub, pr); err != nil {
		return true, err
	}
	return true, lp.setStatus(ctx, t.ID, core.TaskAwaiting, fmt.Sprintf("Marked pull request #%d ready for review", pr.Number))
}
