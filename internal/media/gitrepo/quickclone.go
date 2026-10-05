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

// quickClone makes at dest what `git clone --no-checkout` of from would, with
// none of its cost on a large repository: a local clone still packs and
// re-indexes every object, minutes for a monorepo, where copying the object
// files takes a moment, and nothing at all where the file system copies on
// write. The copy shares nothing writable with from.
//
// The refs are read before the objects are copied, so the copy holds all
// they need unless from repacked meanwhile; a pack that went away fails the
// copy, and every ref is checked against the copy before it is written.
// Whatever fails, the caller clones the ordinary way instead.
func quickClone(ctx context.Context, from, dest string) error {
	objects, err := run(ctx, from, "rev-parse", "--path-format=absolute", "--git-path", "objects")
	if err != nil {
		return err
	}
	objects = strings.TrimSpace(objects)
	if _, err := os.Stat(filepath.Join(objects, "info", "alternates")); err == nil {
		return errors.New("the repository borrows objects from another")
	}
	branches, head, err := sourceRefs(ctx, from)
	if err != nil {
		return err
	}
	before, err := packNames(objects)
	if err != nil {
		return err
	}
	if _, err := run(ctx, filepath.Dir(dest), "init", "--quiet", "--template=", filepath.Base(dest)); err != nil {
		return err
	}
	copied := filepath.Join(dest, ".git", "objects")
	if err := os.RemoveAll(copied); err != nil {
		return err
	}
	if err := copyTree(objects, copied); err != nil {
		return err
	}
	if after, err := packNames(objects); err != nil || !slices.Equal(before, after) {
		return errors.New("the repository repacked while it was copied")
	}
	if err := keepOnlyObjects(copied); err != nil {
		return err
	}
	return writeRefs(ctx, from, dest, branches, head)
}

// sourceRefs are from's branches, as name and commit, and the branch its
// HEAD is on, or its commit when detached.
func sourceRefs(ctx context.Context, from string) (map[string]string, string, error) {
	out, err := run(ctx, from, "for-each-ref", "--format=%(objectname) %(refname:lstrip=2)", "refs/heads")
	if err != nil {
		return nil, "", err
	}
	branches := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if commit, name, ok := strings.Cut(line, " "); ok {
			branches[name] = commit
		}
	}
	if branch, err := run(ctx, from, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		return branches, "refs/heads/" + strings.TrimSpace(branch), nil
	}
	commit, err := run(ctx, from, "rev-parse", "--verify", "--quiet", "HEAD")
	return branches, strings.TrimSpace(commit), err
}

// packNames lists the packs in objects, to tell whether a repack ran during
// the copy.
func packNames(objects string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(objects, "pack"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".pack") {
			names = append(names, e.Name())
		}
	}
	return names, err
}

// keepOnlyObjects drops what was the source's own bookkeeping rather than
// its objects: a .keep would pin its pack here forever, a multi-pack index
// may name packs the copy missed, and temporary files belong to writes in
// progress there.
func keepOnlyObjects(objects string) error {
	pack := filepath.Join(objects, "pack")
	entries, err := os.ReadDir(pack)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".keep") || strings.HasPrefix(name, "multi-pack-index") || strings.HasPrefix(name, "tmp_") {
			if err := os.RemoveAll(filepath.Join(pack, name)); err != nil {
				return err
			}
		}
	}
	return os.RemoveAll(filepath.Join(objects, "info", "alternates"))
}

// writeRefs sets dest up as a clone of from: origin names from, its
// branches are origin's remote branches, and HEAD is on the branch from's
// HEAD is on, tracking it.
func writeRefs(ctx context.Context, from, dest string, branches map[string]string, head string) error {
	var updates strings.Builder
	for name, commit := range branches {
		fmt.Fprintf(&updates, "create refs/remotes/origin/%s %s\n", name, commit)
	}
	branch, onBranch := strings.CutPrefix(head, "refs/heads/")
	if onBranch {
		commit, ok := branches[branch]
		if !ok {
			// An unborn branch: nothing to check out.
			return errors.New("the repository's HEAD has no commit")
		}
		fmt.Fprintf(&updates, "create refs/heads/%s %s\n", branch, commit)
	}
	for _, commit := range branches {
		if _, err := run(ctx, dest, "cat-file", "-e", commit+"^{commit}"); err != nil {
			return fmt.Errorf("the copy lacks %s", commit)
		}
	}
	for _, kv := range [][2]string{
		{"remote.origin.url", from},
		{"remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*"},
	} {
		if _, err := run(ctx, dest, "config", kv[0], kv[1]); err != nil {
			return err
		}
	}
	if _, err := runStdin(ctx, dest, updates.String(), "update-ref", "--stdin"); err != nil {
		return err
	}
	if !onBranch {
		_, err := run(ctx, dest, "update-ref", "--no-deref", "HEAD", head)
		return err
	}
	for _, kv := range [][2]string{
		{"branch." + branch + ".remote", "origin"},
		{"branch." + branch + ".merge", head},
	} {
		if _, err := run(ctx, dest, "config", kv[0], kv[1]); err != nil {
			return err
		}
	}
	if _, err := run(ctx, dest, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/"+branch); err != nil {
		return err
	}
	_, err := run(ctx, dest, "symbolic-ref", "HEAD", head)
	return err
}
