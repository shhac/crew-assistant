//go:build !windows

package gitrepo

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// packedOwnerRepo is ownerRepo with its objects packed, a second branch, and
// the bookkeeping a real repository has: a .keep and a multi-pack index.
func packedOwnerRepo(t *testing.T) string {
	t.Helper()
	dir := ownerRepo(t)
	git(t, dir, "branch", "feature")
	git(t, dir, "repack", "-a", "-d", "-q")
	git(t, dir, "multi-pack-index", "write")
	packs, _ := filepath.Glob(filepath.Join(dir, ".git", "objects", "pack", "*.pack"))
	write(t, packs[0][:len(packs[0])-len(".pack")]+".keep", "")
	return dir
}

// A quick clone is what git clone would make: the same branches as origin's,
// HEAD on the owner's branch and tracking it, and nothing to commit.
func TestAQuickCloneIsWhatGitCloneMakes(t *testing.T) {
	owner := packedOwnerRepo(t)
	quick, plain := filepath.Join(t.TempDir(), "quick"), filepath.Join(t.TempDir(), "plain")
	if err := quickClone(ctx, owner, quick); err != nil {
		t.Fatal(err)
	}
	git(t, quick, "checkout", "-q", "--force")
	git(t, filepath.Dir(plain), "clone", "-q", "--no-tags", owner, plain)
	for _, args := range [][]string{
		{"for-each-ref", "--format=%(refname) %(objectname) %(symref)"},
		{"symbolic-ref", "HEAD"},
		{"config", "--get", "remote.origin.url"},
		{"config", "--get", "remote.origin.fetch"},
		{"config", "--get", "branch.main.remote"},
		{"config", "--get", "branch.main.merge"},
		{"status", "--porcelain"},
	} {
		if q, p := git(t, quick, args...), git(t, plain, args...); q != p {
			t.Errorf("git %v:\nquick: %q\nclone: %q", args, q, p)
		}
	}
	git(t, quick, "fsck", "--connectivity-only", "--no-dangling")
}

// The copy shares no file with the owner's objects, so nothing done to one
// reaches the other, and keeps none of their bookkeeping.
func TestAQuickCloneSharesNoObjectFile(t *testing.T) {
	owner := packedOwnerRepo(t)
	quick := filepath.Join(t.TempDir(), "quick")
	if err := quickClone(ctx, owner, quick); err != nil {
		t.Fatal(err)
	}
	theirs := filepath.Join(owner, ".git", "objects", "pack")
	entries, err := os.ReadDir(filepath.Join(quick, ".git", "objects", "pack"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
		mine, _ := os.Stat(filepath.Join(quick, ".git", "objects", "pack", e.Name()))
		if other, err := os.Stat(filepath.Join(theirs, e.Name())); err == nil && os.SameFile(mine, other) {
			t.Errorf("%s is the owner's own file", e.Name())
		}
	}
	if slices.ContainsFunc(names, func(n string) bool {
		return filepath.Ext(n) == ".keep" || filepath.Base(n) == "multi-pack-index"
	}) {
		t.Errorf("the owner's bookkeeping was copied: %v", names)
	}
	if _, err := os.Stat(filepath.Join(quick, ".git", "objects", "info", "alternates")); err == nil {
		t.Error("the clone borrows objects")
	}
}

// A detached owner's checkout gives a detached clone at the same commit.
func TestAQuickCloneOfADetachedHeadIsDetached(t *testing.T) {
	owner := packedOwnerRepo(t)
	commit := git(t, owner, "rev-parse", "HEAD")
	git(t, owner, "checkout", "-q", "--detach")
	quick := filepath.Join(t.TempDir(), "quick")
	if err := quickClone(ctx, owner, quick); err != nil {
		t.Fatal(err)
	}
	if head := git(t, quick, "rev-parse", "HEAD"); head != commit {
		t.Fatalf("HEAD is %s, want %s", head, commit)
	}
	if _, err := run(ctx, quick, "symbolic-ref", "-q", "HEAD"); err == nil {
		t.Fatal("the clone is on a branch")
	}
}

// A repository that borrows its objects can't be copied whole; the clone
// is made the ordinary way.
func TestARepositoryThatBorrowsObjectsIsClonedTheOrdinaryWay(t *testing.T) {
	lender := packedOwnerRepo(t)
	owner := filepath.Join(t.TempDir(), "owner")
	git(t, filepath.Dir(owner), "clone", "-q", "--shared", lender, owner)
	if err := quickClone(ctx, owner, filepath.Join(t.TempDir(), "quick")); err == nil {
		t.Fatal("a quick clone copied a repository that borrows objects")
	}
	repo, err := Open(ctx, filepath.Join(t.TempDir(), "project"), owner, nil, SignAsOwner)
	if err != nil {
		t.Fatal(err)
	}
	git(t, repo.Workspace(), "fsck", "--connectivity-only", "--no-dangling")
}

// The project's clone is quick: its packs are the owner's, copied, not a
// pack git made anew; and a half-made clone a crash left is made again.
func TestTheProjectsCloneIsQuickAndNeverHalfMade(t *testing.T) {
	owner := packedOwnerRepo(t)
	project := filepath.Join(t.TempDir(), "project")
	write(t, filepath.Join(project, "clone.partial", ".git", "HEAD"), "left by a crash\n")
	repo, err := Open(ctx, project, owner, []string{"node_modules"}, SignAsOwner)
	if err != nil {
		t.Fatal(err)
	}
	theirs, _ := packNames(filepath.Join(owner, ".git", "objects"))
	mine, _ := packNames(filepath.Join(repo.Workspace(), ".git", "objects"))
	if !slices.Equal(theirs, mine) {
		t.Fatalf("packs %v, want the owner's %v", mine, theirs)
	}
	if _, err := os.Stat(filepath.Join(project, "clone.partial")); err == nil {
		t.Fatal("the half-made clone is still there")
	}
	if _, err := os.Stat(filepath.Join(repo.Workspace(), "node_modules", "dep", "index.js")); err != nil {
		t.Fatalf("the prepared folder wasn't copied: %v", err)
	}
	if status := git(t, repo.Workspace(), "status", "--porcelain"); status != "" {
		t.Fatalf("the clone has changes: %q", status)
	}
	task := repo.Task("t1")
	if err := task.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	if head, want := git(t, task.Workspace(), "rev-parse", "HEAD"), git(t, owner, "rev-parse", "HEAD"); head != want {
		t.Fatalf("the task's clone is at %s, want %s", head, want)
	}
}

// A finished task's clone goes even when a test run in it left a folder
// read-only.
func TestAFinishedTasksCloneGoesWithReadOnlyFolders(t *testing.T) {
	owner := packedOwnerRepo(t)
	repo, err := Open(ctx, filepath.Join(t.TempDir(), "project"), owner, nil, SignAsOwner)
	if err != nil {
		t.Fatal(err)
	}
	task := repo.Task("t1")
	if err := task.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(task.Workspace(), ".crew", "tmp", "TestSomething", "checkout")
	write(t, filepath.Join(locked, "file"), "x")
	if err := os.Chmod(locked, 0500); err != nil {
		t.Fatal(err)
	}
	if err := repo.RemoveTask("t1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(task.Workspace()); err == nil {
		t.Fatal("the task's clone is still there")
	}
}
