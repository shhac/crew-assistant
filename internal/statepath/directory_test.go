package statepath

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureDirectoryRefusesNamesThatLeaveTheRoot(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"", ".", "..", "a/b", `a\b`, "a:b"} {
		if _, err := EnsureDirectory(root, name); err == nil {
			t.Errorf("%q was accepted", name)
		}
	}
	if _, err := os.Lstat(filepath.Join(root, "a")); !os.IsNotExist(err) {
		t.Fatalf("a refused name created a directory: %v", err)
	}
}

func TestEnsureDirectoryRefusesARootItCannotTrust(t *testing.T) {
	if _, err := EnsureDirectory("relative/state", "runtime"); err == nil {
		t.Error("a relative root was accepted")
	}
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if _, err := EnsureDirectory(link, "runtime"); err == nil {
		t.Error("a symlinked root was accepted")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureDirectory(file, "runtime"); err == nil {
		t.Error("a file as root was accepted")
	}
}

func TestEnsureDirectoryRefusesAComponentThatIsNotADirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureDirectory(root, "file", "runtime"); err == nil {
		t.Error("a regular file was used as a directory")
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "link")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if _, err := EnsureDirectory(root, "link"); err == nil {
		t.Error("a symlinked component was followed")
	}
}

func TestEnsureDirectoryCreatesAndTightensPrivateDirectories(t *testing.T) {
	root := t.TempDir()
	loose := filepath.Join(root, "loose")
	if err := os.Mkdir(loose, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(loose, 0755); err != nil {
		t.Fatal(err)
	}
	got, err := EnsureDirectory(root, "loose", "fresh")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "loose", "fresh"); got != want {
		t.Fatalf("path %q, want %q", got, want)
	}
	for _, p := range []string{loose, got} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0700 {
			t.Errorf("%s has mode %o, want 700", p, mode)
		}
	}
}
