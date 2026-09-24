package core

import (
	"context"
	"errors"
	"fmt"
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
	// AvatarSVG is drawn when the state is read, never stored.
	AvatarSVG string     `json:"avatar_svg,omitempty"`
	Learnings []Learning `json:"learnings"`
	CreatedAt time.Time  `json:"created_at"`
}

// Learning is one thing a member keeps doing, in every project. Only the
// owner, or the assistant on the owner's word, records one: it becomes part
// of the member's instructions everywhere.
type Learning struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	ProjectID string    `json:"project_id,omitempty"`
	At        time.Time `json:"at"`
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

const maxLearnings = 30

func member(v *Snapshot, id string) *Member {
	for i := range v.Members {
		if v.Members[i].ID == id {
			return &v.Members[i]
		}
	}
	return nil
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
		if in.Avatar != nil {
			m.Avatar = in.Avatar.Normalized()
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
		for i := range v.Members {
			if v.Members[i].ID == id {
				v.Members = append(v.Members[:i], v.Members[i+1:]...)
				return nil
			}
		}
		return ErrNotFound
	})
}

func (s *Service) AddLearning(ctx context.Context, memberID, text, projectID string) (Member, error) {
	text = strings.TrimSpace(text)
	if text == "" || len(text) > 300 {
		return Member{}, errors.New("a learning is 1 to 300 characters")
	}
	var out Member
	err := s.store.update(ctx, func(v *Snapshot) error {
		m := member(v, memberID)
		if m == nil {
			return ErrNotFound
		}
		if len(m.Learnings) >= maxLearnings {
			return fmt.Errorf("%s already keeps %d learnings; forget one first: %w", m.Name, maxLearnings, ErrConflict)
		}
		if projectID != "" && project(v, projectID) == nil {
			return ErrNotFound
		}
		m.Learnings = append(m.Learnings, Learning{ID: uid(), Text: text, ProjectID: projectID, At: s.now().UTC()})
		out = *m
		return nil
	})
	return out, err
}

func (s *Service) ForgetLearning(ctx context.Context, memberID, learningID string) (Member, error) {
	var out Member
	err := s.store.update(ctx, func(v *Snapshot) error {
		m := member(v, memberID)
		if m == nil {
			return ErrNotFound
		}
		for i, l := range m.Learnings {
			if l.ID == learningID {
				m.Learnings = append(m.Learnings[:i], m.Learnings[i+1:]...)
				out = *m
				return nil
			}
		}
		return ErrNotFound
	})
	return out, err
}

// learningsText is what a member has learned, as instructions for a role it
// fills: newest first, within a bound so it never crowds out the task.
func learningsText(m Member) string {
	if len(m.Learnings) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("What you have learned on earlier work. Keep doing these unless this project's brief says otherwise:\n")
	for i := len(m.Learnings) - 1; i >= 0; i-- {
		line := "- " + m.Learnings[i].Text + "\n"
		if b.Len()+len(line) > 4000 {
			break
		}
		b.WriteString(line)
	}
	return strings.TrimRight(b.String(), "\n")
}
