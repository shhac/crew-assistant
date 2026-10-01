package core

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/shhac/crew-assistant/internal/text"
)

// AskAboutPR has a teammate answer something on the task's open pull
// request, asked by the seat from: a reviewer or QA answers with a check, the
// PM with what it decides, and either's reply is posted on the pull request.
func (s *Service) AskAboutPR(ctx context.Context, taskID, from, to, question string) (TeamMessage, error) {
	question = strings.TrimSpace(question)
	if question == "" || len(question) > 8*1024 {
		return TeamMessage{}, errors.New("a question is required and must be at most 8 KiB")
	}
	var out TeamMessage
	err := s.store.update(ctx, func(v *Snapshot) error {
		t := task(v, taskID)
		if t == nil {
			return ErrNotFound
		}
		if t.Finished() || !t.PROpen() {
			return fmt.Errorf("there is no open pull request to answer on: %w", ErrConflict)
		}
		role, err := addressee(t.Roles, to)
		if err != nil {
			return err
		}
		if role.Name == from {
			return fmt.Errorf("%s can't hand the pull request to itself: %w", from, ErrConflict)
		}
		kind := role.Working()
		if kind == RoleImplementer || (kind == "" && !role.Holds(RolePM)) {
			return fmt.Errorf("%s can't answer on the pull request; hand it to a reviewer, QA or the PM: %w", role.Name, ErrConflict)
		}
		if kind == "" {
			kind = RolePM
		}
		now := s.now().UTC()
		out = TeamMessage{ID: uid(), To: role.Name, Kind: kind, From: from, Text: question, Status: MessageWaiting, At: now, ForPR: true}
		t.Messages = append(t.Messages, out)
		t.UpdatedAt = now
		recordTask(v, now, t, "task.message", fmt.Sprintf("%s asked %s about the pull request for %s: %s", from, role.Name, t.Objective, text.Clip(question, 200)))
		return nil
	})
	return out, err
}

// PMOnPR is what the PM decided about a pull request it was handed: a
// reply to post there, direction for the implementer, and a question for
// the owner; any may be empty.
type PMOnPR struct {
	Reply       string `json:"reply"`
	Implementer string `json:"implementer"`
	AskOwner    string `json:"ask_owner"`
}

// AnswerPRAsPM records the PM's answer to a message about the pull request
// in one change: its reply queued for the pull request, its direction given
// to the implementer, its question opened for the owner. failure records a
// PM that couldn't answer.
func (s *Service) AnswerPRAsPM(ctx context.Context, taskID, messageID string, a PMOnPR, failure string) error {
	return s.store.update(ctx, func(v *Snapshot) error {
		t := task(v, taskID)
		if t == nil {
			return ErrNotFound
		}
		i := -1
		for j := range t.Messages {
			if t.Messages[j].ID == messageID {
				i = j
			}
		}
		if i < 0 {
			return ErrNotFound
		}
		m := &t.Messages[i]
		if !m.Open() {
			return nil
		}
		now := s.now().UTC()
		m.AnsweredAt = now
		if failure != "" {
			m.Status, m.Reply = MessageFailed, text.Clip(failure, 1000)
			recordTask(v, now, t, "task.message_failed", fmt.Sprintf("%s could not answer about the pull request for %s", m.To, t.Objective))
			return nil
		}
		said := strings.TrimSpace(strings.Join([]string{a.Reply, a.Implementer, a.AskOwner}, " "))
		m.Status, m.Reply = MessageAnswered, text.Clip(said, 1000)
		if reply := strings.TrimSpace(a.Reply); reply != "" {
			t.Post(PRPost{Body: text.Clip(reply, 4000), By: m.To})
		}
		if note := strings.TrimSpace(a.Implementer); note != "" && !t.Finished() {
			direction := TeamMessage{ID: uid(), To: implementerName(*t), Kind: RoleImplementer, From: m.To, Text: text.Clip(note, 4000), Status: MessageWaiting, At: now}
			if err := direct(v, t, &direction, now); err == nil {
				t.Messages = append(t.Messages, direction)
			}
		}
		if question := strings.TrimSpace(a.AskOwner); question != "" && t.Status != TaskWaiting {
			openTaskDecision(v, t, DecisionQuestion, DecisionInput{
				Title:          fmt.Sprintf("%s asks about the pull request for “%s”", m.To, t.Objective),
				Context:        text.Clip(question, 2000) + "\n\nThe implementer had asked: " + text.Clip(m.Text, 1000),
				Recommendation: "Answer, and the implementer takes it in",
			}, now)
		}
		t.UpdatedAt = now
		recordTask(v, now, t, "task.message_answered", fmt.Sprintf("%s answered about the pull request for %s: %s", m.To, t.Objective, text.Clip(said, 200)))
		derive(v, t)
		return nil
	})
}

func implementerName(t Task) string {
	if r := t.RolesOf(RoleImplementer); len(r) > 0 {
		return r[0].Name
	}
	return "Implementer"
}
