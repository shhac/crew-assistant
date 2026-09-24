package work

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/integrations/github"
	"github.com/shhac/crew-assistant/internal/media/gitrepo"
	"github.com/shhac/crew-assistant/internal/text"
)

// landPR lands a change through a GitHub pull request. The loop does the
// mechanics: it publishes each revision to the branch the project owns, opens
// the pull request, turns reviews and failing checks into feedback for the
// team, catches up when the base moves, and merges once GitHub says the pull
// request is approved and green. Between those, the task waits on wakes.
//
// Each step either moves the task on (done) or lets landing continue.
func (lp *Loop) landPR(ctx context.Context, t core.Task, m gitMedium) error {
	if done, err := lp.wokenRound(ctx, t); done || err != nil {
		return err
	}
	r := t.Revisions[len(t.Revisions)-1]
	prop := core.Proposal{Branch: m.branchName(t)}
	if t.Proposal != nil {
		prop = *t.Proposal
	}
	if prop.Pushed != r.Ref {
		done, err := lp.publish(ctx, t, m, r, &prop)
		if done || err != nil {
			return err
		}
	}
	if prop.Number == 0 {
		if done, err := lp.openPR(ctx, t, m, r, &prop); done || err != nil {
			return err
		}
	}
	pr, err := lp.github.View(ctx, m.playbook.Land.GitHub, prop.Number)
	if err != nil {
		return lp.landingFailed(ctx, t, r, err)
	}
	return lp.reactTo(ctx, t, m, r, prop, pr)
}

// wokenRound gives the implementer a round when a wake it asked for has come,
// after clearing the loop's own wakes, which only made it look again.
func (lp *Loop) wokenRound(ctx context.Context, t core.Task) (bool, error) {
	if _, err := lp.Core.TakeTaskWakes(ctx, t.ID, core.WakeLoop); err != nil {
		return true, err
	}
	snap, err := lp.Core.Snapshot(ctx)
	if err != nil {
		return true, err
	}
	for _, w := range snap.Wakes {
		if w.TaskID != t.ID || w.Owner != core.WakeTask || w.Status != core.WakeFired {
			continue
		}
		_, err = lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
			t.NextRound()
			t.Status, t.Detail = core.TaskWriting, w.Event
			return "", nil
		})
		return true, err
	}
	return false, nil
}

// catchUpIfBehind sends the task to catch up when it lacks what it must
// include before landing.
func (lp *Loop) catchUpIfBehind(ctx context.Context, t core.Task, m gitMedium, r core.Revision) (bool, error) {
	l, err := m.behind(ctx, t)
	if err != nil {
		return true, lp.landingFailed(ctx, t, r, err)
	}
	if l == nil {
		return false, nil
	}
	return true, lp.catchUpRound(ctx, t, m, *l)
}

// publish pushes the revision to the pull request's branch under a lease, after
// catching up and, for an update to an open pull request, after the owner has
// seen anything in it that runs or instructs on their side.
func (lp *Loop) publish(ctx context.Context, t core.Task, m gitMedium, r core.Revision, prop *core.Proposal) (bool, error) {
	if done, err := lp.catchUpIfBehind(ctx, t, m, r); done || err != nil {
		return true, err
	}
	if prop.Number > 0 && !approvalStands(t) {
		notes, err := m.repo.Attention(ctx, prop.Pushed, r.Ref)
		if err != nil {
			return true, lp.landingFailed(ctx, t, r, err)
		}
		if len(notes) > 0 {
			_, err = lp.Core.OpenTaskDecision(ctx, t.ID, core.DecisionUpdate, core.DecisionInput{
				Title:          fmt.Sprintf("Check the update to “%s” before it's pushed", t.Objective),
				Context:        "It changes things that run or instruct on your side:\n- " + strings.Join(notes, "\n- ") + "\n\nApproving pushes it to " + prop.Branch + " on " + m.playbook.Land.GitHub + ".",
				Recommendation: choiceApprove + " if these changes are expected",
				Choices:        []string{choiceApprove, choiceChanges},
			})
			return true, err
		}
	}
	err := m.repo.PushOwned(ctx, m.url(), r.Ref, prop.Branch, prop.Pushed, github.CredentialConfig())
	if errors.Is(err, gitrepo.ErrLeaseLost) {
		// Someone else pushed to the branch. If this revision already took
		// their commits in, lease on what it took in; otherwise catch up.
		head, fetchErr := m.repo.FetchFrom(ctx, m.url(), prop.Branch, github.CredentialConfig())
		if fetchErr != nil {
			return true, lp.landingFailed(ctx, t, r, fetchErr)
		}
		if in, _ := m.repo.Contains(ctx, r.Ref, head); !in {
			return lp.catchUpIfBehind(ctx, t, m, r)
		}
		err = m.repo.PushOwned(ctx, m.url(), r.Ref, prop.Branch, head, github.CredentialConfig())
	}
	if err != nil {
		return true, lp.landingFailed(ctx, t, r, err)
	}
	prop.Pushed = r.Ref
	return false, lp.saveProposal(ctx, t.ID, *prop, "")
}

// openPR opens the pull request, or picks up one that was opened but never
// recorded.
func (lp *Loop) openPR(ctx context.Context, t core.Task, m gitMedium, r core.Revision, prop *core.Proposal) (bool, error) {
	land := m.playbook.Land
	n, url, found, err := lp.github.FindOpen(ctx, land.GitHub, prop.Branch)
	if err != nil {
		return true, lp.landingFailed(ctx, t, r, err)
	}
	if !found {
		body := text.Clip(r.Summary, 3000) + "\n\nOpened by crew-assistant for its owner, who approved it. Its team answers reviews and CI here."
		if n, url, err = lp.github.Open(ctx, land.GitHub, land.Target, prop.Branch, t.Objective, body); err != nil {
			return true, lp.landingFailed(ctx, t, r, err)
		}
	}
	prop.Number, prop.URL = n, url
	return false, lp.saveProposal(ctx, t.ID, *prop, "Opened pull request #"+fmt.Sprint(n))
}

// reactTo does what the pull request's state calls for: record a merge, bring
// a closed one to the owner, take in someone else's push, answer feedback,
// catch up with a moved base, merge when ready, or wait.
func (lp *Loop) reactTo(ctx context.Context, t core.Task, m gitMedium, r core.Revision, prop core.Proposal, pr github.PR) error {
	land := m.playbook.Land
	switch {
	case pr.State == "MERGED":
		landed := r
		if pr.MergeCommit != nil && pr.MergeCommit.Oid != "" {
			landed.Ref = pr.MergeCommit.Oid
		}
		return lp.recordLanded(ctx, t, landed, land.Target, fmt.Sprintf("pull request #%d", prop.Number))
	case pr.State == "CLOSED":
		prop.Number, prop.URL = 0, ""
		if err := lp.saveProposal(ctx, t.ID, prop, ""); err != nil {
			return err
		}
		return lp.landingFailed(ctx, t, r, fmt.Errorf("pull request #%d was closed without merging; trying again opens a new one", pr.Number))
	case pr.HeadRefOid != prop.Pushed, pr.Behind():
		if done, err := lp.catchUpIfBehind(ctx, t, m, r); done || err != nil {
			return err
		}
	}
	if feedback := prFeedback(pr, prop, r); len(feedback) > 0 {
		return lp.answerPR(ctx, t, r, pr, prop, feedback)
	}
	if pr.Ready() {
		if err := lp.github.Merge(ctx, land.GitHub, prop.Number, land.MergeMethod(), r.Ref); err == nil {
			// The next look sees it merged and records the landing.
			return lp.setStatus(ctx, t.ID, core.TaskLanding, fmt.Sprintf("Merging pull request #%d", prop.Number))
		}
	}
	return lp.awaitPR(ctx, t, land.GitHub, pr)
}

func (lp *Loop) saveProposal(ctx context.Context, taskID string, prop core.Proposal, activity string) error {
	_, err := lp.updateOpen(ctx, taskID, func(t *core.Task, _ *core.Project) (string, error) {
		t.Proposal = &prop
		if prop.URL != "" {
			t.DeliveredTo = prop.URL
		}
		if activity != "" {
			return t.Objective + ": " + activity, nil
		}
		return "", nil
	})
	return err
}

// prFeedback is what arrived since the team last looked: reviews and
// comments that ask for something, and checks that failed on the latest
// revision. An approval asks for nothing.
func prFeedback(pr github.PR, prop core.Proposal, r core.Revision) []core.Verdict {
	var out []core.Verdict
	for _, f := range pr.FeedbackSince(prop.Seen) {
		if f.Kind == "review (approved)" && strings.TrimSpace(f.Body) == "" {
			continue
		}
		out = append(out, core.Verdict{
			Role:     "@" + f.Author + " on the pull request",
			Outcome:  core.VerdictRevise,
			Summary:  strings.ToUpper(f.Kind[:1]) + f.Kind[1:] + " on the pull request.",
			Outside:  true,
			Findings: []core.Finding{{Note: text.Clip(f.Body, 1500)}},
		})
	}
	if pr.CheckState() == "FAILURE" && pr.HeadRefOid == r.Ref && prop.ChecksFor != r.Ref {
		var findings []core.Finding
		for _, c := range pr.Checks {
			if c.Failed() {
				findings = append(findings, core.Finding{Criterion: c.Label(), Note: "failed on the pull request: " + c.Link()})
			}
		}
		out = append(out, core.Verdict{Role: "CI", Outcome: core.VerdictRevise, Summary: "Checks failed on the pull request.", Findings: findings})
	}
	return out
}

// answerPR gives the team another round with the pull request's feedback. It
// needs no approval: the owner approved the pull request, and its reviewers
// review what the team pushes next.
func (lp *Loop) answerPR(ctx context.Context, t core.Task, r core.Revision, pr github.PR, prop core.Proposal, feedback []core.Verdict) error {
	prop.Seen = pr.Latest()
	if pr.CheckState() == "FAILURE" {
		prop.ChecksFor = r.Ref
	}
	_, err := lp.updateOpen(ctx, t.ID, func(t *core.Task, p *core.Project) (string, error) {
		now := time.Now().UTC()
		for _, v := range feedback {
			v.Revision, v.BriefVersion, v.At = r.N, p.Brief.Version, now
			t.Verdicts = append(t.Verdicts, v)
		}
		t.Proposal = &prop
		t.NextRound()
		t.Status, t.Detail = core.TaskWriting, fmt.Sprintf("Answering pull request #%d", prop.Number)
		return fmt.Sprintf("Answering feedback on pull request #%d for %s", prop.Number, t.Objective), nil
	})
	return err
}

// awaitPR puts the task to sleep until the pull request's checks or reviews
// change, through wakes the loop holds itself.
func (lp *Loop) awaitPR(ctx context.Context, t core.Task, repo string, pr github.PR) error {
	target := github.PRRef{Repo: repo, Number: pr.Number}.String()
	snap, err := lp.Core.Snapshot(ctx)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, w := range snap.Wakes {
		if w.TaskID == t.ID && w.Owner == core.WakeLoop && w.Status == core.WakeWaiting && w.Target == target {
			have[w.On] = true
		}
	}
	for on, baseline := range map[string]string{core.WakeOnChecks: prChecksValue(pr), core.WakeOnReview: prReviewValue(pr)} {
		if have[on] {
			continue
		}
		if _, err = lp.Core.RegisterWake(ctx, core.WakeInput{Owner: core.WakeLoop, TaskID: t.ID, ProjectID: t.ProjectID, On: on, Target: target, Baseline: baseline, Timeout: core.MaxWakeFor}); err != nil {
			return err
		}
	}
	review := strings.ToLower(strings.ReplaceAll(pr.ReviewDecision, "_", " "))
	if review == "" {
		review = "no review required"
	}
	return lp.setStatus(ctx, t.ID, core.TaskAwaiting, fmt.Sprintf("Waiting on pull request #%d: checks %s, %s", pr.Number, strings.ToLower(pr.CheckState()), review))
}

// prChecksValue changes when the checks, the head, mergeability or the pull
// request's own state do: a pull request closed on GitHub wakes the task too.
func prChecksValue(pr github.PR) string {
	return pr.CheckState() + "@" + text.Short(pr.HeadRefOid) + "/" + pr.MergeStateStatus + "/" + pr.State
}

func prReviewValue(pr github.PR) string {
	return pr.Latest().UTC().Format(time.RFC3339) + "/" + pr.ReviewDecision
}
