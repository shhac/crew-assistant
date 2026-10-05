package work

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

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

// currentLin is the project's Linear account as it is now, which must still
// be the one the turn started with.
func (r roleTools) currentLin() (*linBinding, error) {
	if r.lin == nil {
		return nil, errors.New("this project is not linked to Linear in this turn")
	}
	if r.lp.Demo {
		return nil, errors.New("demo mode does not query external accounts")
	}
	current := r.lp.linBinding(r.projectID)
	if current == nil || current.id != r.lin.id || current.profile != r.lin.profile {
		return nil, errors.New("this project is no longer linked to Linear")
	}
	return current, nil
}

// linearRef reads a Linear issue or project for the PM to link, through the
// project's account and with the checks the owner's links get.
func (r roleTools) linearRef(ctx context.Context, kind, input string) (core.LinearRef, error) {
	current, err := r.currentLin()
	if err != nil {
		return core.LinearRef{}, err
	}
	session, err := r.lp.linear.LinearAccount(ctx, r.lp.Config().Connections, current.id, current.profile)
	if err != nil {
		return core.LinearRef{}, err
	}
	return session.LinearLinkRef(ctx, kind, strings.TrimSpace(input))
}

func (r roleTools) callLin(ctx context.Context, raw json.RawMessage) (string, error) {
	current, err := r.currentLin()
	if err != nil {
		return "", err
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
