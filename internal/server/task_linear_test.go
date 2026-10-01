package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
)

func TestOwnerTaskLinearRoutes(t *testing.T) {
	dir := t.TempDir()
	fake := `#!/bin/sh
case "$1" in
auth) printf '%s' '{"alias":"home"}' ;;
api) printf '%s' '{"issue":{"id":"22222222-2222-2222-2222-222222222222","identifier":"EX-1","title":"Work","url":"https://linear.app/issue/EX-1"}}' ;;
*) exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "lin"), []byte(fake), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	a, call := ownerApp(t)
	ctx := context.Background()
	cfg := a.Config()
	cfg.Connections = []config.Connection{{ID: "lin", Name: "Linear", Tool: "lin", Profiles: []string{"home"}}}
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	p, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Work", Template: "draft", Brief: core.BriefInput{Goal: "Work"}})
	if err != nil {
		t.Fatal(err)
	}
	task, err := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/projects/" + p.ID + "/tasks/" + task.ID + "/linear"
	w := call("POST", path, `{"connection_id":"lin","profile":"home","kind":"issue","ref":"EX-1"}`)
	var out core.Task
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.LinearLinks) != 1 || out.LinearLinks[0].By != core.LinkedByOwner {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call("DELETE", path+"/issue/"+out.LinearLinks[0].ID, "")
	out = core.Task{}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.LinearLinks) != 0 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("DELETE", path+"/issue/22222222-2222-2222-2222-222222222222", ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
	for _, body := range []string{
		`{"connection_id":"lin","profile":"home","kind":"issue","ref":"-EX-1"}`,
		`{"connection_id":"lin","profile":"outside","kind":"issue","ref":"EX-1"}`,
		`{"connection_id":"lin","profile":"home","kind":"bad","ref":"EX-1"}`,
	} {
		if w := call("POST", path, body); w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if w := call("POST", "/api/projects/"+p.ID+"/tasks/missing/linear", `{"kind":"issue","ref":"EX-1"}`); w.Code != 404 {
		t.Fatal(w.Code)
	}
}
