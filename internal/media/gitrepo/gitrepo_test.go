//go:build !windows

package gitrepo

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var ctx = context.Background()

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Owner", "GIT_AUTHOR_EMAIL=owner@example.test", "GIT_COMMITTER_NAME=Owner", "GIT_COMMITTER_EMAIL=owner@example.test")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

// ownerRepo is a small repository standing in for the owner's checkout, with
// an ignored dependency folder and uncommitted work of their own.
func ownerRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "config", "commit.gpgsign", "false")
	write(t, filepath.Join(dir, "main.go"), "package main\n")
	write(t, filepath.Join(dir, ".gitignore"), "node_modules/\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "start")
	write(t, filepath.Join(dir, "node_modules", "dep", "index.js"), "module.exports = 1\n")
	write(t, filepath.Join(dir, "scratch.txt"), "the owner's uncommitted work\n")
	return dir
}

func TestTheOwnersCheckoutIsNeverTouched(t *testing.T) {
	source := ownerRepo(t)
	before := git(t, source, "status", "--porcelain")
	r, err := Open(ctx, t.TempDir(), source, []string{"node_modules"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(r.Workspace(), "node_modules", "dep", "index.js")); err != nil {
		t.Fatal("prepared dependency was not copied into the clone")
	}
	base, _, err := r.Begin(ctx, "crew/note", "")
	if err != nil || base != git(t, source, "rev-parse", "HEAD") {
		t.Fatalf("base %q err %v", base, err)
	}
	write(t, filepath.Join(r.Workspace(), "main.go"), "package main\n\nfunc main() {}\n")
	if _, _, err = r.Snapshot(ctx, base, base, "draft 1"); err != nil {
		t.Fatal(err)
	}
	if git(t, source, "status", "--porcelain") != before || git(t, source, "branch", "--list", "crew/*") != "" {
		t.Fatal("working in the clone changed the owner's checkout")
	}
	if _, err = Open(ctx, t.TempDir(), t.TempDir(), nil); err == nil {
		t.Fatal("a folder that is not a repository was accepted")
	}
	if _, err = Open(ctx, t.TempDir(), source, []string{"../escape"}); err == nil {
		t.Fatal("a prepare path outside the repository was accepted")
	}
}

func TestRevisionsAndResetKeepIgnoredFilesOnly(t *testing.T) {
	source := ownerRepo(t)
	r, err := Open(ctx, t.TempDir(), source, []string{"node_modules"})
	if err != nil {
		t.Fatal(err)
	}
	base, _, err := r.Begin(ctx, "crew/note", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = r.Snapshot(ctx, base, base, "draft 1"); err == nil || !strings.Contains(err.Error(), "changed nothing") {
		t.Fatalf("an unchanged tree became a revision: %v", err)
	}
	write(t, filepath.Join(r.Workspace(), "feature.go"), "package main\n")
	first, files, err := r.Snapshot(ctx, base, base, "draft 1")
	if err != nil || len(files) != 1 || files[0] != "feature.go" {
		t.Fatalf("files %v err %v", files, err)
	}
	// A failed turn leaves edits, strays and caches; reset keeps only caches
	// and ignored dependencies.
	write(t, filepath.Join(r.Workspace(), "feature.go"), "broken")
	write(t, filepath.Join(r.Workspace(), "stray.txt"), "junk")
	write(t, filepath.Join(r.Workspace(), ".crew", "go-build", "cache"), "cache")
	if err = r.Reset(ctx, "crew/note", first); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(r.Workspace(), "feature.go"))
	_, strayErr := os.Stat(filepath.Join(r.Workspace(), "stray.txt"))
	_, cacheErr := os.Stat(filepath.Join(r.Workspace(), ".crew", "go-build", "cache"))
	_, depErr := os.Stat(filepath.Join(r.Workspace(), "node_modules", "dep", "index.js"))
	if string(raw) != "package main\n" || strayErr == nil || cacheErr != nil || depErr != nil {
		t.Fatalf("reset: file=%q stray=%v cache=%v dep=%v", raw, strayErr, cacheErr, depErr)
	}
	preview, err := r.Preview(ctx, base, first, 1<<20)
	if err != nil || len(preview) != 2 || !strings.Contains(preview[1].Content, "+package main") {
		t.Fatalf("preview %+v err %v", preview, err)
	}
}

func TestTasksSharingTheCloneKeepTheirOwnBranches(t *testing.T) {
	source := ownerRepo(t)
	r, err := Open(ctx, t.TempDir(), source, nil)
	if err != nil {
		t.Fatal(err)
	}
	base, _, err := r.Begin(ctx, "crew-task/a", "")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(r.Workspace(), "a.go"), "package main\n")
	a1, _, err := r.Snapshot(ctx, base, base, "a draft 1")
	if err != nil {
		t.Fatal(err)
	}
	// A second task starts while the first waits on the owner.
	if _, _, err = r.Begin(ctx, "crew-task/b", ""); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(r.Workspace(), "b.go"), "package main\n")
	b1, _, err := r.Snapshot(ctx, base, base, "b draft 1")
	if err != nil {
		t.Fatal(err)
	}
	// The owner asks the first task for changes.
	if err = r.Reset(ctx, "crew-task/a", a1); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(r.Workspace(), "b.go")); err == nil {
		t.Fatal("the second task's work is in the first task's round")
	}
	write(t, filepath.Join(r.Workspace(), "a.go"), "package main\n\nfunc A() {}\n")
	a2, _, err := r.Snapshot(ctx, base, a1, "a draft 2")
	if err != nil {
		t.Fatal(err)
	}
	if tip := git(t, r.Workspace(), "rev-parse", "crew-task/b"); tip != b1 {
		t.Fatalf("the first task's round moved the second task's branch to %s", tip)
	}
	if parent := git(t, r.Workspace(), "rev-parse", a2+"^"); parent != a1 {
		t.Fatalf("draft 2 does not follow draft 1: parent %s", parent)
	}
	if _, err = r.Deliver(ctx, "crew-task/a", a2, "paul/a"); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Deliver(ctx, "crew-task/b", b1, "paul/b"); err != nil {
		t.Fatal(err)
	}
}

func TestCatchingUpMergesLandedWorkAndRefusesUnresolvedConflicts(t *testing.T) {
	source := ownerRepo(t)
	r, err := Open(ctx, t.TempDir(), source, nil)
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

func TestPushLandsOnlyByFastForwardAndFollowsTheOwnersCheckoutRules(t *testing.T) {
	source := ownerRepo(t)
	marker := filepath.Join(t.TempDir(), "hook")
	for _, hook := range []string{"pre-receive", "update", "post-receive", "post-update", "push-to-checkout"} {
		path := filepath.Join(source, ".git", "hooks", hook)
		write(t, path, "#!/bin/sh\ntouch "+marker+"\n")
		os.Chmod(path, 0700)
	}
	r, err := Open(ctx, t.TempDir(), source, nil)
	if err != nil {
		t.Fatal(err)
	}
	base, _, err := r.Begin(ctx, "crew-task/a", "main")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(r.Workspace(), "a.go"), "package main\n")
	commit, _, err := r.Snapshot(ctx, base, base, "a")
	if err != nil {
		t.Fatal(err)
	}
	// main is checked out in the owner's repository. Landing may update it in
	// place, but only when the checkout is clean.
	write(t, filepath.Join(source, "main.go"), "package main // the owner's edit\n")
	if err = r.PushFastForward(ctx, "crew-task/a", commit, "main"); !errors.Is(err, ErrDirtyCheckout) {
		t.Fatalf("landed over the owner's uncommitted work: %v", err)
	}
	if raw, _ := os.ReadFile(filepath.Join(source, "main.go")); string(raw) != "package main // the owner's edit\n" {
		t.Fatal("the owner's uncommitted work was changed")
	}
	git(t, source, "checkout", "--", "main.go")
	if err = r.PushFastForward(ctx, "crew-task/a", commit, "main"); err != nil {
		t.Fatal(err)
	}
	if git(t, source, "rev-parse", "main") != commit {
		t.Fatal("main did not land on the change")
	}
	if _, err = os.Stat(filepath.Join(source, "a.go")); err != nil {
		t.Fatal("the owner's clean checkout was not brought up to date")
	}
	if _, err = os.Stat(marker); err == nil {
		t.Fatal("the owner's hooks ran for a daemon push")
	}
	// The daemon owns that choice for its own pushes: the owner's config is
	// untouched, and any other push into their checked-out main is refused
	// as git refuses by default.
	if out, err := exec.Command("git", "-C", source, "config", "--get", "receive.denyCurrentBranch").Output(); err == nil {
		t.Fatalf("the owner's config was changed: %s", out)
	}
	other := t.TempDir()
	git(t, other, "clone", "-q", source, ".")
	write(t, filepath.Join(other, "other.go"), "package main\n")
	git(t, other, "add", "-A")
	git(t, other, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "someone else")
	if out, err := exec.Command("git", "-C", other, "push", "-q", "origin", "main").CombinedOutput(); err == nil || !strings.Contains(string(out), "checked out") {
		t.Fatalf("an ordinary push into the checked-out main was accepted: %s", out)
	}
	// The owner commits to main; a change built on the old tip is never forced over it.
	write(t, filepath.Join(source, "owner.go"), "package main\n")
	git(t, source, "add", "owner.go")
	git(t, source, "commit", "-q", "-m", "owner work")
	ownerTip := git(t, source, "rev-parse", "main")
	if err = r.Reset(ctx, "crew-task/a", commit); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(r.Workspace(), "b.go"), "package main\n")
	next, _, err := r.Snapshot(ctx, base, commit, "b")
	if err != nil {
		t.Fatal(err)
	}
	if err = r.PushFastForward(ctx, "crew-task/a", next, "main"); !errors.Is(err, ErrTargetMoved) {
		t.Fatalf("expected the moved branch to be refused: %v", err)
	}
	if git(t, source, "rev-parse", "main") != ownerTip {
		t.Fatal("the owner's commit on main was lost")
	}
}

func TestAMergeThatChangesNoFilesIsStillRecorded(t *testing.T) {
	source := ownerRepo(t)
	r, err := Open(ctx, t.TempDir(), source, nil)
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

func TestAnOwnedBranchIsOnlyUpdatedUnderItsLease(t *testing.T) {
	source := ownerRepo(t)
	remote := t.TempDir()
	git(t, remote, "init", "-q", "--bare")
	r, err := Open(ctx, t.TempDir(), source, nil)
	if err != nil {
		t.Fatal(err)
	}
	base, _, err := r.Begin(ctx, "crew-task/a", "main")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(r.Workspace(), "a.go"), "package main\n")
	first, _, _ := r.Snapshot(ctx, base, base, "a")
	if err = r.PushOwned(ctx, remote, first, "crew/a", "", nil); err != nil {
		t.Fatal(err)
	}
	// A branch that already exists is not ours to create over.
	if err = r.PushOwned(ctx, remote, first, "crew/a", "", nil); err != nil && !errors.Is(err, ErrLeaseLost) {
		t.Fatal(err)
	}
	write(t, filepath.Join(r.Workspace(), "a.go"), "package main\n\nfunc A() {}\n")
	second, _, _ := r.Snapshot(ctx, base, first, "a2")
	if err = r.PushOwned(ctx, remote, second, "crew/a", first, nil); err != nil {
		t.Fatal(err)
	}
	// Someone else pushes a fix-up to the branch; the next update must not
	// overwrite it.
	other := t.TempDir()
	git(t, other, "clone", "-q", "--branch", "crew/a", remote, ".")
	write(t, filepath.Join(other, "theirs.go"), "package main\n")
	git(t, other, "add", "-A")
	git(t, other, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "reviewer's fix-up")
	git(t, other, "push", "-q", "origin", "crew/a")
	theirs := git(t, other, "rev-parse", "HEAD")
	write(t, filepath.Join(r.Workspace(), "a.go"), "package main\n\nfunc A() { _ = 1 }\n")
	third, _, _ := r.Snapshot(ctx, base, second, "a3")
	if err = r.PushOwned(ctx, remote, third, "crew/a", second, nil); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("overwrote someone else's push: %v", err)
	}
	if got := git(t, remote, "rev-parse", "refs/heads/crew/a"); got != theirs {
		t.Fatal("the other push was lost")
	}
	fetched, err := r.FetchFrom(ctx, remote, "crew/a", nil)
	if err != nil || fetched != theirs {
		t.Fatalf("fetched %s err %v", fetched, err)
	}
}

func TestPlantedHooksAndFsmonitorNeverRunAsTheDaemon(t *testing.T) {
	source := ownerRepo(t)
	r, err := Open(ctx, t.TempDir(), source, nil)
	if err != nil {
		t.Fatal(err)
	}
	base, _, err := r.Begin(ctx, "crew/note", "")
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "ran")
	script := "#!/bin/sh\ntouch " + marker + "\n"
	// Roles cannot reach .git under their sandbox; the daemon must be safe
	// even if something did.
	write(t, filepath.Join(r.Workspace(), ".git", "hooks", "pre-commit"), script)
	write(t, filepath.Join(r.Workspace(), ".git", "hooks", "post-commit"), script)
	write(t, filepath.Join(r.Workspace(), "fsmonitor.sh"), script)
	git(t, r.Workspace(), "config", "core.hooksPath", filepath.Join(r.Workspace(), ".git", "hooks"))
	git(t, r.Workspace(), "config", "core.fsmonitor", filepath.Join(r.Workspace(), "fsmonitor.sh"))
	write(t, filepath.Join(r.Workspace(), "feature.go"), "package main\n")
	commit, _, err := r.Snapshot(ctx, base, base, "draft 1")
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Reset(ctx, "crew/note", commit); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(marker); err == nil {
		t.Fatal("a planted hook or fsmonitor ran as the daemon")
	}
}

func TestDeliverySettlesAndNeverOverwritesABranch(t *testing.T) {
	source := ownerRepo(t)
	r, err := Open(ctx, t.TempDir(), source, nil)
	if err != nil {
		t.Fatal(err)
	}
	base, _, err := r.Begin(ctx, "crew/note", "")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(r.Workspace(), "feature.go"), "package main\n")
	commit, _, err := r.Snapshot(ctx, base, base, "draft 1")
	if err != nil {
		t.Fatal(err)
	}
	// The owner already has a branch by that name, pointing somewhere else.
	git(t, source, "branch", "paul/note", "main")
	before := git(t, source, "status", "--porcelain")
	name, err := r.Deliver(ctx, "crew/note", commit, "paul/note")
	if err != nil || name != "paul/note-2" || git(t, source, "rev-parse", "refs/heads/paul/note-2") != commit {
		t.Fatalf("delivered %q err %v", name, err)
	}
	if git(t, source, "rev-parse", "refs/heads/paul/note") != base || git(t, source, "rev-parse", "--abbrev-ref", "HEAD") != "main" || git(t, source, "status", "--porcelain") != before {
		t.Fatal("delivery moved an existing branch or touched the checkout")
	}
	again, err := r.Deliver(ctx, "crew/note", commit, "paul/note")
	if err != nil || again != "paul/note-2" {
		t.Fatalf("a retried delivery made another branch: %q %v", again, err)
	}
	if _, err = r.Deliver(ctx, "crew/note", base, "paul/other"); err == nil {
		t.Fatal("delivered a commit the task branch does not hold")
	}
}

func TestPreviewFlagsWhatRunsOrInstructsOnTheOwnersSide(t *testing.T) {
	source := ownerRepo(t)
	r, err := Open(ctx, t.TempDir(), source, nil)
	if err != nil {
		t.Fatal(err)
	}
	base, _, err := r.Begin(ctx, "crew/risky", "")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(r.Workspace(), "Makefile"), "all:\n\ttrue\n")
	write(t, filepath.Join(r.Workspace(), ".claude", "settings.json"), "{}")
	write(t, filepath.Join(r.Workspace(), "ordinary.go"), "package main\n")
	if err = os.Symlink("/etc/hosts", filepath.Join(r.Workspace(), "hosts")); err != nil {
		t.Fatal(err)
	}
	commit, _, err := r.Snapshot(ctx, base, base, "draft 1")
	if err != nil {
		t.Fatal(err)
	}
	notes, err := r.Attention(ctx, base, commit)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(notes, "\n")
	for _, want := range []string{"Makefile changed", ".claude/settings.json changed", "hosts is a symlink"} {
		if !strings.Contains(joined, want) {
			t.Errorf("attention misses %q: %v", want, notes)
		}
	}
	if strings.Contains(joined, "ordinary.go") {
		t.Errorf("an ordinary file was flagged: %v", notes)
	}
	preview, err := r.Preview(ctx, base, commit, 1<<20)
	if err != nil || preview[0].Path != "attention" {
		t.Fatalf("preview does not lead with what needs a look: %+v", preview)
	}
}

func TestDaemonGitIgnoresGlobalConfig(t *testing.T) {
	// A filter in the operator's global config, with a .gitattributes a role
	// wrote, must not run as the daemon.
	home := t.TempDir()
	marker := filepath.Join(t.TempDir(), "ran")
	filter := filepath.Join(home, "filter.sh")
	write(t, filter, "#!/bin/sh\ntouch "+marker+"\ncat\n")
	if err := os.Chmod(filter, 0700); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(home, "gitconfig")
	write(t, global, "[filter \"evil\"]\n\tclean = "+filter+"\n")
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	source := ownerRepo(t)
	r, err := Open(ctx, t.TempDir(), source, nil)
	if err != nil {
		t.Fatal(err)
	}
	base, _, err := r.Begin(ctx, "crew/filter", "")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(r.Workspace(), ".gitattributes"), "*.go filter=evil\n")
	write(t, filepath.Join(r.Workspace(), "x.go"), "package main\n")
	if _, _, err = r.Snapshot(ctx, base, base, "draft 1"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(marker); err == nil {
		t.Fatal("a filter from global config ran as the daemon")
	}
}

func TestMergeCleanNeverTouchesTheWorkspace(t *testing.T) {
	source := ownerRepo(t)
	r, err := Open(ctx, t.TempDir(), source, nil)
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

func TestAFirstPushNeverTakesOverABranchAlreadyThere(t *testing.T) {
	source := ownerRepo(t)
	remote := t.TempDir()
	git(t, remote, "init", "-q", "--bare")
	git(t, source, "push", "-q", remote, "main:crew/a")
	r, err := Open(ctx, t.TempDir(), source, nil)
	if err != nil {
		t.Fatal(err)
	}
	base, _, _ := r.Begin(ctx, "crew-task/a", "main")
	write(t, filepath.Join(r.Workspace(), "a.go"), "package main\n")
	commit, _, _ := r.Snapshot(ctx, base, base, "a")
	if err = r.PushOwned(ctx, remote, commit, "crew/a", "", nil); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("pushed over a branch someone else made: %v", err)
	}
	if git(t, remote, "rev-parse", "refs/heads/crew/a") != base {
		t.Fatal("the existing branch was moved")
	}
}
