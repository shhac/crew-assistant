package work

import (
	"context"
	"errors"
	"fmt"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/media"
	"github.com/shhac/crew-assistant/internal/media/gitrepo"
	"github.com/shhac/crew-assistant/internal/text"
)

// gitMedium is code work in a private clone of one of the project's
// repositories. How an approved change lands is its way.
type gitMedium struct {
	repo     gitrepo.Repo
	playbook core.Playbook
	landed   *core.Landing
	remote   func(repo string) string
	way      landWay
}

// url is where a pull request's branch is pushed and its base fetched from.
func (m gitMedium) url() string { return m.remote(m.playbook.Land.GitHub) }

func (m gitMedium) workspace() string  { return m.repo.Workspace() }
func (m gitMedium) env() []string      { return m.repo.Env() }
func (m gitMedium) readable() []string { return m.repo.Readable() }

func (m gitMedium) begin(ctx context.Context, t core.Task) (core.Task, error) {
	if t.Base != "" {
		return t, nil
	}
	t.Branch = "crew-task/" + t.ID
	base, from, err := m.way.start(ctx, m, t)
	t.Base, t.From = base, from
	return t, err
}

func (m gitMedium) behind(ctx context.Context, t core.Task) (*line, error) {
	tip := tipOf(t)
	if tip == "" {
		return nil, nil
	}
	l, err := m.way.line(ctx, m, t)
	if err != nil || l == nil {
		return nil, err
	}
	contains, err := m.repo.Contains(ctx, tip, l.Commit)
	if err != nil || contains {
		return nil, err
	}
	return l, nil
}

// onto is the task measured from what it takes in, unless that was someone
// else's push to its pull request.
func onto(t core.Task, l line) core.Task {
	if !l.Foreign {
		t.Base, t.From = l.Commit, l.Name
	}
	return t
}

func (m gitMedium) cleanMerge(ctx context.Context, t core.Task, l line) (core.Task, string, error) {
	commit, err := m.repo.MergeClean(ctx, tipOf(t), l.Commit, fmt.Sprintf("catch up with %s: %s", l.Name, text.Clip(t.Objective, 60)))
	if err != nil || commit == "" {
		return t, "", err
	}
	return onto(t, l), commit, m.repo.Reset(ctx, t.Branch, commit)
}

func (m gitMedium) conflictMerge(ctx context.Context, t core.Task, l line) (core.Task, []string, error) {
	if err := m.repo.Reset(ctx, t.Branch, tipOf(t)); err != nil {
		return t, nil, err
	}
	conflicts, err := m.repo.Merge(ctx, l.Commit)
	return onto(t, l), conflicts, err
}

func (m gitMedium) alreadyLanded(ctx context.Context, t core.Task, r core.Revision) (bool, error) {
	if r.Ref == "" {
		return false, nil
	}
	return m.way.alreadyLanded(ctx, m, r)
}

func (m gitMedium) files(ctx context.Context, t core.Task, ref string) ([]string, error) {
	return m.repo.ChangedFiles(ctx, t.Base, ref)
}

func (m gitMedium) reset(ctx context.Context, t core.Task) error {
	ref := tipOf(t)
	if ref == "" {
		return errors.New("the task has no starting point")
	}
	return m.repo.Reset(ctx, t.Branch, ref)
}

func (m gitMedium) snapshot(ctx context.Context, t core.Task, n int) (core.Revision, error) {
	commit, files, err := m.repo.Snapshot(ctx, t.Base, tipOf(t), fmt.Sprintf("draft %d: %s", n, text.Clip(t.Objective, 60)))
	return core.Revision{N: n, Files: files, Ref: commit}, err
}

// checkDir is the clone itself, at the revision. Checks run one at a time, so
// nothing else is working there; whatever a check wrote is reset afterwards.
func (m gitMedium) checkDir(ctx context.Context, t core.Task, r core.Revision) (string, func(), error) {
	if err := m.repo.Reset(ctx, t.Branch, r.Ref); err != nil {
		return "", nil, err
	}
	return m.repo.Workspace(), func() { _ = m.repo.Reset(context.WithoutCancel(ctx), t.Branch, r.Ref) }, nil
}

func (m gitMedium) preview(ctx context.Context, t core.Task, r core.Revision) ([]media.File, error) {
	return m.repo.Preview(ctx, t.Base, r.Ref, 256<<10)
}

func (m gitMedium) deliver(ctx context.Context, t core.Task, r core.Revision) (string, error) {
	return m.way.deliver(ctx, m, t, r)
}

func (m gitMedium) deliveryNote(t core.Task) string {
	note := m.way.note(m, t)
	if means := m.playbook.Land.Means; means != "" {
		note += " For this project, landing means: " + means
	}
	return note
}

func (m gitMedium) branchName(t core.Task) string {
	return m.playbook.BranchPrefix + slugify(t.Objective)
}
