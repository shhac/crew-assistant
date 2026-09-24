package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ChatTurn is a durable owner message. Queued content is kept out of the model
// conversation until its turn starts; the client ID makes acceptance retryable.
type ChatTurn struct {
	ModelStatus        string     `json:"model_status,omitempty"`
	RetryAt            time.Time  `json:"retry_at,omitempty"`
	ID                 string     `json:"id"`
	Message            string     `json:"message"`
	Status             string     `json:"status"`
	CreatedAt          time.Time  `json:"created_at"`
	StartedAt          *time.Time `json:"started_at,omitempty"`
	FinishedAt         *time.Time `json:"finished_at,omitempty"`
	UserMessageID      string     `json:"user_message_id,omitempty"`
	AssistantMessageID string     `json:"assistant_message_id,omitempty"`
	Error              string     `json:"error,omitempty"`
	LoadingPhrase      string     `json:"loading_phrase,omitempty"`
	// Origin is empty for the owner's messages, or OriginWake for a turn the
	// daemon queued to deliver the wakes named in WakeIDs.
	Origin  string   `json:"origin,omitempty"`
	WakeIDs []string `json:"wake_ids,omitempty"`
	// Revision moves when the owner edits a queued message, so an edit that
	// lost a race with the daemon starting the turn can be refused.
	Revision int             `json:"revision"`
	Events   []ChatToolEvent `json:"events"`
}

// ChatToolEvent contains application-owned labels only. Arguments, output and
// model-generated tool-call IDs are deliberately absent from the UI contract.
type ChatToolEvent struct {
	ID         string     `json:"id"`
	Tool       string     `json:"tool"`
	Label      string     `json:"label"`
	Status     string     `json:"status"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

var ErrChatValidation = errors.New("invalid chat message")

var ErrChatQueueFull = errors.New("chat queue is full; wait for a message to finish")

func validChatID(id string) bool {
	if len(id) < 1 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func (s *Service) EnqueueChat(ctx context.Context, id, message string) (ChatTurn, error) {
	if !validChatID(id) {
		return ChatTurn{}, fmt.Errorf("message id must contain 1–128 letters, digits, hyphens or underscores: %w", ErrChatValidation)
	}
	if strings.TrimSpace(message) == "" || len(message) > 24000 {
		return ChatTurn{}, fmt.Errorf("message must contain 1–24000 characters: %w", ErrChatValidation)
	}
	out := ChatTurn{ID: id, Message: message, Status: "queued", CreatedAt: s.now().UTC(), Events: []ChatToolEvent{}}
	err := s.store.update(ctx, func(v *Snapshot) error {
		pending := 0
		for _, t := range v.ChatTurns {
			if t.ID == id {
				if t.Message != message {
					return fmt.Errorf("message id is already used with different content: %w", ErrConflict)
				}
				out = t
				return nil
			}
			if t.Status == "queued" || t.Status == "running" {
				pending++
			}
		}
		if pending >= 20 {
			return ErrChatQueueFull
		}
		v.ChatTurns = append(v.ChatTurns, out)
		v.ChatQueueRevision++
		return nil
	})
	return out, err
}

func (s *Service) ChatTurns(ctx context.Context) ([]ChatTurn, error) {
	v, err := s.store.Snapshot(ctx)
	if v.ChatTurns == nil {
		v.ChatTurns = []ChatTurn{}
	}
	return v.ChatTurns, err
}

// StartNextChat atomically claims the oldest pending turn and appends its user
// message. Cancellation and concurrent consumers cannot race that transition.
func (s *Service) StartNextChat(ctx context.Context) (ChatTurn, error) {
	var out ChatTurn
	err := s.store.update(ctx, func(v *Snapshot) error {
		for _, t := range v.ChatTurns {
			if t.Status == "running" {
				return ErrConflict
			}
		}
		now := s.now().UTC()
		for i := range v.ChatTurns {
			t := &v.ChatTurns[i]
			if t.Status != "queued" {
				continue
			}
			// Only the head of the queue is ever started, so holding it holds
			// everything behind it. Comparing against a turn already known to
			// be queued also means a hold naming a started or cancelled turn
			// blocks nothing.
			if live := liveHold(v, now); live != nil && live.TurnID == t.ID {
				return ErrChatHeld
			}
			t.Status = "running"
			t.StartedAt = &now
			t.UserMessageID = uid()
			v.ChatQueueRevision++
			if t.Origin == OriginWake {
				// The report is written now, so its delivered time is true.
				t.Message = wakeTurnMessage(v, t.WakeIDs, now)
			}
			v.Messages = append(v.Messages, Message{ID: t.UserMessageID, Role: "user", Content: t.Message, Origin: t.Origin, CreatedAt: now})
			out = *t
			return nil
		}
		return ErrNotFound
	})
	return out, err
}

func (s *Service) CancelChat(ctx context.Context, id string) (ChatTurn, error) {
	var out ChatTurn
	err := s.store.update(ctx, func(v *Snapshot) error {
		t := chatTurn(v, id)
		if t == nil {
			return ErrNotFound
		}
		if t.Status == "cancelled" {
			out = *t
			return nil
		}
		if t.Status != "queued" {
			return fmt.Errorf("only queued messages can be cancelled: %w", ErrConflict)
		}
		now := s.now().UTC()
		t.Status = "cancelled"
		v.ChatQueueRevision++
		t.FinishedAt = &now
		if t.Origin == OriginWake {
			settleWakeTurn(v, t, "cancelled", now)
		}
		out = *t
		return nil
	})
	return out, err
}

// FinishChat stores the reply and completion together, preventing a daemon crash
// from recording a reply without retiring its turn (or vice versa).
func (s *Service) FinishChat(ctx context.Context, id, status, reply, reason string) error {
	if status != "completed" && status != "failed" && status != "interrupted" {
		return errors.New("invalid chat completion status")
	}
	if status == "completed" && strings.TrimSpace(reply) == "" {
		return errors.New("completed chat needs a reply")
	}
	return s.store.update(ctx, func(v *Snapshot) error {
		t := chatTurn(v, id)
		if t == nil {
			return ErrNotFound
		}
		if t.Status != "running" {
			return ErrConflict
		}
		now := s.now().UTC()
		t.Status = status
		t.FinishedAt = &now
		t.Error = reason
		t.LoadingPhrase = ""
		t.ModelStatus = ""
		t.RetryAt = time.Time{}
		for j := range t.Events {
			if t.Events[j].Status == "running" {
				t.Events[j].Status = "interrupted"
				t.Events[j].FinishedAt = &now
			}
		}
		if status == "completed" {
			t.AssistantMessageID = uid()
			v.Messages = append(v.Messages, Message{ID: t.AssistantMessageID, Role: "assistant", Content: reply, CreatedAt: now})
		}
		if t.Origin == OriginWake {
			settleWakeTurn(v, t, status, now)
		}
		return nil
	})
}

// RecoverChatTurns never replays an interrupted action. Messages not yet started
// remain queued and are safe to process after the daemon restarts.
func (s *Service) RecoverChatTurns(ctx context.Context) error {
	return s.store.update(ctx, func(v *Snapshot) error {
		now := s.now().UTC()
		for i := range v.ChatTurns {
			t := &v.ChatTurns[i]
			if t.Status != "running" {
				continue
			}
			t.Status = "interrupted"
			t.FinishedAt = &now
			t.LoadingPhrase = ""
			t.ModelStatus = ""
			t.RetryAt = time.Time{}
			t.Error = "The assistant stopped before this turn finished. Recorded actions were preserved; the message was not replayed."
			if t.Origin == OriginWake {
				settleWakeTurn(v, t, "interrupted", now)
			}
			for j := range t.Events {
				if t.Events[j].Status == "running" {
					t.Events[j].Status = "interrupted"
					t.Events[j].FinishedAt = &now
				}
			}
		}
		return nil
	})
}

func (s *Service) SetChatLoadingPhrase(ctx context.Context, id, phrase string) error {
	phrase = strings.TrimSpace(phrase)
	if len(phrase) > 160 || strings.ContainsAny(phrase, "\n\r") {
		return errors.New("loading phrase must be a short single line")
	}
	return s.store.update(ctx, func(v *Snapshot) error {
		for i := range v.ChatTurns {
			if v.ChatTurns[i].ID == id {
				if v.ChatTurns[i].Status != "running" {
					return nil
				}
				v.ChatTurns[i].LoadingPhrase = phrase
				return nil
			}
		}
		return ErrNotFound
	})
}

// RecordChatTool records a tool the assistant used, by the label the owner
// sees. The caller vouches for the tool: an unknown name from the model is
// refused before it gets here, so it never reaches state.
func (s *Service) RecordChatTool(ctx context.Context, turnID, eventID, tool, label, status string) error {
	if !validChatID(eventID) || strings.TrimSpace(label) == "" || len(label) > 80 || !validChatID(tool) {
		return errors.New("invalid chat tool event")
	}
	if status != "running" && status != "completed" && status != "failed" {
		return errors.New("invalid chat tool status")
	}
	return s.store.update(ctx, func(v *Snapshot) error {
		t := chatTurn(v, turnID)
		if t == nil {
			return ErrNotFound
		}
		if t.Status != "running" {
			return ErrConflict
		}
		now := s.now().UTC()
		for j := range t.Events {
			e := &t.Events[j]
			if e.ID != eventID {
				continue
			}
			if e.Tool != tool || e.Status != "running" || status == "running" {
				return ErrConflict
			}
			e.Status = status
			e.FinishedAt = &now
			return nil
		}
		if status != "running" {
			return ErrNotFound
		}
		t.Events = append(t.Events, ChatToolEvent{ID: eventID, Tool: tool, Label: label, Status: status, StartedAt: now})
		return nil
	})
}

// OriginWake marks a chat turn and message the daemon wrote to deliver
// wake-ups to the assistant.
const OriginWake = "wake"

func chatTurn(v *Snapshot, id string) *ChatTurn {
	for i := range v.ChatTurns {
		if v.ChatTurns[i].ID == id {
			return &v.ChatTurns[i]
		}
	}
	return nil
}
