//go:build !windows

package gitrepo

import (
	"path/filepath"
	"slices"
	"testing"
)

// An update that took in its target is judged on the task's own change:
// what the target brought, it already had.
func TestOwnAttentionLeavesOutWhatTheTargetBrought(t *testing.T) {
	r := Repo{root: t.TempDir()}
	dir := r.Workspace()
	git(t, filepath.Dir(dir), "init", "-q", "-b", "main", filepath.Base(dir))
	write(t, filepath.Join(dir, "main.go"), "package main\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "start")
	git(t, dir, "checkout", "-q", "-b", "task")
	write(t, filepath.Join(dir, "a.go"), "package main // a\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "the task")
	pushed := git(t, dir, "rev-parse", "HEAD")
	git(t, dir, "checkout", "-q", "main")
	write(t, filepath.Join(dir, "package.json"), "{}\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "main moves on")
	target := git(t, dir, "rev-parse", "HEAD")
	git(t, dir, "checkout", "-q", "task")
	git(t, dir, "merge", "-q", "--no-edit", "main")
	write(t, filepath.Join(dir, "Makefile"), "all:\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "the task's own build change")
	update := git(t, dir, "rev-parse", "HEAD")

	all, err := r.Attention(ctx, pushed, update)
	if err != nil || !slices.Contains(all, "package.json changed") {
		t.Fatalf("the whole update flags %v, %v", all, err)
	}
	own, err := r.OwnAttention(ctx, pushed, update, target)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(own, []string{"Makefile changed"}) {
		t.Fatalf("the task's own change flags %v, want only its Makefile", own)
	}
}
