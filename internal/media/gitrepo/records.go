package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// taskRefs is where the project's clone keeps each task's recorded
// revisions, one ref per revision.
const taskRefs = "refs/crew/tasks/"

// TaskRef names the ref a task's revision n is kept under in the project's
// clone. The attempt is part of the name, so two attempts at one revision
// never write the same ref, and a ref once written is never moved.
func TaskRef(taskID string, n, attempt int) string {
	return fmt.Sprintf("%s%s/r%d-a%d", taskRefs, taskID, n, attempt)
}

// ErrRefTaken is a revision's ref already pointing at another commit.
var ErrRefTaken = errors.New("the revision's ref already points at another commit")

// Publish hands commit to the project's clone under ref, fetching it from
// the task's clone at from when the project's clone lacks it. The ref is
// only ever created: one already at commit counts as done, and one anywhere
// else is refused. It reads the ref back before saying it is done.
func (r Repo) Publish(ctx context.Context, from Repo, commit, ref string) error {
	if _, err := run(ctx, r.Workspace(), "check-ref-format", ref); err != nil || !strings.HasPrefix(ref, taskRefs) {
		return fmt.Errorf("%q is not a revision's ref", ref)
	}
	at, err := r.RefAt(ctx, ref)
	if err != nil {
		return err
	}
	switch {
	case at == commit:
		return nil
	case at != "":
		return fmt.Errorf("%s: %w", ref, ErrRefTaken)
	}
	if !r.Holds(ctx, commit) {
		if _, err = run(ctx, r.Workspace(), append(fetchQuietly, "--no-write-fetch-head", from.Workspace(), commit)...); err != nil {
			return fmt.Errorf("the revision could not be fetched from the task's clone: %w", err)
		}
	}
	if _, err = run(ctx, r.Workspace(), "update-ref", "-m", "crew-assistant: revision", ref, commit, strings.Repeat("0", len(commit))); err != nil {
		return err
	}
	if at, err = r.RefAt(ctx, ref); err != nil || at != commit {
		return fmt.Errorf("%s does not hold the revision after publishing it: %w", ref, errors.Join(err, ErrRefTaken))
	}
	return nil
}

// RefAt is the commit ref points at, or "" when there is no such ref.
func (r Repo) RefAt(ctx context.Context, ref string) (string, error) {
	out, err := run(ctx, r.Workspace(), "for-each-ref", "--format=%(objectname)", ref)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// TaskRefs is every task revision's ref in the project's clone, with the
// commit each points at.
func (r Repo) TaskRefs(ctx context.Context) (map[string]string, error) {
	out, err := run(ctx, r.Workspace(), "for-each-ref", "--format=%(refname) %(objectname)", taskRefs)
	if err != nil {
		return nil, err
	}
	refs := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if name, commit, ok := strings.Cut(line, " "); ok {
			refs[name] = commit
		}
	}
	return refs, nil
}

// RefTask is the task a revision's ref belongs to.
func RefTask(ref string) string {
	id, _, _ := strings.Cut(strings.TrimPrefix(ref, taskRefs), "/")
	return id
}

// DropRef deletes a revision's ref, only while it still points at commit.
func (r Repo) DropRef(ctx context.Context, ref, commit string) error {
	if !strings.HasPrefix(ref, taskRefs) {
		return fmt.Errorf("%q is not a revision's ref", ref)
	}
	_, err := run(ctx, r.Workspace(), "update-ref", "-m", "crew-assistant: tidy", "-d", ref, commit)
	return err
}
