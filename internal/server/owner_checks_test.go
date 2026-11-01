package server

import (
	"context"
	"encoding/json"
	"github.com/shhac/crew-assistant/internal/core"
	"testing"
)

func TestOwnerCheckRoutes(t *testing.T) {
	s, call := ownerServer(t)
	ctx := context.Background()
	p, err := s.CreateProject(ctx, core.ProjectInput{Title: "Checks", Template: "draft", Brief: core.BriefInput{Goal: "Build"}})
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Build", OwnerChecks: []string{"Live check", "Tag release"}})
	if err != nil {
		t.Fatal(err)
	}
	s.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) { t.Status = core.TaskLanded; return "Landed", nil })
	path := "/api/projects/" + p.ID + "/tasks/" + task.ID + "/owner-checks"
	for _, body := range []string{`{"step":"Live check","status":"skipped"}`, `{"step":"Live check","status":"skipped","note":"  "}`} {
		if w := call("POST", path, body); w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w := call("POST", path, `{"step":"Live check","status":"done"}`)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &task) != nil || len(task.CheckMarks) != 1 || task.CheckMarks[0].By != core.FromOwner {
		t.Fatal(w.Code, w.Body.String())
	}
	clear := "/api/projects/" + p.ID + "/owner-checks/clear"
	if w := call("POST", clear, `{"note":" "}`); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("POST", clear, `{"note":"old backlog"}`); w.Code != 200 || w.Body.String() != "{\"cleared\":1}\n" {
		t.Fatal(w.Code, w.Body.String())
	}
	var state core.Snapshot
	w = call("GET", "/api/state", "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &state) != nil || len(state.AfterLanding) != 0 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call("POST", path, `{"step":"Tag release","status":"outstanding"}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call("GET", "/api/state", "")
	json.Unmarshal(w.Body.Bytes(), &state)
	if len(state.AfterLanding) != 1 || state.AfterLanding[0].Step != "Tag release" {
		t.Fatal(state.AfterLanding)
	}
	for _, task := range state.Tasks {
		if task.ID == state.AfterLanding[0].TaskID {
			item := task.Checklist[1]
			if len(item.History) != 1 || !item.History[0].Bulk || item.History[0].By != core.FromOwner {
				t.Fatal(item)
			}
		}
	}
	if w := call("POST", "/api/projects/wrong/tasks/"+task.ID+"/owner-checks", `{"step":"Live check","status":"done"}`); w.Code != 404 {
		t.Fatal(w.Code)
	}
}
