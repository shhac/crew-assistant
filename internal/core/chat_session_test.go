package core

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestAConversationKeepsItsSessionWhenArchivedAndPickedUpAgain(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	exchange(t, s, "one", "Hello", "Hi")
	_, first, _ := s.ChatSession(ctx)
	ref := json.RawMessage(`{"engine":"claude","id":"abc"}`)
	if err := s.SaveChatSession(ctx, first, &ChatSession{Engine: "claude", Model: "opus", Ref: ref, Opened: SessionFresh}); err != nil {
		t.Fatal(err)
	}
	queue, _ := s.ChatQueue(ctx)
	if queue.Session == nil || queue.Session.Model != "opus" || queue.Session.Ref != nil {
		t.Fatalf("the dashboard should see the session without its reference: %+v", queue.Session)
	}
	// /new archives the conversation with its session and starts without one.
	s.EnqueueChat(ctx, "fresh", "/new")
	turn, _ := s.StartNextChat(ctx)
	s.FinishChatCommand(ctx, turn.ID, "", "Started afresh.")
	if session, second, _ := s.ChatSession(ctx); session != nil || second == first {
		t.Fatalf("a fresh conversation carried a session: %+v", session)
	}
	if err := s.SaveChatSession(ctx, first, &ChatSession{Engine: "claude"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a session was saved into a conversation no longer current: %v", err)
	}
	list, _ := s.Conversations(ctx)
	archived, _ := s.Conversation(ctx, list[0].ID)
	if archived.Session == nil || archived.Session.Ref != nil {
		t.Fatalf("the archive should show the session without its reference: %+v", archived.Session)
	}
	if err := s.ResumeConversation(ctx, archived.ID); err != nil {
		t.Fatal(err)
	}
	session, current, _ := s.ChatSession(ctx)
	if current != archived.ID || session == nil || string(session.Ref) != string(ref) {
		t.Fatalf("picking the conversation up should bring its session back: %+v", session)
	}
}
