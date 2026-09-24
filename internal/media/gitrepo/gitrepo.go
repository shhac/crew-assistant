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
	"bytes"
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

	"github.com/shhac/crew-assistant/internal/media"
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
}

// Open prepares the clone under the project's private directory. The owner's
// repository is read from, never written to, until an approved delivery.
func Open(ctx context.Context, projectDir, source string, prepare []string) (Repo, error) {
	if !filepath.IsAbs(projectDir) || !filepath.IsAbs(source) {
		return Repo{}, errors.New("project and repository paths must be absolute")
	}
	r := Repo{root: projectDir, source: source, prepare: prepare}
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
		{"commit.gpgsign", "false"},
		{"tag.gpgsign", "false"},
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
		if out, err := exec.Command("cp", args...).CombinedOutput(); err != nil {
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
	if _, err := run(ctx, dir, "check-ref-format", "--branch", branch); err != nil {
		return "", fmt.Errorf("%q is not a valid branch name", branch)
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
	if _, err := run(ctx, r.source, "check-ref-format", "--branch", branch); err != nil {
		return "", fmt.Errorf("%q is not a valid branch name", branch)
	}
	if _, err := run(ctx, r.Workspace(), "fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--no-auto-gc", r.source, "+refs/heads/"+branch+":refs/remotes/source/"+branch); err != nil {
		return "", err
	}
	tip, err := run(ctx, r.Workspace(), "rev-parse", "refs/remotes/source/"+branch)
	return strings.TrimSpace(tip), err
}

// Contains reports whether commit is already part of tip's history.
func (r Repo) Contains(ctx context.Context, tip, commit string) (bool, error) {
	_, err := run(ctx, r.Workspace(), "merge-base", "--is-ancestor", commit, tip)
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
	out, err := run(ctx, r.Workspace(), "commit-tree", lines[0], "-p", tip, "-p", commit, "-m", message)
	return strings.TrimSpace(out), err
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
	if merging {
		// Completing a merge: refuse to record conflicts nobody resolved.
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
	if _, err := run(ctx, r.Workspace(), "diff", "--cached", "--quiet"); err != nil || merging {
		if _, err = run(ctx, r.Workspace(), "commit", "--quiet", "--no-verify", "-m", message); err != nil {
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
	args := []string{"clean", "-ffdxq", "-e", "/" + cacheDir + "/"}
	for _, rel := range r.prepare {
		args = append(args, "-e", "/"+filepath.ToSlash(filepath.Clean(rel))+"/")
	}
	_, err := run(ctx, r.Workspace(), args...)
	return err
}

// Preview shows the owner what a revision changes, cut at limit bytes.
func (r Repo) Preview(ctx context.Context, base, commit string, limit int) ([]media.File, error) {
	stat, err := run(ctx, r.Workspace(), "diff", "--no-ext-diff", "--no-textconv", "--no-color", "--stat", "--summary", base+".."+commit)
	if err != nil {
		return nil, err
	}
	patch, err := run(ctx, r.Workspace(), "diff", "--no-ext-diff", "--no-textconv", "--no-color", base+".."+commit)
	if err != nil {
		return nil, err
	}
	diff := media.File{Path: "changes.diff", Content: patch, Size: int64(len(patch))}
	if len(patch) > limit {
		diff.Content, diff.Truncated = patch[:limit], true
	}
	files := []media.File{{Path: "summary", Content: stat, Size: int64(len(stat))}, diff}
	if attention, err := r.Attention(ctx, base, commit); err == nil && len(attention) > 0 {
		note := "Look at these before running anything on this branch:\n- " + strings.Join(attention, "\n- ") + "\n"
		files = append([]media.File{{Path: "attention", Content: note, Size: int64(len(note))}}, files...)
	}
	return files, nil
}

// sensitive names files that run, or instruct agents, when someone next works
// in the repository: build and package scripts, hooks and editor settings,
// agent instructions, git attributes and submodules.
var sensitive = []string{"Makefile", "makefile", "GNUmakefile", "package.json", ".envrc", ".gitattributes", ".gitmodules", "AGENTS.md", "CLAUDE.md", "CLAUDE.local.md", "go.mod", "Dockerfile", ".npmrc"}
var sensitiveDirs = []string{".claude/", ".codex/", ".agents/", ".husky/", ".vscode/", ".github/", ".githooks/", ".devcontainer/"}

// Attention lists what a change touches that deserves a look before the owner
// runs anything on the delivered branch: sensitive files, and any change that
// adds a symlink or makes a file executable.
func (r Repo) Attention(ctx context.Context, base, commit string) ([]string, error) {
	out, err := run(ctx, r.Workspace(), "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--raw", base+".."+commit)
	if err != nil {
		return nil, err
	}
	var notes []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		newMode, path := fields[1], fields[len(fields)-1]
		name := filepath.Base(path)
		switch {
		case newMode == "120000":
			notes = append(notes, path+" is a symlink")
		case newMode == "100755" && fields[0] != ":100755":
			notes = append(notes, path+" is executable")
		}
		for _, s := range sensitive {
			if name == s {
				notes = append(notes, path+" changed")
			}
		}
		for _, d := range sensitiveDirs {
			if strings.HasPrefix(path, d) || strings.Contains(path, "/"+d) {
				notes = append(notes, path+" changed")
				break
			}
		}
	}
	return notes, nil
}

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
		if _, err := run(ctx, r.source, "check-ref-format", "--branch", candidate); err != nil {
			return "", fmt.Errorf("%q is not a valid branch name", candidate)
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
		if _, err = run(ctx, r.source, "fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--no-auto-gc", "--no-write-fetch-head", r.Workspace(), "refs/heads/"+taskBranch+":refs/crew-assistant/incoming"); err != nil {
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

// ErrNoChange means a round left the task exactly as it was.
var ErrNoChange = errors.New("the implementer changed nothing")

// ErrLeaseLost means someone else pushed to a branch the project owns since
// the project last did. Their commits are taken in, never overwritten.
var ErrLeaseLost = errors.New("someone else pushed to the branch since the project last did")

// PushOwned updates a branch the project owns on a remote, with a lease on the
// commit it last pushed there. An empty lease means the project never pushed
// it, so the branch must not exist yet.
func (r Repo) PushOwned(ctx context.Context, url, commit, branch, lease string, config []string) error {
	if _, err := run(ctx, r.source, "check-ref-format", "--branch", branch); err != nil {
		return fmt.Errorf("%q is not a valid branch name", branch)
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

// FetchFrom brings a remote branch into the clone and returns its tip.
func (r Repo) FetchFrom(ctx context.Context, url, branch string, config []string) (string, error) {
	if _, err := run(ctx, r.source, "check-ref-format", "--branch", branch); err != nil {
		return "", fmt.Errorf("%q is not a valid branch name", branch)
	}
	ref := "refs/remotes/remote/" + branch
	args := append(append([]string(nil), config...), "fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--no-auto-gc", url, "+refs/heads/"+branch+":"+ref)
	if _, err := run(ctx, r.Workspace(), args...); err != nil {
		return "", err
	}
	tip, err := run(ctx, r.Workspace(), "rev-parse", ref)
	return strings.TrimSpace(tip), err
}

// receivePack runs the receiving side of a push into the owner's repository
// with their hooks and file-system monitor off. Their repository's own rules,
// such as receive.denyCurrentBranch, still apply.
var receivePack = "git " + strings.Join(append(append([]string(nil), safety...), "-c", "receive.autogc=false"), " ") + " receive-pack"

// PushFastForward lands commit on target in the owner's repository by a plain
// push: never forced, so it only succeeds when target has not moved past what
// commit was built on. A checked-out target follows the owner's
// receive.denyCurrentBranch setting.
func (r Repo) PushFastForward(ctx context.Context, taskBranch, commit, target string) error {
	tip, err := run(ctx, r.Workspace(), "rev-parse", "refs/heads/"+taskBranch)
	if err != nil || strings.TrimSpace(tip) != commit {
		return errors.New("the task branch is not at the approved revision")
	}
	if _, err = run(ctx, r.source, "check-ref-format", "--branch", target); err != nil {
		return fmt.Errorf("%q is not a valid branch name", target)
	}
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

// run is the only way this package runs git. Hooks, fsmonitor and system
// configuration are off for every command, wherever it runs.
func run(ctx context.Context, dir string, args ...string) (string, error) {
	full := append(append([]string(nil), safety...), args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	cmd.Env = gitEnvironment()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if len(detail) > 300 {
			detail = detail[:300]
		}
		code := -1
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		}
		return stdout.String(), &gitError{command: args[0], detail: detail, code: code}
	}
	return stdout.String(), nil
}

type gitError struct {
	command, detail string
	code            int
}

func (e *gitError) Error() string { return "git " + e.command + ": " + e.detail }

// safety is prepended to every git command: nothing configured in a
// repository, the operator's global config or the system can make git run a
// program as the daemon.
var safety = []string{
	"-c", "core.hooksPath=/dev/null",
	"-c", "core.fsmonitor=false",
	"-c", "core.attributesFile=/dev/null",
	"-c", "core.excludesFile=/dev/null",
	"-c", "core.sshCommand=false",
	"-c", "gc.auto=0",
	"-c", "maintenance.auto=false",
	"-c", "submodule.recurse=false",
	"-c", "fetch.recurseSubmodules=false",
	"-c", "diff.external=",
}

// gitEnvironment keeps the process's ordinary environment but none of its GIT_
// settings, and ignores global and system git configuration.
func gitEnvironment() []string {
	env := []string{}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") {
			env = append(env, entry)
		}
	}
	return append(env,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_PAGER=cat",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_OPTIONAL_LOCKS=0",
		// Refusals are read from git's own words.
		"LC_ALL=C",
	)
}
