//go:build !windows

package gitrepo

import (
	"errors"
	"path/filepath"
	"testing"
)

// A version tag is recovered only when the remote holds it at the very commit
// being released; a tag anywhere else means the version is taken.
func TestRecoverReleaseTag(t *testing.T) {
	r, _, base, commit := delivery(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	git(t, r.Workspace(), "clone", "-q", "--bare", r.Workspace(), remote)
	local := func(version string) string {
		t.Helper()
		got, err := r.tagAt(ctx, version)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	if err := r.RecoverReleaseTag(ctx, remote, "v1.0.0", commit, nil); err != nil || local("v1.0.0") != "" {
		t.Fatalf("an absent remote tag: %v, local %q", err, local("v1.0.0"))
	}

	git(t, remote, "tag", "-a", "-m", "notes", "v2.0.0", commit)
	if err := r.RecoverReleaseTag(ctx, remote, "v2.0.0", commit, nil); err != nil || local("v2.0.0") != commit {
		t.Fatalf("recovering the remote annotation: %v, local %q", err, local("v2.0.0"))
	}
	if git(t, r.Workspace(), "cat-file", "-t", "refs/tags/v2.0.0") != "tag" {
		t.Fatal("the recovered tag lost its annotation")
	}
	if err := r.RecoverReleaseTag(ctx, remote, "v2.0.0", commit, nil); err != nil {
		t.Fatalf("recovering again: %v", err)
	}

	git(t, remote, "tag", "v3.0.0", base)
	if err := r.RecoverReleaseTag(ctx, remote, "v3.0.0", commit, nil); !errors.Is(err, ErrVersionTaken) || local("v3.0.0") != "" {
		t.Fatalf("a remote tag at another commit: %v, local %q", err, local("v3.0.0"))
	}

	git(t, remote, "tag", "v4.0.0", commit)
	git(t, r.Workspace(), "tag", "v4.0.0", base)
	if err := r.RecoverReleaseTag(ctx, remote, "v4.0.0", commit, nil); !errors.Is(err, ErrVersionTaken) || local("v4.0.0") != base {
		t.Fatalf("a local tag at another commit: %v, local %q", err, local("v4.0.0"))
	}
}

// A build includes a task's work when a commit in the build carries the
// task's marker line exactly, or when the task's revision is an ancestor.
func TestBuildIncludes(t *testing.T) {
	source := ownerRepo(t)
	start := git(t, source, "rev-parse", "HEAD")
	marker := "Crew-Task: abc123"

	git(t, source, "checkout", "-q", "-b", "side")
	write(t, filepath.Join(source, "side.txt"), "side\n")
	git(t, source, "add", "side.txt")
	git(t, source, "commit", "-q", "-m", "side work\n\nCrew-Task: side999")
	side := git(t, source, "rev-parse", "HEAD")

	git(t, source, "checkout", "-q", "main")
	write(t, filepath.Join(source, "squashed.txt"), "squashed\n")
	git(t, source, "add", "squashed.txt")
	git(t, source, "commit", "-q", "-m", "Land the feature\n\n"+marker)
	write(t, filepath.Join(source, "near.txt"), "near\n")
	git(t, source, "add", "near.txt")
	git(t, source, "commit", "-q", "-m", "Mention it\n\nSee Crew-Task: abc123 for context")
	build := git(t, source, "rev-parse", "HEAD")

	for _, tc := range []struct {
		name, revision, marker string
		want                   bool
	}{
		{"squashed landing found by its marker", side, marker, true},
		{"preserved landing found by ancestry", start, "Crew-Task: none", true},
		{"neither marker nor ancestry", side, "Crew-Task: none", false},
		{"a marker only on another branch", side, "Crew-Task: side999", false},
		{"a marker only quoted inside a line", side, "Crew-Task: abc12", false},
	} {
		got, err := BuildIncludes(ctx, source, build, tc.revision, tc.marker)
		if err != nil || got != tc.want {
			t.Errorf("%s: %v %v", tc.name, got, err)
		}
	}
}
