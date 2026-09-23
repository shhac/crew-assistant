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
	payload, _ := json.Marshal(map[string]any{"title": "Existing codebase", "objective": "", "acceptance_criteria": []string{}, "directories": []string{path}})
	result, err := a.Execute(context.Background(), "create_project", payload)
	if err != nil {
		t.Fatal(err)
	}
	p := result.(core.Project)
	if p.ContractDefined || len(p.Directories) != 1 || p.Directories[0] != path {
		t.Fatalf("project=%+v", p)
	}
	payload, _ = json.Marshal(map[string]any{"project_id": p.ID, "objective": "Requested outcome", "acceptance_criteria": []string{"Observable evidence"}, "directories": nil})
	result, err = a.Execute(context.Background(), "update_project", payload)
	if err != nil {
		t.Fatal(err)
	}
	p = result.(core.Project)
	if !p.ContractDefined || len(p.Directories) != 1 || p.ScratchDirectory == "" {
		t.Fatalf("refinement lost paths=%+v", p)
	}
	payload, _ = json.Marshal(map[string]any{"project_id": p.ID, "objective": "Requested outcome", "acceptance_criteria": []string{"Observable evidence"}, "directories": []string{}})
	result, err = a.Execute(context.Background(), "update_project", payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.(core.Project).Directories) != 0 {
		t.Fatal("explicit empty directories did not clear links")
	}
}
