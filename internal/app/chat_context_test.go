package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/engine"
	"github.com/shhac/crew-assistant/internal/testutil"
)

func TestChatHistoryCheckpointBatchesAndRetainsOriginals(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	for i := 0; i < 32; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		if _, err := a.Core.AddMessage(ctx, role, fmt.Sprintf("message-%02d", i)); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := a.Core.Snapshot(ctx)
	var calls atomic.Int32
	provider := testutil.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct {
			Messages []engine.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		var payload struct {
			Dialogue []engine.Message `json:"dialogue"`
		}
		if len(request.Messages) != 2 {
			t.Errorf("unexpected messages %d", len(request.Messages))
		} else if err := json.Unmarshal([]byte(request.Messages[1].Content), &payload); err != nil || len(payload.Dialogue) != 8 {
			t.Errorf("source %+v %v", payload, err)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"summary keeps goals and unresolved decisions"}}]}`))
	}))
	defer provider.Close()
	cfg := engine.Config{Endpoint: provider.URL, Model: "fixture"}
	if err := a.compactChatHistory(ctx, "", cfg); err != nil {
		t.Fatal(err)
	}
	after, _ := a.Core.Snapshot(ctx)
	if !reflect.DeepEqual(before.Messages, after.Messages) || after.ChatCheckpoint.ThroughID != before.Messages[7].ID {
		t.Fatal("lost original source or wrong boundary")
	}
	for _, role := range []string{"user", "assistant"} {
		_, _ = a.Core.AddMessage(ctx, role, "recent exchange")
	}
	if err := a.compactChatHistory(ctx, "", cfg); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("summary churn: %d calls", calls.Load())
	}
	raw, history, err := a.chatContext(ctx, "")
	if err != nil || len(history) != 26 || history[0].Content != "message-08" || !strings.Contains(string(raw), "summary keeps goals") {
		t.Fatalf("lost continuity %d %v", len(history), err)
	}
}
func TestChatSummaryByteLimitKeepsWholeExchanges(t *testing.T) {
	messages := make([]core.Message, 34)
	for i := range messages {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		messages[i] = core.Message{ID: fmt.Sprint(i), Role: role, Content: "small"}
	}
	messages[3].Content = strings.Repeat("large", 10*1024)
	source, through, err := chatSummaryBatch(messages, 0, "")
	if err != nil || len(source) != 2 || through != "1" {
		t.Fatalf("split exchange: %d %s %v", len(source), through, err)
	}
	messages[1].Content = messages[3].Content
	if _, _, err = chatSummaryBatch(messages, 0, ""); err == nil {
		t.Fatal("silently skipped oversized original")
	}
}
func TestChatCheckpointFailurePreservesHistory(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	for i := 0; i < 32; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		_, _ = a.Core.AddMessage(ctx, role, "source")
	}
	provider := testutil.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":""}}]}`))
	}))
	defer provider.Close()
	if err := a.compactChatHistory(ctx, "", engine.Config{Endpoint: provider.URL, Model: "fixture"}); err == nil {
		t.Fatal("accepted empty summary")
	}
	snap, _ := a.Core.Snapshot(ctx)
	if snap.ChatCheckpoint.ThroughID != "" || len(snap.Messages) != 32 {
		t.Fatal("failed summary altered history")
	}
}
func TestContextArchiveStoresCompleteSource(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	source := []engine.Message{{Role: "user", Content: "original owner words"}, {Role: "assistant", Content: "original reply"}}
	cp := engine.ContextCheckpoint{Compacted: true, Summary: "derived", Messages: []engine.Message{{Role: "assistant", Content: "derived"}}}
	if err := a.archiveContext(ctx, cp, source); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(a.Core.StateDirectory(), "context-checkpoint-*", "context.json"))
	if err != nil || len(files) != 1 {
		t.Fatal(files, err)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Transcript []engine.Message         `json:"transcript"`
		Checkpoint engine.ContextCheckpoint `json:"checkpoint"`
	}
	if err = json.Unmarshal(raw, &saved); err != nil || !reflect.DeepEqual(saved.Transcript, source) || saved.Checkpoint.Summary != "derived" {
		t.Fatal("incomplete archive", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err = a.archiveContext(canceled, cp, source); err == nil {
		t.Fatal("archive ignored cancellation")
	}
}
