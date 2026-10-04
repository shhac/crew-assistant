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
		// RegisterWake refreshes an existing loop watch without extending its
		// timeout, so cancellation is visible even before the next poll.
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
	return fmt.Sprintf("%s@%s/%s/%s/merge:%t", pr.CheckState(), text.Short(pr.HeadRefOid), pr.MergeStateStatus, pr.State, pr.MergeInFlight)
}

// prReviewValue changes with any review, comment or thread comment, the
// review decision, or a thread resolved or reopened.
func prReviewValue(pr github.PR) string {
	return fmt.Sprintf("%s/%s/%d/merge:%t", pr.Latest().UTC().Format(time.RFC3339), pr.ReviewDecision, pr.Unresolved(), pr.MergeInFlight)
}

// viewPR reads a pull request named as owner/name#number.
func (lp *Loop) viewPR(ctx context.Context, target string) (github.PR, error) {
	ref, err := github.ParsePRRef(target)
	if err != nil {
		return github.PR{}, err
	}
	return lp.github.View(ctx, ref.Repo, ref.Number)
}

func prValue(on string, pr github.PR) string {
	if on == core.WakeOnReview {
		return prReviewValue(pr)
	}
	return prChecksValue(pr)
}

// prEvent says what changed on a pull request, in words.
func prEvent(w core.Wake, value string) string {
	state, _, _ := strings.Cut(value, "@")
	if w.On != core.WakeOnChecks {
		return fmt.Sprintf("New review activity on pull request %s", w.Target)
	}
	words := map[string]string{"SUCCESS": "passed", "FAILURE": "failed", "PENDING": "are running", "NONE": "are gone"}
	if said, ok := words[state]; ok {
		return fmt.Sprintf("Checks %s on pull request %s", said, w.Target)
	}
	return fmt.Sprintf("Checks on pull request %s are now %s", w.Target, strings.ToLower(state))
}
