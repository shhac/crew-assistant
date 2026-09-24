package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
)

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
	if w := call("PUT", "/api/members/"+ada.ID, `{"name":"Ada","kind":"implementer","engine":"codex"}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"engine":"codex"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call("POST", "/api/members/"+ada.ID+"/learnings", `{"text":"Run the linter before finishing.","project_id":""}`)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &ada) != nil || len(ada.Learnings) != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("GET", "/api/state", ""); !strings.Contains(w.Body.String(), "Run the linter") || strings.Count(w.Body.String(), `"avatar_svg":"\u003csvg`) != 2 {
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
