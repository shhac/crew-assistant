//go:build !windows

package gitrepo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/media"
)

func TestCopyForCheckRecordsWorkingDraftFromBase(t *testing.T) {
	source := ownerRepo(t)
	base := git(t, source, "rev-parse", "HEAD")
	git(t, source, "config", "core.quotePath", "false")
	os.WriteFile(filepath.Join(source, "committed.go"), []byte("package main\n"), 0600)
	git(t, source, "add", "committed.go")
	git(t, source, "commit", "-m", "committed draft")
	os.WriteFile(filepath.Join(source, "main.go"), []byte("package main // uncommitted\n"), 0600)
	os.WriteFile(filepath.Join(source, "untracked.go"), []byte("package main\n"), 0600)
	os.WriteFile(filepath.Join(source, ".gitignore"), []byte("ignored\n"), 0600)
	os.WriteFile(filepath.Join(source, "ignored"), []byte("ignored"), 0600)
	repo, err := Open(context.Background(), t.TempDir(), source, nil, SignNever)
	if err != nil {
		t.Fatal(err)
	}
	c, err := repo.CopyForCheckBase(context.Background(), source, base)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Remove()
	files, err := os.ReadFile(filepath.Join(c.Dir, ".git", changedFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"committed.go", "main.go", "untracked.go"} {
		if !strings.Contains(string(files), name) {
			t.Fatalf("missing %s: %s", name, files)
		}
	}
	if strings.Contains(string(files), "ignored") {
		t.Fatal(string(files))
	}
	// A revision checkout's authoritative record survives another copy.
	copy, err := repo.CopyForCheckBase(context.Background(), c.Dir, "not-a-commit")
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Remove()
	kept, _ := os.ReadFile(filepath.Join(copy.Dir, ".git", changedFile))
	if string(kept) != string(files) {
		t.Fatal("existing record changed")
	}
}

func TestCopyForCheckKeepsDraftAndCachesWithoutChangingSource(t *testing.T) {
	source := ownerRepo(t)
	repo, err := Open(context.Background(), t.TempDir(), source, nil, SignNever)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"main.go", "untracked.txt", ".crew/go-build/warm"} {
		path := filepath.Join(source, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("uncommitted"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	c, err := repo.CopyForCheck(source)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Remove()
	for _, name := range []string{"main.go", "untracked.txt", ".crew/go-build/warm"} {
		got, err := os.ReadFile(filepath.Join(c.Dir, name))
		if err != nil || string(got) != "uncommitted" {
			t.Fatalf("%s: %s %v", name, got, err)
		}
	}
	if err := os.WriteFile(filepath.Join(c.Dir, "main.go"), []byte("check output"), 0600); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(source, "main.go"))
	if string(got) != "uncommitted" {
		t.Fatal("check changed source")
	}
	if err := repo.RemoveChecks(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.Dir); !os.IsNotExist(err) {
		t.Fatal("restart cleanup missed copy")
	}

	// QA's revision is read-only, and its cache sits outside that revision.
	cache := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cache, "go-build"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "go-build", "qa-warm"), []byte("warm"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := media.SetWritable(source, false); err != nil {
		t.Fatal(err)
	}
	defer media.SetWritable(source, true)
	c, err = repo.CopyForCheck(source, cache)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Remove()
	if body, err := os.ReadFile(filepath.Join(c.Dir, ".crew/go-build/qa-warm")); err != nil || string(body) != "warm" {
		t.Fatalf("QA cache %s %v", body, err)
	}
	if err := os.WriteFile(filepath.Join(c.Dir, "main.go"), []byte("build"), 0600); err != nil {
		t.Fatal("copy not writable:", err)
	}
}
