package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shhac/crew-assistant/internal/media"
)

// Checkout is one check's own copy of a recorded revision: never the
// implementer's clone. Its files are read-only, and what the check writes
// goes to Scratch.
type Checkout struct {
	// Dir is the revision, checked out read-only.
	Dir string
	// Scratch is where the check may write: its build caches and temporary
	// files, and, when asked for, Tree.
	Scratch string
	// Tree is a writable copy of Dir inside Scratch, for a check that has to
	// write into the tree it runs in; empty unless asked for.
	Tree   string
	Env    []string
	commit string
	root   string
	skip   []string
}

// checksDir holds the project's checkouts while checks run.
func (r Repo) checksDir() string { return filepath.Join(r.root, "checks") }

// Checkout checks commit out from the project's clone for one check, in a
// folder of its own beside a scratch folder. With tree, the check also gets
// a writable copy of it in the scratch folder to run in. base, when set, is
// the commit the change was made on: the checkout holds it too, for a diff
// against it, and the check is told it and the files the change touches,
// so a check of a large repository can check only what changed.
func (r Repo) Checkout(ctx context.Context, commit, base string, tree bool) (Checkout, error) {
	if err := os.MkdirAll(r.checksDir(), 0700); err != nil {
		return Checkout{}, err
	}
	root, err := os.MkdirTemp(r.checksDir(), "check-")
	if err != nil {
		return Checkout{}, err
	}
	c := Checkout{Dir: filepath.Join(root, "checkout"), Scratch: filepath.Join(root, "scratch"), commit: commit, root: root}
	prepared, err := r.preparedPaths(ctx)
	if err != nil {
		c.Remove()
		return Checkout{}, err
	}
	for _, rel := range prepared {
		c.skip = append(c.skip, filepath.ToSlash(filepath.Clean(rel))+"/")
	}
	if base == commit {
		base = ""
	}
	if err = r.checkOut(ctx, c, base, tree); err != nil {
		c.Remove()
		return Checkout{}, err
	}
	if tree {
		c.Tree = filepath.Join(c.Scratch, "tree")
	}
	c.Env = append(envAt(filepath.Join(c.Scratch, cacheDir)), changeEnv(c.Dir)...)
	return c, nil
}

// The change a checkout holds is kept in its .git, where every copy of it
// made for a check takes it along: the base it was made on, and the files
// it changes from that base, one per line.
const (
	baseFile    = "crew-base"
	changedFile = "crew-changed-files"
)

// keepChange records, in the checkout at dir, the change from base.
func (r Repo) keepChange(ctx context.Context, dir, commit, base string) error {
	names, err := run(ctx, r.Workspace(), "diff", "--name-only", "--no-renames", base, commit)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", changedFile), []byte(names), 0600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ".git", baseFile), []byte(base), 0600)
}

// changeEnv tells a check run in the tree at dir the change it holds, if
// it was recorded: CREW_BASE is its base commit, and CREW_CHANGED_FILES
// names the list of what it changes.
func changeEnv(dir string) []string {
	base, err := os.ReadFile(filepath.Join(dir, ".git", baseFile))
	if err != nil {
		return nil
	}
	return []string{"CREW_BASE=" + string(base), "CREW_CHANGED_FILES=" + filepath.Join(dir, ".git", changedFile)}
}

func (r Repo) checkOut(ctx context.Context, c Checkout, base string, tree bool) error {
	if _, err := run(ctx, filepath.Dir(c.Dir), "init", "--quiet", "--template=", filepath.Base(c.Dir)); err != nil {
		return err
	}
	// Only the revision and its base: a check never needs the history, and a
	// large repository's would take minutes to copy.
	commits := []string{c.commit}
	if base != "" {
		commits = append(commits, base)
	}
	if _, err := run(ctx, c.Dir, append(append(fetchQuietly, "--depth=1", "--no-write-fetch-head", r.Workspace()), commits...)...); err != nil {
		return fmt.Errorf("the revision could not be fetched from the project's clone: %w", err)
	}
	if _, err := run(ctx, c.Dir, "checkout", "--quiet", "--force", "--no-recurse-submodules", "--detach", c.commit); err != nil {
		return err
	}
	if base != "" {
		if err := r.keepChange(ctx, c.Dir, c.commit, base); err != nil {
			return err
		}
	}
	if err := r.copyPrepared(ctx, c.Dir); err != nil {
		return err
	}
	if err := os.MkdirAll(c.Scratch, 0700); err != nil {
		return err
	}
	if tree {
		if err := copyTree(c.Dir, filepath.Join(c.Scratch, "tree")); err != nil {
			return fmt.Errorf("copying the revision for the check: %w", err)
		}
	}
	// The copied dependencies are not the revision's, and Verify ignores
	// them; leaving them as they are spares a walk over every file in them.
	prepared := c.dependencies()
	if err := os.WriteFile(filepath.Join(c.Dir, ".git", preparedFile), []byte(strings.Join(prepared, "\n")), 0600); err != nil {
		return err
	}
	return media.SetWritable(c.Dir, false, prepared...)
}

// preparedFile lists, in a checkout's .git, the copied dependency folders,
// for every copy made of it to leave alone too.
const preparedFile = "crew-prepared"

// dependencies are the copied dependency folders, relative to the checkout.
func (c Checkout) dependencies() []string {
	var rels []string
	for _, p := range c.skip {
		rels = append(rels, strings.TrimSuffix(p, "/"))
	}
	return rels
}

// preparedIn reads the copied dependency folders a checkout at dir recorded.
func preparedIn(dir string) []string {
	list, err := os.ReadFile(filepath.Join(dir, ".git", preparedFile))
	if err != nil || len(list) == 0 {
		return nil
	}
	return strings.Split(string(list), "\n")
}

// Verify says the checkout is still exactly the revision: at its commit,
// with nothing changed, added or left behind, ignored files included. Only
// the copied dependencies are not the revision's own.
func (c Checkout) Verify(ctx context.Context) error {
	head, err := run(ctx, c.Dir, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("%w: %w", media.ErrCheckChanged, err)
	}
	if strings.TrimSpace(head) != c.commit {
		return fmt.Errorf("%w: it is at %s", media.ErrCheckChanged, strings.TrimSpace(head))
	}
	out, err := run(ctx, c.Dir, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignored=matching")
	if err != nil {
		return fmt.Errorf("%w: %w", media.ErrCheckChanged, err)
	}
	var changed []string
	for _, entry := range strings.Split(out, "\x00") {
		if len(entry) < 4 || entry[2] != ' ' {
			continue
		}
		path := entry[3:]
		if !c.prepared(path) {
			changed = append(changed, path)
		}
	}
	if len(changed) > 0 {
		return fmt.Errorf("%w: %s", media.ErrCheckChanged, strings.Join(changed, ", "))
	}
	return nil
}

func (c Checkout) prepared(path string) bool {
	for _, p := range c.skip {
		if strings.HasPrefix(path+"/", p) {
			return true
		}
	}
	return false
}

// Remove deletes the checkout and its scratch folder.
//
// Only the revision's own files are locked, so unlocking them takes
// moments; the folder is then moved aside at once and deleted in the
// background, since a large repository's copied dependencies take many
// minutes to delete. What a stopped daemon left there goes with
// RemoveChecks.
func (c Checkout) Remove() {
	if c.root == "" {
		return
	}
	_ = media.SetWritable(c.Dir, true, c.dependencies()...)
	trash := filepath.Join(filepath.Dir(filepath.Dir(c.root)), trashDir, filepath.Base(c.root))
	if err := os.MkdirAll(filepath.Dir(trash), 0700); err == nil && os.Rename(c.root, trash) == nil {
		go func() { _ = media.RemoveReadOnly(trash) }()
		return
	}
	_ = media.RemoveReadOnly(c.root)
}

// trashDir holds checkouts being deleted, beside the folder of checks
// still running.
const trashDir = "checks-trash"

// RemoveChecks deletes every checkout a check left behind, such as one a
// daemon stopped mid-check never removed.
func (r Repo) RemoveChecks() error {
	return errors.Join(media.RemoveReadOnly(r.checksDir()), media.RemoveReadOnly(filepath.Join(r.root, trashDir)))
}

// CopyForCheck copies uncommitted and prepared files into a disposable writable
// tree. cache optionally names QA's cache outside its read-only checkout.
func (r Repo) CopyForCheck(from string, cache ...string) (Checkout, error) {
	if err := os.MkdirAll(r.checksDir(), 0700); err != nil {
		return Checkout{}, err
	}
	root, err := os.MkdirTemp(r.checksDir(), "run-")
	if err != nil {
		return Checkout{}, err
	}
	c := Checkout{Dir: filepath.Join(root, "tree"), root: root}
	if err = copyTree(from, c.Dir); err != nil {
		c.Remove()
		return Checkout{}, err
	}
	for _, rel := range preparedIn(c.Dir) {
		c.skip = append(c.skip, rel+"/")
	}
	if err = media.SetWritable(c.Dir, true, c.dependencies()...); err != nil {
		c.Remove()
		return Checkout{}, err
	}
	dest := filepath.Join(c.Dir, cacheDir)
	if len(cache) > 0 && cache[0] != "" && cache[0] != filepath.Join(from, cacheDir) {
		if _, err := os.Stat(cache[0]); err == nil {
			if err = media.RemoveReadOnly(dest); err == nil {
				err = copyTree(cache[0], dest)
			}
			if err != nil {
				c.Remove()
				return Checkout{}, err
			}
			if err = media.SetWritable(dest, true); err != nil {
				c.Remove()
				return Checkout{}, err
			}
		} else if !os.IsNotExist(err) {
			c.Remove()
			return Checkout{}, err
		}
	}
	c.Env = append(envAt(dest), changeEnv(c.Dir)...)
	return c, nil
}
