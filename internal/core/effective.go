package core

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
)

// A project's playbook is put together from three owners, so a setting
// lives in one place however many projects use it:
//
//   - the repository it works in holds the code settings: the repository
//     path, the check and how it runs, what is copied in, and how QA runs
//     the app;
//   - the team that staffs it holds the seats, including the PM, and the
//     rounds before the PM must escalate;
//   - the project holds the rest (landing, release, limits, skills,
//     delivery), and, with no team or no repository, what those would hold.
//
// The project's seat overrides are applied last. Project.Playbook is that
// effective view, derived on every read and change and never stored.

// EffectivePlaybook is a project's playbook as it stands in v, or nil when
// the project has none yet.
func (v Snapshot) EffectivePlaybook(p Project) *Playbook {
	pb, ok := v.basePlaybook(p)
	if !ok {
		return nil
	}
	pb.Roles = applyOverrides(pb, p.SeatOverrides)
	return &pb
}

// basePlaybook is the project's playbook before its seat overrides: what the
// project-level controls change, since a seat set there is the team's.
func (v Snapshot) basePlaybook(p Project) (Playbook, bool) {
	if p.Settings == nil {
		return Playbook{}, false
	}
	pb := clonePlaybook(*p.Settings)
	if r := v.Repository(firstRepository(p)); r != nil {
		pb.Repo, pb.Check, pb.Prepare = r.Path, r.Check, slices.Clone(r.Prepare)
		pb.CheckInCopy, pb.CheckLoopback, pb.Run, pb.Tools = r.CheckInCopy, r.CheckLoopback, cloneRun(r.Run), slices.Clone(r.Tools)
	}
	if t := v.Team(p.Team); t != nil {
		pb.Roles, pb.MaxRounds = cloneRoles(t.Roles), t.MaxRounds
	}
	return pb, true
}

// firstRepository is the repository a project's work happens in.
func firstRepository(p Project) string {
	if len(p.Scope.Repositories) == 0 {
		return ""
	}
	return p.Scope.Repositories[0].ID
}

// resolvePlaybooks derives every project's playbook from its owners.
func resolvePlaybooks(v *Snapshot) {
	for i := range v.Projects {
		v.Projects[i].Playbook = v.EffectivePlaybook(v.Projects[i])
	}
}

// errPlaybookEdited means a change wrote a project's derived playbook
// rather than the setting's owner, which would be lost.
var errPlaybookEdited = errors.New("a project's playbook was changed directly; change its settings, repository or team instead")

// derivedPlaybooks copies each project's derived playbook before a change,
// so rederivePlaybooks can tell one the change wrote directly.
func derivedPlaybooks(v Snapshot) map[string]*Playbook {
	out := make(map[string]*Playbook, len(v.Projects))
	for _, p := range v.Projects {
		out[p.ID] = nil
		if p.Playbook != nil {
			pb := clonePlaybook(*p.Playbook)
			out[p.ID] = &pb
		}
	}
	return out
}

// rederivePlaybooks derives every playbook again after a change. It refuses
// a change that wrote a derived playbook instead of its owner, since that
// write would be lost.
func rederivePlaybooks(v *Snapshot, before map[string]*Playbook) error {
	for i := range v.Projects {
		p := &v.Projects[i]
		next := v.EffectivePlaybook(*p)
		if reflect.DeepEqual(p.Playbook, next) {
			continue
		}
		if was, ok := before[p.ID]; !ok || !reflect.DeepEqual(was, p.Playbook) {
			return fmt.Errorf("%w (%s)", errPlaybookEdited, p.Title)
		}
		p.Playbook = next
	}
	return nil
}

// withoutPlaybooks is the projects as they are stored: without the
// playbooks derived from their owners.
func withoutPlaybooks(projects []Project) []Project {
	out := slices.Clone(projects)
	for i := range out {
		out[i].Playbook = nil
	}
	return out
}

func clonePlaybook(p Playbook) Playbook {
	p.Roles = cloneRoles(p.Roles)
	p.Prepare = slices.Clone(p.Prepare)
	p.Tools = slices.Clone(p.Tools)
	p.StageLimits = maps.Clone(p.StageLimits)
	p.DisabledBundledSkills = slices.Clone(p.DisabledBundledSkills)
	p.Run = cloneRun(p.Run)
	p.Land.TrustedBots = slices.Clone(p.Land.TrustedBots)
	if p.Release != nil {
		release := *p.Release
		p.Release = &release
	}
	return p
}

func cloneRun(r *RunRecipe) *RunRecipe {
	if r == nil {
		return nil
	}
	recipe := *r
	return &recipe
}

// cloneRoles copies seats deeply enough that changing a copy's kinds or
// learnings never reaches the team it came from.
func cloneRoles(roles []Role) []Role {
	if roles == nil {
		return nil
	}
	out := make([]Role, len(roles))
	for i, r := range roles {
		r.Kinds = slices.Clone(r.Kinds)
		r.Learnings = slices.Clone(r.Learnings)
		out[i] = r
	}
	return out
}

// placePlaybook stores a project's base playbook, as a project-level control
// changed it, with each part's owner: code settings with its repository,
// seats with its team, the rest with the project. A code project given a new
// repository path gets a repository of its own for it; a new path moves its
// repository, unless other projects share it, when the project gets one of
// its own instead. A project that left its repository keeps its code
// settings as its own until it is given a new path. It returns the
// repository and team it wrote, if any.
func placePlaybook(v *Snapshot, p *Project, after Playbook) (repoID, teamID string) {
	own := clonePlaybook(after)
	r := v.Repository(firstRepository(*p))
	switch {
	case after.Medium != MediumGit:
		// Work that isn't code happens in no repository.
		if r != nil {
			p.Scope = Scope{}
		}
		r = nil
	case r == nil && (after.Repo == "" || (p.Settings != nil && p.Settings.Repo == after.Repo)):
	case r == nil:
		r = addRepository(v, after.Repo)
		p.Scope = Scope{Repositories: []ScopeRepository{{ID: r.ID}}}
	case r.Path != after.Repo && len(v.projectsUsing(r.ID, "")) > 1:
		r = addRepository(v, after.Repo)
		p.Scope = Scope{Repositories: []ScopeRepository{{ID: r.ID}}}
	}
	if r != nil {
		r.Path, r.Check, r.Prepare = after.Repo, after.Check, slices.Clone(after.Prepare)
		r.CheckInCopy, r.CheckLoopback, r.Run, r.Tools = after.CheckInCopy, after.CheckLoopback, cloneRun(after.Run), slices.Clone(after.Tools)
		own.Repo, own.Check, own.Prepare, own.CheckInCopy, own.CheckLoopback, own.Run, own.Tools = "", "", nil, false, false, nil, nil
		repoID = r.ID
	}
	if t := v.Team(p.Team); t != nil {
		t.Roles, t.MaxRounds, t.Template = cloneRoles(after.Roles), after.MaxRounds, after.Template
		own.Roles, own.MaxRounds = nil, 0
		teamID = t.ID
	}
	p.Settings = &own
	return repoID, teamID
}

// projectsUsing is the projects that work in the repository or are staffed
// by the team; an empty id matches nothing.
func (v Snapshot) projectsUsing(repoID, teamID string) []Project {
	var out []Project
	for _, p := range v.Projects {
		if (repoID != "" && slices.ContainsFunc(p.Scope.Repositories, func(s ScopeRepository) bool { return s.ID == repoID })) || (teamID != "" && p.Team == teamID) {
			out = append(out, p)
		}
	}
	return out
}

// settle derives every playbook again after a change to an owner and checks
// what it reaches: the project changed, which must be valid, and every
// project sharing the repository or team, which must not be broken by it.
// before is the state as it was, to tell a project this change broke from
// one that was not yet set up.
func settle(before, v *Snapshot, projectID, repoID, teamID string, valid func(Playbook) error) error {
	resolvePlaybooks(v)
	reached := v.projectsUsing(repoID, teamID)
	if p := project(v, projectID); p != nil && !slices.ContainsFunc(reached, func(q Project) bool { return q.ID == projectID }) {
		reached = append(reached, *p)
	}
	for _, q := range reached {
		if q.Playbook == nil {
			continue
		}
		err := valid(*q.Playbook)
		if err == nil {
			err = v.teamFits(q)
		}
		if err == nil || q.ID == projectID {
			if err != nil {
				return err
			}
			continue
		}
		if was := project(before, q.ID); was != nil && was.Playbook != nil && valid(*was.Playbook) != nil {
			continue
		}
		return fmt.Errorf("this would leave the playbook for %s unusable: %w", q.Title, err)
	}
	return nil
}

// teamFits refuses a team made for another kind of work than the project's:
// a code team's seats are told how code is done.
func (v Snapshot) teamFits(p Project) error {
	t := v.Team(p.Team)
	if t == nil || p.Playbook == nil || Templates[t.Template].Medium == p.Playbook.Medium {
		return nil
	}
	return fmt.Errorf("%s is a %s team, and %s's work is %s", t.Name, t.Template, p.Title, p.Playbook.Medium)
}

// validPlaybook is a playbook's own checks and each seat's engine provider.
func (s *Service) validPlaybook(pb Playbook) error {
	if err := pb.Validate(); err != nil {
		return err
	}
	for _, r := range pb.Roles {
		if err := s.configuration().Engines.CheckProvider(r.Engine, r.Provider); err != nil {
			return fmt.Errorf("role %s: %w", r.Name, err)
		}
	}
	return nil
}

// snapshotCopy is enough of v to judge, after a change, what a project's
// playbook was before it.
func snapshotCopy(v *Snapshot) *Snapshot {
	c := *v
	c.Projects = slices.Clone(v.Projects)
	return &c
}
