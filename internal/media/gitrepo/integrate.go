package gitrepo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Contains reports whether commit is already part of tip's history.
func (r Repo) Contains(ctx context.Context, tip, commit string) (bool, error) {
	return isAncestor(ctx, r.Workspace(), commit, tip)
}

// isAncestor says whether commit is part of tip's history in the repository
// at dir, telling "no" from git failing to say.
func isAncestor(ctx context.Context, dir, commit, tip string) (bool, error) {
	_, err := run(ctx, dir, "merge-base", "--is-ancestor", commit, tip)
	var status *gitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &status) && status.code == 1:
		return false, nil
	}
	return false, err
}

// MergeClean merges commit into tip without touching any working tree. It
// returns the new merge commit, or "" when the two conflict and someone has to
// resolve them.
func (r Repo) MergeClean(ctx context.Context, tip, commit, message string) (string, error) {
	if err := r.bring(ctx, tip, commit); err != nil {
		return "", err
	}
	tree, err := run(ctx, r.Workspace(), "merge-tree", "--write-tree", "--no-messages", tip, commit)
	var status *gitError
	if errors.As(err, &status) && status.code == 1 {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	lines := strings.Fields(tree)
	if len(lines) == 0 {
		return "", errors.New("git merge-tree wrote no tree")
	}
	out, err := r.commit(ctx, "commit-tree", lines[0], "-p", tip, "-p", commit, "-m", message)
	return strings.TrimSpace(out), err
}

// ReplayClean takes a task's own change, what tip holds beyond base, onto
// onto, as one commit, without the history in between. base is where the task
// last joined its target: when the target has since been rewritten, anything
// it dropped stays dropped, rather than coming back with a merge. It returns
// "" when the change does not apply cleanly.
func (r Repo) ReplayClean(ctx context.Context, base, tip, onto, message string) (string, error) {
	if err := r.bring(ctx, base, tip, onto); err != nil {
		return "", err
	}
	tree, err := run(ctx, r.Workspace(), "merge-tree", "--write-tree", "--no-messages", "--merge-base="+base, onto, tip)
	var status *gitError
	if errors.As(err, &status) && status.code == 1 {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	lines := strings.Fields(tree)
	if len(lines) == 0 {
		return "", errors.New("git merge-tree wrote no tree")
	}
	out, err := r.commit(ctx, "commit-tree", lines[0], "-p", onto, "-m", message)
	return strings.TrimSpace(out), err
}

// Replay puts the checked-out task branch at onto and applies the task's own
// change, base..tip, on top without committing, leaving the files that
// conflict for the implementer to resolve. The next snapshot records it.
func (r Repo) Replay(ctx context.Context, branch, base, tip, onto string) ([]string, error) {
	if err := r.bring(ctx, base, tip, onto); err != nil {
		return nil, err
	}
	tree, err := run(ctx, r.Workspace(), "rev-parse", tip+"^{tree}")
	if err != nil {
		return nil, err
	}
	// The change as one commit on base, only so it can be picked onto onto.
	change, err := run(ctx, r.Workspace(), "commit-tree", strings.TrimSpace(tree), "-p", base, "-m", "the task's change")
	if err != nil {
		return nil, err
	}
	if err = r.Reset(ctx, branch, onto); err != nil {
		return nil, err
	}
	marker, err := r.replayPath(ctx)
	if err != nil {
		return nil, err
	}
	if err = os.WriteFile(marker, nil, 0o600); err != nil {
		return nil, err
	}
	_, pickErr := run(ctx, r.Workspace(), "cherry-pick", "--no-commit", strings.TrimSpace(change))
	out, err := run(ctx, r.Workspace(), "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, err
	}
	conflicts := strings.Fields(out)
	if pickErr != nil && len(conflicts) == 0 {
		return nil, pickErr
	}
	return conflicts, nil
}

// replayMarker, in the git directory, says a replay's conflicts are in the
// working tree, so a snapshot refuses unresolved markers as it does for a
// merge. A cherry-pick without a commit leaves git no state of its own.
const replayMarker = "crew-replaying"

func (r Repo) replayPath(ctx context.Context) (string, error) {
	out, err := run(ctx, r.Workspace(), "rev-parse", "--git-path", replayMarker)
	if err != nil {
		return "", err
	}
	path := strings.TrimSpace(out)
	if !filepath.IsAbs(path) {
		path = filepath.Join(r.Workspace(), path)
	}
	return path, nil
}

// Merge brings commit into the checked-out task branch without committing, so
// the implementer's next revision records the merged result. It returns the
// files left with conflicts for the implementer to resolve.
func (r Repo) Merge(ctx context.Context, commit string) ([]string, error) {
	if err := r.bring(ctx, commit); err != nil {
		return nil, err
	}
	_, mergeErr := run(ctx, r.Workspace(), "merge", "--quiet", "--no-commit", "--no-ff", "--no-edit", commit)
	out, err := run(ctx, r.Workspace(), "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, err
	}
	conflicts := strings.Fields(out)
	if mergeErr != nil && len(conflicts) == 0 {
		return nil, mergeErr
	}
	return conflicts, nil
}
