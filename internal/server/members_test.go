package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
)

// A member's subagents are kept as the owner sets them, refused on an engine
// whose sandboxed sessions can't start them, and the dashboard is told where
// they are offered.
func TestAMembersSubagentsRoundTrip(t *testing.T) {
	if !config.Supports("claude", config.UseSubagents) {
		t.Skip("no sandboxed subagents on this platform")
	}
	_, call := ownerServer(t)
	w := call("POST", "/api/members", `{"name":"Ada","kinds":["implementer"],"engine":"claude","subagents":true}`)
	var ada core.Member
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &ada) != nil || !ada.Subagents {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("GET", "/api/state", ""); !strings.Contains(w.Body.String(), `"subagents":true`) {
		t.Fatal("the dashboard doesn't see the member's subagents:", w.Body.String())
	}
	if w := call("PUT", "/api/members/"+ada.ID, `{"name":"Ada","kinds":["implementer"],"engine":"codex","subagents":true}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"subagents":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("PUT", "/api/members/"+ada.ID, `{"name":"Ada","kinds":["implementer"],"engine":"codex"}`); w.Code != 200 || strings.Contains(w.Body.String(), `"subagents"`) {
		t.Fatal("subagents weren't switched off", w.Code, w.Body.String())
	}
	if w := call("POST", "/api/members", `{"name":"Gus","kinds":["implementer"],"engine":"openai-compatible","model":"m","subagents":true}`); w.Code != 400 || !strings.Contains(w.Body.String(), "subagents") {
		t.Fatal("subagents on an engine that can't start them", w.Code, w.Body.String())
	}
	var defaults struct {
		Choices []struct {
			Engine    string `json:"engine"`
			Subagents bool   `json:"subagents"`
		} `json:"choices"`
	}
	_ = json.Unmarshal(call("GET", "/api/config/defaults", "").Body.Bytes(), &defaults)
	offered := map[string]bool{}
	for _, c := range defaults.Choices {
		offered[c.Engine] = c.Subagents
	}
	if !offered["claude"] || !offered["codex"] || offered["grok"] || offered["openai-compatible"] {
		t.Fatalf("subagent choices %v", offered)
	}
}

func TestTheOwnerKeepsATeamOfMembers(t *testing.T) {
	_, call := ownerServer(t)
	w := call("POST", "/api/members", `{"name":"Ada","kind":"implementer","engine":"claude","model":"opus","instructions":"Small commits."}`)
	var ada core.Member
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &ada) != nil || ada.ID == "" {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("POST", "/api/members", `{"name":"ada","kind":"reviewer","engine":"codex"}`); w.Code != 400 || !strings.Contains(w.Body.String(), "already a member called Ada") {
		t.Fatal("a second Ada", w.Code, w.Body.String())
	}
	// A provider never applies to a CLI member.
	if w := call("PUT", "/api/members/"+ada.ID, `{"name":"Ada","kind":"reviewer","engine":"claude","provider":"unknown"}`); w.Code != 400 || !strings.Contains(w.Body.String(), "Provider is only") {
		t.Fatal(w.Code, w.Body.String())
	}

	if w := call("PUT", "/api/members/"+ada.ID, `{"name":"Ada","kind":"implementer","engine":"codex"}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"engine":"codex"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call("POST", "/api/members/"+ada.ID+"/learnings", `{"text":"Run the linter before finishing.","project_id":""}`)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &ada) != nil || len(ada.Learnings) != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("GET", "/api/state", ""); !strings.Contains(w.Body.String(), "Run the linter") || strings.Count(w.Body.String(), `"avatar_svg":"\u003csvg`) != 3 {
		t.Fatal("the dashboard should see members drawn:", w.Body.String())
	}
	if w := call("DELETE", "/api/members/"+ada.ID+"/learnings/"+ada.Learnings[0].ID, ""); w.Code != 200 || strings.Contains(w.Body.String(), "Run the linter") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("DELETE", "/api/members/"+ada.ID, ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("DELETE", "/api/members/"+ada.ID, ""); w.Code != 404 {
		t.Fatal("deleting a member twice", w.Code)
	}
}
