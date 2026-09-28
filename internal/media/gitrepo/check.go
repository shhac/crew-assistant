package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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
// a writable copy of it in the scratch folder to run in.
func (r Repo) Checkout(ctx context.Context, commit string, tree bool) (Checkout, error) {
	if err := os.MkdirAll(r.checksDir(), 0700); err != nil {
		return Checkout{}, err
	}
	root, err := os.MkdirTemp(r.checksDir(), "check-")
	if err != nil {
		return Checkout{}, err
	}
	c := Checkout{Dir: filepath.Join(root, "checkout"), Scratch: filepath.Join(root, "scratch"), commit: commit, root: root}
	for _, rel := range r.prepare {
		c.skip = append(c.skip, filepath.ToSlash(filepath.Clean(rel))+"/")
	}
	if err = r.checkOut(ctx, c, tree); err != nil {
		c.Remove()
		return Checkout{}, err
	}
	if tree {
		c.Tree = filepath.Join(c.Scratch, "tree")
	}
	c.Env = envAt(filepath.Join(c.Scratch, cacheDir))
	return c, nil
}

func (r Repo) checkOut(ctx context.Context, c Checkout, tree bool) error {
	if _, err := run(ctx, filepath.Dir(c.Dir), "init", "--quiet", "--template=", filepath.Base(c.Dir)); err != nil {
		return err
	}
	if _, err := run(ctx, c.Dir, append(fetchQuietly, "--no-write-fetch-head", r.Workspace(), c.commit)...); err != nil {
		return fmt.Errorf("the revision could not be fetched from the project's clone: %w", err)
	}
	if _, err := run(ctx, c.Dir, "checkout", "--quiet", "--force", "--no-recurse-submodules", "--detach", c.commit); err != nil {
		return err
	}
	if err := r.copyPrepared(c.Dir); err != nil {
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
	return setWritable(c.Dir, false)
}

// ErrCheckoutChanged is a check that changed the revision it was given.
var ErrCheckoutChanged = errors.New("the check changed the revision it was checking")

// Verify says the checkout is still exactly the revision: at its commit,
// with nothing changed, added or left behind, ignored files included. Only
// the copied dependencies are not the revision's own.
func (c Checkout) Verify(ctx context.Context) error {
	head, err := run(ctx, c.Dir, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("%w: %w", ErrCheckoutChanged, err)
	}
	if strings.TrimSpace(head) != c.commit {
		return fmt.Errorf("%w: it is at %s", ErrCheckoutChanged, strings.TrimSpace(head))
	}
	out, err := run(ctx, c.Dir, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignored=matching")
	if err != nil {
		return fmt.Errorf("%w: %w", ErrCheckoutChanged, err)
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
		return fmt.Errorf("%w: %s", ErrCheckoutChanged, strings.Join(changed, ", "))
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
func (c Checkout) Remove() {
	if c.root != "" {
		removeAll(c.root)
	}
}

// RemoveChecks deletes every checkout a check left behind, such as one a
// daemon stopped mid-check never removed.
func (r Repo) RemoveChecks() error {
	return removeAll(r.checksDir())
}

func removeAll(dir string) error {
	_ = setWritable(dir, true)
	return os.RemoveAll(dir)
}

// setWritable takes write permission from, or gives it back to, every file
// and folder under dir. Links are left alone: changing one would change
// what it points at.
func setWritable(dir string, writable bool) error {
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := info.Mode().Perm() &^ 0o222
		if writable {
			mode |= 0o200
		}
		return os.Chmod(path, mode)
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
