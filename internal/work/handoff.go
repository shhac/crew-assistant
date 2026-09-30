package work

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/diagnostics"
	"github.com/shhac/crew-assistant/internal/media/gitrepo"
)

// handOff records a revision in two phases, since it changes git as well as
// the record. The intent is stored first (prepare), then the snapshot is
// published under a ref of its own that is only ever created (publish), and
// only once that is verified is the revision appended (commit). A daemon
// stopped at any point leaves a state resume can finish or undo, so a
// revision is neither lost nor recorded twice.
func (lp *Loop) handOff(ctx context.Context, t core.Task, m medium, h core.Handoff) error {
	t, err := lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
		// A draft recorded meanwhile, such as the owner's by hand, is newer
		// than the one this built on, and this one never replaces it.
		if len(t.Revisions) != h.Revision.N-1 {
			return "", fmt.Errorf("draft %d was recorded while the implementer worked: %w", len(t.Revisions), core.ErrConflict)
		}
		t.Attempt++
		h.Name = gitrepo.TaskRef(t.ID, h.Revision.N, t.Attempt)
		t.Handoff = &h
		return "", nil
	})
	if err != nil {
		return err
	}
	if err = m.publish(ctx, t, h.Revision.Ref, h.Name); err != nil {
		if dropErr := lp.dropHandoff(ctx, t.ID, h.Name); dropErr != nil {
			return dropErr
		}
		return lp.roleFailed(ctx, t, "The workspace", fmt.Errorf("the work could not be recorded: %w", err))
	}
	return lp.commitHandoff(ctx, t.ID, h.Name)
}

// commitHandoff appends the revision a handoff under name carries, once its
// ref holds it, and clears the handoff in the same change.
func (lp *Loop) commitHandoff(ctx context.Context, taskID, name string) error {
	var conflict error
	_, err := lp.updateOpen(ctx, taskID, func(t *core.Task, p *core.Project) (string, error) {
		h := t.Handoff
		if h == nil || h.Name != name {
			return "", nil
		}
		t.Handoff = nil
		if len(t.Revisions) != h.Revision.N-1 {
			conflict = fmt.Errorf("draft %d was recorded while the implementer worked: %w", len(t.Revisions), core.ErrConflict)
			return "", nil
		}
		return applyHandoff(t, p, *h), nil
	})
	return errors.Join(err, conflict)
}

// dropHandoff forgets a handoff that will not be finished; its round runs
// again, and its ref, if written, goes when the loop next tidies.
func (lp *Loop) dropHandoff(ctx context.Context, taskID, name string) error {
	_, err := lp.updateOpen(ctx, taskID, func(t *core.Task, _ *core.Project) (string, error) {
		if t.Handoff != nil && t.Handoff.Name == name {
			t.Handoff = nil
		}
		return "", nil
	})
	return err
}

// applyHandoff appends a handoff's revision and whatever else the turn that
// made it changes on the task, and says what happened.
func applyHandoff(t *core.Task, p *core.Project, h core.Handoff) string {
	r := h.Revision
	r.BriefVersion, r.At = p.Brief.Version, time.Now().UTC()
	if c := h.CatchUp; c != nil {
		prev := t.Revisions[len(t.Revisions)-1]
		// Someone else's commits are new work: nothing carries over from them.
		if c.Carry {
			reviewers := map[string]bool{}
			for _, role := range t.RolesOf(core.RoleReviewer) {
				reviewers[t.CheckerGroup(role.Name)] = true
			}
			t.Base, t.From = c.Base, c.From
			r.CleanMergeOf = prev.N
			t.Verdicts = append(t.Verdicts, carriedOver(t.Verdicts, prev.N, r.N, t.CheckerGroup, reviewers, p.Brief.Version, r.At)...)
		}
		t.Revisions = append(t.Revisions, r)
		t.DecisionID, t.Failures, t.RetryAt = "", 0, time.Time{}
		t.Status, t.Detail = core.TaskReviewing, c.Detail
		return fmt.Sprintf("%s caught up cleanly: %s", t.Objective, c.What)
	}
	t.Revisions = append(t.Revisions, r)
	t.AnswerDirection(h.Seen, r.N, h.Reply, r.At)
	t.WakeErrors = h.WakeErrors
	t.Unreachable = h.Unreachable
	if t.WriterRequest == h.Request {
		t.WriterNext = ""
	}
	if h.Seat != nil {
		t.KeepThread(core.RoleImplementer, *h.Seat, h.Session)
	}
	t.Failures, t.RetryAt = 0, time.Time{}
	t.Status, t.Detail = core.TaskReviewing, ""
	return fmt.Sprintf("%s finished version %d of %s", h.Writer, r.N, t.Objective)
}

// resume settles what a daemon stopped mid-step left behind, before any step
// runs. Turns it left running are ended first, where the harness can prove
// they are its own; a task whose turn can't be confirmed ended stays held.
// Then handoffs are finished or undone, the steps it claimed are cleared on
// to fresh attempts, so nothing a late turn reports is recorded, the copies
// checks were given are deleted, and so is anything kept for tasks that
// have settled, as are images a designer generated and never attached. A
// step not finished is claimed again as usual.
func (lp *Loop) resume(ctx context.Context) error {
	snap, err := lp.Core.Snapshot(ctx)
	if err != nil {
		return err
	}
	held, running := lp.reclaimTurns(ctx, snap)
	// Generated images are kept only while the turn that made them runs, and
	// only its tools, which stopped with the daemon, could attach them: any
	// left were left by a turn that never finished. Which session made which
	// is not known, so while any turn may still be running they all stay,
	// until a restart confirms every turn ended.
	if !running {
		os.RemoveAll(lp.generatedRoot())
	}
	var errs []error
	for _, t := range snap.Tasks {
		if t.Handoff == nil || t.Finished() || heldTask(t, held) {
			continue
		}
		errs = append(errs, lp.resumeHandoff(ctx, snap, t))
	}
	errs = append(errs, lp.Core.RecoverClaims(ctx, held))
	for _, p := range snap.Projects {
		if m, ok := lp.keptMedium(ctx, p); ok {
			errs = append(errs, m.removeChecks())
		}
	}
	return errors.Join(append(errs, lp.tidy(ctx, true))...)
}

// keptMedium is the project's medium, when it already keeps anything: a code
// project not yet cloned has nothing to tidy, and is not cloned for it.
func (lp *Loop) keptMedium(ctx context.Context, p core.Project) (medium, bool) {
	if p.Playbook == nil || p.ScratchDirectory == "" {
		return nil, false
	}
	if p.Playbook.Medium == core.MediumGit && !gitrepo.Cloned(p.ScratchDirectory) {
		return nil, false
	}
	m, err := lp.mediumFor(ctx, p, p.Playbook)
	return m, err == nil
}

// resumeHandoff finishes a handoff whose ref holds its snapshot, publishes
// one whose snapshot is still there first, and otherwise drops it, so its
// round runs again.
func (lp *Loop) resumeHandoff(ctx context.Context, snap core.Snapshot, t core.Task) error {
	h := *t.Handoff
	p, ok := findProject(snap, t.ProjectID)
	if !ok {
		return core.ErrNotFound
	}
	m, err := lp.mediumFor(ctx, p, taskPlaybook(p, t))
	if err != nil {
		return err
	}
	at, kept, err := m.published(ctx, t, h)
	if err != nil {
		return err
	}
	switch {
	case at == h.Revision.Ref:
		return lp.commitHandoff(ctx, t.ID, h.Name)
	case at == "" && kept:
		if err := m.publish(ctx, t, h.Revision.Ref, h.Name); err != nil {
			return errors.Join(err, lp.dropHandoff(ctx, t.ID, h.Name))
		}
		return lp.commitHandoff(ctx, t.ID, h.Name)
	case at != "":
		// Refs are named by attempt and never moved, so only something
		// outside the daemon could have done this.
		lp.Diagnostics.Failure(diagnostics.Event{Component: "daemon", Stage: "task_handoff", ProjectID: t.ProjectID}, fmt.Errorf("%s points at %s, not the revision handed over, %s", h.Name, at, h.Revision.Ref))
	}
	return lp.dropHandoff(ctx, t.ID, h.Name)
}

// tidy removes, project by project, what is kept only for tasks that have
// finished: their workspaces, and the refs of their revisions once no
// decision is open on them. strays is for resuming, when nothing else runs:
// what handoffs that never finished left goes too.
func (lp *Loop) tidy(ctx context.Context, strays bool) error {
	snap, err := lp.Core.Snapshot(ctx)
	if err != nil {
		return err
	}
	open := map[string]bool{}
	for _, d := range snap.Decisions {
		if d.Status == core.DecisionOpen {
			open[d.TaskID] = true
		}
	}
	settled := func(t core.Task) bool { return t.Finished() && !open[t.ID] }
	var errs []error
	for _, p := range snap.Projects {
		m, ok := lp.keptMedium(ctx, p)
		if !ok {
			continue
		}
		var tasks []core.Task
		for _, t := range snap.Tasks {
			if t.ProjectID == p.ID {
				tasks = append(tasks, t)
			}
		}
		errs = append(errs, m.tidy(ctx, tasks, settled, strays))
	}
	return errors.Join(errs...)
}
