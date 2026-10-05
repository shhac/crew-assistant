//go:build !windows

package gitrepo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPatternsMatchFolderNames(t *testing.T) {
	for _, c := range []struct {
		pattern, path string
		want          bool
	}{
		{"**/node_modules", "node_modules", true},
		{"**/node_modules", "services/booking/node_modules", true},
		{"**/node_modules", "services/node_modules_old", false},
		{"packages/*/dist", "packages/ui/dist", true},
		{"packages/*/dist", "packages/ui/src/dist", false},
		{".yarn/install-state.g?", ".yarn/install-state.gz", true},
		{"**", "anything/at/all", true},
	} {
		got := matchPath(strings.Split(c.pattern, "/"), strings.Split(c.path, "/"))
		if got != c.want {
			t.Errorf("%q against %q: %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

// A pattern brings in every ignored folder it matches, nested ones too,
// and never the owner's tracked or uncommitted files.
func TestAPreparePatternCopiesEveryIgnoredMatch(t *testing.T) {
	source := ownerRepo(t)
	write(t, filepath.Join(source, ".gitignore"), "node_modules/\nsecret.env\n")
	git(t, source, "commit", "-q", "-am", "ignore more")
	write(t, filepath.Join(source, "services", "a", "node_modules", "x", "index.js"), "1\n")
	write(t, filepath.Join(source, "services", "a", "node_modules", "x", "node_modules", "y", "index.js"), "2\n")
	write(t, filepath.Join(source, "node_modules_list.txt"), "the owner's own note\n")
	write(t, filepath.Join(source, "secret.env"), "TOKEN=1\n")
	r, err := Open(ctx, t.TempDir(), source, []string{"**/node_modules"}, SignAsOwner)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"node_modules/dep/index.js", "services/a/node_modules/x/index.js", "services/a/node_modules/x/node_modules/y/index.js"} {
		if _, err := os.Stat(filepath.Join(r.Workspace(), want)); err != nil {
			t.Errorf("%s wasn't copied: %v", want, err)
		}
	}
	for _, never := range []string{"secret.env", "node_modules_list.txt", "scratch.txt"} {
		if _, err := os.Stat(filepath.Join(r.Workspace(), never)); err == nil {
			t.Errorf("%s was copied", never)
		}
	}
	base, _, err := r.Start(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	c, err := r.Checkout(ctx, base, "", false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Remove()
	if _, err := os.Stat(filepath.Join(c.Dir, "services", "a", "node_modules", "x", "index.js")); err != nil {
		t.Fatalf("the check's checkout lacks the matched folder: %v", err)
	}
	if err := c.Verify(ctx); err != nil {
		t.Fatalf("matched folders count as changes to the revision: %v", err)
	}
}

// Removing a checkout returns at once, and what it held is deleted
// behind it, locked revision and copied dependencies alike.
func TestARemovedCheckoutIsGoneAtOnceAndDeletedBehind(t *testing.T) {
	source := ownerRepo(t)
	r, err := Open(ctx, t.TempDir(), source, []string{"**/node_modules"}, SignAsOwner)
	if err != nil {
		t.Fatal(err)
	}
	base, _, err := r.Start(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	c, err := r.Checkout(ctx, base, "", true)
	if err != nil {
		t.Fatal(err)
	}
	copied, err := r.CopyForCheck(c.Dir)
	if err != nil {
		t.Fatal(err)
	}
	c.Remove()
	copied.Remove()
	for _, gone := range []string{c.Dir, copied.Dir} {
		if _, err := os.Stat(gone); err == nil {
			t.Fatalf("%s is still in place", gone)
		}
	}
	trash := filepath.Join(r.root, trashDir)
	for range 100 {
		if entries, _ := os.ReadDir(trash); len(entries) == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the removed checkouts were never deleted")
}
