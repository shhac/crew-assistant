package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
)

func TestTheOwnerKeepsAssistantsFromTheDashboard(t *testing.T) {
	a, call := ownerApp(t)
	w := call("POST", "/api/assistants", `{"name":"Iris","personality":"Warm.","model":{"engine":"claude","model":"opus","effort":"","max_tokens":2048}}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var iris config.AssistantProfile
	json.Unmarshal(w.Body.Bytes(), &iris)
	if iris.ID == "" || iris.Name != "Iris" || iris.Model.Engine != "claude" {
		t.Fatal(w.Body.String())
	}
	if w := call("POST", "/api/assistants", `{"name":"iris","model":{"engine":"codex","max_tokens":2048}}`); w.Code != 400 || !strings.Contains(w.Body.String(), "already an assistant called Iris") {
		t.Fatal("a second Iris", w.Code, w.Body.String())
	}
	if w := call("POST", "/api/assistants", `{"name":"Moss","model":{"engine":"codex","max_tokens":2048},"seat":true}`); w.Code != 400 {
		t.Fatal("an unknown field", w.Code, w.Body.String())
	}
	if w := call("PUT", "/api/assistants/"+iris.ID, `{"name":"Iris","personality":"Exact.","model":{"engine":"openai-compatible","model":"local","effort":"","max_tokens":1024}}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var state struct {
		core.Snapshot
	}
	json.Unmarshal(call("GET", "/api/state", "").Body.Bytes(), &state)
	if len(state.Assistants) != 2 || state.Assistants[1].Personality != "Exact." || state.Assistants[1].Model.MaxTokens != 1024 || !strings.HasPrefix(state.Assistants[1].AvatarSVG, "<svg") || state.Assistant.ID != "milo" {
		t.Fatalf("%+v %+v", state.Assistant, state.Assistants)
	}
	// Choosing who sits in the seat is a config setting, as Settings saves it.
	cfg := a.Config()
	cfg.Assistant.Seat = iris.ID
	body, _ := json.Marshal(cfg)
	if w := call("PUT", "/api/config", string(body)); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("DELETE", "/api/assistants/"+iris.ID, ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if a.Config().Assistant.Seat != "" {
		t.Fatal("deleting the seated assistant should empty the seat")
	}
	if w := call("DELETE", "/api/assistants/"+iris.ID, ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if w := call("PUT", "/api/assistants/nobody", `{"name":"X","model":{"engine":"codex","max_tokens":2048}}`); w.Code != 404 {
		t.Fatal(w.Code)
	}
}

// Suggestions are kept for a new assistant and a new member apart, and for
// nothing else.
func TestSuggestionRoutesNameTheirSubject(t *testing.T) {
	_, call := ownerApp(t)
	for _, subject := range []string{"assistant", "member"} {
		if w := call("GET", "/api/setup/"+subject, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"messages":[]`) {
			t.Fatal(subject, w.Code, w.Body.String())
		}
		if w := call("DELETE", "/api/setup/"+subject, ""); w.Code != 200 {
			t.Fatal(subject, w.Code, w.Body.String())
		}
	}
	if w := call("GET", "/api/setup/project", ""); w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("POST", "/api/setup/apply", `{"recommendation_id":"x","accepted":true}`); w.Code != 404 && w.Code != 405 {
		t.Fatal("applying a suggestion straight to the config is gone", w.Code)
	}
}
