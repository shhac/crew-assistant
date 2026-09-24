package core

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

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

// Learning is one thing a member keeps doing, in every project. When says
// the situation it applies to, the way a skill's description does: a role
// sees every When from the start and reads the text only when that
// situation comes up.
type Learning struct {
	ID        string    `json:"id"`
	When      string    `json:"when,omitempty"`
	Text      string    `json:"text"`
	Source    string    `json:"source,omitempty"`
	ProjectID string    `json:"project_id,omitempty"`
	TaskID    string    `json:"task_id,omitempty"`
	At        time.Time `json:"at"`
}

// Who recorded a learning.
const (
	LearnedByOwner     = "owner"
	LearnedByAssistant = "assistant"
	LearnedByMember    = "member"
)

type LearningInput struct {
	When      string `json:"when"`
	Text      string `json:"text"`
	ProjectID string `json:"project_id"`
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
		for i := range v.Members {
			if v.Members[i].ID == id {
				v.Members = append(v.Members[:i], v.Members[i+1:]...)
				return nil
			}
		}
		return ErrNotFound
	})
}

func (in LearningInput) clean() (LearningInput, error) {
	in.When, in.Text = strings.TrimSpace(in.When), strings.TrimSpace(in.Text)
	if in.Text == "" || len(in.Text) > 1500 {
		return in, errors.New("a learning is 1 to 1500 characters")
	}
	if len(in.When) > 160 {
		return in, errors.New("when it applies is at most 160 characters")
	}
	// A when becomes a line of every later task's instructions; a second
	// line there could pass for another learning or an instruction.
	if strings.ContainsFunc(in.When, unicode.IsControl) {
		return in, errors.New("when it applies must be one line")
	}
	return in, nil
}

// AddLearning keeps something the owner, or the assistant on the owner's
// word, wants a member to do everywhere.
func (s *Service) AddLearning(ctx context.Context, memberID, source string, in LearningInput) (Member, error) {
	in, err := in.clean()
	if err != nil {
		return Member{}, err
	}
	var out Member
	err = s.store.update(ctx, func(v *Snapshot) error {
		m := member(v, memberID)
		if m == nil {
			return ErrNotFound
		}
		if len(m.Learnings) >= maxLearnings {
			return fmt.Errorf("%s already keeps %d learnings; forget one first: %w", m.Name, maxLearnings, ErrConflict)
		}
		if in.ProjectID != "" && project(v, in.ProjectID) == nil {
			return ErrNotFound
		}
		m.Learnings = append(m.Learnings, Learning{ID: uid(), When: in.When, Text: in.Text, Source: source, ProjectID: in.ProjectID, At: s.now().UTC()})
		out = *m
		return nil
	})
	return out, err
}

// RecordLearning keeps something a member learned by itself on a task. The
// rule it works to is that a shared learning is never about one project and
// any data in it is made up; one naming the project's own paths is refused
// outright. The same situation is not learned twice, and when the member is
// full the oldest thing it taught itself makes room; what the owner added is
// never pushed out.
func (s *Service) RecordLearning(ctx context.Context, memberID, taskID string, in LearningInput, specific []string) (Learning, error) {
	in, err := in.clean()
	if err != nil {
		return Learning{}, err
	}
	if in.When == "" {
		return Learning{}, errors.New("a learning needs to say when it applies")
	}
	if word := naming(in.When+"\n"+in.Text, specific); word != "" {
		return Learning{}, fmt.Errorf("a learning must not be about one project; it names %s", word)
	}
	var out Learning
	err = s.store.update(ctx, func(v *Snapshot) error {
		m := member(v, memberID)
		if m == nil {
			return ErrNotFound
		}
		for _, l := range m.Learnings {
			if strings.EqualFold(l.When, in.When) {
				return fmt.Errorf("%s already has a learning for %q: %w", m.Name, in.When, ErrConflict)
			}
		}
		if len(m.Learnings) >= maxLearnings {
			i := slices.IndexFunc(m.Learnings, func(l Learning) bool { return l.Source == LearnedByMember })
			if i < 0 {
				return fmt.Errorf("%s is full of learnings the owner added: %w", m.Name, ErrConflict)
			}
			m.Learnings = append(m.Learnings[:i], m.Learnings[i+1:]...)
		}
		now := s.now().UTC()
		out = Learning{ID: uid(), When: in.When, Text: in.Text, Source: LearnedByMember, ProjectID: in.ProjectID, TaskID: taskID, At: now}
		m.Learnings = append(m.Learnings, out)
		record(v, now, in.ProjectID, "member.learned", m.Name+" learned: "+in.When)
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

// identifying are things no learning meant for every project would hold:
// addresses, links and anything shaped like a key or token.
var identifying = regexp.MustCompile(`[\w.+-]+@[\w-]+\.[\w.]+|https?://\S+|\b[0-9a-fA-F]{20,}\b|\b[A-Za-z0-9+/_=-]{40,}\b`)

// naming returns what text names that ties it to one project, ignoring case.
func naming(text string, specific []string) string {
	lower := strings.ToLower(text)
	for _, word := range specific {
		if word != "" && strings.Contains(lower, strings.ToLower(word)) {
			return word
		}
	}
	return identifying.FindString(text)
}

// withLearnings is the roles a task starts with: each role copied from a
// member carries a copy of what that member has learned so far. They are
// pinned here, once, because what a role is told at the start is part of its
// session; changing it mid-task would start the writer afresh.
func withLearnings(v *Snapshot, roles []Role) []Role {
	out := append([]Role(nil), roles...)
	for i, r := range out {
		if m := member(v, r.Member); m != nil && len(m.Learnings) > 0 {
			out[i].Learnings = append([]Learning(nil), m.Learnings...)
		}
	}
	return out
}

// SetMemberPicture records a picture drawn for a member, and how it was
// described.
func (s *Service) SetMemberPicture(ctx context.Context, id, image, look string) error {
	return s.store.update(ctx, func(v *Snapshot) error {
		m := member(v, id)
		if m == nil {
			return ErrNotFound
		}
		m.Avatar.Image, m.Avatar.Look = image, look
		return nil
	})
}
