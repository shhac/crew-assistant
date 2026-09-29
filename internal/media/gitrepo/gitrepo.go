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
	"strings"

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
