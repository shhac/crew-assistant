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
// implementer, reviewer or QA with an avatar and what it has learned. A
// project's team copies a member into a role; learnings travel with the
// member into every task it starts.
type Member struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Kind         string        `json:"kind"`
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
	Name         string         `json:"name"`
	Kind         string         `json:"kind"`
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

func (in MemberInput) validate(v *Snapshot, id string) error {
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 40 {
		return errors.New("a member needs a name of 1 to 40 characters")
	}
	switch strings.ToLower(name) {
	case RoleImplementer, RoleReviewer, RoleQA:
		return fmt.Errorf("%q names a kind of role; give the member a name of its own", name)
	}
	for _, m := range v.Members {
		if m.ID != id && strings.EqualFold(m.Name, name) {
			return fmt.Errorf("there is already a member called %s", m.Name)
		}
	}
	switch in.Kind {
	case RoleImplementer, RoleReviewer, RoleQA:
	default:
		return errors.New("kind must be implementer, reviewer or qa")
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
		m.Name, m.Kind, m.Engine = strings.TrimSpace(in.Name), in.Kind, in.Engine
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
