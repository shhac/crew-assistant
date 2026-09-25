package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Deliver puts commit on a new branch in the owner's repository without
// checking anything out there. A branch already at commit counts as
// delivered, so a retried delivery settles; one pointing elsewhere is left
// alone and a numbered name is used instead.
func (r Repo) Deliver(ctx context.Context, taskBranch, commit, name string) (string, error) {
	tip, err := run(ctx, r.Workspace(), "rev-parse", "refs/heads/"+taskBranch)
	if err != nil || strings.TrimSpace(tip) != commit {
		return "", errors.New("the task branch is not at the approved revision")
	}
	current, _ := run(ctx, r.source, "symbolic-ref", "--quiet", "--short", "HEAD")
	for attempt := 1; attempt <= 100; attempt++ {
		candidate := name
		if attempt > 1 {
			candidate = fmt.Sprintf("%s-%d", name, attempt)
		}
		if candidate == strings.TrimSpace(current) {
			continue
		}
		if err := validBranch(ctx, r.source, candidate); err != nil {
			return "", err
		}
		existing, err := run(ctx, r.source, "rev-parse", "--verify", "--quiet", "refs/heads/"+candidate)
		if err == nil {
			if strings.TrimSpace(existing) == commit {
				return candidate, nil
			}
			continue
		}
		// Bring the objects over without naming any branch, then create the
		// branch only if it still does not exist: a branch that appeared in
		// between is never moved.
		if _, err = run(ctx, r.source, append(fetchQuietly, "--no-write-fetch-head", r.Workspace(), "refs/heads/"+taskBranch+":refs/crew-assistant/incoming")...); err != nil {
			return "", fmt.Errorf("the revision could not be fetched: %w", err)
		}
		_, err = run(ctx, r.source, "update-ref", "-m", "crew-assistant delivery", "refs/heads/"+candidate, commit, strings.Repeat("0", len(commit)))
		_, _ = run(ctx, r.source, "update-ref", "-d", "refs/crew-assistant/incoming")
		if err != nil {
			continue
		}
		return candidate, nil
	}
	return "", errors.New("no free branch name")
}

// Why a push to a branch the project does not own was refused. None of them
// is ever answered by forcing: the branch moved, or the owner's checkout of it
// is theirs to deal with.
var (
	ErrTargetMoved   = errors.New("the branch has moved on since this change was checked")
	ErrCheckedOut    = errors.New("the branch is checked out in the owner's repository, which refuses updates to it")
	ErrDirtyCheckout = errors.New("the owner's checkout of the branch has uncommitted changes")
)

// ErrLeaseLost means someone else pushed to a branch the project owns since
// the project last did. Their commits are taken in, never overwritten.
var ErrLeaseLost = errors.New("someone else pushed to the branch since the project last did")

// PushOwned updates a branch the project owns on a remote, with a lease on the
// commit it last pushed there. An empty lease means the project never pushed
// it, so the branch must not exist yet.
func (r Repo) PushOwned(ctx context.Context, url, commit, branch, lease string, config []string) error {
	if err := validBranch(ctx, r.source, branch); err != nil {
		return err
	}
	args := append(append([]string(nil), config...), "push", "--porcelain", "--no-verify", "--force-with-lease=refs/heads/"+branch+":"+lease, url, commit+":refs/heads/"+branch)
	out, err := run(ctx, r.Workspace(), args...)
	if err == nil {
		return nil
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "!") && strings.Contains(strings.ToLower(line), "stale info") {
			return ErrLeaseLost
		}
	}
	return err
}

// receivePack runs the receiving side of a push into the owner's repository
// with their hooks and file-system monitor off. It also lets this push, and
// only this push, update a checked-out target in place when the checkout is
// clean: the project's landing policy is the owner's say-so, so their
// repository's own config is left alone and every other push into it keeps
// git's default refusal.
var receivePack = "git " + strings.Join(append(append([]string(nil), safety...), "-c", "receive.autogc=false", "-c", "receive.denyCurrentBranch=updateInstead"), " ") + " receive-pack"

// PushFastForward lands commit on target in the owner's repository by a plain
// push: never forced, so it only succeeds when target has not moved past what
// commit was built on. A checked-out target is updated in place only when the
// checkout has no uncommitted changes to tracked files.
func (r Repo) PushFastForward(ctx context.Context, taskBranch, commit, target string) error {
	tip, err := run(ctx, r.Workspace(), "rev-parse", "refs/heads/"+taskBranch)
	if err != nil || strings.TrimSpace(tip) != commit {
		return errors.New("the task branch is not at the approved revision")
	}
	if err = validBranch(ctx, r.source, target); err != nil {
		return err
	}
	return r.pushTo(ctx, commit, target)
}

// PushSquashed lands what a task branch holds as one new commit on target:
// the approved commit's tree, on top of target's last fetched tip, with
// message. The task's drafts stay on its own branch, so the owner's history
// reads one commit per change. The branch must already hold everything on
// target, so that the one commit is exactly the change; one that is behind
// is refused as a moved target, to catch up first. It returns the commit
// landed, which is target's tip itself when the change is already there.
func (r Repo) PushSquashed(ctx context.Context, taskBranch, commit, target, message string) (string, error) {
	tip, err := run(ctx, r.Workspace(), "rev-parse", "refs/heads/"+taskBranch)
	if err != nil || strings.TrimSpace(tip) != commit {
		return "", errors.New("the task branch is not at the approved revision")
	}
	if err = validBranch(ctx, r.source, target); err != nil {
		return "", err
	}
	onto, err := run(ctx, r.Workspace(), "rev-parse", "--verify", "refs/remotes/source/"+target)
	if err != nil {
		return "", fmt.Errorf("%s has not been fetched: %w", target, err)
	}
	onto = strings.TrimSpace(onto)
	caughtUp, err := r.Contains(ctx, commit, onto)
	if err != nil {
		return "", err
	}
	if !caughtUp {
		return "", ErrTargetMoved
	}
	trees, err := run(ctx, r.Workspace(), "rev-parse", commit+"^{tree}", onto+"^{tree}")
	if err != nil {
		return "", err
	}
	tree := strings.Fields(trees)
	if len(tree) == 2 && tree[0] == tree[1] {
		return onto, nil
	}
	squash, err := r.commit(ctx, "commit-tree", tree[0], "-p", onto, "-m", message)
	if err != nil {
		return "", err
	}
	squash = strings.TrimSpace(squash)
	return squash, r.pushTo(ctx, squash, target)
}

// DropBranch deletes a task branch from the clone once its change has
// landed, only while it is still at commit: a branch that moved since holds
// work that has not landed, and is kept. A clone left on the branch is
// detached at commit first. The owner's repository is never touched.
func (r Repo) DropBranch(ctx context.Context, branch, commit string) error {
	if err := validBranch(ctx, r.Workspace(), branch); err != nil {
		return err
	}
	if tip, err := run(ctx, r.Workspace(), "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err != nil || strings.TrimSpace(tip) != commit {
		return fmt.Errorf("%s is no longer at what landed; it is kept", branch)
	}
	if current, err := CurrentBranch(ctx, r.Workspace()); err == nil && current == branch {
		if _, err = run(ctx, r.Workspace(), "checkout", "--quiet", "--force", "--detach", commit); err != nil {
			return err
		}
	}
	_, err := run(ctx, r.Workspace(), "update-ref", "-m", "crew-assistant: landed", "-d", "refs/heads/"+branch, commit)
	return err
}

// Mentions reports whether any commit in ref's history has marker in its
// message, such as the trailer a squashed landing leaves.
func (r Repo) Mentions(ctx context.Context, ref, marker string) (bool, error) {
	out, err := run(ctx, r.Workspace(), "log", "--fixed-strings", "--grep="+marker, "--format=%H", "-n", "1", ref)
	return strings.TrimSpace(out) != "", err
}

// pushTo pushes commit to target in the owner's repository, never forced.
func (r Repo) pushTo(ctx context.Context, commit, target string) error {
	out, err := run(ctx, r.Workspace(), "push", "--porcelain", "--no-verify", "--receive-pack="+receivePack, r.source, commit+":refs/heads/"+target)
	if err == nil {
		return nil
	}
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "!") {
			continue
		}
		switch reason := strings.ToLower(line); {
		case strings.Contains(reason, "non-fast-forward"), strings.Contains(reason, "fetch first"), strings.Contains(reason, "stale info"):
			return ErrTargetMoved
		case strings.Contains(reason, "currently checked out"):
			return ErrCheckedOut
		case strings.Contains(reason, "working directory"), strings.Contains(reason, "working tree"):
			return fmt.Errorf("%w: %s", ErrDirtyCheckout, strings.TrimSpace(line[strings.LastIndex(line, "\t")+1:]))
		}
	}
	return err
}
