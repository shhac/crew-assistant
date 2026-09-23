package app

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/integrations/worker"
)

type fakeManaged struct {
	prepared, recovered int
	model               config.Model
	fail                bool
}

func (f *fakeManaged) Prepare(_ context.Context, _, _ string, m config.Model) (*worker.Client, error) {
	f.prepared++
	f.model = m
	if f.fail {
		return nil, errors.New("setup unavailable")
	}
	return nil, nil
}
func (f *fakeManaged) Client(_ context.Context, _, _ string, m config.Model) (*worker.Client, error) {
	f.recovered++
	f.model = m
	return nil, nil
}
func (f *fakeManaged) Close() error { return nil }

func TestAssistantPreparesAndRecoversWorkerWithoutManualCredentials(t *testing.T) {
	ctx := context.Background()
	a := testApp(t)
	a.workerPreflight = func(context.Context, config.Model) error { return nil }
	fake := &fakeManaged{}
	a.managed = fake
	folder, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Personal project", Directories: []string{folder}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		raw, _ := json.Marshal(map[string]string{"project_id": p.ID, "workspace": ""})
		result, err := a.Execute(ctx, "prepare_worker", raw)
		if err != nil {
			t.Fatal(err)
		}
		profile := result.(WorkerDetail)
		if !profile.Managed || profile.ProjectID != p.ID || profile.Workspace != folder || profile.Name != "Worker for Personal project" || profile.Model == nil || profile.Model.Model != a.Config().WorkerModel.Model {
			t.Fatalf("unexpected managed profile %+v", profile)
		}
	}
	cfg := a.Config()
	if len(cfg.Workers) != 1 {
		t.Fatal("duplicate worker registrations", cfg.Workers)
	}
	if fake.model.CodexHome != cfg.Model.CodexHome || fake.model.Engine != "codex" {
		t.Fatal("worker did not share configured CLI login")
	}
	snapshot, err := a.Core.Snapshot(ctx)
	if err != nil || len(snapshot.Agents) != 0 {
		t.Fatal("setup commissioned project work", err)
	}
	saved, err := config.Load(a.configPath)
	if err != nil || len(saved.Workers) != 1 || !saved.Workers[0].Managed {
		t.Fatal("managed profile not persisted", err)
	}
	// An explicit worker override selects another CLI login without changing others.
	override := cfg.WorkerModel
	override.CodexHome = filepath.Join(t.TempDir(), "different-account")
	cfg.Workers[0].ModelProfile = &override
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := a.broker(ctx, cfg.Workers[0].ID); err != nil {
		t.Fatal(err)
	}
	if fake.recovered != 1 || fake.model.CodexHome != override.CodexHome {
		t.Fatal("specific login override not respected")
	}
}
func TestPreparationScopeAndFailuresDoNotGrantAuthority(t *testing.T) {
	ctx := context.Background()
	a := testApp(t)
	a.workerPreflight = func(context.Context, config.Model) error { return nil }
	fake := &fakeManaged{}
	a.managed = fake
	folder, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Project", Directories: []string{folder}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.PrepareWorker(ctx, p.ID, t.TempDir()); err == nil {
		t.Fatal("unlinked folder accepted")
	}
	if _, err := a.PrepareWorker(ctx, "missing", folder); err == nil {
		t.Fatal("unknown project accepted")
	}
	a.Demo = true
	if _, err := a.PrepareWorker(ctx, p.ID, folder); err == nil {
		t.Fatal("demo prepared")
	}
	a.Demo = false
	if err := a.Core.SetPaused(ctx, true); err != nil {
		t.Fatal(err)
	}
	if _, err := a.PrepareWorker(ctx, p.ID, folder); err == nil {
		t.Fatal("paused prepared")
	}
	if err := a.Core.SetPaused(ctx, false); err != nil {
		t.Fatal(err)
	}
	if fake.prepared != 0 {
		t.Fatal("rejected setup reached provider")
	}
	fake.fail = true
	if _, err := a.PrepareWorker(ctx, p.ID, folder); err == nil {
		t.Fatal("setup failure hidden")
	}
	if len(a.Config().Workers) != 0 {
		t.Fatal("failed setup registered worker")
	}
	// Descendant evidence cannot invoke host setup.
	raw, _ := json.Marshal(map[string]string{"project_id": p.ID, "workspace": folder})
	if _, err := (projectExecutor{app: a, projectID: p.ID}).Execute(ctx, "prepare_worker", raw); err == nil {
		t.Fatal("descendant enabled host setup")
	}
	a.SetNoDispatch()
	if _, err := a.PrepareWorker(ctx, p.ID, folder); err == nil {
		t.Fatal("disabled dispatch prepared")
	}
}
