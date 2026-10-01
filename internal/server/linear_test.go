package server

import (
	"context"
	"encoding/json"
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"testing"
)

func TestOwnerProjectLinearRoutes(t *testing.T) {
	a, call := ownerApp(t)
	cfg := a.Config()
	cfg.Connections = []config.Connection{{ID: "lin", Name: "Linear", Tool: "lin", Profiles: []string{"home"}}, {ID: "slack", Name: "Slack", Tool: "agent-slack", Profiles: []string{"home"}}}
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	p, err := a.Core.CreateProject(context.Background(), core.ProjectInput{Title: "Writing", Template: "draft", Brief: core.BriefInput{Goal: "Write"}})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/projects/" + p.ID + "/linear"
	body := `{"connection_id":"lin","profile":"home","kind":"team","id":"11111111-1111-1111-1111-111111111111","name":"Team","rules":{"pick_up":true,"states":["Todo"],"assignee":"unassigned","users":[]}}`
	w := call("PUT", path, body)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &p) != nil || p.Linear == nil || p.Linear.Version != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call("DELETE", path, "")
	p.Linear = nil
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &p) != nil || p.Linear != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, path := range []string{"/api/connections/slack/linear/teams?profile=home", "/api/connections/lin/linear/teams?profile=outside"} {
		w = call("GET", path, "")
		if w.Code == 200 {
			t.Fatal("scope accepted", path)
		}
	}
	w = call("PUT", "/api/projects/"+p.ID+"/linear", `{"connection_id":"lin","profile":"outside"}`)
	if w.Code == 200 {
		t.Fatal("invalid link accepted")
	}
}
