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

	"github.com/shhac/crew-assistant/internal/text"
)

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

const maxLearnings = 30

// clean checks a learning from source. The owner and the assistant may leave
// out when it applies, and its heading stands in; a member must say.
func (in LearningInput) clean(source string) (LearningInput, error) {
	in.When, in.Text = strings.TrimSpace(in.When), strings.TrimSpace(in.Text)
	if in.Text == "" || len(in.Text) > 1500 {
		return in, errors.New("a learning is 1 to 1500 characters")
	}
	if in.When == "" && source == LearnedByMember {
		return in, errors.New("a learning needs to say when it applies")
	}
	if in.When == "" {
		in.When = Heading(in.Text)
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

// Heading is what a learning is indexed by when it says nothing of when it
// applies: its opening words, up to the end of the first sentence or line.
func Heading(learning string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(learning), "\n")
	if i := strings.Index(first, ". "); i > 0 {
		first = first[:i]
	}
	return text.Clip(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, first), 120)
}

// headLegacyLearnings gives a when to learnings kept before one was always
// filled in, so every reader sees one.
func headLegacyLearnings(v *Snapshot) {
	head := func(ls []Learning) {
		for i := range ls {
			if ls[i].When == "" {
				ls[i].When = Heading(ls[i].Text)
			}
		}
	}
	for i := range v.Members {
		head(v.Members[i].Learnings)
	}
	for i := range v.Tasks {
		for j := range v.Tasks[i].Roles {
			head(v.Tasks[i].Roles[j].Learnings)
		}
	}
}

// AddLearning keeps something the owner, or the assistant on the owner's
// word, wants a member to do everywhere.
func (s *Service) AddLearning(ctx context.Context, memberID, source string, in LearningInput) (Member, error) {
	in, err := in.clean(source)
	if err != nil {
		return Member{}, err
	}
	return s.updateMember(ctx, memberID, func(m *Member, v *Snapshot) error {
		if len(m.Learnings) >= maxLearnings {
			return fmt.Errorf("%s already keeps %d learnings; forget one first: %w", m.Name, maxLearnings, ErrConflict)
		}
		if in.ProjectID != "" && project(v, in.ProjectID) == nil {
			return ErrNotFound
		}
		m.Learnings = append(m.Learnings, Learning{ID: uid(), When: in.When, Text: in.Text, Source: source, ProjectID: in.ProjectID, At: s.now().UTC()})
		return nil
	})
}

// RecordLearning keeps something a member learned by itself on a task. The
// rule it works to is that a shared learning is never about one project and
// any data in it is made up; one naming the project's own paths is refused
// outright. The same situation is not learned twice, and when the member is
// full the oldest thing it taught itself makes room; what the owner added is
// never pushed out.
func (s *Service) RecordLearning(ctx context.Context, memberID, taskID string, in LearningInput, specific []string) (Learning, error) {
	in, err := in.clean(LearnedByMember)
	if err != nil {
		return Learning{}, err
	}
	if word := naming(in.When+"\n"+in.Text, specific); word != "" {
		return Learning{}, fmt.Errorf("a learning must not be about one project; it names %s", word)
	}
	var out Learning
	_, err = s.updateMember(ctx, memberID, func(m *Member, v *Snapshot) error {
		if slices.ContainsFunc(m.Learnings, func(l Learning) bool { return strings.EqualFold(l.When, in.When) }) {
			return fmt.Errorf("%s already has a learning for %q: %w", m.Name, in.When, ErrConflict)
		}
		if len(m.Learnings) >= maxLearnings {
			i := slices.IndexFunc(m.Learnings, func(l Learning) bool { return l.Source == LearnedByMember })
			if i < 0 {
				return fmt.Errorf("%s is full of learnings the owner added: %w", m.Name, ErrConflict)
			}
			m.Learnings = slices.Delete(m.Learnings, i, i+1)
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
	return s.updateMember(ctx, memberID, func(m *Member, _ *Snapshot) error {
		i := slices.IndexFunc(m.Learnings, func(l Learning) bool { return l.ID == learningID })
		if i < 0 {
			return ErrNotFound
		}
		m.Learnings = slices.Delete(m.Learnings, i, i+1)
		return nil
	})
}

// identifying are things no learning meant for every project would hold:
// addresses, links and anything shaped like a key or token.
var identifying = regexp.MustCompile(`[\w.+-]+@[\w-]+\.[\w.]+|https?://\S+|\b[0-9a-fA-F]{20,}\b|\b[A-Za-z0-9+/_=-]{40,}\b`)

// naming returns what s names that ties it to one project, ignoring case.
func naming(s string, specific []string) string {
	lower := strings.ToLower(s)
	for _, word := range specific {
		if word != "" && strings.Contains(lower, strings.ToLower(word)) {
			return word
		}
	}
	return identifying.FindString(s)
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
