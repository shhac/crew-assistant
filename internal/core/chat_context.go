package core

import (
	"context"
	"errors"
	"strings"
	"time"
)

// ChatCheckpoint is derived memory, not owner authorization. Full messages remain
// in durable state; ThroughID identifies exactly the dialogue summarized.
type ChatCheckpoint struct {
	ThroughID string    `json:"through_id"`
	Summary   string    `json:"summary"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Service) SaveChatCheckpoint(ctx context.Context, expected, through, summary string) error {
	if strings.TrimSpace(summary) == "" || len(summary) > 16*1024 {
		return errors.New("invalid conversation checkpoint")
	}
	return s.store.update(ctx, func(v *Snapshot) error {
		if v.ChatCheckpoint.ThroughID != expected {
			return ErrConflict
		}
		prior, next := -1, -1
		for i, m := range v.Messages {
			if m.ID == expected {
				prior = i
			}
			if m.ID == through {
				next = i
			}
		}
		if expected != "" && prior < 0 {
			return errors.New("conversation checkpoint source is missing")
		}
		if next < 0 || next <= prior {
			return errors.New("conversation checkpoint cursor must advance")
		}
		if v.Messages[next].Role != "assistant" {
			return errors.New("conversation checkpoint must end at a completed exchange")
		}
		v.ChatCheckpoint = ChatCheckpoint{ThroughID: through, Summary: summary, CreatedAt: s.now().UTC()}
		return nil
	})
}
func (s *Service) SetChatModelStatus(ctx context.Context, id, status string, retryAt time.Time) error {
	if len(status) > 240 {
		return errors.New("model status too long")
	}
	return s.store.update(ctx, func(v *Snapshot) error {
		t := chatTurn(v, id)
		if t == nil {
			return ErrNotFound
		}
		if t.Status == "running" {
			t.ModelStatus = status
			t.RetryAt = retryAt
		}
		return nil
	})
}

// SetChatBrowserNote keeps the browser fallback visible after the reply finishes.
func (s *Service) SetChatBrowserNote(ctx context.Context, id, note string) error {
	if len(note) > 4096 {
		return errors.New("browser note too long")
	}
	return s.store.update(ctx, func(v *Snapshot) error {
		t := chatTurn(v, id)
		if t == nil {
			return ErrNotFound
		}
		if t.Status == "running" {
			t.BrowserNote = note
		}
		return nil
	})
}
