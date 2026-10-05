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
func (lp *Loop) landPR(ctx context.Context, p core.Project, t core.Task, m gitMedium) error {
	if t.Delivering == nil && !t.PRMergePending() {
		if done, err := lp.wokenRound(ctx, t); done || err != nil {
			return err
		}
		if why := t.StackBroken(); why != "" {
			return lp.askUnstack(ctx, t, m.playbook.Land, why)
		}
	}
	r := t.Revisions[len(t.Revisions)-1]
	prop := core.Proposal{}
	if t.Proposal != nil {
		prop = *t.Proposal
	}
	prop.Branch = m.prBranch(t)
	// What is stacked on this pull request follows what it did, once it is
	// done, however this step ends.
	moved := false
	defer func() {
		if moved {
			lp.stackChanged(context.WithoutCancel(ctx), p, t.ID, m)
		}
	}()
	if prop.Pushed != r.Ref && t.Delivering == nil && !t.PRMergePending() {
		done, err := lp.publish(ctx, p, t, m, r, &prop)
		if done || err != nil {
			return err
		}
		moved = true
	}
	if prop.Number == 0 {
		if done, err := lp.openPR(ctx, t, m, r, &prop); done || err != nil {
			return err
		}
		moved = true
	}
	if t.Delivering == nil && !t.PRMergePending() {
		if err := lp.postOutbox(ctx, t.ID, m.playbook.Land.GitHub, prop.Number); err != nil {
			return lp.landingFailed(ctx, t, r, err)
		}
	}
	text := prText(t)
	if body := description(t, text); prop.Described != described(text.Title, body) && t.Delivering == nil && !t.PRMergePending() {
		// The implementer rewrote the pull request's text with a later draft,
		// or the task gained a Linear issue or an author since.
		if err := lp.github.Edit(ctx, m.playbook.Land.GitHub, prop.Number, text.Title, body); err != nil {
			return lp.landingFailed(ctx, t, r, err)
		}
		prop.Described = described(text.Title, body)
	}
	pr, err := lp.github.View(ctx, m.playbook.Land.GitHub, prop.Number)
	if err != nil {
		return lp.landingFailed(ctx, t, r, err)
	}
	prop.Observed = observed(pr, prop, m.playbook.Land, time.Now().UTC())
	if err := lp.editProposal(ctx, t.ID, "", func(p *core.Proposal) { p.Observed, p.Described = prop.Observed, prop.Described }); err != nil {
		return err
	}
	if done, err := lp.retarget(ctx, t, m.playbook.Land, r, &prop, pr); done || err != nil {
		return err
	}
	return lp.reactTo(ctx, p, t, m, r, prop, pr)
}

// retarget points a stacked task's pull request at the branch it now merges
// into: the target, once the task was replayed onto it after the pull
// request below merged. GitHub may have done so already, when that branch
// was deleted.
func (lp *Loop) retarget(ctx context.Context, t core.Task, land core.LandPolicy, r core.Revision, prop *core.Proposal, pr github.PR) (bool, error) {
	want := prBase(t, land)
	if pr.State != "OPEN" || prop.Base == "" || prop.Base == want || t.From != want {
		return false, nil
	}
	if pr.BaseRefName != want {
		if err := lp.github.EditBase(ctx, land.GitHub, prop.Number, want); err != nil {
			return true, lp.landingFailed(ctx, t, r, err)
		}
	}
	prop.Base = want
	return false, lp.editProposal(ctx, t.ID, fmt.Sprintf("Pull request #%d now merges into %s", prop.Number, want), func(p *core.Proposal) { p.Base = want })
}

// askUnstack asks the owner what becomes of a stacked task whose parent
// won't merge: rebased onto the target, or stopped.
func (lp *Loop) askUnstack(ctx context.Context, t core.Task, land core.LandPolicy, why string) error {
	_, err := lp.Core.OpenTaskDecision(ctx, t.ID, core.DecisionUnstack, core.DecisionInput{
		Title:          fmt.Sprintf("“%s” was built on work that won't merge", t.Objective),
		Context:        fmt.Sprintf("Its change was built on another's that won't merge: %s. Rebasing it replays only its own change onto %s, and its pull request then merges into %s; nothing of “%s” comes with it.", why, land.Target, land.Target, t.Stack.Objective),
		Recommendation: core.ChoiceUnstack + " if this change stands on its own",
		Choices:        []string{core.ChoiceUnstack, choiceStop},
	})
	return err
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
// include before landing; required when GitHub says the pull request is
// behind its base or conflicts with it.
func (lp *Loop) catchUpIfBehind(ctx context.Context, t core.Task, m gitMedium, r core.Revision, required bool) (bool, error) {
	l, err := m.behindFor(ctx, t, required)
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
func (lp *Loop) publish(ctx context.Context, p core.Project, t core.Task, m gitMedium, r core.Revision, prop *core.Proposal) (bool, error) {
	if done, err := lp.catchUpIfBehind(ctx, t, m, r, false); done || err != nil {
		return true, err
	}
	if prop.Number > 0 && !approvalStands(t) {
		notes, err := m.repo.OwnAttention(ctx, prop.Pushed, r.Ref, t.Base)
		if err != nil {
			return true, lp.landingFailed(ctx, t, r, err)
		}
		if len(notes) > 0 {
			_, err = lp.Core.OpenTaskDecision(ctx, t.ID, core.DecisionUpdate, core.DecisionInput{
				Against:        &core.DecisionVersions{Revision: r.N, Brief: p.Brief.Version, Text: t.TextVersion},
				Title:          fmt.Sprintf("Check the update to “%s” before it's pushed", t.Objective),
				Context:        "It changes things that run or instruct on your side:\n- " + strings.Join(notes, "\n- ") + "\n\nApproving pushes it to " + prop.Branch + " on " + m.playbook.Land.GitHub + ".",
				Recommendation: choiceApprove + " if these changes are expected",
				Choices:        []string{choiceApprove, choiceChanges},
			})
			return true, err
		}
	}
	err := m.pushGitHub(ctx, r.Ref, prop.Branch, prop.Pushed)
	if errors.Is(err, gitrepo.ErrLeaseLost) {
		// Someone else pushed to the branch. If this revision already took
		// their commits in, lease on what it took in; otherwise catch up.
		head, fetchErr := m.fetchGitHub(ctx, prop.Branch)
		if fetchErr != nil {
			return true, lp.landingFailed(ctx, t, r, fetchErr)
		}
		if in, _ := m.repo.Contains(ctx, r.Ref, head); !in {
			return lp.catchUpIfBehind(ctx, t, m, r, false)
		}
		err = m.pushGitHub(ctx, r.Ref, prop.Branch, head)
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
	base := prBase(t, land)
	if !found {
		body := description(t, pr)
		if n, url, err = lp.github.Open(ctx, land.GitHub, base, prop.Branch, pr.Title, body, land.Draft); err != nil {
			return true, lp.landingFailed(ctx, t, r, err)
		}
		prop.Described = described(pr.Title, body)
	}
	prop.Number, prop.URL, prop.Base = n, url, base
	return false, lp.editProposal(ctx, t.ID, "Opened pull request #"+fmt.Sprint(n), func(p *core.Proposal) {
		p.Branch, p.Number, p.URL, p.Described, p.Base = prop.Branch, prop.Number, prop.URL, prop.Described, prop.Base
	})
}

// reactTo does what the pull request's state calls for: record a merge, bring
// a closed one to the owner, take in someone else's push, answer feedback,
// catch up with a moved base, ask the owner about outsiders' threads or
// about a draft ready for review, merge when ready, or wait.
func (lp *Loop) reactTo(ctx context.Context, p core.Project, t core.Task, m gitMedium, r core.Revision, prop core.Proposal, pr github.PR) error {
	land := m.playbook.Land
	if prop.Observed == nil {
		prop.Observed = observed(pr, prop, land, time.Now().UTC())
	}
	if pr.State == "MERGED" {
		landed := mergeRevision(t, r)
		if t.Delivering != nil || t.PRMergePending() {
			if pr.HeadRefOid != landed.Ref || pr.MergeCommit == nil || pr.MergeCommit.Oid == "" {
				return fmt.Errorf("pull request merge outcome does not confirm the intended head and merge commit")
			}
		}
		if pr.MergeCommit != nil && pr.MergeCommit.Oid != "" {
			landed.Ref = pr.MergeCommit.Oid
		}
		return lp.recordLanded(ctx, t, landed, land.Target, fmt.Sprintf("pull request #%d", prop.Number))
	}
	if pr.State == "CLOSED" {
		if t.Delivering != nil {
			if err := lp.notDelivering(ctx, t.ID); err != nil {
				return err
			}
		}
		if err := lp.editProposal(ctx, t.ID, "", func(p *core.Proposal) { p.Number, p.URL, p.MergeRequested = 0, "", "" }); err != nil {
			return err
		}
		lp.stackedLook(ctx, t, fmt.Sprintf("the pull request of “%s”, which this is stacked on, was closed", t.Objective))
		return lp.landingFailed(ctx, t, r, fmt.Errorf("pull request #%d was closed without merging; trying again opens a new one", pr.Number))
	}
	// The fresh observation above reconciles a recorded merge before new
	// feedback, approval or asset holds can redirect its continuation.
	if t.Delivering != nil || t.PRMergePending() {
		if pr.MergeInFlight {
			if t.Delivering != nil {
				if err := lp.acknowledgeMerge(ctx, t.ID, mergeRevision(t, r), prop.Number); err != nil {
					return err
				}
			}
			return lp.awaitPR(ctx, t, land.GitHub, pr)
		}
		if t.Delivering != nil && (t.Delivering.Requested || t.Delivering.Failure != "") {
			if t.Delivering.Refused {
				return lp.settleDelivery(ctx, p, t)
			}
			return lp.awaitPR(ctx, t, land.GitHub, pr)
		}
		// A fresh queue observation proves there is no request in flight.
		// Release the old intent before feedback or integration starts a draft.
		var err error
		t, err = lp.updateOpen(ctx, t.ID, func(task *core.Task, _ *core.Project) (string, error) {
			task.Delivering = nil
			task.Proposal.MergeRequested = ""
			return "", nil
		})
		if err != nil {
			return err
		}
		if t.PRSwitchBy != "" {
			// Schedule applies the deferred policy before any further PR action.
			return nil
		}
		if t.NeedsAssetIntegration() || t.DirectionPending > 0 {
			return lp.setStatus(ctx, t.ID, core.TaskWriting, "Integrate assets and pending direction after merge reconciliation")
		}
	}
	// A task sitting on another branch than its target, such as the pull
	// request it is stacked on, looks there each time: what moved on that
	// branch is never in this pull request's own state.
	if pr.HeadRefOid != prop.Pushed || pr.Behind() || (t.From != "" && t.From != land.Target) {
		if done, err := lp.catchUpIfBehind(ctx, t, m, r, pr.Behind()); done || err != nil {
			return err
		}
	}
	if done, err := lp.markReady(ctx, t, land, r, prop, pr); done || err != nil {
		return err
	}
	if feedback := prFeedback(pr, prop, r, land); len(feedback) > 0 {
		lp.addCILogs(ctx, land.GitHub, pr, feedback)
		return lp.answerPR(ctx, t, r, pr, prop, feedback)
	}
	threads := sortThreads(pr, prop, land)
	if len(threads.unasked) > 0 && heldOnlyByThreads(pr, *prop.Observed, threads) {
		return lp.askAboutOutsideThreads(ctx, p, t, land, r, pr, threads.unasked)
	}
	if pr.IsDraft {
		return lp.offerReadyForReview(ctx, p, t, land, r, prop, pr, threads)
	}
	if !prop.Observed.Ready && len(threads.owner) > 0 && heldOnlyByThreads(pr, *prop.Observed, threads) {
		return lp.awaitKeptThreads(ctx, t, land, pr, threads)
	}
	if !prop.Observed.Ready {
		return lp.awaitPR(ctx, t, land.GitHub, pr)
	}
	return lp.mergeReady(ctx, p, t, land, r, prop, pr)
}

// mergeReady merges a pull request ready to land, unless something holds it
// or whoever approves merging hasn't yet, who then decides, as checks
// passing does, with the pull request still watched meanwhile.
func (lp *Loop) mergeReady(ctx context.Context, p core.Project, t core.Task, land core.LandPolicy, r core.Revision, prop core.Proposal, pr github.PR) error {
	if t.PRMergePending() {
		return lp.awaitPR(ctx, t, land.GitHub, pr)
	}
	if t.Delivering == nil && t.NeedsAssetIntegration() {
		return lp.setStatus(ctx, t.ID, core.TaskWriting, "Integrate delivered assets and provenance in a new draft")
	}
	if t.Delivering == nil && pendingLandingAssets(t) {
		_, err := lp.Core.RetainLandingAssets(ctx, t, nil, "PM", true, p.Brief.Version)
		return err
	}
	if held := core.LandingHeld(&p, t); len(held) > 0 && t.Delivering == nil {
		return lp.holdReadyPR(ctx, t, land.GitHub, pr, held)
	}
	if t.Delivering == nil && !mergeApproved(t, r) && land.MergeGate() != core.ApproveNone {
		if err := lp.watchPR(ctx, t, land.GitHub, pr); err != nil {
			return err
		}
		return lp.setStatus(ctx, t.ID, core.TaskDeciding, fmt.Sprintf("Pull request #%d is ready to merge", prop.Number))
	}
	held, err := lp.beginDelivering(ctx, t.ID, r, "")
	if err != nil {
		return err
	}
	if len(held) > 0 {
		return lp.holdReadyPR(ctx, t, land.GitHub, pr, held)
	}
	intentSnapshot, err := lp.Core.Snapshot(ctx)
	if err != nil {
		return err
	}
	intentTask, ok := intentSnapshot.FindTask(t.ID)
	if !ok || intentTask.Delivering == nil {
		return core.ErrStale
	}
	intent := *intentTask.Delivering
	if _, err := lp.updateOpen(ctx, t.ID, func(now *core.Task, _ *core.Project) (string, error) {
		if now.Delivering == nil || !now.Delivering.At.Equal(intent.At) {
			return "", core.ErrStale
		}
		now.Delivering.Requested = true
		return "", nil
	}); err != nil {
		return err
	}
	merged := lp.github.Merge(ctx, land.GitHub, prop.Number, land.MergeMethod(), r.Ref)
	// A successful request may only enqueue the merge. The next observation
	// records MERGED; until then external conditions must still hold landing.
	if merged != nil {
		if err := lp.Core.RecordDeliveryOutcome(context.WithoutCancel(ctx), t.ID, intent, text.Clip(fmt.Sprintf("GitHub refused to merge pull request #%d or its response was lost: %v", prop.Number, merged), 900), github.MergeRefused(merged)); err != nil {
			return err
		}
		// A lost response may follow a remotely applied merge. Observe its
		// outcome without discarding the durable intent or retrying the merge.
		current, err := lp.Core.Snapshot(context.Background())
		if err != nil {
			return err
		}
		now, ok := findTask(current, t.ProjectID, t.ID)
		if !ok {
			return core.ErrNotFound
		}
		if err := lp.settleDelivery(context.Background(), p, now); err != nil {
			return lp.reportDeliveryObservation(context.Background(), now, "Delivery observation could not be confirmed: "+text.Clip(err.Error(), 900))
		}
		return nil
	}
	return lp.acknowledgeMerge(ctx, t.ID, r, prop.Number)
}

// mergeRevision binds an outward action to the draft requested.
func mergeRevision(t core.Task, fallback core.Revision) core.Revision {
	for _, r := range t.Revisions {
		if t.Delivering != nil && r.N == t.Delivering.Revision || t.Proposal != nil && r.Ref == t.Proposal.MergeRequested {
			return r
		}
	}
	return fallback
}

func (lp *Loop) acknowledgeMerge(ctx context.Context, taskID string, r core.Revision, number int) error {
	_, err := lp.updateOpen(ctx, taskID, func(task *core.Task, _ *core.Project) (string, error) {
		if task.Delivering == nil || task.Delivering.Revision != r.N {
			return "", core.ErrConflict
		}
		if task.Proposal == nil {
			task.Proposal = &core.Proposal{}
		}
		task.Proposal.MergeRequested = r.Ref
		task.Delivering = nil
		task.Status, task.Detail = core.TaskLanding, fmt.Sprintf("Merging pull request #%d", number)
		return "", nil
	})
	return err
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

// closeEndedPRs closes the pull requests of tasks that now land another way,
// saying why on each. One already closed or merged needs nothing more; one
// GitHub refuses to close is tried again next pass, and left to the owner
// after maxPostFailures.
func (lp *Loop) closeEndedPRs(ctx context.Context, snap core.Snapshot) {
	for _, t := range snap.Tasks {
		c := t.ClosePR
		if c == nil {
			continue
		}
		err := lp.github.Close(ctx, c.Repo, c.Number, c.Note+"\n\n— crew-assistant\n"+ownPost)
		if err != nil {
			if pr, viewErr := lp.github.View(ctx, c.Repo, c.Number); viewErr == nil && pr.State != "OPEN" {
				err = nil
			}
		}
		_, _ = lp.Core.UpdateTask(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
			if t.ClosePR == nil || *t.ClosePR != *c {
				return "", nil
			}
			if err == nil {
				t.ClosePR = nil
				return fmt.Sprintf("Closed pull request #%d for %s", c.Number, t.Objective), nil
			}
			if t.ClosePR.Failures++; t.ClosePR.Failures < maxPostFailures {
				return "", nil
			}
			t.ClosePR = nil
			return fmt.Sprintf("Couldn't close pull request #%d for %s, which now lands another way; close it yourself: %s", c.Number, t.Objective, text.Clip(err.Error(), 200)), nil
		})
	}
}
