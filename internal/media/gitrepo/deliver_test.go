//go:build !windows

package gitrepo

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// delivery is a project on a fresh owner's repository with one recorded
// revision, and the commit it was built on.
func delivery(t *testing.T) (r Repo, source, base, commit string) {
	t.Helper()
	source = ownerRepo(t)
	r, err := Open(ctx, t.TempDir(), source, nil, SignAsOwner)
	if err != nil {
		t.Fatal(err)
	}
	base, _, err = r.Begin(ctx, "crew/x", "main")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(r.Workspace(), "feature.go"), "package main\n")
	commit, _, err = r.Snapshot(ctx, base, base, "draft 1")
	if err != nil {
		t.Fatal(err)
	}
	return r, source, base, commit
}

func TestAPushIntoALinkedWorktreeLandsOnlyWhenItIsClean(t *testing.T) {
	r, source, base, commit := delivery(t)
	git(t, source, "branch", "target", "main")
	linked := filepath.Join(t.TempDir(), "linked")
	if err := AddWorktree(ctx, source, linked, "target"); err != nil {
		t.Fatal(err)
	}
	edit := filepath.Join(linked, "main.go")
	write(t, edit, "package main // the owner's edit\n")
	if err := r.PushFastForward(ctx, commit, "target"); !errors.Is(err, ErrDirtyCheckout) {
		t.Fatalf("landed over the owner's uncommitted work in a linked worktree: %v", err)
	}
	if raw, _ := os.ReadFile(edit); string(raw) != "package main // the owner's edit\n" {
		t.Fatal("the owner's uncommitted work was changed")
	}
	if git(t, source, "rev-parse", "target") != base {
		t.Fatal("the branch moved though its checkout is dirty")
	}
	// ErrCheckedOut is never the answer here: the push lets git update a
	// clean checkout in place, linked worktrees included, so with this git a
	// checked-out target only ever refuses as dirty. ErrCheckedOut is left for
	// a git whose receive-pack refuses updateInstead outright.
	git(t, linked, "checkout", "--", "main.go")
	if err := r.PushFastForward(ctx, commit, "target"); err != nil {
		t.Fatalf("a clean linked worktree refused the landing: %v", err)
	}
	if git(t, source, "rev-parse", "target") != commit {
		t.Fatal("target did not land on the change")
	}
	if _, err := os.Stat(filepath.Join(linked, "feature.go")); err != nil {
		t.Fatal("the clean linked worktree was not brought up to date")
	}
}
