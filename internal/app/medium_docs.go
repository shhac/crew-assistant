package app

import (
	"context"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/media"
	"github.com/shhac/crew-assistant/internal/media/localdocs"
)

// docsMedium is written work in the project's private folder, delivered by
// copying the approved draft to a folder the owner chose.
type docsMedium struct {
	docs      localdocs.Docs
	deliverTo string
}

func (m docsMedium) workspace() string  { return m.docs.Workspace() }
func (m docsMedium) env() []string      { return nil }
func (m docsMedium) readable() []string { return nil }
func (m docsMedium) begin(_ context.Context, t core.Task) (core.Task, error) {
	return t, nil
}
func (m docsMedium) reset(_ context.Context, t core.Task) error {
	return m.docs.Reset(t.ID, len(t.Revisions))
}
func (m docsMedium) snapshot(_ context.Context, t core.Task, n int) (core.Revision, error) {
	files, err := m.docs.Snapshot(t.ID, n)
	return core.Revision{N: n, Files: files}, err
}
func (m docsMedium) checkDir(_ context.Context, t core.Task, r core.Revision) (string, func(), error) {
	return m.docs.ReviewCopy(t.ID, r.N)
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
	return "It stays with the project, ready to read on its page."
}
