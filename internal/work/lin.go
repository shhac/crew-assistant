package work

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/integrations/connections"
)

type linBinding struct {
	id, profile string
	writes      bool
}

func (lp *Loop) linBinding(projectID string) *linBinding {
	if lp.Config == nil {
		return nil
	}
	snap, err := lp.Core.Snapshot(context.Background())
	if err != nil {
		return nil
	}
	for _, p := range snap.Projects {
		if p.ID == projectID && p.Linear != nil {
			l := p.Linear
			bindings := lp.Config().Connections
			if core.LinearBinding(bindings, l.ConnectionID, l.Profile) != nil {
				return nil
			}
			for _, b := range bindings {
				if b.ID == l.ConnectionID {
					return &linBinding{id: b.ID, profile: l.Profile, writes: b.AllowWrites}
				}
			}
		}
	}
	return nil
}
func (r roleTools) callLin(ctx context.Context, raw json.RawMessage) (string, error) {
	if r.lin == nil {
		return "", errors.New("this turn has no lin tool")
	}
	if r.lp.Demo {
		return "", errors.New("demo mode does not query external accounts")
	}
	current := r.lp.linBinding(r.projectID)
	if current == nil || current.id != r.lin.id || current.profile != r.lin.profile {
		return "", errors.New("this project is no longer linked to Linear")
	}
	var in struct {
		Args      []string `json:"args"`
		Reference string   `json:"reference"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return "", errors.New("invalid lin arguments")
	}
	call := connections.LinCall{ConnectionID: current.id, Profile: current.profile, Args: in.Args, Reference: in.Reference, Writes: current.writes && !r.notesOnly}
	if in.Reference == "" {
		if _, err := connections.LinValidate(in.Args, call.Writes); err != nil {
			return "", err
		}
	}
	out, err := r.lp.linear.LinRecorded(ctx, r.lp.Core, r.lp.Config().Connections, call, r.projectID, r.name)
	if err != nil {
		return "", err
	}
	data, _ := json.Marshal(out)
	return string(data), nil
}
