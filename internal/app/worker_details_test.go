package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
)

type editableManaged struct {
	fakeManaged
	resets     int
	resetError error
}

func (f *editableManaged) ResetIdle(string) error { f.resets++; return f.resetError }

func editableWorker(t *testing.T) (*App, core.Project, config.Worker, *editableManaged) {
	t.Helper()
	a := testApp(t)
	ctx := context.Background()
	folder, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Local work", Description: "Improve a fixture", AcceptanceCriteria: "Checks pass", Directories: []string{folder}})
	if err != nil {
		t.Fatal(err)
	}
	f := &editableManaged{}
	a.managed = f
	a.workerPreflight = func(context.Context, config.Model) error { return nil }
	w, err := a.PrepareWorker(ctx, p.ID, folder)
	if err != nil {
		t.Fatal(err)
	}
	return a, p, w, f
}

func TestWorkerDetailsRedactConnectionSettings(t *testing.T) {
	a, p, w, _ := editableWorker(t)
	cfg := a.Config()
	m := cfg.WorkerModel
	m.CodexHome = "/sensitive/login"
	m.ClaudeBin = "/private/bin/claude"
	cfg.Workers[0].ModelProfile = &m
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	details, err := a.WorkerDetails(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(details) != 1 || details[0].ID != w.ID || details[0].Model.Model != m.Model || details[0].ModelStatus != "configured" || !details[0].SettingsEditable {
		t.Fatal(details)
	}
	raw, _ := json.Marshal(details)
	for _, forbidden := range []string{"sensitive", "private/bin", "codex_home", "claude_bin", "endpoint", "api_key"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatal("leaked configuration", string(raw))
		}
	}
	all, err := a.WorkerDetails(context.Background(), "")
	if err != nil || len(all) != 1 {
		t.Fatal(all, err)
	}
	if _, err := a.WorkerDetails(context.Background(), "missing"); !errors.Is(err, core.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestWorkerUpdateKeepsLoginAndScopeAndReopensIdleRuntime(t *testing.T) {
	a, p, w, f := editableWorker(t)
	original := a.Config().WorkerModel
	a.workerPreflight = func(_ context.Context, m config.Model) error {
		if m.Engine != "claude" || m.Model != "opus" || m.ClaudeHome != original.ClaudeHome || m.CodexHome != original.CodexHome {
			t.Fatal(m)
		}
		return nil
	}
	result, err := a.UpdateWorker(context.Background(), p.ID, w.ID, WorkerUpdate{Name: "Builder", Engine: "claude", Model: "opus", Effort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Name != "Builder" || result.Model.Engine != "claude" || result.ModelStatus != "configured" || f.resets != 1 {
		t.Fatal(result, f.resets)
	}
	saved, err := config.Load(a.configPath)
	if err != nil {
		t.Fatal(err)
	}
	got := saved.Workers[0]
	if got.ModelProfile == nil || got.ModelProfile.ClaudeHome != original.ClaudeHome || got.Workspace != w.Workspace || got.ProjectID != w.ProjectID || !got.Managed {
		t.Fatal(got)
	}
	if saved.WorkerModel != original {
		t.Fatal("global worker defaults changed")
	}
	// Name-only changes need no runtime reset.
	if _, err := a.UpdateWorker(context.Background(), p.ID, w.ID, WorkerUpdate{Name: "Reviewer", Engine: "claude", Model: "opus", Effort: "high"}); err != nil {
		t.Fatal(err)
	}
	if f.resets != 1 {
		t.Fatal("name change reset runtime")
	}
}

func TestWorkerUpdateRejectsBusyOrUnverifiedWithoutSaving(t *testing.T) {
	for _, mode := range []string{"busy", "unverified", "broker-busy", "save-failure"} {
		t.Run(mode, func(t *testing.T) {
			a, p, w, f := editableWorker(t)
			before := a.Config()
			switch mode {
			case "busy":
				if _, err := a.Core.Delegate(context.Background(), core.DelegateInput{ProjectID: p.ID, ProfileID: w.ID, Task: "Review fixtures", AcceptanceCriteria: "Evidence recorded", Role: "worker", Capabilities: []string{"review"}}); err != nil {
					t.Fatal(err)
				}
			case "unverified":
				a.workerPreflight = func(context.Context, config.Model) error { return errors.New("private diagnostic") }
			case "broker-busy":
				f.resetError = errors.New("execution cleanup pending")
			case "save-failure":
				a.configPath = filepath.Join(t.TempDir(), "directory")
				if err := os.Mkdir(a.configPath, 0700); err != nil {
					t.Fatal(err)
				}
			}
			_, err := a.UpdateWorker(context.Background(), p.ID, w.ID, WorkerUpdate{Name: "New", Engine: "claude", Model: "opus", Effort: "high"})
			if err == nil || strings.Contains(err.Error(), "private diagnostic") {
				t.Fatal(err)
			}
			if a.Config().Workers[0].Name != before.Workers[0].Name || a.Config().Workers[0].ModelProfile != nil {
				t.Fatal("failed edit changed settings")
			}
			if mode == "busy" || mode == "unverified" {
				if f.resets != 0 {
					t.Fatal("rejected edit reset runtime")
				}
			}
		})
	}
}

func TestWorkerUpdatePreservesUnrelatedConcurrentConfigEdit(t *testing.T) {
	a, p, w, _ := editableWorker(t)
	a.workerPreflight = func(context.Context, config.Model) error {
		cfg := a.Config()
		cfg.Assistant.Name = "Changed elsewhere"
		return a.UpdateConfig(cfg)
	}
	if _, err := a.UpdateWorker(context.Background(), p.ID, w.ID, WorkerUpdate{Name: "Builder", Engine: "claude", Model: "opus", Effort: "high"}); err != nil {
		t.Fatal(err)
	}
	if a.Config().Assistant.Name != "Changed elsewhere" {
		t.Fatal("clobbered concurrent edit")
	}
}

func TestWorkerUpdateRejectsConcurrentProfileChanges(t *testing.T) {
	a, p, w, f := editableWorker(t)
	a.workerPreflight = func(context.Context, config.Model) error {
		cfg := a.Config()
		cfg.Workers = append([]config.Worker(nil), cfg.Workers...)
		cfg.Workers[0].Name = "Changed elsewhere"
		return a.UpdateConfig(cfg)
	}
	if _, err := a.UpdateWorker(context.Background(), p.ID, w.ID, WorkerUpdate{Name: "Builder", Engine: "claude", Model: "opus", Effort: "high"}); !errors.Is(err, core.ErrConflict) {
		t.Fatal(err)
	}
	if f.resets != 0 || a.Config().Workers[0].Name != "Changed elsewhere" {
		t.Fatal("clobbered worker edit")
	}
}
