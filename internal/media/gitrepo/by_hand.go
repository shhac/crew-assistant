package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Adopt brings the commit ref names in the owner's repository into the
// project's clone for a task and returns it. It touches no working tree: the
// task's next step checks it out in the task's own clone. One ref per task
// keeps the latest such commit in the clone, whether or not it was taken.
func (r Repo) Adopt(ctx context.Context, taskID, ref string) (string, error) {
	commit, err := resolve(ctx, r.source, ref)
	if err != nil {
		return "", err
	}
	if _, err := run(ctx, r.Workspace(), append(fetchQuietly, "--no-write-fetch-head", r.source, "+"+commit+":refs/crew-assistant/adopted/"+taskID)...); err != nil {
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
	if tip, err := BranchTip(ctx, dir, branch); err == nil && !force {
		behind, err := isAncestor(ctx, dir, tip, commit)
		if err != nil {
			return err
		}
		if !behind {
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

// HasBlob reports whether a commit contains the exact bytes, without running
// filters or checking out any workspace content.
func (r Repo) HasBlob(ctx context.Context, commit string, data []byte) (bool, error) {
	return r.hasBlob(ctx, commit, data, nil)
}

// HasChangedBlob requires an exact blob at a path changed by this draft.
// NUL-delimited paths preserve whitespace in asset filenames.
func (r Repo) HasChangedBlob(ctx context.Context, from, commit string, data []byte) (bool, error) {
	out, err := run(ctx, r.Workspace(), "diff", "--no-ext-diff", "--no-textconv", "--name-only", "-z", from+".."+commit)
	if err != nil {
		return false, err
	}
	paths := make(map[string]bool)
	for _, path := range strings.Split(out, "\x00") {
		if path != "" {
			paths[path] = true
		}
	}
	return r.hasBlob(ctx, commit, data, paths)
}

func (r Repo) hasBlob(ctx context.Context, commit string, data []byte, paths map[string]bool) (bool, error) {
	hash, err := runInput(ctx, r.Workspace(), gitEnvironment(), data, "hash-object", "--no-filters", "--stdin")
	if err != nil {
		return false, err
	}
	tree, err := run(ctx, r.Workspace(), "ls-tree", "-r", "-z", commit)
	if err != nil {
		return false, err
	}
	for _, entry := range strings.Split(tree, "\x00") {
		metadata, path, _ := strings.Cut(entry, "\t")
		fields := strings.Fields(metadata)
		if len(fields) == 3 && fields[1] == "blob" && fields[2] == strings.TrimSpace(hash) && (paths == nil || paths[path]) {
			return true, nil
		}
	}
	return false, nil
}
