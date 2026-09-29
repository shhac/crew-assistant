//go:build !windows

package gitrepo

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCatchingUpMergesLandedWorkAndRefusesUnresolvedConflicts(t *testing.T) {
	source := ownerRepo(t)
	r, err := Open(ctx, t.TempDir(), source, nil, SignAsOwner)
	if err != nil {
		t.Fatal(err)
	}
	base, _, err := r.Begin(ctx, "crew-task/a", "")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(r.Workspace(), "main.go"), "package main\n\nfunc A() {}\n")
	landed, _, err := r.Snapshot(ctx, base, base, "a")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = r.Begin(ctx, "crew-task/b", ""); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(r.Workspace(), "main.go"), "package main\n\nfunc B() {}\n")
	b1, _, err := r.Snapshot(ctx, base, base, "b")
	if err != nil {
		t.Fatal(err)
	}
	if in, err := r.Contains(ctx, b1, landed); err != nil || in {
		t.Fatalf("b already contains a: %v %v", in, err)
	}
	if err = r.Reset(ctx, "crew-task/b", b1); err != nil {
		t.Fatal(err)
	}
	conflicts, err := r.Merge(ctx, landed)
	if err != nil || len(conflicts) != 1 || conflicts[0] != "main.go" {
		t.Fatalf("conflicts %v err %v", conflicts, err)
	}
	if _, _, err = r.Snapshot(ctx, landed, b1, "b caught up"); err == nil || !strings.Contains(err.Error(), "conflict markers") {
		t.Fatalf("recorded unresolved conflicts: %v", err)
	}
	// A failed round is reset; the next one merges again and resolves.
	if err = r.Reset(ctx, "crew-task/b", b1); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Merge(ctx, landed); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(r.Workspace(), "main.go"), "package main\n\nfunc A() {}\n\nfunc B() {}\n")
	b2, files, err := r.Snapshot(ctx, landed, b1, "b caught up")
	if err != nil || len(files) != 1 {
		t.Fatalf("files %v err %v", files, err)
	}
	if in, err := r.Contains(ctx, b2, landed); err != nil || !in {
		t.Fatalf("the caught-up revision does not contain what landed: %v %v", in, err)
	}
	if in, _ := r.Contains(ctx, b2, b1); !in {
		t.Fatal("the caught-up revision dropped the task's own history")
	}
}

func TestAMergeThatChangesNoFilesIsStillRecorded(t *testing.T) {
	source := ownerRepo(t)
	r, err := Open(ctx, t.TempDir(), source, nil, SignAsOwner)
	if err != nil {
		t.Fatal(err)
	}
	base, _, err := r.Begin(ctx, "crew-task/a", "")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(r.Workspace(), "same.go"), "package main\n")
	landed, _, _ := r.Snapshot(ctx, base, base, "a")
	if _, _, err = r.Begin(ctx, "crew-task/b", ""); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(r.Workspace(), "same.go"), "package main\n")
	b1, _, _ := r.Snapshot(ctx, base, base, "b, identical")
	if err = r.Reset(ctx, "crew-task/b", b1); err != nil {
		t.Fatal(err)
	}
	if conflicts, err := r.Merge(ctx, landed); err != nil || len(conflicts) != 0 {
		t.Fatalf("conflicts %v err %v", conflicts, err)
	}
	merged, _, err := r.Snapshot(ctx, landed, b1, "catch up")
	if err != nil {
		t.Fatal(err)
	}
	if in, _ := r.Contains(ctx, merged, landed); !in {
		t.Fatal("the merge was not recorded, so the task never catches up")
	}
}

func TestMergeCleanNeverTouchesTheWorkspace(t *testing.T) {
	source := ownerRepo(t)
	r, err := Open(ctx, t.TempDir(), source, nil, SignAsOwner)
	if err != nil {
		t.Fatal(err)
	}
	base, _, _ := r.Begin(ctx, "crew-task/a", "")
	write(t, filepath.Join(r.Workspace(), "main.go"), "package main // a\n")
	a, _, _ := r.Snapshot(ctx, base, base, "a")
	r.Begin(ctx, "crew-task/b", "")
	write(t, filepath.Join(r.Workspace(), "main.go"), "package main // b\n")
	b, _, _ := r.Snapshot(ctx, base, base, "b")
	before := git(t, r.Workspace(), "rev-parse", "HEAD")
	if commit, err := r.MergeClean(ctx, b, a, "merge"); err != nil || commit != "" {
		t.Fatalf("a conflicting merge was made clean: %q %v", commit, err)
	}
	if git(t, r.Workspace(), "rev-parse", "HEAD") != before || git(t, r.Workspace(), "status", "--porcelain") != "" {
		t.Fatal("a merge attempt changed the workspace")
	}
	r.Begin(ctx, "crew-task/c", "")
	write(t, filepath.Join(r.Workspace(), "other.go"), "package main\n")
	c, _, _ := r.Snapshot(ctx, base, base, "c")
	merged, err := r.MergeClean(ctx, c, a, "merge")
	if err != nil || merged == "" {
		t.Fatalf("a clean merge failed: %v", err)
	}
	if parents := strings.Fields(git(t, r.Workspace(), "rev-list", "--parents", "-n1", merged)); len(parents) != 3 || parents[1] != c || parents[2] != a {
		t.Fatalf("the merge does not have both parents: %v", parents)
	}
}
