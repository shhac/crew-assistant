package work

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
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
	prop := core.Proposal{}
	if t.Proposal != nil {
		prop = *t.Proposal
	}
	if prop.Branch == "" {
		prop.Branch = m.branchName(t)
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
	if err := lp.postOutbox(ctx, t.ID, m.playbook.Land.GitHub, prop.Number); err != nil {
		return lp.landingFailed(ctx, t, r, err)
	}
	if text := prText(t); prop.Described != described(text) {
		// The implementer rewrote the pull request's text with a later draft.
		if err := lp.github.Edit(ctx, m.playbook.Land.GitHub, prop.Number, text.Title, description(text)); err != nil {
			return lp.landingFailed(ctx, t, r, err)
		}
		prop.Described = described(text)
	}
	pr, err := lp.github.View(ctx, m.playbook.Land.GitHub, prop.Number)
	if err != nil {
		return lp.landingFailed(ctx, t, r, err)
	}
	prop.Observed = observed(pr, prop, time.Now().UTC())
	if err := lp.editProposal(ctx, t.ID, "", func(p *core.Proposal) { p.Observed, p.Described = prop.Observed, prop.Described }); err != nil {
		return err
	}
	return lp.reactTo(ctx, t, m, r, prop, pr)
}

// checksGrace is how long after a push a pull request with no checks is
// taken to have checks still to start, not none at all.
const checksGrace = 3 * time.Minute

// observed is what the loop records of a pull request it looked at.
func observed(pr github.PR, prop core.Proposal, now time.Time) *core.Observed {
	checks := pr.CheckState()
	if checks == "NONE" && now.Sub(prop.PushedAt) < checksGrace {
		checks = "PENDING"
	}
	return &core.Observed{
		Checks:      checks,
		Review:      pr.ReviewDecision,
		Unresolved:  pr.Unresolved(),
		Conflicting: pr.Mergeable == "CONFLICTING" || pr.MergeStateStatus == "DIRTY",
		Ready:       pr.Ready() && checks != "PENDING",
		Ignored:     len(untrusted(pr, prop)),
		At:          now,
	}
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
			if t.Proposal != nil {
				t.Proposal.Answering = true
			}
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
	prop.Pushed, prop.PushedAt = r.Ref, time.Now().UTC()
	return false, lp.editProposal(ctx, t.ID, "", func(p *core.Proposal) {
		p.Branch, p.Pushed, p.PushedAt = prop.Branch, prop.Pushed, prop.PushedAt
	})
}

// openPR opens the pull request, or picks up one that was opened but never
// recorded.
func (lp *Loop) openPR(ctx context.Context, t core.Task, m gitMedium, r core.Revision, prop *core.Proposal) (bool, error) {
	land := m.playbook.Land
	n, url, found, err := lp.github.FindOpen(ctx, land.GitHub, prop.Branch)
	if err != nil {
		return true, lp.landingFailed(ctx, t, r, err)
	}
	pr := prText(t)
	if !found {
		if n, url, err = lp.github.Open(ctx, land.GitHub, land.Target, prop.Branch, pr.Title, description(pr)); err != nil {
			return true, lp.landingFailed(ctx, t, r, err)
		}
		prop.Described = described(pr)
	}
	prop.Number, prop.URL = n, url
	return false, lp.editProposal(ctx, t.ID, "Opened pull request #"+fmt.Sprint(n), func(p *core.Proposal) {
		p.Branch, p.Number, p.URL, p.Described = prop.Branch, prop.Number, prop.URL, prop.Described
	})
}

// reactTo does what the pull request's state calls for: record a merge, bring
// a closed one to the owner, take in someone else's push, answer feedback,
// catch up with a moved base, merge when ready, or wait.
func (lp *Loop) reactTo(ctx context.Context, t core.Task, m gitMedium, r core.Revision, prop core.Proposal, pr github.PR) error {
	land := m.playbook.Land
	if prop.Observed == nil {
		prop.Observed = observed(pr, prop, time.Now().UTC())
	}
	switch {
	case pr.State == "MERGED":
		landed := r
		if pr.MergeCommit != nil && pr.MergeCommit.Oid != "" {
			landed.Ref = pr.MergeCommit.Oid
		}
		return lp.recordLanded(ctx, t, landed, land.Target, fmt.Sprintf("pull request #%d", prop.Number))
	case pr.State == "CLOSED":
		if err := lp.editProposal(ctx, t.ID, "", func(p *core.Proposal) { p.Number, p.URL = 0, "" }); err != nil {
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
	if prop.Observed.Ready {
		held, err := lp.heldFromMerging(ctx, t)
		if err != nil {
			return err
		}
		if len(held) > 0 {
			return lp.holdReadyPR(ctx, t, land.GitHub, pr, held)
		}
	}
	if prop.Observed.Ready && !mergeApproved(t, r) && land.MergeGate() != core.ApproveNone {
		// Ready: whoever approves merging decides, as checks passing does,
		// with the pull request still watched meanwhile.
		if err := lp.watchPR(ctx, t, land.GitHub, pr); err != nil {
			return err
		}
		return lp.setStatus(ctx, t.ID, core.TaskDeciding, fmt.Sprintf("Pull request #%d is ready to merge", prop.Number))
	}
	if prop.Observed.Ready {
		var held []string
		if _, err := lp.updateOpen(ctx, t.ID, func(current *core.Task, p *core.Project) (string, error) {
			if held = core.LandingHeld(p, *current); len(held) > 0 && current.Delivering == nil {
				return "", errDeliveryBlocked
			}
			current.Delivering = &core.Delivering{Revision: r.N, At: time.Now().UTC()}
			return "", nil
		}); err != nil {
			if errors.Is(err, errDeliveryBlocked) {
				return lp.holdReadyPR(ctx, t, land.GitHub, pr, held)
			}
			return err
		}
		mergeErr := lp.github.Merge(ctx, land.GitHub, prop.Number, land.MergeMethod(), r.Ref)
		// A successful request may only enqueue the merge. The next observation
		// records MERGED; until then external conditions must still hold landing.
		if err := lp.notDelivering(ctx, t.ID); err != nil {
			return err
		}
		if mergeErr == nil {
			return lp.setStatus(ctx, t.ID, core.TaskLanding, fmt.Sprintf("Merging pull request #%d", prop.Number))
		}
	}
	return lp.awaitPR(ctx, t, land.GitHub, pr)
}

// heldFromMerging says what holds the task's ready pull request from
// merging: its blockers and the project's landing pause.
func (lp *Loop) heldFromMerging(ctx context.Context, t core.Task) ([]string, error) {
	snap, err := lp.Core.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	p, ok := findProject(snap, t.ProjectID)
	if !ok {
		return nil, core.ErrNotFound
	}
	if fresh, ok := findTask(snap, t.ProjectID, t.ID); ok {
		t = fresh
	}
	return core.LandingHeld(&p, t), nil
}

// holdReadyPR waits on a ready pull request something holds from merging,
// still watching it, so the team answers whatever it says meanwhile; lifting
// the hold looks at it again.
func (lp *Loop) holdReadyPR(ctx context.Context, t core.Task, repo string, pr github.PR, held []string) error {
	if err := lp.watchPR(ctx, t, repo, pr); err != nil {
		return err
	}
	return lp.setStatus(ctx, t.ID, core.TaskAwaiting, fmt.Sprintf("Pull request #%d is ready, but held: %s", pr.Number, strings.Join(held, "; ")))
}

// prFooter ends every pull request description the loop writes.
const prFooter = "Opened by crew-assistant for its owner. Its team answers reviews and CI here."

// prText is the pull request's title and description: the implementer's,
// or the request and its latest draft's summary where it wrote none.
func prText(t core.Task) core.PRText {
	var out core.PRText
	if t.Proposal != nil {
		out = core.PRText{Title: t.Proposal.Title, Body: t.Proposal.Body}
	}
	if out.Title == "" {
		out.Title = text.Clip(t.Objective, 120)
	}
	if out.Body == "" && len(t.Revisions) > 0 {
		out.Body = text.Clip(t.Revisions[len(t.Revisions)-1].Summary, 3000)
	}
	return out
}

// described fingerprints the text GitHub was given, so a rewrite is noticed.
func described(pr core.PRText) string {
	sum := sha256.Sum256([]byte(pr.Title + "\x00" + pr.Body))
	return hex.EncodeToString(sum[:8])
}

// description is what GitHub is given as the pull request's description.
func description(pr core.PRText) string {
	return strings.TrimSpace(pr.Body) + "\n\n" + prFooter
}

// maxPostFailures is how often GitHub may refuse a post before the team
// gives it up, so one bad reply never holds the pull request.
const maxPostFailures = 3

// postOutbox posts what the team has to say on the pull request, one at a
// time, each only once the draft it came with is pushed and each taken off
// the outbox in the change after GitHub has it, so a restart never posts
// twice and a failure loses nothing. One GitHub refuses is tried again at
// the next look, and given up after maxPostFailures.
func (lp *Loop) postOutbox(ctx context.Context, taskID, repo string, number int) error {
	lp.posting.Lock()
	defer lp.posting.Unlock()
	for {
		snap, err := lp.Core.Snapshot(ctx)
		if err != nil {
			return err
		}
		t, ok := snap.FindTask(taskID)
		if !ok {
			return nil
		}
		post, ok := nextPost(t)
		if !ok {
			return nil
		}
		sent := lp.sendPost(ctx, repo, number, post)
		retry := false
		if _, err = lp.Core.UpdateTask(ctx, taskID, func(t *core.Task, _ *core.Project) (string, error) {
			if t.Proposal == nil {
				return "", nil
			}
			outbox := t.Proposal.Outbox
			i := slices.Index(outbox, post)
			if i < 0 {
				return "", nil
			}
			if sent != nil {
				outbox[i].Failures++
				if retry = outbox[i].Failures < maxPostFailures; retry {
					return "", nil
				}
			}
			t.Proposal.Outbox = slices.Delete(outbox, i, i+1)
			if sent != nil {
				return fmt.Sprintf("Gave up posting %s's reply on pull request #%d: %s", post.By, number, text.Clip(sent.Error(), 200)), nil
			}
			return "", nil
		}); err != nil {
			return err
		}
		if retry {
			return nil
		}
	}
}

// nextPost is the first post the task's pull request has the draft for.
func nextPost(t core.Task) (core.PRPost, bool) {
	if t.Proposal == nil {
		return core.PRPost{}, false
	}
	pushed := 0
	for _, r := range t.Revisions {
		if r.Ref == t.Proposal.Pushed {
			pushed = r.N
		}
	}
	for _, post := range t.Proposal.Outbox {
		if post.Revision <= pushed {
			return post, true
		}
	}
	return core.PRPost{}, false
}

// sendPost puts one post on the pull request.
func (lp *Loop) sendPost(ctx context.Context, repo string, number int, post core.PRPost) error {
	switch {
	case post.Resolve:
		return lp.github.Resolve(ctx, post.Thread)
	case post.Thread != "":
		return lp.github.Reply(ctx, post.Thread, signed(post))
	}
	return lp.github.Comment(ctx, repo, number, signed(post))
}

// signed is a post as GitHub shows it: whose it is, and marked as the
// team's own so it is never taken for feedback.
func signed(post core.PRPost) string {
	return post.Body + "\n\n— " + post.By + ", for crew-assistant\n" + ownPost
}

// editProposal changes the fields of the task's stored proposal that one
// step owns, on the record as it is now, never from a copy taken earlier: so
// what other steps wrote meanwhile, the team's posts and the pull request's
// text, is kept.
func (lp *Loop) editProposal(ctx context.Context, taskID, activity string, edit func(*core.Proposal)) error {
	_, err := lp.updateOpen(ctx, taskID, func(t *core.Task, _ *core.Project) (string, error) {
		if t.Proposal == nil {
			t.Proposal = &core.Proposal{}
		}
		edit(t.Proposal)
		if t.Proposal.URL != "" {
			t.DeliveredTo = t.Proposal.URL
		}
		if activity != "" {
			return t.Objective + ": " + activity, nil
		}
		return "", nil
	})
	return err
}

// ownPost marks what the loop posts on a pull request, so the team never
// takes its own replies for feedback.
const ownPost = "<!-- crew-assistant -->"

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

// awaitPR puts the task to sleep until the pull request's checks or reviews
// change, through wakes the loop holds itself.
func (lp *Loop) awaitPR(ctx context.Context, t core.Task, repo string, pr github.PR) error {
	if err := lp.watchPR(ctx, t, repo, pr); err != nil {
		return err
	}
	review := strings.ToLower(strings.ReplaceAll(pr.ReviewDecision, "_", " "))
	if review == "" {
		review = "no review required"
	}
	return lp.setStatus(ctx, t.ID, core.TaskAwaiting, fmt.Sprintf("Waiting on pull request #%d: checks %s, %s", pr.Number, strings.ToLower(pr.CheckState()), review))
}

// watchPR has the loop's wakes watch the pull request's checks and reviews,
// so a change to them is noticed whatever the task is doing meanwhile.
func (lp *Loop) watchPR(ctx context.Context, t core.Task, repo string, pr github.PR) error {
	target := github.PRRef{Repo: repo, Number: pr.Number}.String()
	snap, err := lp.Core.Snapshot(ctx)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, w := range snap.Wakes {
		if w.TaskID == t.ID && w.Owner == core.WakeLoop && w.Status == core.WakeWaiting && (w.Target == target || w.On == core.WakeOnTime) {
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
	// Checks that may yet start after a push change nothing GitHub reports,
	// so the loop looks again once they've had the time to.
	if pr.CheckState() == "NONE" && t.Proposal != nil && time.Since(t.Proposal.PushedAt) < checksGrace && !have[core.WakeOnTime] {
		at := t.Proposal.PushedAt.Add(checksGrace).UTC().Format(time.RFC3339)
		if _, err = lp.Core.RegisterWake(ctx, core.WakeInput{Owner: core.WakeLoop, TaskID: t.ID, ProjectID: t.ProjectID, On: core.WakeOnTime, Target: at, Baseline: "not yet", Timeout: checksGrace + time.Minute}); err != nil {
			return err
		}
	}
	return nil
}

// prChecksValue changes when the checks, the head, mergeability or the pull
// request's own state do: a pull request closed on GitHub wakes the task too.
func prChecksValue(pr github.PR) string {
	return pr.CheckState() + "@" + text.Short(pr.HeadRefOid) + "/" + pr.MergeStateStatus + "/" + pr.State
}

// prReviewValue changes with any review, comment or thread comment, the
// review decision, or a thread resolved or reopened.
func prReviewValue(pr github.PR) string {
	return fmt.Sprintf("%s/%s/%d", pr.Latest().UTC().Format(time.RFC3339), pr.ReviewDecision, pr.Unresolved())
}

// closeEndedPRs closes the pull requests of tasks that now land another way,
// saying why on each. One that can't be closed yet is tried again next pass.
func (lp *Loop) closeEndedPRs(ctx context.Context, snap core.Snapshot) {
	for _, t := range snap.Tasks {
		c := t.ClosePR
		if c == nil {
			continue
		}
		if err := lp.github.Close(ctx, c.Repo, c.Number, c.Note+"\n\n— crew-assistant\n"+ownPost); err != nil {
			continue
		}
		_, _ = lp.Core.UpdateTask(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
			if t.ClosePR == nil || *t.ClosePR != *c {
				return "", nil
			}
			t.ClosePR = nil
			return fmt.Sprintf("Closed pull request #%d for %s", c.Number, t.Objective), nil
		})
	}
}
