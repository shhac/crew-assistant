package core

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// How a chat session was last opened.
const (
	SessionFresh   = "fresh"   // a new conversation in the CLI
	SessionResumed = "resumed" // the same conversation, picked up again
	SessionRebuilt = "rebuilt" // a new one, because the old could not be resumed
)

// ChatSession is the model session a conversation with the assistant runs on.
// The daemon keeps its reference so a restart resumes the same conversation in
// the CLI, and archives it with its conversation, so one picked up again from
// History resumes there too. The rest is what the owner is shown about it.
type ChatSession struct {
	Engine string `json:"engine"`
	Model  string `json:"model"`
	// Ref is the harness's reference to resume the session by. It names
	// folders on this machine and never leaves the daemon.
	Ref       json.RawMessage `json:"ref,omitempty"`
	StartedAt time.Time       `json:"started_at"`
	Opened    string          `json:"opened"`
	// SeenAt is the newest activity the assistant has been told about.
	SeenAt      time.Time `json:"seen_at,omitzero"`
	Compactions int       `json:"compactions,omitempty"`
	// What the latest turn reported: how full the context is, and how much of
	// its input the provider served from cache.
	ContextUsed   int64     `json:"context_used,omitempty"`
	ContextWindow int64     `json:"context_window,omitempty"`
	Input         int64     `json:"input,omitempty"`
	CachedInput   int64     `json:"cached_input,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// shown is the session as the dashboard gets it: without the reference.
func (c *ChatSession) shown() *ChatSession {
	if c == nil {
		return nil
	}
	out := *c
	out.Ref = nil
	return &out
}

// ChatSession is the current conversation's session, if it has one, and the
// conversation it belongs to.
func (s *Service) ChatSession(ctx context.Context) (*ChatSession, string, error) {
	v, err := s.store.Snapshot(ctx)
	if err != nil {
		return nil, "", err
	}
	return v.ChatSession, v.ConversationID, nil
}

// SaveChatSession records the session of the conversation named, or clears it
// when session is nil. It refuses when that is no longer the current
// conversation, so a turn finishing after /new or a switch from History never
// writes its session into another conversation.
func (s *Service) SaveChatSession(ctx context.Context, conversationID string, session *ChatSession) error {
	return s.store.update(ctx, func(v *Snapshot) error {
		if v.ConversationID != conversationID {
			return fmt.Errorf("the conversation changed: %w", ErrConflict)
		}
		if session != nil {
			saved := *session
			saved.UpdatedAt = s.now().UTC()
			session = &saved
		}
		v.ChatSession = session
		return nil
	})
}
