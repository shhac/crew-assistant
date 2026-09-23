package app

import (
	"context"
	"fmt"
	"strconv"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/engine"
	"github.com/shhac/crew-assistant/internal/media/localdocs"
)

// teamFrom builds a playbook from a template and the few choices the assistant
// may make about it. Anything left empty keeps the template's choice.
func teamFrom(in engine.SetTeamArgs) (core.Playbook, error) {
	template := in.Template
	if template == "" {
		template = "draft"
	}
	playbook, ok := core.Templates[template]
	if !ok {
		return core.Playbook{}, fmt.Errorf("unknown team template %q", template)
	}
	playbook.Roles = append([]core.Role(nil), playbook.Roles...)
	for i := range playbook.Roles {
		switch {
		case playbook.Roles[i].Kind == core.RoleImplementer && in.WriterEngine != "":
			playbook.Roles[i].Engine = in.WriterEngine
		case playbook.Roles[i].Kind == core.RoleReviewer && in.ReviewerEngine != "":
			playbook.Roles[i].Engine = in.ReviewerEngine
		}
	}
	if in.MaxRounds != "" {
		rounds, err := strconv.Atoi(in.MaxRounds)
		if err != nil {
			return core.Playbook{}, fmt.Errorf("max_rounds must be a number")
		}
		playbook.MaxRounds = rounds
	}
	playbook.DeliverTo = in.DeliverTo
	return playbook, playbook.Validate()
}

// SetTeam applies a team choice made in the dashboard or by the assistant.
func (a *App) SetTeam(ctx context.Context, in engine.SetTeamArgs) (core.Project, error) {
	playbook, err := teamFrom(in)
	if err != nil {
		return core.Project{}, err
	}
	return a.Core.SetPlaybook(ctx, in.ProjectID, playbook)
}

// RevisionPreview returns one revision's files for the owner to read.
func (a *App) RevisionPreview(ctx context.Context, projectID, taskID string, n int) ([]localdocs.File, error) {
	snap, err := a.Core.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	p, ok := findProject(snap, projectID)
	if !ok {
		return nil, core.ErrNotFound
	}
	for _, t := range snap.Tasks {
		if t.ID != taskID || t.ProjectID != projectID {
			continue
		}
		for _, r := range t.Revisions {
			if r.N == n {
				docs, err := localdocs.Open(p.ScratchDirectory)
				if err != nil {
					return nil, err
				}
				return docs.Preview(taskID, n, 256<<10)
			}
		}
	}
	return nil, core.ErrNotFound
}
