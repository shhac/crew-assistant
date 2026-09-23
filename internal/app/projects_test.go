package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
)

func TestProjectToolsTrackAndRefineDirectoryMetadata(t *testing.T) {
	a := testApp(t)
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"title": "Existing codebase", "goal": "", "audience": "", "constraints": "", "template": "", "criteria": []string{}, "directories": []string{path}})
	result, err := a.Execute(context.Background(), "create_project", payload)
	if err != nil {
		t.Fatal(err)
	}
	p := result.(core.Project)
	if p.Brief.Version != 0 || p.Playbook != nil || len(p.Directories) != 1 || p.Directories[0] != path {
		t.Fatalf("project=%+v", p)
	}
	payload, _ = json.Marshal(map[string]any{"project_id": p.ID, "goal": "Requested outcome", "audience": "", "constraints": "", "criteria": []string{"Observable evidence"}})
	result, err = a.Execute(context.Background(), "update_brief", payload)
	if err != nil {
		t.Fatal(err)
	}
	p = result.(core.Project)
	if p.Brief.Version != 1 || len(p.Directories) != 1 || p.ScratchDirectory == "" {
		t.Fatalf("brief update lost paths=%+v", p)
	}
	payload, _ = json.Marshal(map[string]any{"project_id": p.ID, "template": "draft", "writer_engine": "codex", "reviewer_engine": "", "max_rounds": "2", "deliver_to": ""})
	result, err = a.Execute(context.Background(), "set_team", payload)
	if err != nil {
		t.Fatal(err)
	}
	p = result.(core.Project)
	if p.Playbook == nil || p.Playbook.MaxRounds != 2 || p.Playbook.Roles[0].Engine != "codex" || p.Playbook.Roles[1].Engine != "codex" {
		t.Fatalf("team=%+v", p.Playbook)
	}
}
