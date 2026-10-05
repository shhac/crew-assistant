package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// errNotRecorded is a commit to land that the project's clone does not hold:
// only a recorded revision lands.
var errNotRecorded = errors.New("the approved revision is not in the project's clone")

// ErrDeliveryChanged means the recorded branch no longer proves the outcome.
var ErrDeliveryChanged = errors.New("the recorded delivery branch has changed")

// ErrBranchTaken confirms that delivery did not create the chosen branch.
var ErrBranchTaken = errors.New("the delivery branch was taken")

// ErrNotDelivered confirms failure before the destination could be changed.
var ErrNotDelivered = errors.New("the revision was not delivered")

// runDelivery pins only local branch operations to the source's git directory.
// Workspace commands, signing and checks retain ordinary repository discovery.
func (r Repo) runDelivery(ctx context.Context, args ...string) (string, error) {
	return run(ctx, r.source, append([]string{"--git-dir=" + r.sourceGitDir}, args...)...)
}

// readBranch distinguishes a confirmed missing ref from an unreadable or
// corrupt one. A ref whose object cannot be read is not delivery evidence.
func (r Repo) readBranch(ctx context.Context, branch string) (string, bool, error) {
	ref := "refs/heads/" + branch
	out, err := r.runDelivery(ctx, "rev-parse", "--verify", "--quiet", ref)
	if err != nil {
		var failure *gitError
		if errors.As(err, &failure) && failure.code == 1 && failure.detail == "" {
			return "", false, nil
		}
		return "", false, fmt.Errorf("could not read delivery branch %s: %w", branch, err)
	}
	commit := strings.TrimSpace(out)
	if _, err := r.runDelivery(ctx, "cat-file", "-e", commit+"^{commit}"); err != nil {
		return "", false, fmt.Errorf("could not read delivery branch %s: %w", branch, err)
	}
	return commit, true, nil
}

func numberedBranch(name string, attempt int) string {
	if attempt == 1 {
		return name
	}
	return fmt.Sprintf("%s-%d", name, attempt)
}

// Destination chooses the exact branch before recording an outward intent.
func (r Repo) Destination(ctx context.Context, commit, name string) (string, error) {
	if !r.Holds(ctx, commit) {
		return "", errNotRecorded
	}
	current, err := r.runDelivery(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		var failure *gitError
		if !errors.As(err, &failure) || failure.code != 1 || failure.detail != "" {
			return "", err
		}
		// Quiet symbolic-ref also reports malformed HEAD as non-symbolic.
		// Only a readable detached commit permits branch selection.
		if _, err := r.runDelivery(ctx, "rev-parse", "--verify", "HEAD^{commit}"); err != nil {
			return "", err
		}
	}
	for attempt := 1; attempt <= 100; attempt++ {
		candidate := numberedBranch(name, attempt)
		if err := validBranch(ctx, r.source, candidate); err != nil {
			return "", err
		}
		existing, exists, err := r.readBranch(ctx, candidate)
		if err != nil {
			return "", err
		}
		if exists {
			if existing == commit {
				return candidate, nil
			}
			continue
		}
		if candidate != strings.TrimSpace(current) {
			return candidate, nil
		}
	}
	return "", errors.New("no free branch name")
}

// DeliverTo creates only the recorded destination, never moving an existing
// branch. A failed read-back leaves the outcome unknown to the caller.
func (r Repo) DeliverTo(ctx context.Context, commit, branch string) (string, error) {
	if !r.Holds(ctx, commit) {
		return "", fmt.Errorf("%w: %w", ErrNotDelivered, errNotRecorded)
	}
	if err := validBranch(ctx, r.source, branch); err != nil {
		return "", fmt.Errorf("%w: %w", ErrNotDelivered, err)
	}
	if _, err := r.runDelivery(ctx, append(fetchQuietly, "--no-write-fetch-head", r.Workspace(), "+"+commit+":refs/crew-assistant/incoming")...); err != nil {
		return "", fmt.Errorf("%w: the revision could not be fetched: %w", ErrNotDelivered, err)
	}
	_, err := r.runDelivery(ctx, "update-ref", "-m", "crew-assistant delivery", "refs/heads/"+branch, commit, strings.Repeat("0", len(commit)))
	_, _ = r.runDelivery(ctx, "update-ref", "-d", "refs/crew-assistant/incoming")
	if err == nil {
		return branch, nil
	}
	existing, exists, readErr := r.readBranch(ctx, branch)
	if readErr != nil {
		return "", readErr
	}
	if existing == commit {
		return branch, nil
	}
	if !exists {
		return "", fmt.Errorf("%w: creating %s: %w", ErrNotDelivered, branch, err)
	}
	return "", fmt.Errorf("%w: %s: %w", ErrBranchTaken, branch, err)
}

// Deliver composes selection and creation for callers without a durable intent.
func (r Repo) Deliver(ctx context.Context, commit, name string) (string, error) {
	branch, err := r.Destination(ctx, commit, name)
	if err != nil {
		return "", err
	}
	return r.DeliverTo(ctx, commit, branch)
}

// DeliveredTo reconciles the recorded destination. An unexpected tip cannot
// prove that delivery never happened, so it retains the outward intent.
func (r Repo) DeliveredTo(ctx context.Context, commit, branch string) (string, bool, error) {
	existing, exists, err := r.readBranch(ctx, branch)
	if err != nil || !exists {
		return "", false, err
	}
	if existing != commit {
		return "", false, fmt.Errorf("%w: delivery branch %s is at %s, expected %s", ErrDeliveryChanged, branch, existing, commit)
	}
	return branch, true, nil
}

// Delivered reconciles older intents without a destination. Search every
// supported name: the owner may have removed an earlier numbered branch.
func (r Repo) Delivered(ctx context.Context, commit, name string) (string, bool, error) {
	return r.delivered(ctx, commit, name, true)
}

// DeliveredBeforeIntent keeps the original first-unused-name lookup for work
// that has no outward intent. Only interrupted legacy intents need to look
// beyond gaps left by deleted branches.
func (r Repo) DeliveredBeforeIntent(ctx context.Context, commit, name string) (string, bool, error) {
	return r.delivered(ctx, commit, name, false)
}

func (r Repo) delivered(ctx context.Context, commit, name string, interrupted bool) (string, bool, error) {
	for attempt := 1; attempt <= 100; attempt++ {
		candidate := numberedBranch(name, attempt)
		existing, exists, err := r.readBranch(ctx, candidate)
		if err != nil {
			return "", false, err
		}
		if exists && existing == commit {
			return candidate, true, nil
		}
		if !exists && !interrupted {
			return "", false, nil
		}
	}
	return "", false, nil
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
func (r Repo) PushFastForward(ctx context.Context, commit, target string) error {
	if !r.Holds(ctx, commit) {
		return errNotRecorded
	}
	if err := validBranch(ctx, r.source, target); err != nil {
		return err
	}
	return r.pushTo(ctx, commit, target)
}

// PushSquashed lands an approved commit as one new commit on target: its
// tree, on top of target's last fetched tip, with message. The task's drafts
// stay in the project's clone, so the owner's history reads one commit per
// change. The commit must already hold everything on target, so that the one
// commit is exactly the change; one that is behind is refused as a moved
// target, to catch up first. It returns the commit landed, which is target's
// tip itself when the change is already there.
func (r Repo) PushSquashed(ctx context.Context, commit, target, message string) (string, error) {
	if !r.Holds(ctx, commit) {
		return "", errNotRecorded
	}
	if err := validBranch(ctx, r.source, target); err != nil {
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
