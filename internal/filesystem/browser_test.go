package filesystem

import (
	"context"
	"errors"
	"fmt"
	"github.com/shhac/crew-assistant/internal/testutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMetadataSelectionAndCanonicalPaths(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"z-dir", "a-dir", ".hidden"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"z.txt", "a.txt", ".secret"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("contents must never be returned"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	canonical, _ := filepath.EvalSymlinks(root)
	b := New(root)
	list, err := b.List(context.Background(), Query{Kind: "file"})
	if err != nil {
		t.Fatal(err)
	}
	if list.Path != canonical || len(list.Entries) != 4 || list.NextCursor != nil {
		t.Fatalf("unexpected listing: %+v", list)
	}
	for i, want := range []string{"a-dir", "z-dir", "a.txt", "z.txt"} {
		e := list.Entries[i]
		if e.Name != want || e.Path != filepath.Join(canonical, want) || e.Selectable != (i >= 2) {
			t.Fatalf("entry %d: %+v", i, e)
		}
	}
	list, err = b.List(context.Background(), Query{Path: root, Kind: "directory", Hidden: true})
	if err != nil || len(list.Entries) != 3 {
		t.Fatalf("hidden directories: %+v %v", list, err)
	}
	for _, e := range list.Entries {
		if e.Kind != "directory" || !e.Selectable {
			t.Fatal(e)
		}
	}
	for _, path := range []string{"relative", filepath.Join(root, "z.txt")} {
		if _, err = b.List(context.Background(), Query{Path: path}); !errors.Is(err, ErrPath) {
			t.Fatalf("accepted invalid directory %q: %v", path, err)
		}
	}
	if _, err = b.List(context.Background(), Query{Path: root, Kind: "invalid"}); !errors.Is(err, ErrKind) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = b.List(ctx, Query{Path: root}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestPaginationSnapshotAndExpiredCursor(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 511; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("item-%04d", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	b := New(root)
	now := time.Now()
	b.now = func() time.Time { return now }
	list, err := b.List(context.Background(), Query{Path: root, Kind: "any"})
	if err != nil || len(list.Entries) != pageSize || list.NextCursor == nil {
		t.Fatalf("first page: %+v %v", list, err)
	}
	cursor := *list.NextCursor
	// An insertion cannot reorder the existing pagination snapshot.
	if err = os.WriteFile(filepath.Join(root, "000-new"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	total := len(list.Entries)
	last := list.Entries[len(list.Entries)-1].Name
	for list.NextCursor != nil {
		list, err = b.List(context.Background(), Query{Path: list.Path, Kind: "any", Cursor: *list.NextCursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range list.Entries {
			if e.Name <= last {
				t.Fatalf("duplicate/reordered entry: %q <= %q", e.Name, last)
			}
			last = e.Name
		}
		total += len(list.Entries)
	}
	if total != 511 {
		t.Fatal(total)
	}
	if _, err = b.List(context.Background(), Query{Path: list.Path, Kind: "directory", Cursor: cursor}); !errors.Is(err, ErrCursor) {
		t.Fatalf("cursor changed filters: %v", err)
	}
	if _, err = b.List(context.Background(), Query{Path: list.Path, Cursor: strings.Replace(cursor, ":250", ":-1", 1)}); !errors.Is(err, ErrCursor) {
		t.Fatal(err)
	}
	now = now.Add(snapshotLifetime)
	if _, err = b.List(context.Background(), Query{Path: list.Path, Cursor: cursor}); !errors.Is(err, ErrCursor) {
		t.Fatal(err)
	}
	fresh, err := b.List(context.Background(), Query{Path: root})
	if err != nil || fresh.Entries[0].Name != "000-new" {
		t.Fatalf("refresh did not reread: %+v %v", fresh, err)
	}
}
func TestSymlinkNavigationDoesNotReadFileContent(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(root, "linked")); err != nil {
		testutil.RequireSymlinkResult(t, err)
	}
	b := New(root)
	list, err := b.List(context.Background(), Query{})
	if err != nil || len(list.Entries) != 1 || list.Entries[0].Kind != "directory" {
		t.Fatalf("symlink directory: %+v %v", list, err)
	}
	inside, err := b.List(context.Background(), Query{Path: list.Entries[0].Path})
	canonical, _ := filepath.EvalSymlinks(target)
	if err != nil || inside.Path != canonical || len(inside.Entries) != 0 {
		t.Fatalf("symlink target: %+v %v", inside, err)
	}
}
func TestSnapshotCacheBounded(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 251; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprint(i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	b := New(root)
	first, err := b.List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxSnapshots; i++ {
		if _, err = b.List(context.Background(), Query{}); err != nil {
			t.Fatal(err)
		}
	}
	if len(b.snapshots) != maxSnapshots {
		t.Fatal(len(b.snapshots))
	}
	if _, err = b.List(context.Background(), Query{Path: first.Path, Cursor: *first.NextCursor}); !errors.Is(err, ErrCursor) {
		t.Fatal(err)
	}
}
