package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/media"
	"github.com/shhac/crew-assistant/internal/media/gitrepo"
	"github.com/shhac/crew-assistant/internal/media/localdocs"
)

// medium is what differs between kinds of work: where roles work, how a
// revision is recorded and restored, what reviewers see, and how an approved
// revision leaves.
type medium interface {
	workspace() string
	env() []string
	// begin readies the workspace for a task's first round and returns the
	// task with anything the medium needs to remember.
	begin(ctx context.Context, t core.Task) (core.Task, error)
	// reset restores the latest revision, or the starting point before any.
	reset(ctx context.Context, t core.Task) error
	snapshot(ctx context.Context, t core.Task, n int) (core.Revision, error)
	// checkDir is where a checking role reads revision r, and what to do after.
	checkDir(ctx context.Context, t core.Task, r core.Revision) (string, func(), error)
	preview(ctx context.Context, t core.Task, r core.Revision) ([]media.File, error)
	// deliver makes the approved revision real and says where it went.
	deliver(ctx context.Context, t core.Task, r core.Revision) (string, error)
	// deliveryNote tells the owner what approving will do.
	deliveryNote(t core.Task) string
}

func (a *App) mediumFor(ctx context.Context, p core.Project, playbook *core.Playbook) (medium, error) {
	if playbook == nil || playbook.Medium == core.MediumDocuments {
		docs, err := localdocs.Open(p.ScratchDirectory)
		return docsMedium{docs: docs, deliverTo: deliverTo(playbook)}, err
	}
	if playbook.Medium != core.MediumGit {
		return nil, fmt.Errorf("unsupported medium %q", playbook.Medium)
	}
	repo, err := gitrepo.Open(ctx, p.ScratchDirectory, playbook.Repo, playbook.Prepare)
	if err != nil {
		return nil, err
	}
	return gitMedium{repo: repo, playbook: *playbook}, nil
}

func deliverTo(p *core.Playbook) string {
	if p == nil {
		return ""
	}
	return p.DeliverTo
}

type docsMedium struct {
	docs      localdocs.Docs
	deliverTo string
}

func (m docsMedium) workspace() string { return m.docs.Workspace() }
func (m docsMedium) env() []string     { return nil }
func (m docsMedium) begin(_ context.Context, t core.Task) (core.Task, error) {
	return t, nil
}
func (m docsMedium) reset(_ context.Context, t core.Task) error {
	return m.docs.Reset(t.ID, len(t.Revisions))
}
func (m docsMedium) snapshot(_ context.Context, t core.Task, n int) (core.Revision, error) {
	files, err := m.docs.Snapshot(t.ID, n)
	return core.Revision{N: n, Files: files}, err
}
func (m docsMedium) checkDir(_ context.Context, t core.Task, r core.Revision) (string, func(), error) {
	return m.docs.ReviewCopy(t.ID, r.N)
}
func (m docsMedium) preview(_ context.Context, t core.Task, r core.Revision) ([]media.File, error) {
	return m.docs.Preview(t.ID, r.N, 256<<10)
}
func (m docsMedium) deliver(_ context.Context, t core.Task, r core.Revision) (string, error) {
	if m.deliverTo == "" {
		return "", nil
	}
	return m.docs.Deliver(t.ID, r.N, m.deliverTo, t.Objective)
}
func (m docsMedium) deliveryNote(core.Task) string {
	if m.deliverTo != "" {
		return "Approving copies it into " + m.deliverTo + "."
	}
	return "It stays with the project, ready to read on its page."
}

type gitMedium struct {
	repo     gitrepo.Repo
	playbook core.Playbook
}

func (m gitMedium) workspace() string { return m.repo.Workspace() }
func (m gitMedium) env() []string     { return m.repo.Env() }

func (m gitMedium) begin(ctx context.Context, t core.Task) (core.Task, error) {
	if t.Base != "" {
		return t, nil
	}
	t.Branch = "crew-task/" + t.ID
	base, from, err := m.repo.Begin(ctx, t.Branch)
	t.Base, t.From = base, from
	return t, err
}

func (m gitMedium) reset(ctx context.Context, t core.Task) error {
	ref := t.Base
	if n := len(t.Revisions); n > 0 {
		ref = t.Revisions[n-1].Ref
	}
	if ref == "" {
		return errors.New("the task has no starting point")
	}
	return m.repo.Reset(ctx, t.Branch, ref)
}

func (m gitMedium) snapshot(ctx context.Context, t core.Task, n int) (core.Revision, error) {
	previous := t.Base
	if len(t.Revisions) > 0 {
		previous = t.Revisions[len(t.Revisions)-1].Ref
	}
	commit, files, err := m.repo.Snapshot(ctx, t.Base, previous, fmt.Sprintf("draft %d: %s", n, clip(t.Objective, 60)))
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
	return m.repo.Deliver(ctx, t.Branch, r.Ref, m.branchName(t))
}

func (m gitMedium) branchName(t core.Task) string {
	return m.playbook.BranchPrefix + slugify(t.Objective)
}

func (m gitMedium) deliveryNote(t core.Task) string {
	return "Approving creates the branch " + m.branchName(t) + " in " + filepath.Base(m.playbook.Repo) + ", from " + startedFrom(t) + ". Nothing is pushed, and your checkout is not touched."
}

func startedFrom(t core.Task) string {
	if t.From == "" || len(t.Base) < 7 {
		return "where the task started"
	}
	return t.From + " at " + t.Base[:7]
}

// slugify keeps whole words, up to 40 characters, so a branch name never ends
// mid-word.
func slugify(text string) string {
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	out := ""
	for _, word := range words {
		next := word
		if out != "" {
			next = out + "-" + word
		}
		if len(next) > 40 {
			break
		}
		out = next
	}
	if out == "" && len(words) > 0 {
		out = words[0][:min(len(words[0]), 40)]
	}
	if out == "" {
		return "change"
	}
	return out
}
