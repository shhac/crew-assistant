//go:build !windows

package gitrepo

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/shhac/crew-assistant/internal/media"
)

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
