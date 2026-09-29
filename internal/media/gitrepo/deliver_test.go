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

func TestDeliveredFindsTheBranchDeliverChose(t *testing.T) {
	r, source, _, commit := delivery(t)
	if name, ok, err := r.Delivered(ctx, commit, "paul/x"); err != nil || ok {
		t.Fatalf("found a delivery before any was made: %q %v %v", name, ok, err)
	}
	// The owner already has a branch by that name, at another commit.
	git(t, source, "branch", "paul/x", "main")
	delivered, err := r.Deliver(ctx, commit, "paul/x")
	if err != nil || delivered != "paul/x-2" {
		t.Fatalf("delivered %q: %v", delivered, err)
	}
	if name, ok, err := r.Delivered(ctx, commit, "paul/x"); err != nil || !ok || name != delivered {
		t.Fatalf("Delivered says %q %v %v, Deliver chose %q", name, ok, err, delivered)
	}
	// The owner checks the delivered branch out: a retried delivery settles on
	// it rather than making another, and both still agree on its name.
	git(t, source, "checkout", "-q", delivered)
	again, err := r.Deliver(ctx, commit, "paul/x")
	if err != nil || again != delivered {
		t.Fatalf("a retried delivery chose %q rather than %q: %v", again, delivered, err)
	}
	if out := git(t, source, "branch", "--list", "paul/x-3"); out != "" {
		t.Fatalf("a retried delivery made another branch: %s", out)
	}
	if name, ok, err := r.Delivered(ctx, commit, "paul/x"); err != nil || !ok || name != delivered {
		t.Fatalf("Delivered says %q %v %v, Deliver chose %q", name, ok, err, delivered)
	}
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
