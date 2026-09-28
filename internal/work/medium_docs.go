package work

import (
	"context"
	"errors"
	"fmt"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/media"
	"github.com/shhac/crew-assistant/internal/media/localdocs"
)

// docsMedium is written work in the project's private folder, delivered by
// copying the approved draft to a folder the owner chose. Each task has a
// workspace of its own; a revision is a copy of it, named by its digest.
type docsMedium struct {
	docs      localdocs.Docs
	deliverTo string
}

func (m docsMedium) workspace(t core.Task) string { return m.docs.Workspace(t.ID) }
func (m docsMedium) env(core.Task) []string       { return nil }
func (m docsMedium) readable() []string           { return nil }
func (m docsMedium) begin(_ context.Context, t core.Task) (core.Task, error) {
	return t, nil
}
func (m docsMedium) reset(_ context.Context, t core.Task) error {
	return m.docs.Reset(t.ID, len(t.Revisions))
}
func (m docsMedium) snapshot(_ context.Context, t core.Task, n int) (core.Revision, error) {
	files, digest, err := m.docs.Snapshot(t.ID, n)
	return core.Revision{N: n, Files: files, Ref: digest}, err
}

// publish has nothing to do: the snapshot is already where revisions are
// kept.
func (m docsMedium) publish(context.Context, core.Task, string, string) error { return nil }

func (m docsMedium) published(_ context.Context, t core.Task, h core.Handoff) (string, bool, error) {
	at, err := m.docs.Digest(t.ID, h.Revision.N)
	return at, false, err
}

// check is a read-only copy of the revision of the checker's own. A reviewer
// reads it where it is; QA runs in a scratch folder beside it, and reads it.
// A draft has no app to run, so it is never given a writable copy.
func (m docsMedium) check(_ context.Context, t core.Task, r core.Revision, qa, _ bool) (checkout, error) {
	c, err := m.docs.Checkout(t.ID, r.N, r.Ref)
	if err != nil {
		return checkout{}, err
	}
	out := checkout{ref: r.Ref, workDir: c.Dir, verify: func(context.Context) error { return c.Verify() }, remove: c.Remove}
	if !qa {
		return out, nil
	}
	out.workDir, out.write, out.read = c.Scratch, true, []string{c.Dir}
	out.note = fmt.Sprintf("\n\nThe draft is at %s, read-only: read and check it there. Your working directory is not the draft but a scratch folder for anything the check writes.\n", c.Dir)
	return out, nil
}
func (m docsMedium) preview(_ context.Context, t core.Task, r core.Revision) ([]media.File, error) {
	return m.docs.Preview(t.ID, r.N, 256<<10)
}
func (m docsMedium) deliver(_ context.Context, t core.Task, r core.Revision) (string, error) {
	if m.deliverTo == "" {
		return "", nil
	}
	return m.docs.Deliver(t.ID, r.N, m.deliverTo, t.Objective)
}
func (m docsMedium) deliveryNote(core.Task) string {
	if m.deliverTo != "" {
		return "Approving copies it into " + m.deliverTo + "."
	}
	return "It stays on the project."
}

// tidy removes the workspaces of finished tasks. Their revisions stay, to
// be read and delivered.
func (m docsMedium) tidy(_ context.Context, tasks []core.Task, _ func(core.Task) bool, _ bool) error {
	finished := map[string]bool{}
	known := map[string]bool{}
	for _, t := range tasks {
		known[t.ID], finished[t.ID] = true, t.Finished()
	}
	ids, err := m.docs.Workspaces()
	errs := []error{err}
	for _, id := range ids {
		if finished[id] || !known[id] {
			errs = append(errs, m.docs.RemoveWorkspace(id))
		}
	}
	return errors.Join(errs...)
}

func (m docsMedium) removeChecks() error { return m.docs.RemoveChecks() }
