package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
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

// The assistant sets how QA runs a code project's app as the owner says, and
// takes it away with nothing given.
func TestTheAssistantSetsHowQARunsTheApp(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	p, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Shop", Directories: []string{t.TempDir()}, Brief: core.BriefInput{Goal: "Sell"}})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"project_id": p.ID, "template": "code", "writer_engine": "", "reviewer_engine": "", "max_rounds": "", "deliver_to": "", "repo": "", "branch_prefix": "", "check": "make check", "sign": "", "check_in_copy": "", "implementer_member": "", "reviewer_member": "", "qa_member": "", "researcher_member": "", "designer_member": "", "pm_member": "", "prepare": []string{}})
	if _, err := a.Execute(ctx, "set_team", payload); err != nil {
		t.Fatal(err)
	}
	payload, _ = json.Marshal(map[string]any{"project_id": p.ID, "setup": "", "start": "npm start", "url": "http://localhost:{port}/", "ready": ""})
	result, err := a.Execute(ctx, "set_run_recipe", payload)
	if err != nil {
		t.Fatal(err)
	}
	if p = result.(core.Project); p.Playbook.Run == nil || p.Playbook.Run.Start != "npm start" {
		t.Fatalf("recipe %+v", p.Playbook.Run)
	}
	payload, _ = json.Marshal(map[string]any{"project_id": p.ID, "setup": "", "start": "", "url": "", "ready": ""})
	if result, err = a.Execute(ctx, "set_run_recipe", payload); err != nil || result.(core.Project).Playbook.Run != nil {
		t.Fatalf("taking it away: %v", err)
	}
}

// The assistant renames a project when the owner asks, and the rename is
// recorded like its other project changes.
func TestTheAssistantRenamesAProject(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	p, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Shop"})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"project_id": p.ID, "title": "Corner shop"})
	result, err := a.Execute(ctx, "rename_project", payload)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.(core.Project); got.Title != "Corner shop" || got.Prefix != p.Prefix {
		t.Fatalf("renamed %+v", got)
	}
	s, _ := a.Snapshot(ctx)
	if !slices.ContainsFunc(s.Activity, func(e core.Activity) bool {
		return e.Kind == "project.renamed" && e.ProjectID == p.ID && e.Summary == "Renamed from “Shop” to “Corner shop”"
	}) {
		t.Fatalf("activity %+v", s.Activity)
	}
	payload, _ = json.Marshal(map[string]any{"project_id": p.ID, "title": " "})
	if _, err := a.Execute(ctx, "rename_project", payload); err == nil {
		t.Fatal("renamed a project to nothing")
	}
}
