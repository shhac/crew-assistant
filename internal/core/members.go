package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
)

// Member is someone the owner keeps on their team across projects: a named
// researcher, designer, implementer, reviewer, QA or PM, or several of them,
// with an avatar and what it has learned. A project's team copies a member
// into a role; learnings travel with the member into every task it starts.
type Member struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Kinds []string `json:"kinds"`
	// LegacyKind is the one kind a member held before members could hold
	// several; it is read into Kinds and never written again.
	LegacyKind   string        `json:"kind,omitempty"`
	Engine       string        `json:"engine"`
	Model        string        `json:"model,omitempty"`
	Effort       string        `json:"effort,omitempty"`
	Instructions string        `json:"instructions,omitempty"`
	Avatar       config.Avatar `json:"avatar"`
	// AvatarSVG is filled in when the state is read, and the drawing status
	// by the app, which does the drawing; neither is stored.
	AvatarSVG string     `json:"avatar_svg,omitempty"`
	Drawing   bool       `json:"drawing,omitempty"`
	DrawError string     `json:"draw_error,omitempty"`
	Learnings []Learning `json:"learnings"`
	CreatedAt time.Time  `json:"created_at"`
}

type MemberInput struct {
	Name  string   `json:"name"`
	Kinds []string `json:"kinds"`
	// Kind is the one kind older clients send; it counts as Kinds.
	Kind         string         `json:"kind,omitempty"`
	Engine       string         `json:"engine"`
	Model        string         `json:"model"`
	Effort       string         `json:"effort"`
	Instructions string         `json:"instructions"`
	Avatar       *config.Avatar `json:"avatar,omitempty"`
}

// Member finds a member by id.
func (v Snapshot) Member(id string) (Member, bool) {
	i := slices.IndexFunc(v.Members, func(m Member) bool { return m.ID == id })
	if i < 0 {
		return Member{}, false
	}
	return v.Members[i], true
}

func member(v *Snapshot, id string) *Member {
	for i := range v.Members {
		if v.Members[i].ID == id {
			return &v.Members[i]
		}
	}
	return nil
}

// updateMember changes one member atomically and returns it as changed.
func (s *Service) updateMember(ctx context.Context, id string, fn func(*Member, *Snapshot) error) (Member, error) {
	var out Member
	err := s.store.update(ctx, func(v *Snapshot) error {
		m := member(v, id)
		if m == nil {
			return ErrNotFound
		}
		if err := fn(m, v); err != nil {
			return err
		}
		out = *m
		return nil
	})
	return out, err
}

// kinds is what the member is asked to hold, taking an older client's single
// kind as the list.
func (in MemberInput) kinds() []string {
	if len(in.Kinds) == 0 && in.Kind != "" {
		return []string{in.Kind}
	}
	return in.Kinds
}

// Holds reports whether the member holds a kind of role.
func (m Member) Holds(kind string) bool { return slices.Contains(m.Kinds, kind) }

func (in MemberInput) validate(v *Snapshot, id string) error {
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 40 {
		return errors.New("a member needs a name of 1 to 40 characters")
	}
	if slices.Contains(roleKinds, strings.ToLower(name)) {
		return fmt.Errorf("%q names a kind of role; give the member a name of its own", name)
	}
	for _, m := range v.Members {
		if m.ID != id && strings.EqualFold(m.Name, name) {
			return fmt.Errorf("there is already a member called %s", m.Name)
		}
	}
	kinds := in.kinds()
	if len(kinds) == 0 {
		return errors.New("a member needs at least one role")
	}
	for i, kind := range kinds {
		if !slices.Contains(roleKinds, kind) {
			return fmt.Errorf("%q is not a role; roles are %s", kind, strings.Join(roleKinds, ", "))
		}
		if slices.Contains(kinds[:i], kind) {
			return fmt.Errorf("%s is listed twice", kind)
		}
	}
	if in.Engine != "codex" && in.Engine != "claude" {
		return errors.New("engine must be codex or claude")
	}
	if len(in.Model) > 80 || len(in.Effort) > 20 {
		return errors.New("model or effort is too long")
	}
	if len(in.Instructions) > 4000 {
		return errors.New("instructions must be at most 4000 characters")
	}
	if in.Avatar != nil {
		if err := in.Avatar.Normalized().Validate(); err != nil {
			return fmt.Errorf("avatar: %w", err)
		}
	}
	return nil
}

// SaveMember creates a member (empty id) or changes one. Project teams that
// copied the member keep their copy; what changes here reaches tasks that
// start from a team chosen afterwards.
func (s *Service) SaveMember(ctx context.Context, id string, in MemberInput) (Member, error) {
	var out Member
	err := s.store.update(ctx, func(v *Snapshot) error {
		if err := in.validate(v, id); err != nil {
			return err
		}
		m := member(v, id)
		if id != "" && m == nil {
			return ErrNotFound
		}
		if m == nil {
			v.Members = append(v.Members, Member{ID: uid(), Avatar: config.DefaultAvatar(in.Name), Learnings: []Learning{}, CreatedAt: s.now().UTC()})
			m = &v.Members[len(v.Members)-1]
		}
		m.Name, m.Kinds, m.Engine = strings.TrimSpace(in.Name), in.kinds(), in.Engine
		m.Model, m.Effort, m.Instructions = strings.TrimSpace(in.Model), strings.TrimSpace(in.Effort), strings.TrimSpace(in.Instructions)
		// A drawn picture is changed only by drawing again.
		if in.Avatar != nil {
			next := in.Avatar.Normalized()
			next.Image, next.Look = m.Avatar.Image, m.Avatar.Look
			m.Avatar = next
		}
		out = *m
		return nil
	})
	return out, err
}

// DeleteMember removes a member, and hands any project role it filled back to
// the team's template. Requests under way keep the team they started with.
func (s *Service) DeleteMember(ctx context.Context, id string) error {
	return s.store.update(ctx, func(v *Snapshot) error {
		i := slices.IndexFunc(v.Members, func(m Member) bool { return m.ID == id })
		if i < 0 {
			return ErrNotFound
		}
		gone := v.Members[i]
		v.Members = slices.Delete(v.Members, i, i+1)
		now := s.now().UTC()
		// Every team is checked before any is changed: if one would be left
		// broken, the member stays and nothing is saved.
		for j := range v.Projects {
			p := &v.Projects[j]
			vacated, err := vacate(p, id)
			if err != nil {
				return fmt.Errorf("%s can't leave the team for %s: %w", gone.Name, p.Title, err)
			}
			if vacated {
				p.UpdatedAt = now
				record(v, now, p.ID, "playbook.set", gone.Name+" left the team for "+p.Title)
			}
		}
		return nil
	})
}

// vacate takes a member's seat off a team and gives each kind of role it held
// back to the template's seat for it, if the template has one, and reports
// whether it had a seat. The team it leaves must still be valid.
func vacate(p *Project, memberID string) (bool, error) {
	if p.Playbook == nil {
		return false, nil
	}
	playbook := *p.Playbook
	playbook.Roles = slices.Clone(playbook.Roles)
	k := slices.IndexFunc(playbook.Roles, func(r Role) bool { return r.Member == memberID })
	if k < 0 {
		return false, nil
	}
	kinds := playbook.Roles[k].Kinds
	playbook.Roles = slices.Delete(playbook.Roles, k, k+1)
	for _, kind := range kinds {
		if seat, ok := playbook.TemplateSeat(kind); ok {
			playbook.Roles = slices.Insert(playbook.Roles, k, seat)
			k++
		}
	}
	if err := playbook.Validate(); err != nil {
		return false, err
	}
	p.Playbook = &playbook
	return true, nil
}

// SetMemberPicture records a picture drawn for a member, and how it was
// described.
func (s *Service) SetMemberPicture(ctx context.Context, id, image, look string) error {
	_, err := s.updateMember(ctx, id, func(m *Member, _ *Snapshot) error {
		m.Avatar.Image, m.Avatar.Look = image, look
		return nil
	})
	return err
}

// foldLegacyKinds reads the single kind older state kept, for members and for
// every seat on a team, into the kinds they hold now.
func foldLegacyKinds(v *Snapshot) {
	fold := func(kinds *[]string, legacy *string) {
		if len(*kinds) == 0 && *legacy != "" {
			*kinds = []string{*legacy}
		}
		*legacy = ""
	}
	seats := func(roles []Role) {
		for i := range roles {
			fold(&roles[i].Kinds, &roles[i].LegacyKind)
		}
	}
	for i := range v.Members {
		fold(&v.Members[i].Kinds, &v.Members[i].LegacyKind)
	}
	for i := range v.Projects {
		if v.Projects[i].Playbook != nil {
			seats(v.Projects[i].Playbook.Roles)
		}
	}
	for i := range v.Tasks {
		seats(v.Tasks[i].Roles)
		if v.Tasks[i].Playbook != nil {
			seats(v.Tasks[i].Playbook.Roles)
		}
	}
}

// What older state called the researcher: its kind of role, the template's
// seat for it and a task's status while it worked.
const (
	legacyPlanner     = "planner"
	legacyPlannerSeat = "Planner"
	legacyPlanning    = "planning"
)

// foldPlanner reads the planner of older state as the researcher, for
// members, every seat on a team, and tasks under way, so each keeps its
// settings, assignments, learnings and place in the loop. The template's seat
// takes its new name; a member's seat keeps the member's.
func foldPlanner(v *Snapshot) {
	kinds := func(held []string) []string {
		if !slices.Contains(held, legacyPlanner) {
			return held
		}
		var out []string
		for _, kind := range held {
			if kind == legacyPlanner {
				kind = RoleResearcher
			}
			if !slices.Contains(out, kind) {
				out = append(out, kind)
			}
		}
		return out
	}
	// seats returns what the template's planner seat is now called, if the
	// team had one.
	seats := func(roles []Role) (renamed string) {
		for i := range roles {
			r := &roles[i]
			planned := slices.Contains(r.Kinds, legacyPlanner)
			r.Kinds = kinds(r.Kinds)
			if planned && r.Member == "" && r.Name == legacyPlannerSeat {
				r.Name = Playbook{Roles: roles}.FreeName(i, "Researcher")
				renamed = r.Name
			}
		}
		return renamed
	}
	for i := range v.Members {
		v.Members[i].Kinds = kinds(v.Members[i].Kinds)
	}
	for i := range v.Projects {
		if v.Projects[i].Playbook != nil {
			seats(v.Projects[i].Playbook.Roles)
		}
	}
	status := func(s *string) {
		if *s == legacyPlanning {
			*s = TaskResearching
		}
	}
	for i := range v.Tasks {
		t := &v.Tasks[i]
		if renamed := seats(t.Roles); renamed != "" && t.Plan != nil && t.Plan.Role == legacyPlannerSeat {
			t.Plan.Role = renamed
		}
		if t.Playbook != nil {
			seats(t.Playbook.Roles)
		}
		status(&t.Status)
		status(&t.ResumeStatus)
	}
	for i := range v.Wakes {
		if w := &v.Wakes[i]; w.On == WakeOnTask {
			status(&w.Match)
			status(&w.Baseline)
		}
	}
}
