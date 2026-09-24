package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/integrations/github"
)

// landWay is how an approved code change lands: a new local branch, a
// fast-forward push onto a target, or a GitHub pull request. Each way owns
// where a task starts, what it must catch up with, and how it delivers.
type landWay interface {
	// start creates the task branch at its starting point and names it.
	start(ctx context.Context, m gitMedium, t core.Task) (base, from string, err error)
	// line is what a task must include before it lands, or nil.
	line(ctx context.Context, m gitMedium, t core.Task) (*line, error)
	deliver(ctx context.Context, m gitMedium, t core.Task, r core.Revision) (string, error)
	alreadyLanded(ctx context.Context, m gitMedium, r core.Revision) (bool, error)
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
	base, from, err := m.repo.Begin(ctx, t.Branch, "")
	if err != nil || m.landed == nil {
		return base, from, err
	}
	// Build on what already landed when the owner's branch has not moved past
	// it, rather than starting behind and catching up later.
	ahead, err := m.repo.Contains(ctx, m.landed.Commit, base)
	if err != nil || !ahead || m.landed.Commit == base {
		return base, from, err
	}
	return m.landed.Commit, m.landed.Branch, m.repo.Reset(ctx, t.Branch, m.landed.Commit)
}

func (branchWay) line(_ context.Context, m gitMedium, t core.Task) (*line, error) {
	if m.landed == nil || m.landed.TaskID == t.ID {
		return nil, nil
	}
	return &line{Commit: m.landed.Commit, Name: m.landed.Branch, What: fmt.Sprintf("%q landed on branch %s", m.landed.Objective, m.landed.Branch)}, nil
}

func (branchWay) deliver(ctx context.Context, m gitMedium, t core.Task, r core.Revision) (string, error) {
	return m.repo.Deliver(ctx, t.Branch, r.Ref, m.branchName(t))
}

func (branchWay) alreadyLanded(context.Context, gitMedium, core.Revision) (bool, error) {
	return false, nil
}

func (branchWay) note(m gitMedium, t core.Task) string {
	note := "Approving creates the branch " + m.branchName(t) + " in " + filepath.Base(m.playbook.Repo) + ", from " + startedFrom(t) + "."
	if m.landed != nil && m.landed.TaskID != t.ID && t.Base == m.landed.Commit {
		note += " It builds on " + m.landed.Objective + ", which landed first, so it includes that change too."
	}
	return note + " Nothing is pushed, and your checkout is not touched."
}

// pushWay fast-forwards a target branch in the owner's repository. Tasks
// start from the target and catch up with it, freshly fetched.
type pushWay struct{}

func (pushWay) start(ctx context.Context, m gitMedium, t core.Task) (string, string, error) {
	return m.repo.Begin(ctx, t.Branch, m.playbook.Land.Target)
}

func (pushWay) line(ctx context.Context, m gitMedium, t core.Task) (*line, error) {
	target := m.playbook.Land.Target
	tip, err := m.repo.Fetch(ctx, target)
	if err != nil {
		return nil, err
	}
	what := fmt.Sprintf("%s moved on since this task started (it is now at %s)", target, short(tip))
	if m.landed != nil && m.landed.TaskID != t.ID && m.landed.Commit == tip {
		what = fmt.Sprintf("%q landed on %s", m.landed.Objective, target)
	}
	return &line{Commit: tip, Name: target, What: what}, nil
}

func (pushWay) deliver(ctx context.Context, m gitMedium, t core.Task, r core.Revision) (string, error) {
	target := m.playbook.Land.Target
	return target, m.repo.PushFastForward(ctx, t.Branch, r.Ref, target)
}

func (pushWay) alreadyLanded(ctx context.Context, m gitMedium, r core.Revision) (bool, error) {
	tip, err := m.repo.Fetch(ctx, m.playbook.Land.Target)
	if err != nil {
		return false, err
	}
	return m.repo.Contains(ctx, tip, r.Ref)
}

func (pushWay) note(m gitMedium, _ core.Task) string {
	target := m.playbook.Land.Target
	return "Approving lands it on " + target + " in " + filepath.Base(m.playbook.Repo) + " by fast-forward: " + target + " only moves forward, nothing already on it is replaced, and nothing is pushed anywhere else."
}

// prWay lands through a GitHub pull request. Tasks start from the target on
// GitHub; commits someone else pushes to the pull request's branch are taken
// in before anything is pushed over them.
type prWay struct{}

func (prWay) start(ctx context.Context, m gitMedium, t core.Task) (string, string, error) {
	target := m.playbook.Land.Target
	base, err := m.repo.FetchFrom(ctx, m.url(), target, github.CredentialConfig())
	if err != nil {
		return "", "", err
	}
	return base, target, m.repo.Reset(ctx, t.Branch, base)
}

func (prWay) line(ctx context.Context, m gitMedium, t core.Task) (*line, error) {
	if prop := t.Proposal; prop != nil && prop.Pushed != "" {
		head, err := m.repo.FetchFrom(ctx, m.url(), prop.Branch, github.CredentialConfig())
		if err == nil && head != prop.Pushed {
			if in, err := m.repo.Contains(ctx, tipOf(t), head); err == nil && !in {
				return &line{Commit: head, Name: prop.Branch, What: "someone else pushed to the pull request's branch " + prop.Branch, Foreign: true}, nil
			}
		}
	}
	target := m.playbook.Land.Target
	tip, err := m.repo.FetchFrom(ctx, m.url(), target, github.CredentialConfig())
	if err != nil {
		return nil, err
	}
	return &line{Commit: tip, Name: target, What: fmt.Sprintf("%s on GitHub moved on since this task started (it is now at %s)", target, short(tip))}, nil
}

func (prWay) deliver(context.Context, gitMedium, core.Task, core.Revision) (string, error) {
	return "", errors.New("a change landing by pull request is merged by GitHub, not delivered")
}

func (prWay) alreadyLanded(context.Context, gitMedium, core.Revision) (bool, error) {
	return false, nil
}

func (prWay) note(m gitMedium, t core.Task) string {
	land := m.playbook.Land
	return fmt.Sprintf("Approving pushes it to %s as the branch %s and opens a pull request into %s. From then on the team answers reviews and CI on it, and it merges by %s once GitHub says it is approved and green; you are asked again only if an update touches what runs or instructs on your side.", land.GitHub, m.branchName(t), land.Target, land.MergeMethod())
}
