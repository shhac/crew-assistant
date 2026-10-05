package gitrepo

import (
	"context"
	"errors"
	"strings"
)

// MirrorBranch points a local branch of the project's clone at commit, for a
// tool that reads branches by name, such as g2g reading a stack of pull
// requests. The branch checked out there is never moved, and with keep a
// branch that already exists is left as it is.
func (r Repo) MirrorBranch(ctx context.Context, branch, commit string, keep bool) error {
	if err := validBranch(ctx, r.Workspace(), branch); err != nil {
		return err
	}
	if !r.Holds(ctx, commit) {
		return errNotRecorded
	}
	current, _ := run(ctx, r.Workspace(), "symbolic-ref", "--quiet", "--short", "HEAD")
	existing, err := run(ctx, r.Workspace(), "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	switch {
	case err == nil && (keep || strings.TrimSpace(existing) == commit):
		return nil
	case branch == strings.TrimSpace(current):
		return errors.New("the branch is checked out in the project's clone")
	}
	_, err = run(ctx, r.Workspace(), "update-ref", "-m", "crew-assistant stack", "refs/heads/"+branch, commit)
	return err
}

// DefaultBranch is the branch the clone's origin names as its default, or ""
// when it names none.
func (r Repo) DefaultBranch(ctx context.Context) string {
	out, err := run(ctx, r.Workspace(), "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.TrimSpace(out), "origin/")
}
