package engine

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/testutil"
)

func largeChatHistory() []Message {
	var history []Message
	for i := 0; i < 6; i++ {
		history = append(history, Message{Role: "user", Content: "Keep the owner-approved scope."}, Message{Role: "assistant", Content: strings.Repeat("Prior reported evidence. ", 700)})
	}
	return history
}
func TestChatCompactionArchivesBeforeFurtherInferenceAndCountsSummary(t *testing.T) {
	archived, reservations, calls := false, 0, 0
	remote := testutil.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var in struct {
			Messages []Message `json:"messages"`
			Tools    []Tool    `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
		}
		if len(in.Tools) == 0 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"Older reports were summarized. Verify their claims against current state; no success inferred."}}],"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15}}`))
			return
		}
		if !archived {
			t.Error("compacted inference preceded durable archive hook")
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"I retained the scoped outcome and latest evidence."}}],"usage":{"prompt_tokens":8,"completion_tokens":2,"total_tokens":10}}`))
	}))
	defer remote.Close()
	e, err := New(Config{Endpoint: remote.URL, Model: "fixture", BeforeRequest: func(context.Context) error { reservations++; return nil }, OnContext: func(_ context.Context, cp ContextCheckpoint, original []Message) error {
		if !cp.Compacted || contextBytes(original) != cp.BeforeBytes || cp.AfterBytes >= cp.BeforeBytes {
			t.Fatal("invalid checkpoint", cp)
		}
		if !strings.Contains(original[0].Content, "Never implement project work") {
			t.Fatal("archive lost contract")
		}
		archived = true
		return nil
	}}, ExecutorFunc(func(context.Context, string, json.RawMessage) (any, error) {
		t.Fatal("summary executed tool")
		return nil, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := e.Chat(context.Background(), Request{Message: "Continue within the agreed scope", History: largeChatHistory()})
	if err != nil || !archived || calls != 2 || reservations != 2 || result.Usage.TotalTokens != 25 || !result.Usage.Known {
		t.Fatal(result, err, archived, calls, reservations)
	}
}
func TestChatArchiveFailureStopsAfterToolsDisabledSummary(t *testing.T) {
	calls := 0
	remote := testutil.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"Checkpoint; no success inferred."}}]}`))
	}))
	defer remote.Close()
	expected := errors.New("archive unavailable")
	e, err := New(Config{Endpoint: remote.URL, Model: "fixture", OnContext: func(context.Context, ContextCheckpoint, []Message) error { return expected }}, ExecutorFunc(func(context.Context, string, json.RawMessage) (any, error) { return nil, nil }))
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Chat(context.Background(), Request{Message: "Continue", History: largeChatHistory()})
	if !errors.Is(err, expected) || calls != 1 {
		t.Fatal("continued without archive", err, calls)
	}
}
