package localdocs

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestRevisionsResetAndReviewCopies(t *testing.T) {
	d, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.Snapshot("task", 1); err == nil {
		t.Fatal("an empty workspace was recorded as a draft")
	}
	write(t, filepath.Join(d.Workspace(), "draft.md"), "first")
	write(t, filepath.Join(d.Workspace(), "notes", "a.md"), "note")
	files, err := d.Snapshot("task", 1)
	if err != nil || len(files) != 2 || files[0] != "draft.md" || files[1] != "notes/a.md" {
		t.Fatalf("files %v err %v", files, err)
	}
	// A failed turn scribbles; resetting restores revision 1 exactly.
	write(t, filepath.Join(d.Workspace(), "draft.md"), "half-written")
	write(t, filepath.Join(d.Workspace(), "stray.txt"), "junk")
	if err = d.Reset("task", 1); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(filepath.Join(d.Workspace(), "draft.md")); string(raw) != "first" {
		t.Fatalf("reset left %q", raw)
	}
	if _, err = os.Stat(filepath.Join(d.Workspace(), "stray.txt")); err == nil {
		t.Fatal("reset kept a file the revision never had")
	}
	// A reviewer's copy is its own.
	dir, cleanup, err := d.ReviewCopy("task", 1)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "draft.md"), "reviewer edit")
	cleanup()
	preview, err := d.Preview("task", 1, 3)
	if err != nil || len(preview) != 2 || preview[0].Content != "fir" || !preview[0].Truncated {
		t.Fatalf("preview %+v err %v", preview, err)
	}
	if err = d.Reset("task", 0); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(d.Workspace()); len(entries) != 0 {
		t.Fatal("reset to nothing kept files")
	}
}

func TestLinksAreNotFollowed(t *testing.T) {
	d, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret")
	write(t, outside, "not a draft")
	write(t, filepath.Join(d.Workspace(), "draft.md"), "draft")
	if err = os.Symlink(outside, filepath.Join(d.Workspace(), "link.md")); err != nil {
		t.Skip("symlinks unavailable")
	}
	files, err := d.Snapshot("task", 1)
	if err != nil || len(files) != 1 {
		t.Fatalf("a link was recorded: %v %v", files, err)
	}
}

func TestDeliveryNeverOverwritesAndSettlesRetries(t *testing.T) {
	d, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(d.Workspace(), "draft.md"), "final")
	if _, err = d.Snapshot("task", 2); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	write(t, filepath.Join(dest, "thank-you-note-r2", "draft.md"), "owner's own file")
	first, err := d.Deliver("task", 2, dest, "Thank-you note!")
	if err != nil || filepath.Base(first) != "thank-you-note-r2-2" {
		t.Fatalf("delivered to %q err %v", first, err)
	}
	if raw, _ := os.ReadFile(filepath.Join(dest, "thank-you-note-r2", "draft.md")); string(raw) != "owner's own file" {
		t.Fatal("delivery overwrote the owner's file")
	}
	again, err := d.Deliver("task", 2, dest, "Thank-you note!")
	if err != nil || again != first {
		t.Fatalf("a retried delivery made another copy: %q vs %q", again, first)
	}
	if _, err = d.Deliver("task", 2, "relative", "x"); err == nil {
		t.Fatal("relative destination accepted")
	}
}
