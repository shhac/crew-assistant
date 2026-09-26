// Package gitrepo is the medium for code: a private clone of the owner's
// repository that team roles work in, revisions as commits on a task branch,
// and delivery as a local branch in the owner's repository.
//
// Roles run sandboxed and cannot change the clone's .git, but the daemon runs
// git here outside any sandbox. Every git command therefore disables hooks,
// fsmonitor and system configuration, so nothing a role left in the working
// tree runs as the daemon.
package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/shhac/crew-assistant/internal/procgroup"
)

const (
	author    = "crew-assistant"
	authorKey = "crew@localhost"
	// cacheDir holds build caches and temporary files inside the clone, where
	// sandboxed roles may write. Git ignores it through .git/info/exclude.
	cacheDir = ".crew"
)

// Repo is one project's clone of the owner's repository.
type Repo struct {
	root    string
	source  string
	prepare []string
	sign    Signing
}

// Open prepares the clone under the project's private directory. The owner's
// repository is read from, never written to, until an approved delivery.
func Open(ctx context.Context, projectDir, source string, prepare []string, sign Signing) (Repo, error) {
	if !filepath.IsAbs(projectDir) || !filepath.IsAbs(source) {
		return Repo{}, errors.New("project and repository paths must be absolute")
	}
	r := Repo{root: projectDir, source: source, prepare: prepare, sign: sign}
	if _, err := run(ctx, source, "rev-parse", "--git-dir"); err != nil {
		return Repo{}, fmt.Errorf("%s is not a git repository", source)
	}
	if _, err := os.Stat(filepath.Join(r.Workspace(), ".git")); errors.Is(err, os.ErrNotExist) {
		if err = os.MkdirAll(r.root, 0700); err != nil {
			return Repo{}, err
		}
		// An empty template: no hooks or config from the operator's own
		// git templates reach the clone.
		if _, err = run(ctx, r.root, "clone", "--quiet", "--template=", "--no-hardlinks", "--no-tags", "--no-recurse-submodules", source, "clone"); err != nil {
			return Repo{}, fmt.Errorf("the repository could not be cloned: %w", err)
		}
		if err = r.configure(ctx); err != nil {
			return Repo{}, err
		}
		// Only now, before any role has worked here: later, a folder a role
		// replaced with a link could lead the copy outside the clone.
		if err = r.copyPrepared(); err != nil {
			return Repo{}, err
		}
	}
	return r, nil
}

// Workspace is where team roles work.
func (r Repo) Workspace() string { return filepath.Join(r.root, "clone") }

// Readable is what roles may read outside the clone: the owner's Go module
// cache, so an offline build finds the modules the owner already has.
func (r Repo) Readable() []string {
	if dir := moduleCache(); dir != "" {
		return []string{dir}
	}
	return nil
}

// moduleCache is the owner's Go module cache, asked once of the Go toolchain
// from outside any repository so no project setting can move it.
var moduleCache = sync.OnceValue(func() string {
	cmd := exec.Command("go", "env", "GOMODCACHE")
	procgroup.Detach(cmd)
	cmd.Dir = os.TempDir()
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOFLAGS=")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	dir := strings.TrimSpace(string(out))
	if info, err := os.Stat(dir); err != nil || !info.IsDir() || !filepath.IsAbs(dir) {
		return ""
	}
	return dir
})

// Env is the environment roles need to build and test inside their sandbox:
// caches and temporary files in the clone, and no attempts at the network.
func (r Repo) Env() []string {
	cache := filepath.Join(r.Workspace(), cacheDir)
	for _, dir := range []string{"go-build", "tmp", "npm", "xdg"} {
		_ = os.MkdirAll(filepath.Join(cache, dir), 0700)
	}
	env := []string{
		"GOCACHE=" + filepath.Join(cache, "go-build"),
		"TMPDIR=" + filepath.Join(cache, "tmp"),
		"npm_config_cache=" + filepath.Join(cache, "npm"),
		"XDG_CACHE_HOME=" + filepath.Join(cache, "xdg"),
		"npm_config_update_notifier=false",
		"GOPROXY=off",
		"GOTOOLCHAIN=local",
		"CI=1",
	}
	if dir := moduleCache(); dir != "" {
		env = append(env, "GOMODCACHE="+dir)
	}
	return env
}

func (r Repo) configure(ctx context.Context) error {
	for _, kv := range [][2]string{
		{"core.hooksPath", "/dev/null"},
		{"core.fsmonitor", "false"},
		{"user.name", author},
		{"user.email", authorKey},
	} {
		if _, err := run(ctx, r.Workspace(), "config", kv[0], kv[1]); err != nil {
			return err
		}
	}
	exclude := filepath.Join(r.Workspace(), ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(exclude), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(exclude, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString("\n/" + cacheDir + "/\n")
	return err
}

// copyPrepared copies ignored dependencies, such as node_modules, from the
// owner's checkout into a fresh clone, so roles can build without the network.
func (r Repo) copyPrepared() error {
	for _, rel := range r.prepare {
		clean := filepath.Clean(rel)
		if filepath.IsAbs(clean) || clean == "." || strings.HasPrefix(clean, "..") {
			return fmt.Errorf("prepare path %q must be inside the repository", rel)
		}
		from, to := filepath.Join(r.source, clean), filepath.Join(r.Workspace(), clean)
		if _, err := os.Stat(from); err != nil {
			continue
		}
		if _, err := os.Lstat(to); err == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(to), 0700); err != nil {
			return err
		}
		args := []string{"-R", from, to}
		if runtime.GOOS == "darwin" {
			args = []string{"-Rc", from, to} // copy-on-write clones on APFS
		}
		cmd := exec.Command("cp", args...)
		procgroup.Detach(cmd)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("copying %s into the clone: %s", clean, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// Begin starts a task: the clone catches up with the owner's branch from, or
// their current branch when from is empty, and a task branch is created from
// its tip. It returns the base commit and the branch it came from.
func (r Repo) Begin(ctx context.Context, branch, from string) (base, start string, err error) {
	if from == "" {
		if from, err = CurrentBranch(ctx, r.source); err != nil {
			return "", "", err
		}
	}
	if base, err = r.Fetch(ctx, from); err != nil {
		return "", "", err
	}
	return base, from, r.Reset(ctx, branch, base)
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

// ChangedFiles lists what differs between two commits.
func (r Repo) ChangedFiles(ctx context.Context, from, to string) ([]string, error) {
	out, err := run(ctx, r.Workspace(), "diff", "--no-ext-diff", "--no-textconv", "--name-only", from+".."+to)
	return strings.Fields(out), err
}

// Merge brings commit into the checked-out task branch without committing, so
// the implementer's next revision records the merged result. It returns the
// files left with conflicts for the implementer to resolve.
func (r Repo) Merge(ctx context.Context, commit string) ([]string, error) {
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
			return "", nil, fmt.Errorf("conflict markers are still in %s", strings.Join(slices.Compact(left), ", "))
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
// copied dependencies are kept.
func (r Repo) Reset(ctx context.Context, branch, commit string) error {
	// Several tasks share the clone, so each step checks out its own task's
	// branch; a bare reset would move whichever branch was left checked out.
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
