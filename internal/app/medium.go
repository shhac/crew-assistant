package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/integrations/github"
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
	// behind reports what the task must take in before it can land, or nil.
	behind(ctx context.Context, t core.Task) (*line, error)
	// catchUp brings l into the task. A clean merge comes back as a commit the
	// daemon can record; otherwise the workspace holds the merge with the
	// files left in conflict for the implementer. Either way the task returns
	// on its new base.
	catchUp(ctx context.Context, t core.Task, l line) (moved core.Task, clean string, conflicts []string, err error)
	// alreadyLanded reports whether the revision is already where it lands.
	alreadyLanded(ctx context.Context, t core.Task, r core.Revision) (bool, error)
	// files lists what a revision changes from the task's base.
	files(ctx context.Context, t core.Task, ref string) ([]string, error)
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
		return docsMedium{docs: docs, deliverTo: deliverTo(playbook)}, err
	}
	if playbook.Medium != core.MediumGit {
		return nil, fmt.Errorf("unsupported medium %q", playbook.Medium)
	}
	repo, err := gitrepo.Open(ctx, p.ScratchDirectory, playbook.Repo, playbook.Prepare)
	if err != nil {
		return nil, err
	}
	return gitMedium{repo: repo, playbook: *playbook, landed: p.Landed, remote: a.githubURL}, nil
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

func (m docsMedium) workspace() string  { return m.docs.Workspace() }
func (m docsMedium) env() []string      { return nil }
func (m docsMedium) readable() []string { return nil }
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
func (m docsMedium) behind(context.Context, core.Task) (*line, error) { return nil, nil }
func (m docsMedium) catchUp(_ context.Context, t core.Task, _ line) (core.Task, string, []string, error) {
	return t, "", nil, nil
}
func (m docsMedium) alreadyLanded(context.Context, core.Task, core.Revision) (bool, error) {
	return false, nil
}
func (m docsMedium) files(context.Context, core.Task, string) ([]string, error) { return nil, nil }
func (m docsMedium) deliveryNote(core.Task) string {
	if m.deliverTo != "" {
		return "Approving copies it into " + m.deliverTo + "."
	}
	return "It stays with the project, ready to read on its page."
}

type gitMedium struct {
	repo     gitrepo.Repo
	playbook core.Playbook
	landed   *core.Landing
	remote   func(repo string) string
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
	// A task that lands on a target starts from it, whatever the owner has
	// checked out.
	if m.playbook.Land.Way() == core.LandPullRequest {
		base, err := m.repo.FetchFrom(ctx, m.url(), m.playbook.Land.Target, github.CredentialConfig())
		if err != nil {
			return t, err
		}
		t.Base, t.From = base, m.playbook.Land.Target
		return t, m.repo.Reset(ctx, t.Branch, base)
	}
	from := ""
	if m.playbook.Land.Way() != core.LandBranch {
		from = m.playbook.Land.Target
	}
	base, start, err := m.repo.Begin(ctx, t.Branch, from)
	if err != nil {
		return t, err
	}
	t.Base, t.From = base, start
	// Build on what already landed as a new branch when the owner's branch has
	// not moved past it, rather than starting behind and catching up later.
	if m.playbook.Land.Way() != core.LandBranch || m.landed == nil {
		return t, nil
	}
	ahead, err := m.repo.Contains(ctx, m.landed.Commit, base)
	if err != nil || !ahead || m.landed.Commit == base {
		return t, err
	}
	t.Base, t.From = m.landed.Commit, m.landed.Branch
	return t, m.repo.Reset(ctx, t.Branch, t.Base)
}

// lineFor is what the task must include: a freshly fetched target tip when it
// lands on a target (never the project's record of what landed, which may
// name another task's branch), or the last new-branch landing otherwise.
func (m gitMedium) lineFor(ctx context.Context, t core.Task) (*line, error) {
	land := m.playbook.Land
	if land.Way() == core.LandBranch {
		if m.landed == nil || m.landed.TaskID == t.ID {
			return nil, nil
		}
		return &line{Commit: m.landed.Commit, Name: m.landed.Branch, What: fmt.Sprintf("%q landed on branch %s", m.landed.Objective, m.landed.Branch)}, nil
	}
	if land.Way() == core.LandPullRequest {
		// Commits someone else pushed to the pull request's branch come first:
		// they must be taken in before anything is pushed over them.
		if prop := t.Proposal; prop != nil && prop.Pushed != "" {
			head, err := m.repo.FetchFrom(ctx, m.url(), prop.Branch, github.CredentialConfig())
			if err == nil && head != prop.Pushed {
				if in, err := m.repo.Contains(ctx, tipOf(t), head); err == nil && !in {
					return &line{Commit: head, Name: prop.Branch, What: "someone else pushed to the pull request's branch " + prop.Branch, Foreign: true}, nil
				}
			}
		}
		tip, err := m.repo.FetchFrom(ctx, m.url(), land.Target, github.CredentialConfig())
		if err != nil {
			return nil, err
		}
		return &line{Commit: tip, Name: land.Target, What: fmt.Sprintf("%s on GitHub moved on since this task started (it is now at %s)", land.Target, short(tip))}, nil
	}
	tip, err := m.repo.Fetch(ctx, land.Target)
	if err != nil {
		return nil, err
	}
	what := fmt.Sprintf("%s moved on since this task started (it is now at %s)", land.Target, tip[:7])
	if m.landed != nil && m.landed.TaskID != t.ID && m.landed.Commit == tip {
		what = fmt.Sprintf("%q landed on %s", m.landed.Objective, land.Target)
	}
	return &line{Commit: tip, Name: land.Target, What: what}, nil
}

func tipOf(t core.Task) string {
	if n := len(t.Revisions); n > 0 {
		return t.Revisions[n-1].Ref
	}
	return t.Base
}

func (m gitMedium) behind(ctx context.Context, t core.Task) (*line, error) {
	tip := tipOf(t)
	if tip == "" {
		return nil, nil
	}
	l, err := m.lineFor(ctx, t)
	if err != nil || l == nil {
		return nil, err
	}
	contains, err := m.repo.Contains(ctx, tip, l.Commit)
	if err != nil || contains {
		return nil, err
	}
	return l, nil
}

func (m gitMedium) catchUp(ctx context.Context, t core.Task, l line) (core.Task, string, []string, error) {
	tip := tipOf(t)
	moved := t
	// The task's own change is now measured from what it took in, unless
	// that was someone else's push to its pull request.
	if !l.Foreign {
		moved.Base, moved.From = l.Commit, l.Name
	}
	clean, err := m.repo.MergeClean(ctx, tip, l.Commit, fmt.Sprintf("catch up with %s: %s", l.Name, clip(t.Objective, 60)))
	if err != nil {
		return t, "", nil, err
	}
	if clean != "" {
		return moved, clean, nil, m.repo.Reset(ctx, t.Branch, clean)
	}
	if err = m.repo.Reset(ctx, t.Branch, tip); err != nil {
		return t, "", nil, err
	}
	conflicts, err := m.repo.Merge(ctx, l.Commit)
	return moved, "", conflicts, err
}

func (m gitMedium) alreadyLanded(ctx context.Context, t core.Task, r core.Revision) (bool, error) {
	if m.playbook.Land.Way() != core.LandPush || r.Ref == "" {
		return false, nil
	}
	tip, err := m.repo.Fetch(ctx, m.playbook.Land.Target)
	if err != nil {
		return false, err
	}
	return m.repo.Contains(ctx, tip, r.Ref)
}

func (m gitMedium) files(ctx context.Context, t core.Task, ref string) ([]string, error) {
	return m.repo.ChangedFiles(ctx, t.Base, ref)
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
	if land := m.playbook.Land; land.Way() == core.LandPush {
		return land.Target, m.repo.PushFastForward(ctx, t.Branch, r.Ref, land.Target)
	}
	return m.repo.Deliver(ctx, t.Branch, r.Ref, m.branchName(t))
}

func (m gitMedium) branchName(t core.Task) string {
	return m.playbook.BranchPrefix + slugify(t.Objective)
}

func (m gitMedium) deliveryNote(t core.Task) string {
	if land := m.playbook.Land; land.Way() == core.LandPullRequest {
		method := land.Method
		if method == "" {
			method = "squash"
		}
		note := fmt.Sprintf("Approving pushes it to %s as the branch %s and opens a pull request into %s. From then on the team answers reviews and CI on it, and it merges by %s once GitHub says it is approved and green; you are asked again only if an update touches what runs or instructs on your side.", land.GitHub, m.branchName(t), land.Target, method)
		if land.Means != "" {
			note += " For this project, landing means: " + land.Means
		}
		return note
	}
	if land := m.playbook.Land; land.Way() == core.LandPush {
		note := "Approving lands it on " + land.Target + " in " + filepath.Base(m.playbook.Repo) + " by fast-forward: " + land.Target + " only moves forward, nothing already on it is replaced, and nothing is pushed anywhere else."
		if land.Means != "" {
			note += " For this project, landing means: " + land.Means
		}
		return note
	}
	note := "Approving creates the branch " + m.branchName(t) + " in " + filepath.Base(m.playbook.Repo) + ", from " + startedFrom(t) + "."
	if m.landed != nil && m.landed.TaskID != t.ID && t.Base == m.landed.Commit {
		note += " It builds on " + m.landed.Objective + ", which landed first, so it includes that change too."
	}
	return note + " Nothing is pushed, and your checkout is not touched."
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
