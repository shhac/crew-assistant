package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
)

func TestChatQueueIdempotencyContextAndAtomicMessages(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	first, err := s.EnqueueChat(ctx, "first", "Prepare the worker")
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := s.EnqueueChat(ctx, "first", first.Message)
	if err != nil || duplicate.ID != first.ID {
		t.Fatal(duplicate, err)
	}
	if _, err = s.EnqueueChat(ctx, "first", "Changed content"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err = s.EnqueueChat(ctx, "second", "PRIVATE FUTURE PROMPT"); err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(ctx)
	raw, _ := json.Marshal(snap)
	if len(snap.Messages) != 0 || strings.Contains(string(raw), "PRIVATE FUTURE PROMPT") {
		t.Fatal("queued message entered model snapshot", string(raw))
	}
	turn, err := s.StartNextChat(ctx)
	if err != nil || turn.ID != "first" || turn.UserMessageID == "" {
		t.Fatal(turn, err)
	}
	if _, err = s.CancelChat(ctx, "first"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err = s.StartNextChat(ctx); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err = s.RecordChatTool(ctx, turn.ID, "event-1", "set_team", "Choose the project's team", "running"); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordChatTool(ctx, turn.ID, "event-1", "set_team", "Choose the project's team", "completed"); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishChat(ctx, turn.ID, "completed", "The worker is ready.", ""); err != nil {
		t.Fatal(err)
	}
	turns, _ := s.ChatTurns(ctx)
	snap, _ = s.Snapshot(ctx)
	if len(snap.Messages) != 2 || turns[0].AssistantMessageID != snap.Messages[1].ID || turns[0].UserMessageID != snap.Messages[0].ID {
		t.Fatal(turns, snap.Messages)
	}
	if turns[0].Events[0].Label != "Choose the project's team" || turns[0].Events[0].FinishedAt == nil {
		t.Fatal(turns[0].Events)
	}
	if _, err = s.CancelChat(ctx, "second"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.StartNextChat(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestChatQueueBoundsAndSafeToolVocabulary(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		if _, err := s.EnqueueChat(ctx, fmt.Sprintf("msg-%d", i), "same repeated owner words"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.EnqueueChat(ctx, "extra", "hello"); !errors.Is(err, ErrChatQueueFull) {
		t.Fatal(err)
	}
	if _, err := s.EnqueueChat(ctx, "msg-0", "same repeated owner words"); err != nil {
		t.Fatal("idempotent retry must work even when full", err)
	}
	turn, _ := s.StartNextChat(ctx)
	if err := s.RecordChatTool(ctx, turn.ID, "event", "set_team", "", "running"); err == nil {
		t.Fatal("a tool event without a label was recorded")
	}
	if err := s.SetChatLoadingPhrase(ctx, turn.ID, strings.Repeat("x", 161)); err == nil {
		t.Fatal("unbounded phrase")
	}
	if err := s.SetChatLoadingPhrase(ctx, turn.ID, "Checking possibilities"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishChat(ctx, turn.ID, "failed", "", "Could not finish"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetChatLoadingPhrase(ctx, turn.ID, "Late callback"); err != nil {
		t.Fatal(err)
	}
	turns, _ := s.ChatTurns(ctx)
	if turns[0].LoadingPhrase != "" {
		t.Fatal("late phrase overwrote terminal state")
	}
}

func TestChatRestartInterruptsOnlyStartedTurn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	ctx := context.Background()
	cfg := config.Default()
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewService(st, cfg)
	s.EnqueueChat(ctx, "started", "Act once")
	s.EnqueueChat(ctx, "pending", "Act later")
	t1, err := s.StartNextChat(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordChatTool(ctx, t1.ID, "event", "set_team", "Choose the project's team", "running"); err != nil {
		t.Fatal(err)
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s = NewService(st, cfg)
	if err = s.RecoverChatTurns(ctx); err != nil {
		t.Fatal(err)
	}
	turns, _ := s.ChatTurns(ctx)
	if len(turns) != 2 || turns[0].Status != "interrupted" || turns[1].Status != "queued" || turns[0].Events[0].Status != "interrupted" {
		t.Fatal(turns)
	}
	next, err := s.StartNextChat(ctx)
	if err != nil || next.ID != "pending" {
		t.Fatal(next, err)
	}
	snap, _ := s.Snapshot(ctx)
	if len(snap.Messages) != 2 {
		t.Fatal("recovery replayed an owner turn", snap.Messages)
	}
}
