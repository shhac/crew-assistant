package work

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/shhac/crew-assistant/internal/core"
)

// The team controls below change a team by its own id, for every project it
// staffs, with the same rules the project-level controls use on a project's
// team. Requests under way keep the team they started with.

// SetTeamSeat gives one kind of role on a team to a member, or with no
// member back to the template's seat for it; see SetSeat.
func (lp *Loop) SetTeamSeat(ctx context.Context, teamID, kind, memberID string) (core.Team, error) {
	return lp.editTeam(ctx, teamID, func(snap *core.Snapshot, pb *core.Playbook) error {
		return giveSeat(pb, kind, memberID, *snap)
	})
}

// AddToTeamRole gives a team one more seat for a kind of role; see
// AddToRole.
func (lp *Loop) AddToTeamRole(ctx context.Context, teamID, kind, memberID string) (core.Team, error) {
	return lp.editTeam(ctx, teamID, func(snap *core.Snapshot, pb *core.Playbook) error {
		return addToRole(pb, kind, memberID, *snap)
	})
}

// AddTeamSeat adds another seat filled like the named one; see AddSeat.
func (lp *Loop) AddTeamSeat(ctx context.Context, teamID, seat string) (core.Team, error) {
	return lp.editTeam(ctx, teamID, func(_ *core.Snapshot, pb *core.Playbook) error {
		_, err := pb.AddSeat(seat)
		return err
	})
}

// RemoveTeamSeat takes the named seat off a team, or with a kind takes only
// that kind of role from it; see RemoveSeat and RemoveFromRole.
func (lp *Loop) RemoveTeamSeat(ctx context.Context, teamID, seat, kind string) (core.Team, error) {
	return lp.editTeam(ctx, teamID, func(_ *core.Snapshot, pb *core.Playbook) error {
		if kind != "" {
			return removeFromRole(pb, seat, kind)
		}
		return pb.RemoveSeat(seat)
	})
}

func (lp *Loop) editTeam(ctx context.Context, teamID string, change func(*core.Snapshot, *core.Playbook) error) (core.Team, error) {
	t, err := lp.Core.EditTeam(ctx, teamID, func(snap *core.Snapshot, _ *core.Team, pb *core.Playbook) error {
		return change(snap, pb)
	})
	// A seat added may be free for work waiting on one.
	if err == nil {
		lp.Nudge()
	}
	return t, err
}

// SeatOverrideChoice is one seat override as the owner chooses it: what to
// do to a kind of role, and the member to do it with, or none for the
// template's seat for that kind.
type SeatOverrideChoice struct {
	Action string `json:"action"`
	Kind   string `json:"kind"`
	Member string `json:"member,omitempty"`
}

// SetSeatOverrides replaces a project's seat overrides with the owner's
// choices, each seat filled from its member as a team's seat is.
func (lp *Loop) SetSeatOverrides(ctx context.Context, projectID string, choices []SeatOverrideChoice) (core.Project, error) {
	p, err := lp.Core.EditSeatOverrides(ctx, projectID, func(snap *core.Snapshot, p *core.Project, base core.Playbook) ([]core.SeatOverride, error) {
		return overridesFrom(choices, *snap, base)
	})
	if err == nil {
		lp.Nudge()
	}
	return p, err
}

// overridesFrom makes the seats choices name, named apart from the base
// seats and from each other.
func overridesFrom(choices []SeatOverrideChoice, snap core.Snapshot, base core.Playbook) ([]core.SeatOverride, error) {
	named := base
	named.Roles = slices.Clone(base.Roles)
	var out []core.SeatOverride
	for _, c := range choices {
		o := core.SeatOverride{Action: c.Action, Kind: c.Kind}
		if c.Action == core.OverrideExclude {
			if c.Member != "" {
				return nil, errors.New("leaving a kind of role out names no member")
			}
			out = append(out, o)
			continue
		}
		seat, ok := base.TemplateSeat(c.Kind)
		if c.Member != "" {
			m, err := memberFor(c.Kind, c.Member, snap)
			if err != nil {
				return nil, err
			}
			seat = memberSeat(m, []string{c.Kind}, seat.Instructions)
		} else if !ok {
			return nil, fmt.Errorf("no template fills the %s role; choose a member", c.Kind)
		}
		// A seat that takes a kind over is named apart from the seats left.
		among := named
		if c.Action == core.OverrideReplace {
			among.Unseat(c.Kind)
		}
		seat.Name = among.FreeName(-1, core.SeatBase(seat.Name))
		named.Roles = append(named.Roles, seat)
		o.Seat = &seat
		out = append(out, o)
	}
	return out, nil
}
