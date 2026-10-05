package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Team is a durable group of seats that does work in a known way: its
// default seats by kind of role, the PM's among them, and the rounds before
// the PM must escalate. Projects it staffs take its seats, adjusted by their
// own seat overrides. A member busy in any project is busy in all of them.
type Team struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Template is the template its seats were made from, which a seat given
	// back to no member returns to.
	Template  string `json:"template"`
	Roles     []Role `json:"roles"`
	MaxRounds int    `json:"max_rounds"`
}

// Team is the team with this id, to change in place, or nil.
func (v *Snapshot) Team(id string) *Team {
	if id == "" {
		return nil
	}
	for i := range v.Teams {
		if v.Teams[i].ID == id {
			return &v.Teams[i]
		}
	}
	return nil
}

// playbook is the team as the seat controls change it.
func (t Team) playbook() Playbook {
	return Playbook{Template: t.Template, Roles: cloneRoles(t.Roles), MaxRounds: t.MaxRounds}
}

// validate checks a team on its own: its seats, and its rounds. Each
// project it staffs checks the rest with its playbook.
func (t Team) validate() error {
	if strings.TrimSpace(t.Name) == "" || len(t.Name) > 200 {
		return errors.New("a team needs a name of up to 200 characters")
	}
	if _, ok := Templates[t.Template]; !ok {
		return fmt.Errorf("unknown team template %q", t.Template)
	}
	if t.MaxRounds < 1 || t.MaxRounds > 10 {
		return errors.New("max_rounds must be between 1 and 10")
	}
	return validateSeats(t.Roles, true)
}

// Scope is what a project expects to touch: repositories, and code areas in
// them. Its work happens in the first repository; for now that is the only
// one.
type Scope struct {
	Repositories []ScopeRepository `json:"repositories,omitempty"`
}

// ScopeRepository is a repository in a project's scope, and the code areas
// of it the project names; none means the whole repository.
type ScopeRepository struct {
	ID    string   `json:"id"`
	Areas []string `json:"areas,omitempty"`
}

// SeatOverride adjusts a team's seats for one project, by kind of role.
type SeatOverride struct {
	// Action is OverrideAdd, OverrideReplace or OverrideExclude.
	Action string `json:"action"`
	Kind   string `json:"kind"`
	// Seat is the seat added, or the one that takes the kind over.
	Seat *Role `json:"seat,omitempty"`
}

const (
	// OverrideAdd gives the project one more seat, such as a designer for
	// this project only, or a second reviewer.
	OverrideAdd = "add"
	// OverrideReplace has another seat hold the kind for this project: the
	// team's seats give it up and this one takes it.
	OverrideReplace = "replace"
	// OverrideExclude leaves the kind out of this project, as no researcher
	// on a small one.
	OverrideExclude = "exclude"
)

// validate checks an override on its own; the playbook it makes is checked
// as a whole.
func (o SeatOverride) validate() error {
	if !IsRoleKind(o.Kind) {
		return fmt.Errorf("a seat override needs a kind of role: one of %s", strings.Join(roleKinds, ", "))
	}
	switch o.Action {
	case OverrideAdd, OverrideReplace:
		if o.Seat == nil {
			return fmt.Errorf("an override that adds or replaces needs the seat")
		}
		if !o.Seat.Holds(o.Kind) {
			return fmt.Errorf("the seat %s doesn't hold %s", o.Seat.Name, o.Kind)
		}
	case OverrideExclude:
		if o.Seat != nil {
			return errors.New("an override that leaves a kind out has no seat")
		}
	default:
		return fmt.Errorf("a seat override adds, replaces or excludes, not %q", o.Action)
	}
	return nil
}

// applyOverrides is the seats a project works with: its base seats with
// each override applied in order.
func applyOverrides(pb Playbook, overrides []SeatOverride) []Role {
	for _, o := range overrides {
		switch o.Action {
		case OverrideExclude:
			pb.Unseat(o.Kind)
		case OverrideReplace:
			if o.Seat == nil {
				continue
			}
			at := pb.Unseat(o.Kind)
			pb.Roles = slices.Insert(pb.Roles, at, cloneRoles([]Role{*o.Seat})[0])
		case OverrideAdd:
			if o.Seat == nil {
				continue
			}
			at := len(pb.Roles)
			for i, r := range pb.Roles {
				if r.Holds(o.Kind) {
					at = i + 1
				}
			}
			pb.Roles = slices.Insert(slices.Clone(pb.Roles), at, cloneRoles([]Role{*o.Seat})[0])
		}
	}
	return pb.Roles
}

// TeamInput is a new team: from a template, or from the seats a project has
// now, which the project then takes as its team.
type TeamInput struct {
	Name        string `json:"name"`
	Template    string `json:"template"`
	FromProject string `json:"from_project"`
}

// CreateTeam makes a team.
func (s *Service) CreateTeam(ctx context.Context, in TeamInput) (Team, error) {
	var out Team
	err := s.store.update(ctx, func(v *Snapshot) error {
		t := Team{ID: uid(), Name: strings.TrimSpace(in.Name), Template: in.Template}
		var p *Project
		if in.FromProject != "" {
			if p = project(v, in.FromProject); p == nil {
				return ErrNotFound
			}
			if p.Team != "" {
				return fmt.Errorf("%s already has a team: %w", p.Title, ErrConflict)
			}
			pb, ok := v.basePlaybook(*p)
			if !ok {
				return errors.New("choose a team first")
			}
			t.Template, t.Roles, t.MaxRounds = pb.Template, pb.Roles, pb.MaxRounds
		} else {
			template, ok := Templates[t.Template]
			if !ok {
				return fmt.Errorf("unknown team template %q", in.Template)
			}
			t.Roles, t.MaxRounds = cloneRoles(template.Roles), template.MaxRounds
		}
		if err := t.validate(); err != nil {
			return err
		}
		v.Teams = append(v.Teams, t)
		now := s.now().UTC()
		if p != nil {
			own := clonePlaybook(*p.Settings)
			own.Roles, own.MaxRounds = nil, 0
			p.Settings, p.Team, p.UpdatedAt = &own, t.ID, now
			resolvePlaybooks(v)
			record(v, now, p.ID, "project.team", p.Title+" is staffed by the new team "+t.Name)
		}
		record(v, now, "", "team.created", "Team "+t.Name)
		out = t
		return nil
	})
	return out, err
}

// TeamUpdate is a team's name and its rounds before the PM escalates.
type TeamUpdate struct {
	Name      string `json:"name"`
	MaxRounds int    `json:"max_rounds"`
}

// UpdateTeam renames a team and sets its rounds, for every project it
// staffs.
func (s *Service) UpdateTeam(ctx context.Context, id string, in TeamUpdate) (Team, error) {
	return s.EditTeam(ctx, id, func(_ *Snapshot, t *Team, pb *Playbook) error {
		t.Name, pb.MaxRounds = strings.TrimSpace(in.Name), in.MaxRounds
		return nil
	})
}

// EditTeam changes a team in one step, for every project it staffs; each
// must keep a usable playbook. change gets the team, and its seats and rounds
// as a playbook made from its template, which are written back. Tasks under
// way keep the team they started with.
func (s *Service) EditTeam(ctx context.Context, id string, change func(v *Snapshot, t *Team, pb *Playbook) error) (Team, error) {
	var out Team
	err := s.store.update(ctx, func(v *Snapshot) error {
		t := v.Team(id)
		if t == nil {
			return ErrNotFound
		}
		before := snapshotCopy(v)
		pb := t.playbook()
		if err := change(v, t, &pb); err != nil {
			return err
		}
		t.Roles, t.MaxRounds = pb.Roles, pb.MaxRounds
		if err := t.validate(); err != nil {
			return err
		}
		if err := settle(before, v, "", "", id, s.validPlaybook); err != nil {
			return err
		}
		now := s.now().UTC()
		using := v.projectsUsing("", id)
		for _, p := range using {
			project(v, p.ID).UpdatedAt = now
		}
		record(v, now, "", "team.updated", "Team "+t.Name+": "+playbookSummary(pb)+sharedBy(using))
		out = *t
		return nil
	})
	return out, err
}

// DeleteTeam removes a team no project is staffed by.
func (s *Service) DeleteTeam(ctx context.Context, id string) error {
	return s.store.update(ctx, func(v *Snapshot) error {
		t := v.Team(id)
		if t == nil {
			return ErrNotFound
		}
		if using := v.projectsUsing("", id); len(using) > 0 {
			return fmt.Errorf("%s is still staffed by %s; give it another team or its own seats first: %w", using[0].Title, t.Name, ErrConflict)
		}
		name := t.Name
		v.Teams = slices.DeleteFunc(v.Teams, func(t Team) bool { return t.ID == id })
		record(v, s.now().UTC(), "", "team.deleted", "Team "+name+" removed")
		return nil
	})
}

// ProjectSetup is who staffs a project and where its work happens: a team,
// or "" for seats of its own; a repository, or "" for none; and the code
// areas of that repository it expects to touch.
type ProjectSetup struct {
	Team       string   `json:"team"`
	Repository string   `json:"repository"`
	Areas      []string `json:"areas"`
}

// SetProjectSetup gives a project a team and a repository, or takes them
// away. Leaving a team keeps its seats as the project's own, and leaving a
// repository keeps its code settings, so the project works on as it did.
// A repository must be one of the project's folders. A project with no
// playbook yet starts from the team's template, or the code template.
// Tasks under way keep the team and settings they started with.
func (s *Service) SetProjectSetup(ctx context.Context, projectID string, in ProjectSetup) (Project, error) {
	var out Project
	err := s.store.update(ctx, func(v *Snapshot) error {
		p := project(v, projectID)
		if p == nil {
			return ErrNotFound
		}
		before := snapshotCopy(v)
		team, repo := v.Team(in.Team), v.Repository(in.Repository)
		if in.Team != "" && team == nil {
			return fmt.Errorf("there is no team %q: %w", in.Team, ErrNotFound)
		}
		if in.Repository != "" && repo == nil {
			return fmt.Errorf("there is no repository %q: %w", in.Repository, ErrNotFound)
		}
		base, ok := v.basePlaybook(*p)
		if !ok {
			name := "code"
			if team != nil {
				name = team.Template
			} else if repo == nil {
				return errors.New("choose a team first")
			}
			base = clonePlaybook(Templates[name])
		}
		// What the project leaves stays its own, so it works on as it did.
		own := clonePlaybook(base)
		if team != nil {
			own.Roles, own.MaxRounds = nil, 0
		}
		if repo != nil {
			if own.Medium != MediumGit {
				return errors.New("a repository is for code projects; choose a code team first")
			}
			if !slices.Contains(p.Directories, repo.Path) {
				return fmt.Errorf("link %s to the project as a folder first", repo.Path)
			}
			for _, name := range in.Areas {
				if !slices.ContainsFunc(repo.Areas, func(a CodeArea) bool { return a.Name == name }) {
					return fmt.Errorf("%s has no code area %q", repo.Name, name)
				}
			}
			own.Repo, own.Check, own.Prepare, own.CheckInCopy, own.CheckLoopback, own.Run = "", "", nil, false, false, nil
			p.Scope = Scope{Repositories: []ScopeRepository{{ID: repo.ID, Areas: slices.Compact(slices.Sorted(slices.Values(in.Areas)))}}}
		} else {
			if len(in.Areas) > 0 {
				return errors.New("code areas belong to a repository; choose one first")
			}
			p.Scope = Scope{}
		}
		p.Settings, p.Team = &own, in.Team
		if err := settle(before, v, p.ID, in.Repository, in.Team, s.validPlaybook); err != nil {
			return err
		}
		p.UpdatedAt = s.now().UTC()
		record(v, p.UpdatedAt, p.ID, "project.setup", p.Title+": "+setupSummary(team, repo))
		out = *p
		return nil
	})
	return out, err
}

func setupSummary(team *Team, repo *Repository) string {
	staffed := "staffed by its own seats"
	if team != nil {
		staffed = "staffed by " + team.Name
	}
	if repo == nil {
		return staffed
	}
	return staffed + ", working in " + repo.Name
}

// SetSeatOverrides replaces a project's seat overrides. The playbook they
// make must be usable. Tasks under way keep the seats they started with.
func (s *Service) SetSeatOverrides(ctx context.Context, projectID string, overrides []SeatOverride) (Project, error) {
	return s.EditSeatOverrides(ctx, projectID, func(*Snapshot, *Project, Playbook) ([]SeatOverride, error) {
		return overrides, nil
	})
}

// EditSeatOverrides replaces a project's seat overrides with those build
// makes from the state and the project's seats before any override, in one
// step. build must not call back into the Service.
func (s *Service) EditSeatOverrides(ctx context.Context, projectID string, build func(v *Snapshot, p *Project, base Playbook) ([]SeatOverride, error)) (Project, error) {
	var out Project
	err := s.store.update(ctx, func(v *Snapshot) error {
		p := project(v, projectID)
		if p == nil {
			return ErrNotFound
		}
		base, ok := v.basePlaybook(*p)
		if !ok {
			return errors.New("choose a team first")
		}
		overrides, err := build(v, p, base)
		if err != nil {
			return err
		}
		before := snapshotCopy(v)
		p.SeatOverrides = nil
		for _, o := range overrides {
			if err := o.validate(); err != nil {
				return err
			}
			if o.Seat != nil {
				seat := cloneRoles([]Role{*o.Seat})[0]
				o.Seat = &seat
			}
			p.SeatOverrides = append(p.SeatOverrides, o)
		}
		if err := settle(before, v, p.ID, "", "", s.validPlaybook); err != nil {
			return err
		}
		p.UpdatedAt = s.now().UTC()
		record(v, p.UpdatedAt, p.ID, "playbook.set", "Seats for "+p.Title+": "+playbookSummary(*p.Playbook))
		out = *p
		return nil
	})
	return out, err
}
