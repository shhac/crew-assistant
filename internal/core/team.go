package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/text"
)

// TeamMessage is something the owner or the assistant said directly to one
// member of a task's team. To the implementer it is direction for its next
// round; to a reviewer or QA it asks for a check of the latest revision now.
type TeamMessage struct {
	ID string `json:"id"`
	// To is the role's name, Kind its kind.
	To     string `json:"to"`
	Kind   string `json:"kind"`
	From   string `json:"from"`
	Text   string `json:"text"`
	Status string `json:"status"`
	Reply  string `json:"reply,omitempty"`
	// Outcome is a checker's verdict on Revision, the revision its reply is
	// about.
	Outcome  string `json:"outcome,omitempty"`
	Revision int    `json:"revision,omitempty"`
	// Direction is the message's place in the task's direction: the first
	// revision written with it in view answers it.
	Direction  int       `json:"direction,omitempty"`
	At         time.Time `json:"at"`
	AnsweredAt time.Time `json:"answered_at,omitzero"`
}

const (
	MessageWaiting  = "waiting"
	MessageWorking  = "working"
	MessageAnswered = "answered"
	MessageFailed   = "failed"
	MessageClosed   = "closed"

	FromOwner     = "owner"
	FromAssistant = "assistant"
)

// Open reports a message still to be answered.
func (m TeamMessage) Open() bool { return m.Status == MessageWaiting || m.Status == MessageWorking }

// NextRound starts another round. max_rounds is where the owner is asked,
// not a cap on work, so it moves up with the round.
func (t *Task) NextRound() {
	t.Round++
	t.MaxRounds = max(t.MaxRounds, t.Round)
}

// AnswerDirection answers the implementer messages that were in view when a
// revision's prompt was written (the first seen entries of Direction), and
// leaves pending whatever arrived while the implementer worked.
func (t *Task) AnswerDirection(seen, revision int, reply string, now time.Time) {
	t.DirectionPending = max(0, len(t.Direction)-seen)
	for i := range t.Messages {
		m := &t.Messages[i]
		if m.Kind == RoleImplementer && m.Open() && m.Direction < seen {
			m.Status, m.Reply, m.Revision, m.AnsweredAt = MessageAnswered, text.Clip(reply, 2000), revision, now
		}
	}
}

func closeMessages(t *Task, now time.Time) {
	for i := range t.Messages {
		if m := &t.Messages[i]; m.Open() {
			m.Status, m.Reply, m.AnsweredAt = MessageClosed, "The request finished first.", now
		}
	}
}

// SendTeamMessage delivers a message to one member of a task's team, in one
// change: a message to the implementer that the task is waiting on the owner
// for answers that wait, so it can never be read as one of the fixed choices.
func (s *Service) SendTeamMessage(ctx context.Context, projectID, taskID, to, from, message string) (TeamMessage, error) {
	message = strings.TrimSpace(message)
	if message == "" || len(message) > 8*1024 {
		return TeamMessage{}, errors.New("a message is required and must be at most 8 KiB")
	}
	if from != FromOwner && from != FromAssistant {
		return TeamMessage{}, fmt.Errorf("unknown sender %q", from)
	}
	var out TeamMessage
	err := s.store.update(ctx, func(v *Snapshot) error {
		t := task(v, taskID)
		if t == nil || t.ProjectID != projectID {
			return ErrNotFound
		}
		p := project(v, projectID)
		if p == nil {
			return ErrNotFound
		}
		if t.Finished() {
			return fmt.Errorf("this request has finished; ask for a new one instead: %w", ErrConflict)
		}
		team := t.Roles
		if len(team) == 0 && p.Playbook != nil {
			team = p.Playbook.Roles
		}
		role, err := addressee(team, to)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		out = TeamMessage{ID: uid(), To: role.Name, Kind: role.Kind, From: from, Text: message, Status: MessageWaiting, At: now}
		if role.Kind == RoleImplementer {
			if err := direct(v, t, &out, now); err != nil {
				return err
			}
		} else if len(t.Revisions) == 0 {
			return fmt.Errorf("there's nothing for %s to check until the first version is done: %w", role.Name, ErrConflict)
		}
		t.Messages = append(t.Messages, out)
		t.UpdatedAt = now
		record(v, now, projectID, "task.message", fmt.Sprintf("To %s about %s: %s", role.Name, t.Objective, text.Clip(message, 200)))
		return nil
	})
	return out, err
}

// addressee finds a team member by name, or by kind when only one member has
// it.
func addressee(team []Role, to string) (Role, error) {
	to = strings.TrimSpace(to)
	var names []string
	var byKind []Role
	for _, r := range team {
		if strings.EqualFold(r.Name, to) {
			return r, nil
		}
		if strings.EqualFold(r.Kind, to) {
			byKind = append(byKind, r)
		}
		names = append(names, r.Name)
	}
	if len(byKind) == 1 {
		return byKind[0], nil
	}
	if len(byKind) > 1 {
		var which []string
		for _, r := range byKind {
			which = append(which, r.Name)
		}
		return Role{}, fmt.Errorf("more than one team member is a %s; name one: %s", to, strings.Join(which, ", "))
	}
	return Role{}, fmt.Errorf("no one on this team is called %q; the team is %s", to, strings.Join(names, ", "))
}

// direct makes a message to the implementer part of the task's direction and
// moves the task on so the implementer sees it: at once when the task was
// waiting on the owner or on a pull request, otherwise in its next round.
func direct(v *Snapshot, t *Task, m *TeamMessage, now time.Time) error {
	if t.Status == TaskLanding {
		return fmt.Errorf("it is landing right now; message the implementer once it has landed or is waiting on its pull request: %w", ErrConflict)
	}
	entry := m.Text
	var open *Decision
	for i := range v.Decisions {
		if d := &v.Decisions[i]; t.Status == TaskWaiting && d.ID == t.DecisionID && d.Status == DecisionOpen {
			open = d
		}
	}
	if open != nil && open.Kind == DecisionQuestion {
		entry = "Answer to a reviewer's question (" + text.Clip(open.Context, 300) + "): " + m.Text
	}
	m.Direction = len(t.Direction)
	t.Direction = append(t.Direction, entry)
	t.DirectionPending++
	switch {
	case open != nil && open.Kind == DecisionFailure:
		// A retry that would go straight back to landing must not land
		// without the owner's direction.
		if t.ResumeStatus == TaskLanding {
			t.ResumeStatus = TaskWriting
			t.NextRound()
		}
	case open != nil:
		open.Status, open.Disposition, open.Answer, open.ResolvedAt = DecisionResolved, DispositionCustom, m.Text, &now
		record(v, now, open.ProjectID, "decision.resolved", open.Title+": "+m.Text)
		t.ReviseWithDirection()
	case t.Status == TaskAwaiting:
		t.ReviseWithDirection()
	}
	return nil
}

// ReviseWithDirection sends the task back to the implementer, in a new round,
// to take in what it was told.
func (t *Task) ReviseWithDirection() {
	t.NextRound()
	t.Status, t.DecisionID, t.Detail = TaskWriting, "", "Revising with your note"
}

// AnswerTeamMessage records a reviewer's or QA's reply to a message, in the
// same change as its verdict, so a restart never records a check twice. The
// verdict counts toward the task only while the task is being checked; later,
// a check that did not pass is added to the approval the owner is looking at.
func (s *Service) AnswerTeamMessage(ctx context.Context, taskID, messageID string, verdict *Verdict, failure string) error {
	return s.store.update(ctx, func(v *Snapshot) error {
		t := task(v, taskID)
		if t == nil {
			return ErrNotFound
		}
		var m *TeamMessage
		for i := range t.Messages {
			if t.Messages[i].ID == messageID {
				m = &t.Messages[i]
			}
		}
		if m == nil {
			return ErrNotFound
		}
		if !m.Open() {
			return nil
		}
		now := s.now().UTC()
		m.AnsweredAt = now
		if verdict == nil {
			m.Status, m.Reply = MessageFailed, text.Clip(failure, 1000)
			record(v, now, t.ProjectID, "task.message_failed", fmt.Sprintf("%s could not answer about %s", m.To, t.Objective))
			return nil
		}
		m.Status, m.Reply, m.Outcome, m.Revision = MessageAnswered, verdict.Summary, verdict.Outcome, verdict.Revision
		latest := 0
		if n := len(t.Revisions); n > 0 {
			latest = t.Revisions[n-1].N
		}
		switch {
		case (t.Status == TaskReviewing || t.Status == TaskDeciding) && verdict.Revision == latest:
			counted := *verdict
			counted.Asked = m.ID
			t.Verdicts = append(t.Verdicts, counted)
		case verdict.Outcome != VerdictPass && t.Status == TaskWaiting:
			for i := range v.Decisions {
				if d := &v.Decisions[i]; d.ID == t.DecisionID && d.Status == DecisionOpen && d.Approves() {
					d.Context += fmt.Sprintf("\n\nAsked directly, %s did not pass it: %s", m.To, verdict.Summary)
				}
			}
		}
		t.UpdatedAt = now
		record(v, now, t.ProjectID, "task.message_answered", fmt.Sprintf("%s answered about %s: %s", m.To, t.Objective, text.Clip(verdict.Summary, 200)))
		return nil
	})
}
