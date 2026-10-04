package core

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

type PMChatChange struct {
	Kind    string   `json:"kind"`
	Summary string   `json:"summary"`
	Tasks   []string `json:"tasks"`
}
type PMChatMessage struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	// Conversation separates transport threads from the dashboard conversation.
	Conversation string         `json:"conversation,omitempty"`
	From         string         `json:"from"`
	By           string         `json:"by,omitempty"`
	Text         string         `json:"text"`
	Status       string         `json:"status"`
	Error        string         `json:"error,omitempty"`
	Token        string         `json:"token,omitempty"`
	ReplyTo      string         `json:"reply_to,omitempty"`
	Changes      []PMChatChange `json:"changes,omitempty"`
	At           time.Time      `json:"at"`
	StartedAt    *time.Time     `json:"started_at,omitempty"`
	FinishedAt   *time.Time     `json:"finished_at,omitempty"`
}

func pmChat(v *Snapshot, projectID, id string) *PMChatMessage {
	for i := range v.PMChats {
		m := &v.PMChats[i]
		if m.ProjectID == projectID && m.ID == id {
			return m
		}
	}
	return nil
}
func (s *Service) SendPMChat(ctx context.Context, projectID, id, text string) (PMChatMessage, error) {
	return s.SendPMChatInConversation(ctx, projectID, id, text, "")
}

func (s *Service) SendPMChatInConversation(ctx context.Context, projectID, id, text, conversation string) (PMChatMessage, error) {
	if !validChatID(id) || strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > 24000 {
		return PMChatMessage{}, fmt.Errorf("message needs an id and 1–24000 characters: %w", ErrChatValidation)
	}
	if len(conversation) > 512 || strings.ContainsAny(conversation, "\r\n") {
		return PMChatMessage{}, ErrChatValidation
	}
	var out PMChatMessage
	err := s.store.update(ctx, func(v *Snapshot) error {
		p := project(v, projectID)
		if p == nil {
			return ErrNotFound
		}
		if m := pmChat(v, projectID, id); m != nil {
			if m.Text != text || m.From != "owner" || m.Conversation != conversation {
				return ErrConflict
			}
			out = *m
			return nil
		}
		if _, ok := p.PMSeat(); !ok {
			return fmt.Errorf("This project has no PM now: %w", ErrConflict)
		}
		pending := 0
		for _, m := range v.PMChats {
			if m.ProjectID == projectID && (m.Status == "waiting" || m.Status == "working") {
				pending++
			}
		}
		if pending >= 5 {
			return fmt.Errorf("Five messages are waiting; wait for a reply: %w", ErrConflict)
		}
		out = PMChatMessage{ID: id, ProjectID: projectID, Conversation: conversation, From: "owner", Text: text, Status: "waiting", At: s.now().UTC()}
		v.PMChats = append(v.PMChats, out)
		trimPMChats(v, projectID)
		return nil
	})
	return out, err
}
func (s *Service) PMChat(ctx context.Context, projectID string) ([]PMChatMessage, error) {
	v, err := s.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	if project(&v, projectID) == nil {
		return nil, ErrNotFound
	}
	out := []PMChatMessage{}
	for _, m := range v.PMChats {
		if m.ProjectID == projectID {
			m.Token = ""
			out = append(out, m)
		}
	}
	return out, nil
}
func (s *Service) ClaimPMChat(ctx context.Context, projectID string, admit Admit) (PMChatMessage, Claim, Role, bool, error) {
	var out PMChatMessage
	var c Claim
	var seat Role
	var ok bool
	err := s.store.update(ctx, func(v *Snapshot) error {
		if s.upgradeDraining {
			return nil
		}
		p := project(v, projectID)
		if p == nil {
			return ErrNotFound
		}
		for i := range v.PMChats {
			m := &v.PMChats[i]
			if m.ProjectID != projectID || m.Status != "waiting" {
				continue
			}
			if _, has := p.PMSeat(); !has {
				m.Status = "failed"
				m.Error = "This project has no PM now"
				now := s.now().UTC()
				m.FinishedAt = &now
				continue
			}
			var err error
			c, seat, ok, err = s.takePM(v, projectID, StepPMChat, admit)
			if err != nil || !ok {
				return err
			}
			p.Claims[len(p.Claims)-1].Message = m.ID
			now := s.now().UTC()
			m.Status = "working"
			m.Token = c.Token
			m.By = seat.Name
			m.StartedAt = &now
			out = *m
			return nil
		}
		return nil
	})
	return out, c, seat, ok, err
}
func (s *Service) AnswerPMChat(ctx context.Context, projectID, id, token, by, reply string, changes []PMChatChange) error {
	if strings.TrimSpace(reply) == "" {
		return fmt.Errorf("PM gave no reply: %w", ErrChatValidation)
	}
	return s.finishPMChat(ctx, projectID, id, token, by, reply, "", changes)
}
func (s *Service) FailPMChat(ctx context.Context, projectID, id, token, reason string, changes []PMChatChange) error {
	return s.finishPMChat(ctx, projectID, id, token, "", "", reason, changes)
}
func (s *Service) finishPMChat(ctx context.Context, projectID, id, token, by, reply, reason string, changes []PMChatChange) error {
	return s.store.update(FencedProject(ctx, projectID, token), func(v *Snapshot) error {
		m := pmChat(v, projectID, id)
		if m == nil {
			return ErrNotFound
		}
		if m.Status != "working" || m.Token != token {
			return ErrStale
		}
		now := s.now().UTC()
		m.FinishedAt = &now
		m.Status = "answered"
		m.Error = reason
		if reason != "" {
			m.Status = "failed"
			m.Changes = append(m.Changes, changes...)
		} else {
			v.PMChats = append(v.PMChats, PMChatMessage{ID: uid(), ProjectID: projectID, Conversation: m.Conversation, From: "pm", By: by, Text: reply, Status: "answered", ReplyTo: id, Changes: changes, At: now})
		}
		trimPMChats(v, projectID)
		return nil
	})
}
func (s *Service) RetryPMChat(ctx context.Context, projectID, id string) (PMChatMessage, error) {
	var out PMChatMessage
	err := s.store.update(ctx, func(v *Snapshot) error {
		p := project(v, projectID)
		if p == nil {
			return ErrNotFound
		}
		m := pmChat(v, projectID, id)
		if m == nil {
			return ErrNotFound
		}
		if m.Status == "waiting" {
			out = *m
			return nil
		}
		if m.Status != "failed" {
			return ErrConflict
		}
		if _, ok := p.PMSeat(); !ok {
			return ErrConflict
		}
		pending := 0
		for _, o := range v.PMChats {
			if o.ProjectID == projectID && (o.Status == "waiting" || o.Status == "working") {
				pending++
			}
		}
		if pending >= 5 {
			return ErrConflict
		}
		m.Status = "waiting"
		m.Error = ""
		m.Token = ""
		m.StartedAt = nil
		m.FinishedAt = nil
		out = *m
		return nil
	})
	return out, err
}
func (s *Service) stopPMChat(v *Snapshot, projectID, token string) {
	for i := range v.PMChats {
		m := &v.PMChats[i]
		if m.ProjectID == projectID && m.Token == token && m.Status == "working" {
			now := s.now().UTC()
			m.Status = "failed"
			m.Error = "Stopped before " + m.By + " replied"
			m.FinishedAt = &now
		}
	}
}
func trimPMChats(v *Snapshot, projectID string) {
	count := 0
	for _, m := range v.PMChats {
		if m.ProjectID == projectID {
			count++
		}
	}
	for i := 0; i < len(v.PMChats) && count > 200; {
		m := v.PMChats[i]
		if m.ProjectID == projectID && m.Status != "waiting" && m.Status != "working" {
			v.PMChats = slices.Delete(v.PMChats, i, i+1)
			count--
		} else {
			i++
		}
	}
}
