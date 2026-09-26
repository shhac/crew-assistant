package work

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/text"
)

// TaskPlace is where a task's work is, for the owner to look at or change
// by hand.
type TaskPlace struct {
	ProjectID string `json:"project_id"`
	TaskID    string `json:"task_id"`
	Status    string `json:"status"`
	Stage     string `json:"stage"`
	// Workspace is the daemon's own working copy. Other tasks share it and
	// each step resets it, so it is for looking, not changing.
	Workspace string `json:"workspace"`
	// Repo, Branch, Base and From are a code task's: the owner's repository,
	// the task's branch, the commit it started from and the owner's branch
	// that was.
	Repo   string `json:"repo,omitempty"`
	Branch string `json:"branch,omitempty"`
	Base   string `json:"base,omitempty"`
	From   string `json:"from,omitempty"`
	// Draft is the latest draft, and Approved the one the owner approved.
	Draft    *core.Revision `json:"draft,omitempty"`
	Approved int            `json:"approved,omitempty"`
	// Running says a role is at work on it right now.
	Running bool `json:"running"`
}

// Place is where a task's work is.
func (lp *Loop) Place(ctx context.Context, projectID, taskID string) (TaskPlace, error) {
	t, p, m, err := lp.taskAt(ctx, projectID, taskID)
	if err != nil {
		return TaskPlace{}, err
	}
	place := TaskPlace{ProjectID: p.ID, TaskID: t.ID, Status: t.Status, Stage: t.Stage, Branch: t.Branch, Base: t.Base, From: t.From, Approved: t.Approved, Running: lp.running(t.ID), Workspace: m.workspace()}
	if n := len(t.Revisions); n > 0 {
		place.Draft = &t.Revisions[n-1]
	}
	if g, ok := m.(gitMedium); ok {
		place.Repo = g.playbook.Repo
	}
	return place, nil
}

// AdoptDraft takes the commit ref names in the owner's repository as the
// task's next draft, made by the owner; see core.AdoptDraft. The commit
// must build on where the task started. It holds the task from start to
// finish, and is refused while a step of the loop holds it, whose draft
// would build on the one this replaces.
func (lp *Loop) AdoptDraft(ctx context.Context, projectID, taskID, ref, note string, approve bool) (core.Task, error) {
	if !lp.claim(taskID) {
		return core.Task{}, fmt.Errorf("the team is at work on this task right now; adopt your change once its step ends: %w", core.ErrConflict)
	}
	defer lp.release(taskID)
	t, _, m, err := lp.taskAt(ctx, projectID, taskID)
	if err != nil {
		return core.Task{}, err
	}
	g, ok := m.(gitMedium)
	if !ok {
		return core.Task{}, errors.New("only a code task's draft can be changed by hand")
	}
	r, err := ownersRevision(ctx, g, t, ref, note)
	if err != nil {
		return core.Task{}, err
	}
	out, err := lp.Core.AdoptDraft(ctx, t.ID, r, approve)
	lp.nudgeUnless(err)
	return out, err
}

// ownersRevision is the owner's commit as the task's next draft: fetched
// into the clone, and built on where the task started.
func ownersRevision(ctx context.Context, g gitMedium, t core.Task, ref, note string) (core.Revision, error) {
	if strings.TrimSpace(ref) == "" {
		ref = t.Branch
	}
	commit, err := g.repo.Adopt(ctx, t.ID, ref)
	if err != nil {
		return core.Revision{}, err
	}
	built, err := g.repo.Contains(ctx, commit, t.Base)
	if err != nil {
		return core.Revision{}, err
	}
	if !built {
		return core.Revision{}, fmt.Errorf("%s isn't built on where “%s” started (%s)", ref, t.Objective, text.Short(t.Base))
	}
	files, err := g.repo.ChangedFiles(ctx, t.Base, commit)
	if err != nil {
		return core.Revision{}, err
	}
	summary := strings.TrimSpace(note)
	if summary == "" {
		summary, _ = g.repo.Subject(ctx, commit)
	}
	return core.Revision{Ref: commit, Files: files, Summary: summary}, nil
}

// taskAt is a task, its project and the medium its work is in.
func (lp *Loop) taskAt(ctx context.Context, projectID, taskID string) (core.Task, core.Project, medium, error) {
	snap, err := lp.Core.Snapshot(ctx)
	if err != nil {
		return core.Task{}, core.Project{}, nil, err
	}
	t, ok := findTask(snap, projectID, taskID)
	if !ok {
		return core.Task{}, core.Project{}, nil, core.ErrNotFound
	}
	p, ok := findProject(snap, projectID)
	if !ok {
		return core.Task{}, core.Project{}, nil, core.ErrNotFound
	}
	m, err := lp.mediumFor(ctx, p, taskPlaybook(p, t))
	return t, p, m, err
}

// running says a role is at work on the task right now.
func (lp *Loop) running(taskID string) bool {
	return lp.turns.has(taskID)
}
