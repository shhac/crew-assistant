//go:build !windows

package gitrepo

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseTagsAndHistory(t *testing.T) {
	r, source, base, commit := delivery(t)
	git(t, source, "tag", "v1.0.0", base)
	if err := r.FetchVersionTags(ctx, "", nil); err != nil {
		t.Fatal(err)
	}
	tag, err := r.LatestVersionTag(ctx, commit)
	if err != nil || tag != "v1.0.0" {
		t.Fatal(tag, err)
	}
	subjects, count, err := r.CommitsSince(ctx, tag, commit)
	if err != nil || count != 1 || len(subjects) != 1 {
		t.Fatal(subjects, count, err)
	}
	if err = r.MakeTag(ctx, commit, "v1.1.0", "Feature notes\n\nDetails."); err != nil {
		t.Fatal(err)
	}
	object := git(t, r.Workspace(), "rev-parse", "refs/tags/v1.1.0")
	if git(t, r.Workspace(), "cat-file", "-t", object) != "tag" {
		t.Fatal("not annotated")
	}
	if !strings.Contains(git(t, r.Workspace(), "cat-file", "-p", object), "Feature notes\n\nDetails.") {
		t.Fatal("notes lost")
	}
	if err = r.MakeTag(ctx, commit, "v1.1.0", "other notes"); err != nil || git(t, r.Workspace(), "rev-parse", "refs/tags/v1.1.0") != object {
		t.Fatal("not idempotent", err)
	}
	if err = r.TagSource(ctx, "v1.1.0"); err != nil {
		t.Fatal(err)
	}
	if got, err := r.RemoteTag(ctx, source, "v1.1.0", nil); err != nil || got != commit {
		t.Fatal(got, err)
	}
	if err = r.TagSource(ctx, "v1.1.0"); err != nil {
		t.Fatal(err)
	}
	git(t, source, "tag", "-f", "v1.1.0", base)
	if err = r.TagSource(ctx, "v1.1.0"); !errors.Is(err, ErrVersionTaken) {
		t.Fatal(err)
	}
	if err = r.MakeTag(ctx, base, "v1.1.0", "taken"); !errors.Is(err, ErrVersionTaken) {
		t.Fatal(err)
	}
}
func TestReleasePushesOnlyFastForwardCheckedCommit(t *testing.T) {
	r, source, base, commit := delivery(t)
	remote := filepath.Join(t.TempDir(), "github.git")
	git(t, source, "clone", "--bare", source, remote)
	if err := r.PushReleaseBranch(ctx, remote, commit, "main", nil); err != nil {
		t.Fatal(err)
	}
	if git(t, remote, "rev-parse", "main") != commit {
		t.Fatal("wrong branch")
	}
	if err := r.PushReleaseBranch(ctx, remote, base, "main", nil); err != nil {
		t.Fatal("already contained", err)
	}
	if err := r.MakeTag(ctx, commit, "v1.1.0", "notes"); err != nil {
		t.Fatal(err)
	}
	if err := r.PushTag(ctx, remote, "v1.1.0", nil); err != nil {
		t.Fatal(err)
	}
	if at, err := r.RemoteTag(ctx, remote, "v1.1.0", nil); err != nil || at != commit {
		t.Fatal(at, err)
	}
	git(t, remote, "update-ref", "refs/heads/main", base)
	// A different child of the same base diverges from the checked commit.
	foreign := git(t, r.Workspace(), "commit-tree", base+"^{tree}", "-p", base, "-m", "foreign")
	git(t, remote, "fetch", r.Workspace(), foreign)
	git(t, remote, "update-ref", "refs/heads/main", foreign)
	if err := r.PushReleaseBranch(ctx, remote, commit, "main", nil); !errors.Is(err, ErrTargetMoved) {
		t.Fatal(err)
	}
	if git(t, remote, "rev-parse", "main") != foreign {
		t.Fatal("diverged branch changed")
	}
}

func TestReleaseAbandonedCloneTagCanBeReusedOnlyWhenUnpublished(t *testing.T) {
	r, source, base, commit := delivery(t)
	remote := filepath.Join(t.TempDir(), "github.git")
	git(t, source, "clone", "--bare", source, remote)
	if err := r.MakeTag(ctx, base, "v1.1.0", "abandoned"); err != nil {
		t.Fatal(err)
	}
	if err := r.DiscardUnpublishedTag(ctx, "v1.1.0", commit, remote, nil); err != nil {
		t.Fatal(err)
	}
	if err := r.MakeTag(ctx, commit, "v1.1.0", "new notes"); err != nil {
		t.Fatal(err)
	}
	if err := r.PushTag(ctx, remote, "v1.1.0", nil); err != nil {
		t.Fatal(err)
	}
	if err := r.DiscardUnpublishedTag(ctx, "v1.1.0", base, remote, nil); !errors.Is(err, ErrVersionTaken) {
		t.Fatal("published tag discarded", err)
	}
	if at, err := r.tagAt(ctx, "v1.1.0"); err != nil || at != commit {
		t.Fatal(at, err)
	}
}
func TestReleaseHighestVersionIncludesOffTargetTags(t *testing.T) {
	r, source, base, commit := delivery(t)
	git(t, source, "tag", "v1.0.0", base)
	git(t, source, "checkout", "-qb", "side", base)
	git(t, source, "commit", "--allow-empty", "-qm", "side commit")
	git(t, source, "tag", "v2.0.0")
	if err := r.FetchVersionTags(ctx, "", nil); err != nil {
		t.Fatal(err)
	}
	if got, err := r.LatestVersionTag(ctx, commit); err != nil || got != "v1.0.0" {
		t.Fatal(got, err)
	}
	if got, err := r.LatestVersionTag(ctx, ""); err != nil || got != "v2.0.0" {
		t.Fatal(got, err)
	}
}

func TestReleaseTagFetchesPruneDeletedVersions(t *testing.T) {
	r, source, base, commit := delivery(t)
	git(t, source, "tag", "v1.0.0", base)
	git(t, source, "tag", "v2.0.0", base)
	if err := r.MakeTag(ctx, commit, "v3.0.0", "unpublished annotation"); err != nil {
		t.Fatal(err)
	}
	for _, remote := range []bool{false, true} {
		t.Run(fmt.Sprintf("remote=%t", remote), func(t *testing.T) {
			git(t, source, "tag", "-f", "v2.0.0", base)
			fetch := func() (string, error) {
				if remote {
					return r.HighestRemoteVersion(ctx, source, nil)
				}
				if err := r.FetchVersionTags(ctx, "", nil); err != nil {
					return "", err
				}
				return r.LatestVersionTag(ctx, commit)
			}
			if got, err := fetch(); err != nil || got != "v2.0.0" {
				t.Fatal(got, err)
			}
			git(t, source, "tag", "-d", "v2.0.0")
			if got, err := fetch(); err != nil || got != "v1.0.0" {
				t.Fatal("deleted tag survived", got, err)
			}
			if got, err := r.tagAt(ctx, "v3.0.0"); err != nil || got != commit {
				t.Fatal("private pruning removed unpublished annotation", got, err)
			}
		})
	}
	if got, err := r.LatestVersionTag(ctx, ""); err != nil || got != "v1.0.0" {
		t.Fatal("deleted tag counts toward highest", got, err)
	}
}
