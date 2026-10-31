package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
)

// The owner names the board lane each role works in; the name stays with
// the role when someone else fills it or the team is chosen again.
func TestTheOwnerNamesARolesLane(t *testing.T) {
	a, call := ownerApp(t)
	ctx := context.Background()
	p, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Book", Brief: core.BriefInput{Goal: "Write"}})
	if err != nil {
		t.Fatal(err)
	}
	if w := call("PUT", "/api/projects/"+p.ID+"/team", `{"template":"draft"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	lanes := func(path, body string) map[string]string {
		t.Helper()
		w := call("PUT", "/api/projects/"+p.ID+path, body)
		var project core.Project
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &project) != nil {
			t.Fatal(w.Code, w.Body.String())
		}
		out := map[string]string{}
		for _, r := range project.Playbook.Roles {
			out[r.Name] = r.Lane
		}
		return out
	}
	lanes("/team/implementer/lane", `{"lane":" Drafting "}`)
	if got := lanes("/team/reviewer/lane", `{"lane":"Editing"}`); got["Writer"] != "Drafting" || got["Reviewer"] != "Editing" {
		t.Fatalf("the lanes weren't named: %v", got)
	}
	nova, err := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Novelist", Kinds: []string{core.RoleImplementer}, Engine: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if got := lanes("/team/implementer", `{"member":"`+nova.ID+`"}`); got["Novelist"] != "Drafting" {
		t.Fatalf("the member filling the role lost its lane: %v", got)
	}
	if got := lanes("/team", `{"template":"draft","implementer_member":"`+nova.ID+`","max_rounds":"4"}`); got["Novelist"] != "Drafting" || got["Reviewer"] != "Editing" {
		t.Fatalf("choosing the team again lost its lanes: %v", got)
	}
	if w := call("PUT", "/api/projects/"+p.ID+"/team/reviewer/lane", `{"lane":"`+strings.Repeat("x", 25)+`"}`); w.Code != 400 {
		t.Fatal("a lane too long for its column was accepted", w.Code, w.Body.String())
	}
	if w := call("PUT", "/api/projects/"+p.ID+"/team/pm/lane", `{"lane":"Listing"}`); w.Code != 404 {
		t.Fatal("a lane was named for a role the team doesn't have", w.Code, w.Body.String())
	}
	if got := lanes("/team/reviewer/lane", `{"lane":""}`); got["Reviewer"] != "" {
		t.Fatalf("the lane didn't go back to its own name: %v", got)
	}
}
