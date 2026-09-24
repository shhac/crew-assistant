package app

import (
	"context"
	"fmt"
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
	// readable is what roles may read outside the workspace.
	readable() []string
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

// catcher is a medium whose work can fall behind what lands, and catch up.
// Only git work can; a document has nothing to fall behind.
type catcher interface {
	medium
	// behind reports what the task must take in before it can land, or nil.
	behind(ctx context.Context, t core.Task) (*line, error)
	// cleanMerge merges l into the task without touching the workspace and
	// returns the task on its new base and the merge commit, or "" when the
	// two conflict.
	cleanMerge(ctx context.Context, t core.Task, l line) (core.Task, string, error)
	// conflictMerge leaves the workspace mid-merge for the implementer and
	// returns the files in conflict.
	conflictMerge(ctx context.Context, t core.Task, l line) (core.Task, []string, error)
	// files lists what a revision changes from the task's base.
	files(ctx context.Context, t core.Task, ref string) ([]string, error)
	// alreadyLanded reports whether the revision is already where it lands.
	alreadyLanded(ctx context.Context, t core.Task, r core.Revision) (bool, error)
}

// lag is what the task must take in before it lands, with the medium that
// can take it in, if its medium can fall behind at all.
func lag(ctx context.Context, m medium, t core.Task) (catcher, *line, error) {
	c, ok := m.(catcher)
	if !ok {
		return nil, nil, nil
	}
	l, err := c.behind(ctx, t)
	return c, l, err
}

// line is work a task must include before it lands: the target branch's tip
// for a push or pull request, the project's last landing for new branches, or
// commits someone else pushed to a pull request's branch.
type line struct {
	Commit, Name string
	// What says in words what moved, for the implementer and the activity log.
	What string
	// Foreign marks commits from outside the team. Merging them in is never a
	// clean catch-up that keeps an approval or carries reviews over.
	Foreign bool
}

func (a *App) mediumFor(ctx context.Context, p core.Project, playbook *core.Playbook) (medium, error) {
	if playbook == nil || playbook.Medium == core.MediumDocuments {
		docs, err := localdocs.Open(p.ScratchDirectory)
		dest := ""
		if playbook != nil {
			dest = playbook.DeliverTo
		}
		return docsMedium{docs: docs, deliverTo: dest}, err
	}
	return a.gitMediumFor(ctx, p, playbook)
}

// gitMediumFor is the git medium, for code that needs git itself.
func (a *App) gitMediumFor(ctx context.Context, p core.Project, playbook *core.Playbook) (gitMedium, error) {
	if playbook.Medium != core.MediumGit {
		return gitMedium{}, fmt.Errorf("unsupported medium %q", playbook.Medium)
	}
	repo, err := gitrepo.Open(ctx, p.ScratchDirectory, playbook.Repo, playbook.Prepare)
	if err != nil {
		return gitMedium{}, err
	}
	return gitMedium{repo: repo, playbook: *playbook, landed: p.Landed, remote: a.githubURL, way: wayFor(playbook.Land)}, nil
}

func tipOf(t core.Task) string {
	if n := len(t.Revisions); n > 0 {
		return t.Revisions[n-1].Ref
	}
	return t.Base
}

func startedFrom(t core.Task) string {
	if t.From == "" || len(t.Base) < 7 {
		return "where the task started"
	}
	return t.From + " at " + short(t.Base)
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
