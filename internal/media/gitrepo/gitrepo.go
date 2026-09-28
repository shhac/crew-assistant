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

// Repo is a clone of the owner's repository: the project's own clone, which
// fetches from the owner, keeps every recorded revision and lands them, or a
// task's clone, made from the project's, which its implementer works in.
type Repo struct {
	root    string
	source  string
	prepare []string
	sign    Signing
	// records is the project's clone a task's clone fetches from; empty for
	// the project's clone itself.
	records string
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
		if err = r.cloneFrom(ctx, source); err != nil {
			return Repo{}, err
		}
		return r, nil
	}
	// Task clones and checks fetch revisions from here by commit.
	return r, r.serveCommits(ctx)
}

// Cloned reports whether the project at projectDir has its clone yet.
func Cloned(projectDir string) bool {
	_, err := os.Stat(filepath.Join(Repo{root: projectDir}.Workspace(), ".git"))
	return err == nil
}

// cloneFrom makes the clone from another repository on this machine.
func (r Repo) cloneFrom(ctx context.Context, from string) error {
	if err := os.MkdirAll(r.root, 0700); err != nil {
		return err
	}
	// A clone a crash left half made is made again.
	if err := os.RemoveAll(r.Workspace()); err != nil {
		return err
	}
	// An empty template: no hooks or config from the operator's own git
	// templates reach the clone.
	if _, err := run(ctx, r.root, "clone", "--quiet", "--template=", "--no-hardlinks", "--no-tags", "--no-recurse-submodules", from, "clone"); err != nil {
		return fmt.Errorf("the repository could not be cloned: %w", err)
	}
	if err := r.configure(ctx); err != nil {
		return err
	}
	// Only now, before any role has worked here: later, a folder a role
	// replaced with a link could lead the copy outside the clone.
	return r.copyPrepared(r.Workspace())
}

// serveCommits lets clones made from this one fetch any commit it holds by
// name, recorded or not, such as what landed.
func (r Repo) serveCommits(ctx context.Context) error {
	const key = "uploadpack.allowAnySHA1InWant"
	if on, _ := run(ctx, r.Workspace(), "config", "--get", key); strings.TrimSpace(on) == "true" {
		return nil
	}
	_, err := run(ctx, r.Workspace(), "config", key, "true")
	return err
}

// Workspace is the clone's working tree: for a task's clone, where its
// implementer works.
func (r Repo) Workspace() string { return filepath.Join(r.root, "clone") }

// Task is the clone task taskID's implementer works in, made from this one,
// the project's, on first use. Nothing is on disk until Ready.
func (r Repo) Task(taskID string) Repo {
	return Repo{root: r.taskDir(taskID), source: r.source, prepare: r.prepare, sign: r.sign, records: r.Workspace()}
}

func (r Repo) taskDir(taskID string) string {
	return filepath.Join(r.root, "tasks", filepath.Base(taskID))
}

// Ready makes a task's clone if it has none: a local clone of the project's,
// configured as the project's is, with the prepared folders copied in. A
// task whose clone was removed gets a new one, and fetches its revisions
// from the project's clone as it needs them.
func (r Repo) Ready(ctx context.Context) error {
	if _, err := os.Stat(filepath.Join(r.Workspace(), ".git")); err == nil {
		return nil
	}
	if r.records == "" {
		return errors.New("only a task's clone is made on demand")
	}
	return r.cloneFrom(ctx, r.records)
}

// RemoveTask deletes task taskID's clone, once the task has finished. Its
// revisions stay in the project's clone.
func (r Repo) RemoveTask(taskID string) error {
	return os.RemoveAll(r.taskDir(taskID))
}

// TaskClones names the tasks that have a clone of their own.
func (r Repo) TaskClones() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(r.root, "tasks"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	return ids, err
}

// bring fetches, into a task's clone, the commits it lacks from the
// project's clone, which holds every recorded revision and everything
// fetched from the owner.
func (r Repo) bring(ctx context.Context, commits ...string) error {
	if r.records == "" {
		return nil
	}
	for _, c := range commits {
		if c == "" || r.Holds(ctx, c) {
			continue
		}
		if _, err := run(ctx, r.Workspace(), append(fetchQuietly, "--no-write-fetch-head", r.records, c)...); err != nil {
			return fmt.Errorf("fetching %s from the project's clone: %w", c, err)
		}
	}
	return nil
}

// Holds reports whether the clone has commit.
func (r Repo) Holds(ctx context.Context, commit string) bool {
	_, err := run(ctx, r.Workspace(), "cat-file", "-e", commit+"^{commit}")
	return err == nil
}

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
	return envAt(filepath.Join(r.Workspace(), cacheDir))
}

// envAt is that environment with its caches and temporary files in cache,
// made if the folder it is in exists.
func envAt(cache string) []string {
	if _, err := os.Stat(filepath.Dir(cache)); err == nil {
		for _, dir := range []string{"go-build", "tmp", "npm", "xdg"} {
			_ = os.MkdirAll(filepath.Join(cache, dir), 0700)
		}
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
		{"uploadpack.allowAnySHA1InWant", "true"},
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
// owner's checkout into a fresh clone or checkout at dir, so roles can build
// without the network.
func (r Repo) copyPrepared(dir string) error {
	for _, rel := range r.prepare {
		clean := filepath.Clean(rel)
		if filepath.IsAbs(clean) || clean == "." || strings.HasPrefix(clean, "..") {
			return fmt.Errorf("prepare path %q must be inside the repository", rel)
		}
		from, to := filepath.Join(r.source, clean), filepath.Join(dir, clean)
		if _, err := os.Stat(from); err != nil {
			continue
		}
		if _, err := os.Lstat(to); err == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(to), 0700); err != nil {
			return err
		}
		if err := copyTree(from, to); err != nil {
			return fmt.Errorf("copying %s into the clone: %w", clean, err)
		}
	}
	return nil
}

// copyTree copies a folder, with copy-on-write clones where the file system
// has them.
func copyTree(from, to string) error {
	args := []string{"-R", from, to}
	if runtime.GOOS == "darwin" {
		args = []string{"-Rc", from, to} // copy-on-write clones on APFS
	}
	cmd := exec.Command("cp", args...)
	procgroup.Detach(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return errors.New(strings.TrimSpace(string(out)))
	}
	return nil
}

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

// ChangedFiles lists what differs between two commits.
func (r Repo) ChangedFiles(ctx context.Context, from, to string) ([]string, error) {
	out, err := run(ctx, r.Workspace(), "diff", "--no-ext-diff", "--no-textconv", "--name-only", from+".."+to)
	return strings.Fields(out), err
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
