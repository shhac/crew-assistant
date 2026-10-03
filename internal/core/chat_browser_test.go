package core

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestBrowserNoteOnlyChangesRunningTurnAndSurvivesCompletion(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	if err := s.SetChatBrowserNote(ctx, "missing", "note"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	s.EnqueueChat(ctx, "turn", "hello")
	s.SetChatBrowserNote(ctx, "turn", "waiting")
	snap, _ := s.Snapshot(ctx)
	if snap.ChatTurns[0].BrowserNote != "" {
		t.Fatal("changed waiting turn")
	}
	s.StartNextChat(ctx, "claude")
	if err := s.SetChatBrowserNote(ctx, "turn", strings.Repeat("x", 4097)); err == nil {
		t.Fatal("accepted long note")
	}
	if err := s.SetChatBrowserNote(ctx, "turn", "Running without the browser."); err != nil {
		t.Fatal(err)
	}
	s.FinishChat(ctx, "turn", "completed", "reply", "")
	s.SetChatBrowserNote(ctx, "turn", "replacement")
	s.RecoverChatTurns(ctx)
	snap, _ = s.Snapshot(ctx)
	if snap.ChatTurns[0].BrowserNote != "Running without the browser." {
		t.Fatal(snap.ChatTurns[0])
	}
}
