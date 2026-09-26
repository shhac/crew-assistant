package gitrepo

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// Adopting an owner's commit leaves the shared clone's checkout exactly as
// it was, keeping the commit by a ref of the task's.
func TestAdoptLeavesTheSharedCloneAlone(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	git(t, source, "init", "-q", "-b", "main")
	write(t, filepath.Join(source, "a.go"), "package x\n")
	git(t, source, "add", ".")
	git(t, source, "commit", "-q", "-m", "start")
	r, err := Open(ctx, t.TempDir(), source, nil, SignAsOwner)
	if err != nil {
		t.Fatal(err)
	}
	git(t, source, "checkout", "-q", "-b", "mine")
	write(t, filepath.Join(source, "b.go"), "package x\n")
	git(t, source, "add", ".")
	git(t, source, "commit", "-q", "-m", "by hand")
	want := git(t, source, "rev-parse", "HEAD")
	write(t, filepath.Join(r.Workspace(), "wip.go"), "package x // another task's work\n")
	head := git(t, r.Workspace(), "rev-parse", "HEAD")

	got, err := r.Adopt(ctx, "task-one", "mine")
	if err != nil || got != want {
		t.Fatalf("adopted %s, %v", got, err)
	}
	if git(t, r.Workspace(), "rev-parse", "HEAD") != head || git(t, r.Workspace(), "status", "--porcelain") != "?? wip.go" {
		t.Fatal("adopting touched the shared checkout")
	}
	if git(t, r.Workspace(), "rev-parse", "refs/crew-assistant/adopted/task-one") != want {
		t.Fatal("the commit isn't kept for the task")
	}
	if _, err := r.Adopt(ctx, "task-one", "no-such-branch"); err == nil {
		t.Fatal("a missing ref was adopted")
	}
}

// A branch behind the draft moves up to it; a name git wouldn't take as a
// branch is refused.
func TestCheckoutDraftFastForwards(t *testing.T) {
	ctx := context.Background()
	clone := t.TempDir()
	git(t, clone, "init", "-q", "-b", "main")
	git(t, clone, "commit", "-q", "--allow-empty", "-m", "one")
	first := git(t, clone, "rev-parse", "HEAD")
	git(t, clone, "commit", "-q", "--allow-empty", "-m", "two")
	second := git(t, clone, "rev-parse", "HEAD")
	owner := t.TempDir()
	git(t, owner, "init", "-q", "-b", "main")
	git(t, owner, "commit", "-q", "--allow-empty", "-m", "start")
	if err := CheckoutDraft(ctx, owner, clone, first, "crew-task/one", false); err != nil {
		t.Fatal(err)
	}
	if err := CheckoutDraft(ctx, owner, clone, second, "crew-task/one", false); err != nil {
		t.Fatalf("a branch behind the draft wasn't moved up: %v", err)
	}
	if git(t, owner, "rev-parse", "crew-task/one") != second {
		t.Fatal("the branch didn't move")
	}
	if err := CheckoutDraft(ctx, owner, clone, second, "bad..name", false); err == nil {
		t.Fatal("an invalid branch name was taken")
	}
}

// Checking a draft out never moves a branch past commits of the owner's the
// draft lacks, unless forced, and never the branch the owner is on.
func TestCheckoutDraftKeepsTheOwnersWork(t *testing.T) {
	ctx := context.Background()
	clone := t.TempDir()
	git(t, clone, "init", "-q", "-b", "main")
	write(t, filepath.Join(clone, "a.go"), "package x\n")
	git(t, clone, "add", ".")
	git(t, clone, "commit", "-q", "-m", "draft")
	draft := git(t, clone, "rev-parse", "HEAD")
	owner := t.TempDir()
	git(t, owner, "init", "-q", "-b", "main")
	git(t, owner, "commit", "-q", "--allow-empty", "-m", "start")

	if err := CheckoutDraft(ctx, owner, clone, draft, "crew-task/one", false); err != nil {
		t.Fatal(err)
	}
	if got := git(t, owner, "rev-parse", "crew-task/one"); got != draft {
		t.Fatalf("branch at %s", got)
	}
	git(t, owner, "checkout", "-q", "crew-task/one")
	git(t, owner, "commit", "-q", "--allow-empty", "-m", "mine")
	git(t, owner, "checkout", "-q", "main")
	if err := CheckoutDraft(ctx, owner, clone, draft, "crew-task/one", false); !errors.Is(err, ErrOwnersWork) {
		t.Fatalf("moved past the owner's commit: %v", err)
	}
	if err := CheckoutDraft(ctx, owner, clone, draft, "crew-task/one", true); err != nil {
		t.Fatalf("forced: %v", err)
	}
	if err := CheckoutDraft(ctx, owner, clone, draft, "main", true); err == nil {
		t.Fatal("moved the branch the owner is on")
	}
}
