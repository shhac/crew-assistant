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
// planner, implementer, reviewer or QA, or several of them, with an avatar
// and what it has learned. A project's team copies a member into a role;
// learnings travel with the member into every task it starts.
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

// DeleteMember removes a member. Project roles copied from it stay as they
// are; they just stop gaining its learnings.
func (s *Service) DeleteMember(ctx context.Context, id string) error {
	return s.store.update(ctx, func(v *Snapshot) error {
		i := slices.IndexFunc(v.Members, func(m Member) bool { return m.ID == id })
		if i < 0 {
			return ErrNotFound
		}
		v.Members = slices.Delete(v.Members, i, i+1)
		return nil
	})
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
