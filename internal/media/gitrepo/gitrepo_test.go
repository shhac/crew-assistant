//go:build !windows

package gitrepo

import (
	"context"
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
	base, _, err := r.Begin(ctx, "crew/note")
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
	base, _, err := r.Begin(ctx, "crew/note")
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
	if err = r.Reset(ctx, first); err != nil {
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

func TestPlantedHooksAndFsmonitorNeverRunAsTheDaemon(t *testing.T) {
	source := ownerRepo(t)
	r, err := Open(ctx, t.TempDir(), source, nil)
	if err != nil {
		t.Fatal(err)
	}
	base, _, err := r.Begin(ctx, "crew/note")
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
	if err = r.Reset(ctx, commit); err != nil {
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
	base, _, err := r.Begin(ctx, "crew/note")
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
	base, _, err := r.Begin(ctx, "crew/risky")
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
	base, _, err := r.Begin(ctx, "crew/filter")
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
