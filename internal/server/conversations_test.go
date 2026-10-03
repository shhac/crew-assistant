package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
)

func TestPastConversationsCanBeListedReadAndPickedUpAgain(t *testing.T) {
	s, call := ownerServer(t)
	// An unknown command is refused with what to type instead.
	if w := call("POST", "/api/chat/messages", `{"id":"typo","message":"/nwe"}`); w.Code != 400 || !strings.Contains(w.Body.String(), "/nwe isn't a command. Try /compact") {
		t.Fatal(w.Code, w.Body.String())
	}
	ctx := context.Background()
	for _, m := range []struct{ id, text string }{{"one", "A fictional topic"}, {"new", "/new"}} {
		if w := call("POST", "/api/chat/messages", `{"id":"`+m.id+`","message":"`+m.text+`"}`); w.Code != 202 {
			t.Fatal(w.Code, w.Body.String())
		}
		turn, err := s.StartNextChat(ctx, "claude")
		if err != nil {
			t.Fatal(err)
		}
		if turn.Command != "" {
			err = s.FinishChatCommand(ctx, turn.ID, "", "")
		} else {
			err = s.FinishChat(ctx, turn.ID, "completed", "A reply", "")
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	w := call("GET", "/api/chat/conversations", "")
	var list struct {
		Conversations []core.ConversationEntry `json:"conversations"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Conversations) != 1 || list.Conversations[0].Title != "A fictional topic" {
		t.Fatal(w.Body.String())
	}
	id := list.Conversations[0].ID
	if w := call("GET", "/api/chat/conversations/"+id, ""); w.Code != 200 || !strings.Contains(w.Body.String(), "A reply") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("GET", "/api/chat/conversations/missing", ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
	// The queue names the conversation its turns belong to, and the turns of
	// the one archived are gone from it.
	queue := func() core.ChatQueueView {
		t.Helper()
		var q core.ChatQueueView
		if err := json.Unmarshal(call("GET", "/api/chat/turns", "").Body.Bytes(), &q); err != nil {
			t.Fatal(err)
		}
		return q
	}
	fresh := queue()
	if len(fresh.Turns) != 0 || fresh.Conversation == "" || fresh.Conversation == id {
		t.Fatal(fresh)
	}
	state := call("GET", "/api/state", "").Body.String()
	if !strings.Contains(state, "Fresh start") || strings.Contains(state, "A reply") {
		t.Fatal("the archive leaked into state", state)
	}
	if w := call("POST", "/api/chat/conversations/"+id+"/resume", ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if state := call("GET", "/api/state", "").Body.String(); !strings.Contains(state, "A reply") {
		t.Fatal("the conversation was not picked up", state)
	}
	// Its turns are back, under its own name.
	if back := queue(); back.Conversation != id || len(back.Turns) != 2 || back.Turns[0].ID != "one" {
		t.Fatal(back)
	}
}
