package work

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/text"
)

// landWay is how an approved code change lands: a new local branch, a
// fast-forward push onto a target, or a GitHub pull request. Each way owns
// where a task starts, what it must catch up with, and how it delivers.
type landWay interface {
	// start is where the task starts, fetched into the project's clone, and
	// the branch that is; the task's own clone checks it out.
	start(ctx context.Context, m gitMedium, t core.Task) (base, from string, err error)
	// line is what a task must include before it lands, or nil.
	line(ctx context.Context, m gitMedium, t core.Task) (*line, error)
	deliver(ctx context.Context, m gitMedium, t core.Task, r core.Revision) (string, error)
	alreadyLanded(ctx context.Context, m gitMedium, t core.Task, r core.Revision) (bool, error)
	// note tells the owner what approving will do.
	note(m gitMedium, t core.Task) string
}

func wayFor(land core.LandPolicy) landWay {
	switch land.Way() {
	case core.LandPush:
		return pushWay{}
	case core.LandPullRequest:
		return prWay{}
	}
	return branchWay{}
}

// branchWay delivers each approved change as a new local branch. Later tasks
// catch up with the last one that landed.
type branchWay struct{}

func (branchWay) start(ctx context.Context, m gitMedium, t core.Task) (string, string, error) {
	base, from, err := m.repo.Start(ctx, "")
	if err != nil || m.landed == nil {
		return base, from, err
	}
	// Build on what already landed when the owner's branch has not moved past
	// it, rather than starting behind and catching up later.
	ahead, err := m.repo.Contains(ctx, m.landed.Commit, base)
	if err != nil || !ahead || m.landed.Commit == base {
		return base, from, err
	}
	return m.landed.Commit, m.landed.Branch, nil
}

func (branchWay) line(_ context.Context, m gitMedium, t core.Task) (*line, error) {
	if m.landed == nil || m.landed.TaskID == t.ID {
		return nil, nil
	}
	return &line{Commit: m.landed.Commit, Name: m.landed.Branch, What: fmt.Sprintf("“%s” landed on branch %s", m.landed.Objective, m.landed.Branch)}, nil
}

func (branchWay) deliver(ctx context.Context, m gitMedium, t core.Task, r core.Revision) (string, error) {
	return m.repo.Deliver(ctx, r.Ref, m.branchName(t))
}

func (branchWay) alreadyLanded(ctx context.Context, m gitMedium, t core.Task, r core.Revision) (bool, error) {
	_, there, err := m.repo.Delivered(ctx, r.Ref, m.branchName(t))
	return there, err
}

func (branchWay) note(m gitMedium, t core.Task) string {
	note := "Approving creates the branch " + m.branchName(t) + " in " + filepath.Base(m.playbook.Repo) + ", from " + startedFrom(t) + "."
	if m.landed != nil && m.landed.TaskID != t.ID && t.Base == m.landed.Commit {
		note += " It builds on " + m.landed.Objective + ", which landed first, so it includes that change too."
	}
	return note + " Nothing is pushed."
}

// pushWay fast-forwards a target branch in the owner's repository. Tasks
// start from the target and catch up with it, freshly fetched.
type pushWay struct{}

func (pushWay) start(ctx context.Context, m gitMedium, t core.Task) (string, string, error) {
	return m.repo.Start(ctx, m.playbook.Land.Target)
}

func (pushWay) line(ctx context.Context, m gitMedium, t core.Task) (*line, error) {
	target := m.playbook.Land.Target
	tip, err := m.repo.Fetch(ctx, target)
	if err != nil {
		return nil, err
	}
	what := fmt.Sprintf("%s moved on since this request started (it is now at %s)", target, text.Short(tip))
	if m.landed != nil && m.landed.TaskID != t.ID && m.landed.Commit == tip {
		what = fmt.Sprintf("“%s” landed on %s", m.landed.Objective, target)
	}
	return &line{Commit: tip, Name: target, What: what}, nil
}

// deliver lands the change as one commit, so the target's history reads one
// commit per change rather than every draft the team made on the way. A PM
// that chose to keep the task's commits has the target fast-forwarded onto
// them as they are instead. Neither is ever forced.
func (pushWay) deliver(ctx context.Context, m gitMedium, t core.Task, r core.Revision) (string, error) {
	target := m.playbook.Land.Target
	if keepsCommits(t) {
		return target, m.repo.PushFastForward(ctx, r.Ref, target)
	}
	_, err := m.repo.PushSquashed(ctx, r.Ref, target, landingMessage(t, r))
	return target, err
}

// alreadyLanded finds the change on the target either as the revision itself,
// put there by hand, or as the one commit a landing made of it.
func (pushWay) alreadyLanded(ctx context.Context, m gitMedium, t core.Task, r core.Revision) (bool, error) {
	tip, err := m.repo.Fetch(ctx, m.playbook.Land.Target)
	if err != nil {
		return false, err
	}
	if there, err := m.repo.Contains(ctx, tip, r.Ref); err != nil || there {
		return there, err
	}
	return m.repo.Mentions(ctx, tip, landedTrailer(t, r))
}

// landedTrailer marks the commit a landing made, so a landing retried after
// a crash, or a later change built on this one, can find it.
func landedTrailer(t core.Task, r core.Revision) string {
	return fmt.Sprintf("Crew-Task: %s revision %d", t.ID, r.N)
}

// landingMessage words the one commit a change lands as: what was asked,
// with its first clause as the subject.
func landingMessage(t core.Task, r core.Revision) string {
	objective := strings.TrimSpace(t.Objective)
	subject, rest := objective, ""
	if head, tail, ok := strings.Cut(objective, ": "); ok && len(head) >= 10 && len(head) <= 72 {
		subject, rest = head, strings.TrimSpace(tail)
	}
	subject = text.Clip(subject, 72)
	drafts := "1 reviewed draft"
	if r.N > 1 {
		drafts = fmt.Sprintf("%d reviewed drafts", r.N)
	}
	body := "Landed by crew-assistant from " + drafts + "."
	if rest != "" {
		body = rest + "\n\n" + body
	}
	return subject + "\n\n" + body + "\n\n" + landedTrailer(t, r)
}

func (pushWay) note(m gitMedium, _ core.Task) string {
	target := m.playbook.Land.Target
	return "Approving moves " + target + " in " + filepath.Base(m.playbook.Repo) + " forward to include it. Nothing already on " + target + " is replaced."
}

// prWay lands through a GitHub pull request. Tasks start from the target on
// GitHub, or a stacked task from the open pull request it is stacked on;
// commits someone else pushes to the pull request's branch are taken in
// before anything is pushed over them.
type prWay struct{}

func (prWay) start(ctx context.Context, m gitMedium, t core.Task) (string, string, error) {
	if s := t.Stack; s != nil && s.Open() {
		base, err := m.fetchGitHub(ctx, s.Branch)
		if err != nil {
			return "", "", err
		}
		return base, s.Branch, nil
	}
	target := m.playbook.Land.Target
	base, err := m.fetchGitHub(ctx, target)
	if err != nil {
		return "", "", err
	}
	return base, target, nil
}

func (prWay) line(ctx context.Context, m gitMedium, t core.Task) (*line, error) {
	if prop := t.Proposal; prop != nil && prop.Pushed != "" {
		head, err := m.fetchGitHub(ctx, prop.Branch)
		if err == nil && head != prop.Pushed {
			if in, err := m.repo.Contains(ctx, tipOf(t), head); err == nil && !in {
				return &line{Commit: head, Name: prop.Branch, What: "someone else pushed to the pull request's branch " + prop.Branch, Foreign: true}, nil
			}
		}
	}
	if t.OnParent() {
		return stackLine(ctx, m, t)
	}
	target := m.playbook.Land.Target
	tip, err := m.fetchGitHub(ctx, target)
	if err != nil {
		return nil, err
	}
	// A task built on another branch, such as one it was stacked on, takes
	// the target in however cleanly they would merge.
	return &line{Commit: tip, Name: target, What: fmt.Sprintf("%s on GitHub moved on since this request started (it is now at %s)", target, text.Short(tip)), Required: t.From != "" && t.From != target}, nil
}

// stackLine is what a stacked task still on its parent's branch follows:
// that branch while its pull request is open, taken in whenever it was
// rewritten, and the target, replayed onto, once it has merged. One whose
// parent will not merge follows nothing until the owner decides.
func stackLine(ctx context.Context, m gitMedium, t core.Task) (*line, error) {
	s, target := t.Stack, m.playbook.Land.Target
	if s.Merged() {
		tip, err := m.fetchGitHub(ctx, target)
		if err != nil {
			return nil, err
		}
		return &line{Commit: tip, Name: target, What: fmt.Sprintf("“%s”, which this is stacked on, merged into %s", s.Objective, target), Required: true}, nil
	}
	if !s.Open() {
		return nil, nil
	}
	tip, err := m.fetchGitHub(ctx, s.Branch)
	if err != nil {
		return nil, err
	}
	joined, err := m.repo.Contains(ctx, tip, t.Base)
	if err != nil {
		return nil, err
	}
	return &line{Commit: tip, Name: s.Branch, What: fmt.Sprintf("“%s”, which this is stacked on, moved its pull request's branch %s on (it is now at %s)", s.Objective, s.Branch, text.Short(tip)), Required: !joined}, nil
}

// prBase is the branch a task's pull request merges into: the branch of the
// pull request it is stacked on, while it sits there and that has not
// merged, else the target.
func prBase(t core.Task, land core.LandPolicy) string {
	if t.OnParent() && !t.Stack.Merged() {
		return t.Stack.Branch
	}
	return land.Target
}

func (prWay) deliver(context.Context, gitMedium, core.Task, core.Revision) (string, error) {
	return "", errors.New("a change landing by pull request is merged by GitHub, not delivered")
}

func (prWay) alreadyLanded(ctx context.Context, m gitMedium, t core.Task, r core.Revision) (bool, error) {
	_, merged, err := mergedPR(ctx, m, t, r)
	if errors.Is(err, errDeliveryClosed) {
		err = nil
	}
	return merged, err
}

var errDeliveryClosed = errors.New("pull request closed without merging")

// mergedPR also returns the actual target commit (which may be a squash).
func mergedPR(ctx context.Context, m gitMedium, t core.Task, r core.Revision) (string, bool, error) {
	if t.Proposal == nil || t.Proposal.Number == 0 {
		return "", false, nil
	}
	pr, err := m.github.View(ctx, m.playbook.Land.GitHub, t.Proposal.Number)
	if err != nil {
		return "", false, err
	}
	if pr.State == "MERGED" {
		if pr.HeadRefOid != r.Ref {
			return "", false, errors.New("merged pull request head differs from the delivery intent")
		}
		if pr.MergeCommit == nil || pr.MergeCommit.Oid == "" {
			return "", false, errors.New("pull request merge commit is unconfirmed")
		}
		return pr.MergeCommit.Oid, true, nil
	}
	if pr.State == "OPEN" {
		if t.Delivering != nil && t.Delivering.Refused {
			return "", false, nil
		}
		return "", false, errDeliveryPending
	}
	if pr.State != "CLOSED" {
		return "", false, errors.New("pull request delivery state is unconfirmed")
	}
	return "", false, errDeliveryClosed
}

func (prWay) note(m gitMedium, t core.Task) string {
	land := m.playbook.Land
	if t.PROpen() {
		return fmt.Sprintf("Approving merges pull request #%d (%s) into %s by %s. It is approved where review is asked for, its checks are green and every thread is resolved.", t.Proposal.Number, t.Proposal.URL, land.Target, land.MergeMethod())
	}
	note := fmt.Sprintf("Approving opens a pull request on %s from %s into %s. The team answers its reviews and checks, and it merges by %s once it's approved and green.", land.GitHub, m.prBranch(t), prBase(t, land), land.MergeMethod())
	if t.OnParent() {
		note += fmt.Sprintf(" It is stacked on “%s”: it merges only after that does, replayed onto %s.", t.Stack.Objective, land.Target)
	}
	pr := prText(t)
	return note + "\n\nIt opens as “" + pr.Title + "”:\n" + text.Clip(pr.Body, 1500)
}
