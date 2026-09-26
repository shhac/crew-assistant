package work

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/shhac/crew-assistant/internal/core"
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
	snap, err := lp.Core.Snapshot(ctx)
	if err != nil {
		return TaskPlace{}, err
	}
	t, ok := findTask(snap, projectID, taskID)
	if !ok {
		return TaskPlace{}, core.ErrNotFound
	}
	p, ok := findProject(snap, projectID)
	if !ok {
		return TaskPlace{}, core.ErrNotFound
	}
	place := TaskPlace{ProjectID: p.ID, TaskID: t.ID, Status: t.Status, Stage: t.Stage, Branch: t.Branch, Base: t.Base, From: t.From, Approved: t.Approved, Running: lp.running(t.ID)}
	if n := len(t.Revisions); n > 0 {
		place.Draft = &t.Revisions[n-1]
	}
	playbook := taskPlaybook(p, t)
	m, err := lp.mediumFor(ctx, p, playbook)
	if err != nil {
		return TaskPlace{}, err
	}
	place.Workspace = m.workspace()
	if playbook != nil && playbook.Medium == core.MediumGit {
		place.Repo = playbook.Repo
	}
	return place, nil
}

// AdoptDraft takes the commit ref names in the owner's repository as the
// task's next draft, made by the owner; see core.AdoptDraft. The commit
// must build on where the task started. It is refused while a role is at
// work on the task, whose next draft would replace it.
func (lp *Loop) AdoptDraft(ctx context.Context, projectID, taskID, ref, note string, approve bool) (core.Task, error) {
	snap, err := lp.Core.Snapshot(ctx)
	if err != nil {
		return core.Task{}, err
	}
	t, ok := findTask(snap, projectID, taskID)
	if !ok {
		return core.Task{}, core.ErrNotFound
	}
	p, ok := findProject(snap, projectID)
	if !ok {
		return core.Task{}, core.ErrNotFound
	}
	if lp.running(t.ID) {
		return core.Task{}, fmt.Errorf("a role is at work on “%s” right now; adopt your change once its turn ends: %w", t.Objective, core.ErrConflict)
	}
	m, err := lp.mediumFor(ctx, p, taskPlaybook(p, t))
	if err != nil {
		return core.Task{}, err
	}
	g, ok := m.(gitMedium)
	if !ok {
		return core.Task{}, errors.New("only a code task's draft can be changed by hand")
	}
	if strings.TrimSpace(ref) == "" {
		ref = t.Branch
	}
	commit, err := g.repo.Adopt(ctx, ref)
	if err != nil {
		return core.Task{}, err
	}
	if built, err := g.repo.Contains(ctx, commit, t.Base); err != nil || !built {
		return core.Task{}, errors.Join(fmt.Errorf("%s isn't built on where “%s” started (%s)", ref, t.Objective, short(t.Base)), err)
	}
	files, err := g.repo.ChangedFiles(ctx, t.Base, commit)
	if err != nil {
		return core.Task{}, err
	}
	summary := strings.TrimSpace(note)
	if summary == "" {
		summary, _ = g.repo.Subject(ctx, commit)
	}
	out, err := lp.Core.AdoptDraft(ctx, t.ID, core.Revision{Ref: commit, Files: files, Summary: summary}, approve)
	lp.nudgeUnless(err)
	return out, err
}

func short(commit string) string { return commit[:min(len(commit), 7)] }

// running says a role is at work on the task right now.
func (lp *Loop) running(taskID string) bool {
	for _, t := range lp.Turns() {
		if t.TaskID == taskID {
			return true
		}
	}
	return false
}
