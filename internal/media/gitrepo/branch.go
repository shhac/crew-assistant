package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// ErrConflictMarkers refuses snapshots with unresolved integrations.
var ErrConflictMarkers = errors.New("conflict markers")

// Begin starts a task: the clone catches up with the owner's branch from, or
// their current branch when from is empty, and a task branch is created from
// its tip. It returns the base commit and the branch it came from.
func (r Repo) Begin(ctx context.Context, branch, from string) (base, start string, err error) {
	if base, from, err = r.Start(ctx, from); err != nil {
		return "", "", err
	}
	return base, from, r.Reset(ctx, branch, base)
}

// Start is where a task starts: the tip of the owner's branch from, or of
// their current branch when from is empty, fetched into the clone. It
// returns the commit and the branch, and checks nothing out.
func (r Repo) Start(ctx context.Context, from string) (base, start string, err error) {
	if from == "" {
		if from, err = CurrentBranch(ctx, r.source); err != nil {
			return "", "", err
		}
	}
	base, err = r.Fetch(ctx, from)
	return base, from, err
}

// CurrentBranch names the branch a checkout is on.
func CurrentBranch(ctx context.Context, dir string) (string, error) {
	current, err := run(ctx, dir, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return "", errors.New("the repository is not on a branch to start from")
	}
	return strings.TrimSpace(current), nil
}

// BranchTip reads a branch's tip in a repository without changing anything.
func BranchTip(ctx context.Context, dir, branch string) (string, error) {
	if err := validBranch(ctx, dir, branch); err != nil {
		return "", err
	}
	tip, err := run(ctx, dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	if err != nil {
		return "", fmt.Errorf("there is no branch %s", branch)
	}
	return strings.TrimSpace(tip), nil
}

// Fetch brings the owner's branch into the clone and returns its tip. It
// fetches from the repository's path, never a configured remote whose
// settings could name a command to run.
func (r Repo) Fetch(ctx context.Context, branch string) (string, error) {
	if err := validBranch(ctx, r.source, branch); err != nil {
		return "", err
	}
	if _, err := run(ctx, r.Workspace(), append(fetchQuietly, r.source, "+refs/heads/"+branch+":refs/remotes/source/"+branch)...); err != nil {
		return "", err
	}
	tip, err := run(ctx, r.Workspace(), "rev-parse", "refs/remotes/source/"+branch)
	return strings.TrimSpace(tip), err
}

// FetchFrom brings a remote branch into the clone and returns its tip.
func (r Repo) FetchFrom(ctx context.Context, url, branch string, config []string) (string, error) {
	if err := validBranch(ctx, r.source, branch); err != nil {
		return "", err
	}
	ref := "refs/remotes/remote/" + branch
	args := append(append(append([]string(nil), config...), fetchQuietly...), url, "+refs/heads/"+branch+":"+ref)
	if _, err := run(ctx, r.Workspace(), args...); err != nil {
		return "", err
	}
	tip, err := run(ctx, r.Workspace(), "rev-parse", ref)
	return strings.TrimSpace(tip), err
}

// ChangedFiles lists what differs between two commits.
func (r Repo) ChangedFiles(ctx context.Context, from, to string) ([]string, error) {
	out, err := run(ctx, r.Workspace(), "diff", "--no-ext-diff", "--no-textconv", "--name-only", from+".."+to)
	return strings.Fields(out), err
}

// Snapshot records the working tree as a commit on the task branch and
// returns it with the files changed since base. Nothing new since previous is
// an error: the role changed nothing.
func (r Repo) Snapshot(ctx context.Context, base, previous, message string) (string, []string, error) {
	if _, err := run(ctx, r.Workspace(), "add", "-A"); err != nil {
		return "", nil, err
	}
	_, mergeErr := run(ctx, r.Workspace(), "rev-parse", "--quiet", "--verify", "MERGE_HEAD")
	merging := mergeErr == nil
	replayed, err := r.replayPath(ctx)
	if err != nil {
		return "", nil, err
	}
	_, statErr := os.Stat(replayed)
	replaying := statErr == nil
	if merging || replaying {
		// Completing a merge or a replay: refuse to record conflicts nobody
		// resolved.
		check, _ := run(ctx, r.Workspace(), "diff", "--cached", "--check")
		var left []string
		for _, line := range strings.Split(check, "\n") {
			if strings.Contains(line, "leftover conflict marker") {
				left = append(left, strings.SplitN(line, ":", 2)[0])
			}
		}
		if len(left) > 0 {
			return "", nil, fmt.Errorf("%w in %s", ErrConflictMarkers, strings.Join(slices.Compact(left), ", "))
		}
	}
	// A merge is recorded even when it changes no files: the task must then
	// contain what it merged, or it would try to catch up forever.
	if _, err := run(ctx, r.Workspace(), "diff", "--cached", "--quiet"); err != nil || merging || replaying {
		if _, err = r.commit(ctx, "commit", "--quiet", "--no-verify", "--allow-empty", "-m", message); err != nil {
			return "", nil, err
		}
	}
	if replaying {
		if err := os.Remove(replayed); err != nil {
			return "", nil, err
		}
	}
	head, err := run(ctx, r.Workspace(), "rev-parse", "HEAD")
	if err != nil {
		return "", nil, err
	}
	head = strings.TrimSpace(head)
	if head == previous {
		return "", nil, ErrNoChange
	}
	names, err := run(ctx, r.Workspace(), "diff", "--no-ext-diff", "--no-textconv", "--name-only", base+".."+head)
	if err != nil {
		return "", nil, err
	}
	return head, strings.Fields(names), nil
}

// Reset puts the clone on branch at commit, dropping anything a role left behind,
// ignored files included: an ignored source file would still be compiled, so a
// check could pass on code that never ships. Only the build caches and the
// copied dependencies are kept. A task's clone fetches commit from the
// project's clone first if it lacks it.
func (r Repo) Reset(ctx context.Context, branch, commit string) error {
	if err := r.bring(ctx, commit); err != nil {
		return err
	}
	// Checking out the branch, rather than a bare reset, moves the branch
	// named and never whichever one was left checked out.
	if _, err := run(ctx, r.Workspace(), "checkout", "--quiet", "--force", "--no-recurse-submodules", "-B", branch, commit); err != nil {
		return err
	}
	// A merge a failed round left in progress must not carry into the next.
	if _, err := run(ctx, r.Workspace(), "reset", "--quiet", "--hard"); err != nil {
		return err
	}
	if marker, err := r.replayPath(ctx); err == nil {
		os.Remove(marker)
	}
	args := []string{"clean", "-ffdxq", "-e", "/" + cacheDir + "/"}
	for _, rel := range r.prepare {
		args = append(args, "-e", "/"+filepath.ToSlash(filepath.Clean(rel))+"/")
	}
	_, err := run(ctx, r.Workspace(), args...)
	return err
}

// ErrNoChange means a round left the task exactly as it was.
var ErrNoChange = errors.New("the implementer changed nothing")
