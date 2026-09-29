package localdocs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/shhac/crew-assistant/internal/media"
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
	if _, _, err = d.Snapshot("task", 1); err == nil {
		t.Fatal("an empty workspace was recorded as a draft")
	}
	write(t, filepath.Join(d.Workspace("task"), "draft.md"), "first")
	write(t, filepath.Join(d.Workspace("task"), "notes", "a.md"), "note")
	files, sum, err := d.Snapshot("task", 1)
	if err != nil || len(files) != 2 || files[0] != "draft.md" || files[1] != "notes/a.md" {
		t.Fatalf("files %v err %v", files, err)
	}
	if got, err := d.Digest("task", 1); err != nil || got != sum || sum == "" {
		t.Fatalf("revision 1 is named %q, its snapshot said %q: %v", got, sum, err)
	}
	if got, err := d.Digest("task", 2); err != nil || got != "" {
		t.Fatalf("a revision never made is named %q: %v", got, err)
	}
	// A failed turn scribbles; resetting restores revision 1 exactly.
	write(t, filepath.Join(d.Workspace("task"), "draft.md"), "half-written")
	write(t, filepath.Join(d.Workspace("task"), "stray.txt"), "junk")
	if err = d.Reset("task", 1); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(filepath.Join(d.Workspace("task"), "draft.md")); string(raw) != "first" {
		t.Fatalf("reset left %q", raw)
	}
	if _, err = os.Stat(filepath.Join(d.Workspace("task"), "stray.txt")); err == nil {
		t.Fatal("reset kept a file the revision never had")
	}
	// A check's copy is its own, read-only, and says when it was changed.
	first, _ := d.Digest("task", 1)
	c, err := d.Checkout("task", 1, first)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(c.Dir, "draft.md"), []byte("checker edit"), 0o600); err == nil {
		t.Fatal("a check's copy is writable")
	}
	if err = os.WriteFile(filepath.Join(c.Dir, "planted.md"), nil, 0o600); err == nil {
		t.Fatal("a check can add to its copy")
	}
	write(t, filepath.Join(c.Scratch, "notes.txt"), "scratch")
	if err = c.Verify(); err != nil {
		t.Fatalf("an unchanged copy: %v", err)
	}
	os.Chmod(c.Dir, 0o700)
	os.Symlink(filepath.Join(c.Dir, "draft.md"), filepath.Join(c.Dir, "link.md"))
	if err = c.Verify(); !errors.Is(err, media.ErrCheckChanged) {
		t.Fatalf("a link added to the copy went unseen: %v", err)
	}
	os.Remove(filepath.Join(c.Dir, "link.md"))
	os.Chmod(filepath.Join(c.Dir, "draft.md"), 0o600)
	write(t, filepath.Join(c.Dir, "draft.md"), "checker edit")
	if err = c.Verify(); !errors.Is(err, media.ErrCheckChanged) {
		t.Fatalf("an edited copy went unseen: %v", err)
	}
	c.Remove()
	if _, err = os.Stat(c.Dir); !os.IsNotExist(err) {
		t.Fatal("the copy was left")
	}
	if _, err = d.Checkout("task", 1, "another digest"); err == nil {
		t.Fatal("a copy that is not the recorded revision was given")
	}
	preview, err := d.Preview("task", 1, 3)
	if err != nil || len(preview) != 2 || preview[0].Content != "fir" || !preview[0].Truncated {
		t.Fatalf("preview %+v err %v", preview, err)
	}
	if err = d.Reset("task", 0); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(d.Workspace("task")); len(entries) != 0 {
		t.Fatal("reset to nothing kept files")
	}
}

// Each task has a workspace of its own: one task's writer never sees or
// changes another's work, and a finished task's workspace goes while its
// revisions stay.
func TestEachTaskHasItsOwnWorkspace(t *testing.T) {
	d, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if d.Workspace("one") == d.Workspace("two") {
		t.Fatal("two tasks share a workspace")
	}
	write(t, filepath.Join(d.Workspace("one"), "draft.md"), "one's draft")
	if _, _, err = d.Snapshot("one", 1); err != nil {
		t.Fatal(err)
	}
	if err = d.Reset("two", 0); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(d.Workspace("two"), "other.md"), "two's draft")
	if raw, _ := os.ReadFile(filepath.Join(d.Workspace("one"), "draft.md")); string(raw) != "one's draft" {
		t.Fatalf("another task's reset changed this one's work: %q", raw)
	}
	if _, err = os.Stat(filepath.Join(d.Workspace("one"), "other.md")); err == nil {
		t.Fatal("another task's work showed up in this one's workspace")
	}
	if ids, err := d.Workspaces(); err != nil || len(ids) != 2 {
		t.Fatalf("workspaces %v: %v", ids, err)
	}
	if err = d.RemoveWorkspace("one"); err != nil {
		t.Fatal(err)
	}
	if ids, _ := d.Workspaces(); len(ids) != 1 || ids[0] != "two" {
		t.Fatalf("workspaces after removing one: %v", ids)
	}
	if preview, err := d.Preview("one", 1, 100); err != nil || len(preview) != 1 {
		t.Fatalf("removing the workspace lost the revision: %v %v", preview, err)
	}
}

func TestLinksAreNotFollowed(t *testing.T) {
	d, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret")
	write(t, outside, "not a draft")
	write(t, filepath.Join(d.Workspace("task"), "draft.md"), "draft")
	if err = os.Symlink(outside, filepath.Join(d.Workspace("task"), "link.md")); err != nil {
		t.Skip("symlinks unavailable")
	}
	files, _, err := d.Snapshot("task", 1)
	if err != nil || len(files) != 1 {
		t.Fatalf("a link was recorded: %v %v", files, err)
	}
}

func TestDeliveryNeverOverwritesAndSettlesRetries(t *testing.T) {
	d, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(d.Workspace("task"), "draft.md"), "final")
	if _, _, err = d.Snapshot("task", 2); err != nil {
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
