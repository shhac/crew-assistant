package server

import (
	"context"
	"encoding/json"
	"github.com/shhac/crew-assistant/internal/core"
	"testing"
)

func TestOwnerSetsAndClearsExternalBlockers(t *testing.T) {
	s, call := ownerServer(t)
	ctx := context.Background()
	p, err := s.CreateProject(ctx, core.ProjectInput{Title: "Writing", Template: "draft", Brief: core.BriefInput{Goal: "Write"}})
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.QueueTask(ctx, p.ID, core.TaskInput{Objective: "A request"})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/projects/" + p.ID + "/tasks/" + task.ID + "/blockers"
	w := call("POST", path, `{"kind":"manual","description":"the service is ready","landing_only":true}`)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &task) != nil || len(task.Blockers) != 1 || task.Blockers[0].By != core.LinkedByOwner || !task.Blockers[0].LandingOnly {
		t.Fatalf("POST: %d %s", w.Code, w.Body.String())
	}
	path += "/" + task.Blockers[0].ID
	w = call("DELETE", path, "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &task) != nil || task.Blockers[0].ClearedAt == nil {
		t.Fatalf("DELETE: %d %s", w.Code, w.Body.String())
	}
	if w := call("DELETE", path, ""); w.Code != 409 {
		t.Fatalf("second clear: %d", w.Code)
	}
}
