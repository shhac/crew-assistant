package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Adopt brings the commit ref names in the owner's repository into the
// clone and returns it. It touches no working tree, since other tasks share
// the clone: the task's next step checks it out. A ref keeps the commit in
// the clone until the task's branch holds it.
func (r Repo) Adopt(ctx context.Context, ref string) (string, error) {
	commit, err := resolve(ctx, r.source, ref)
	if err != nil {
		return "", err
	}
	if _, err := run(ctx, r.Workspace(), append(fetchQuietly, "--no-write-fetch-head", r.source, "+"+commit+":refs/crew-assistant/adopted/"+commit)...); err != nil {
		return "", err
	}
	return commit, nil
}

// Subject is the first line of commit's message in the clone.
func (r Repo) Subject(ctx context.Context, commit string) (string, error) {
	out, err := run(ctx, r.Workspace(), "log", "-1", "--format=%s", commit)
	return strings.TrimSpace(out), err
}

// ErrOwnersWork is a branch the owner has commits on that a draft lacks.
var ErrOwnersWork = errors.New("the branch has commits the draft doesn't")

// CheckoutDraft puts commit, from the clone at clone, in the owner's
// repository at dir as branch: a fast-forward of an existing branch, or a
// new one. It never moves a branch past commits of the owner's the draft
// lacks unless forced, and git itself refuses one that is checked out.
func CheckoutDraft(ctx context.Context, dir, clone, commit, branch string, force bool) error {
	if err := validBranch(ctx, dir, branch); err != nil {
		return err
	}
	if _, err := run(ctx, dir, append(fetchQuietly, "--no-write-fetch-head", clone, commit)...); err != nil {
		return err
	}
	if tip, err := run(ctx, dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil && !force {
		if _, err := run(ctx, dir, "merge-base", "--is-ancestor", strings.TrimSpace(tip), commit); err != nil {
			return fmt.Errorf("%s: %w; use --force to replace it", branch, ErrOwnersWork)
		}
	}
	_, err := run(ctx, dir, "branch", "--force", "--no-track", branch, commit)
	return err
}

// AddWorktree checks branch out at path, a new worktree of the repository at
// dir.
func AddWorktree(ctx context.Context, dir, path, branch string) error {
	_, err := run(ctx, dir, "worktree", "add", "--quiet", path, branch)
	return err
}

// resolve is the commit ref names in the repository at dir.
func resolve(ctx context.Context, dir, ref string) (string, error) {
	out, err := run(ctx, dir, "rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("there is no commit %q in the repository", ref)
	}
	return strings.TrimSpace(out), nil
}
