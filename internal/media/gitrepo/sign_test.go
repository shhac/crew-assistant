//go:build !windows

package gitrepo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSigner stands in for gpg: it signs anything and notes the key it was
// asked for.
func fakeSigner(t *testing.T) (program, calls string) {
	t.Helper()
	dir := t.TempDir()
	program, calls = filepath.Join(dir, "gpg"), filepath.Join(dir, "calls")
	write(t, program, "#!/bin/sh\necho \"$@\" >> "+calls+"\ncat > /dev/null\necho '[GNUPG:] SIG_CREATED D 1 8 00 1 KEY' >&2\nprintf -- '-----BEGIN PGP SIGNATURE-----\\nfake\\n-----END PGP SIGNATURE-----\\n'\n")
	if err := os.Chmod(program, 0700); err != nil {
		t.Fatal(err)
	}
	return program, calls
}

func signed(t *testing.T, r Repo, commit string) bool {
	t.Helper()
	return strings.Contains(git(t, r.Workspace(), "cat-file", "commit", commit), "\ngpgsig ")
}

func TestCommitsAreMadeAsTheOwnerIsForTheirFolder(t *testing.T) {
	source := ownerRepo(t)
	home := t.TempDir()
	personal := filepath.Join(home, "personal")
	write(t, personal, "[user]\n\tname = Owner\n\temail = owner@personal.test\n")
	folder, err := filepath.EvalSymlinks(filepath.Dir(source))
	if err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(home, "gitconfig")
	// A work identity everywhere, and a personal one for the folders the
	// source sits in, as an includeIf for a directory gives it.
	write(t, global, "[user]\n\tname = Owner at Work\n\temail = owner@work.test\n[includeIf \"gitdir:"+folder+"/\"]\n\tpath = "+personal+"\n")
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	r, err := Open(ctx, t.TempDir(), source, nil, SignNever)
	if err != nil {
		t.Fatal(err)
	}
	base, _, _ := r.Begin(ctx, "crew-task/a", "")
	write(t, filepath.Join(r.Workspace(), "a.go"), "package main\n")
	a, _, err := r.Snapshot(ctx, base, base, "a")
	if err != nil {
		t.Fatal(err)
	}
	if who := git(t, r.Workspace(), "log", "-1", "--format=%an <%ae> %cn <%ce>", a); who != "Owner <owner@personal.test> Owner <owner@personal.test>" {
		t.Fatalf("committed as %s", who)
	}

	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	write(t, filepath.Join(r.Workspace(), "a.go"), "package main // 2\n")
	b, _, _ := r.Snapshot(ctx, base, a, "b")
	if who := git(t, r.Workspace(), "log", "-1", "--format=%ae", b); who != authorKey {
		t.Fatalf("without an identity of the owner's, committed as %s", who)
	}
}

func TestCommitsAreSignedAsTheOwnersGitConfigSaysUnlessTheProjectDecides(t *testing.T) {
	source := ownerRepo(t)
	program, calls := fakeSigner(t)
	global := filepath.Join(t.TempDir(), "gitconfig")
	write(t, global, "[user]\n\tname = Owner\n\temail = owner@example.test\n[commit]\n\tgpgsign = true\n[gpg]\n\tprogram = "+program+"\n")
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	project := t.TempDir()
	r, err := Open(ctx, project, source, nil, SignAsOwner)
	if err != nil {
		t.Fatal(err)
	}
	base, _, _ := r.Begin(ctx, "crew-task/a", "")
	write(t, filepath.Join(r.Workspace(), "a.go"), "package main\n")
	a, _, err := r.Snapshot(ctx, base, base, "a")
	if err != nil || !signed(t, r, a) {
		t.Fatalf("a revision was not signed as the owner's config asks: %v", err)
	}
	if got, _ := os.ReadFile(calls); !strings.Contains(string(got), "Owner <owner@example.test>") {
		t.Fatalf("signed with a key other than the owner's: %s", got)
	}
	r.Begin(ctx, "crew-task/b", "")
	write(t, filepath.Join(r.Workspace(), "b.go"), "package main\n")
	b, _, _ := r.Snapshot(ctx, base, base, "b")
	if merged, err := r.MergeClean(ctx, b, a, "catch up"); err != nil || !signed(t, r, merged) {
		t.Fatalf("a catch-up merge was not signed: %v", err)
	}

	// The repository's own config has the last word, as it does for the owner.
	git(t, source, "config", "commit.gpgsign", "false")
	write(t, filepath.Join(r.Workspace(), "b.go"), "package main // 2\n")
	b2, _, _ := r.Snapshot(ctx, base, b, "b2")
	if signed(t, r, b2) {
		t.Fatal("signed although the repository's config turns signing off")
	}
	// A project that chose for itself overrides both.
	always, _ := Open(ctx, project, source, nil, SignAlways)
	write(t, filepath.Join(r.Workspace(), "b.go"), "package main // 3\n")
	if b3, _, _ := always.Snapshot(ctx, base, b2, "b3"); !signed(t, r, b3) {
		t.Fatal("a project that always signs made an unsigned commit")
	}
	git(t, source, "config", "commit.gpgsign", "true")
	never, _ := Open(ctx, project, source, nil, SignNever)
	write(t, filepath.Join(r.Workspace(), "b.go"), "package main // 4\n")
	b4, _, _ := never.Snapshot(ctx, base, b2, "b4")
	if signed(t, r, b4) {
		t.Fatal("a project that never signs made a signed commit")
	}

	git(t, source, "config", "gpg.program", "false")
	write(t, filepath.Join(r.Workspace(), "b.go"), "package main // 5\n")
	if _, _, err = r.Snapshot(ctx, base, b4, "b5"); err == nil || !strings.Contains(err.Error(), "signing") {
		t.Fatalf("a failed signature should say signing is why: %v", err)
	}
}
