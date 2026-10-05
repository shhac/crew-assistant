package work

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/integrations/github"
	"github.com/shhac/crew-assistant/internal/media"
	"github.com/shhac/crew-assistant/internal/media/gitrepo"
	"github.com/shhac/crew-assistant/internal/text"
)

// gitMedium is code work on one of the project's repositories. repo is the
// project's clone: it fetches from the owner, keeps every recorded revision,
// and is where checks, previews and landing read them. Each task's
// implementer works in a clone of its own, made from it. How an approved
// change lands is its way.
type gitMedium struct {
	repo     gitrepo.Repo
	playbook core.Playbook
	landed   *core.Landing
	remote   func(repo string) string
	way      landWay
	github   github.Client
}

// projectLocks keep changes to each project's clone one at a time: fetching
// the owner's work, handing drafts to it, making checks' copies from it,
// landing from it and tidying it. Steps of several tasks run at once, and
// each task's own clone is its own.
var projectLocks sync.Map

// locked holds the project's clone until the returned func is called.
func (m gitMedium) locked() func() {
	mu, _ := projectLocks.LoadOrStore(m.repo.Workspace(), &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	return mu.(*sync.Mutex).Unlock
}

// url is where a pull request's branch is pushed and its base fetched from.
// fetchGitHub fetches branch from the project's GitHub repository with the
// owner's gh login, returning its tip.
func (m gitMedium) fetchGitHub(ctx context.Context, branch string) (string, error) {
	return m.repo.FetchFrom(ctx, m.url(), branch, github.CredentialConfig())
}

// pushGitHub pushes ref to the project's own branch on GitHub with the
// owner's gh login, leased on what was last pushed there.
func (m gitMedium) pushGitHub(ctx context.Context, ref, branch, lease string) error {
	return m.repo.PushOwned(ctx, m.url(), ref, branch, lease, github.CredentialConfig())
}

func (m gitMedium) url() string { return m.remote(m.playbook.Land.GitHub) }

func (m gitMedium) workspace(t core.Task) string { return m.repo.Task(t.ID).Workspace() }
func (m gitMedium) env(t core.Task) []string     { return m.repo.Task(t.ID).Env() }
func (m gitMedium) readable() []string           { return m.repo.Readable() }

// clone is the task's own clone, made if it has none: one removed, or never
// made, is rebuilt from the revisions the project's clone keeps.
func (m gitMedium) clone(ctx context.Context, t core.Task) (gitrepo.Repo, error) {
	tc := m.repo.Task(t.ID)
	return tc, tc.Ready(ctx)
}

func (m gitMedium) begin(ctx context.Context, t core.Task) (core.Task, error) {
	defer m.locked()()
	if t.Base != "" {
		return t, nil
	}
	t.Branch = "crew-task/" + t.ID
	base, from, err := m.way.start(ctx, m, t)
	t.Base, t.From = base, from
	return t, err
}

func (m gitMedium) behind(ctx context.Context, t core.Task) (*line, error) {
	return m.behindFor(ctx, t, false)
}

// behindFor is what the task must take in before it goes on. A pull request
// takes in its target's new commits only when they conflict with it, or
// when GitHub itself requires it (required): its checks run on the merge
// with the target anyway, and catching up with every commit on a busy main
// would rerun the team's checks for nothing.
func (m gitMedium) behindFor(ctx context.Context, t core.Task, required bool) (*line, error) {
	defer m.locked()()
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
	if _, byPR := m.way.(prWay); byPR && !l.Foreign && !required {
		clean, err := m.repo.MergesCleanly(ctx, tip, l.Commit)
		if err != nil || clean {
			return nil, err
		}
	}
	if !l.Foreign && t.Base != "" {
		joined, err := m.repo.Contains(ctx, l.Commit, t.Base)
		if err != nil {
			return nil, err
		}
		l.Diverged = !joined
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

// cleanMerge is made in the task's clone, as a draft is: the project's clone
// holds it only once a handoff publishes it.
func (m gitMedium) cleanMerge(ctx context.Context, t core.Task, l line) (core.Task, string, error) {
	tc, err := m.clone(ctx, t)
	if err != nil {
		return t, "", err
	}
	message := fmt.Sprintf("catch up with %s: %s", l.Name, text.Clip(t.Objective, 60))
	merge := func() (string, error) { return tc.MergeClean(ctx, tipOf(t), l.Commit, message) }
	if l.Diverged {
		merge = func() (string, error) { return tc.ReplayClean(ctx, t.Base, tipOf(t), l.Commit, message) }
	}
	commit, err := merge()
	if err != nil || commit == "" {
		return t, "", err
	}
	return onto(t, l), commit, nil
}

func (m gitMedium) resetTo(ctx context.Context, t core.Task, commit string) error {
	tc, err := m.clone(ctx, t)
	if err != nil {
		return err
	}
	return tc.Reset(ctx, t.Branch, commit)
}

func (m gitMedium) conflictMerge(ctx context.Context, t core.Task, l line) (core.Task, []string, error) {
	tc, err := m.clone(ctx, t)
	if err != nil {
		return t, nil, err
	}
	if err := tc.Reset(ctx, t.Branch, tipOf(t)); err != nil {
		return t, nil, err
	}
	if l.Diverged {
		conflicts, err := tc.Replay(ctx, t.Branch, t.Base, tipOf(t), l.Commit)
		return onto(t, l), conflicts, err
	}
	conflicts, err := tc.Merge(ctx, l.Commit)
	return onto(t, l), conflicts, err
}

func (m gitMedium) alreadyLanded(ctx context.Context, t core.Task, r core.Revision) (bool, error) {
	defer m.locked()()
	if r.Ref == "" {
		return false, nil
	}
	return m.way.alreadyLanded(ctx, m, t, r)
}

func (m gitMedium) files(ctx context.Context, t core.Task, merge string) ([]string, error) {
	tc, err := m.clone(ctx, t)
	if err != nil {
		return nil, err
	}
	return tc.ChangedFiles(ctx, t.Base, merge)
}

func (m gitMedium) reset(ctx context.Context, t core.Task) error {
	ref := tipOf(t)
	if ref == "" {
		return errors.New("the task has no starting point")
	}
	return m.resetTo(ctx, t, ref)
}

func (m gitMedium) snapshot(ctx context.Context, t core.Task, n int) (core.Revision, error) {
	commit, files, err := m.repo.Task(t.ID).Snapshot(ctx, t.Base, tipOf(t), fmt.Sprintf("draft %d: %s", n, text.Clip(t.Objective, 60)))
	return core.Revision{N: n, Files: files, Ref: commit}, err
}

func (m gitMedium) publish(ctx context.Context, t core.Task, ref, name string) error {
	defer m.locked()()
	return m.repo.Publish(ctx, m.repo.Task(t.ID), ref, name)
}

func (m gitMedium) published(ctx context.Context, t core.Task, h core.Handoff) (string, bool, error) {
	defer m.locked()()
	at, err := m.repo.RefAt(ctx, h.Name)
	if err != nil {
		return "", false, err
	}
	kept := m.repo.Holds(ctx, h.Revision.Ref) || m.repo.Task(t.ID).Holds(ctx, h.Revision.Ref)
	return at, kept, nil
}

// check is a checkout of the revision of its own, made from the project's
// clone and read-only. A reviewer reads it where it is. QA runs in a scratch
// folder that holds its caches and temporary files, and reads the checkout,
// or, where the team's check has to write into the tree it runs in, runs in
// a writable copy of it there. QA that runs the app gets that copy too, for
// its setup and start commands to build in, whichever the check runs in.
func (m gitMedium) check(ctx context.Context, t core.Task, r core.Revision, qa, app bool) (checkout, error) {
	defer m.locked()()
	inCopy := qa && m.playbook.CheckInCopy
	c, err := m.repo.Checkout(ctx, r.Ref, t.Base, inCopy || (qa && app))
	if err != nil {
		return checkout{}, err
	}
	out := checkout{ref: r.Ref, checkDir: c.Dir, workDir: c.Dir, env: c.Env, verify: c.Verify, remove: c.Remove}
	if !qa {
		return out, nil
	}
	out.workDir, out.write, out.read, out.tree = c.Scratch, true, []string{c.Dir}, c.Tree
	out.note = fmt.Sprintf("\n\nThe repository is checked out, read-only, at %s: that is the repository root to run the check from. Your working directory is a scratch folder for anything the check writes; build caches and temporary files already go there.\n", c.Dir)
	if inCopy {
		out.note = fmt.Sprintf("\n\nThe repository root to run the check from is %s: a writable copy of the revision, in your scratch folder, for a check that writes into the tree it runs in. The revision itself is at %s, read-only.\n", c.Tree, c.Dir)
	}
	out.hostedNote = fmt.Sprintf("\n\nThe revision is checked out, read-only, at %s. run_check runs the check in its own writable copy, regardless of the check-in-copy setting. Your working directory is a scratch folder.\n", c.Dir)
	if t.Base != "" && t.Base != r.Ref {
		const change = "The check's environment names the commit this change was made on as $CREW_BASE, held in the checkout for a diff against it, and $CREW_CHANGED_FILES is a file listing the paths the change touches, one per line, for a check that checks only what changed.\n"
		out.note += change
		out.hostedNote += change
	}
	return out, nil
}

func (m gitMedium) preview(ctx context.Context, t core.Task, r core.Revision) ([]media.File, error) {
	return m.repo.Preview(ctx, t.Base, r.Ref, 256<<10)
}

func (m gitMedium) deliver(ctx context.Context, t core.Task, r core.Revision) (string, error) {
	defer m.locked()()
	return m.way.deliver(ctx, m, t, r)
}

func (m gitMedium) deliveryNote(t core.Task) string {
	note := m.way.note(m, t)
	if means := m.playbook.Land.Means; means != "" {
		note += " Landing here means: " + means + "."
	}
	return note
}

// tidy removes the clones of tasks that have finished, and the refs the
// project's clone keeps for tasks that have settled. With strays, a ref that
// is neither a recorded revision nor named by a handoff under way, left by
// one that never finished, goes too; only when nothing else is running.
func (m gitMedium) tidy(ctx context.Context, tasks []core.Task, settled func(core.Task) bool, strays bool) error {
	defer m.locked()()
	byID := map[string]core.Task{}
	for _, t := range tasks {
		byID[t.ID] = t
	}
	clones, err := m.repo.TaskClones()
	if err != nil {
		return err
	}
	var errs []error
	for _, id := range clones {
		if t, ok := byID[id]; !ok || t.Finished() {
			errs = append(errs, m.repo.RemoveTask(id))
		}
	}
	refs, err := m.repo.TaskRefs(ctx)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for ref, commit := range refs {
		t, ok := byID[gitrepo.RefTask(ref)]
		if ok && !settled(t) && (!strays || keeps(t, ref, commit)) {
			continue
		}
		errs = append(errs, m.repo.DropRef(ctx, ref, commit))
	}
	return errors.Join(errs...)
}

// keeps reports whether a task still needs a ref: one of its revisions, or
// the one its handoff is writing.
func keeps(t core.Task, ref, commit string) bool {
	if t.Handoff != nil && t.Handoff.Name == ref {
		return true
	}
	return slices.ContainsFunc(t.Revisions, func(r core.Revision) bool { return r.Ref == commit })
}

func (m gitMedium) removeChecks() error {
	defer m.locked()()
	return m.repo.RemoveChecks()
}

func (m gitMedium) branchName(t core.Task) string {
	return m.playbook.BranchPrefix + slugify(t.Objective)
}
