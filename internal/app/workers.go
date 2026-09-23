package app

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/diagnostics"
	"github.com/shhac/crew-assistant/internal/engine"
	"github.com/shhac/crew-assistant/internal/integrations/worker"
	"github.com/shhac/crew-assistant/internal/managedworkers"
)

type managedWorkerService interface {
	Prepare(context.Context, string, string, config.Model) (*worker.Client, error)
	Client(context.Context, string, string, config.Model) (*worker.Client, error)
	Close() error
}

func (a *App) managedWorkers() (managedWorkerService, error) {
	a.managedMu.Lock()
	defer a.managedMu.Unlock()
	if a.managed == nil {
		manager, err := managedworkers.New(a.Core.StateDirectory())
		if err != nil {
			return nil, err
		}
		manager.Diagnostics = a.Diagnostics
		manager.Admit = a.workerInferenceAdmission
		manager.TokenBudget = func() int64 { return a.Config().Limits.WorkerTokenBudget }
		a.managed = manager
	}
	return a.managed, nil
}
func (a *App) closeManagedWorkers() {
	a.managedMu.Lock()
	defer a.managedMu.Unlock()
	if a.managed != nil {
		_ = a.managed.Close()
		a.managed = nil
	}
}

// PrepareWorker gives the PA a constrained setup operation, never a host shell.
// It enrolls only a directory already attached to this project and creates no run.
func (a *App) PrepareWorker(ctx context.Context, projectID, workspace string) (config.Worker, error) {
	if a.Demo || a.dispatchDisabled.Load() {
		return config.Worker{}, errors.New("worker setup is disabled for this session")
	}
	a.prepareMu.Lock()
	defer a.prepareMu.Unlock()
	snapshot, err := a.Core.Snapshot(ctx)
	if err != nil {
		return config.Worker{}, err
	}
	if snapshot.Paused {
		return config.Worker{}, errors.New("coordination is paused")
	}
	var project *core.Project
	for i := range snapshot.Projects {
		if snapshot.Projects[i].ID == projectID {
			project = &snapshot.Projects[i]
			break
		}
	}
	if project == nil {
		return config.Worker{}, core.ErrNotFound
	}
	if workspace == "" && len(project.Directories) == 1 {
		workspace = project.Directories[0]
	}
	if workspace == "" || !slices.Contains(project.Directories, workspace) {
		return config.Worker{}, errors.New("choose the linked project folder to use for this work")
	}
	profile := config.Worker{ID: "managed-" + project.ID, Name: "Worker for " + project.Title, ProjectID: project.ID, Workspace: workspace, Managed: true, Capabilities: []string{"implement", "review"}}
	cfg := a.Config()
	for _, w := range cfg.Workers {
		if w.ID == profile.ID {
			profile.ModelProfile = w.ModelProfile
			profile.Name = w.Name
		}
		if w.ID == profile.ID && (!w.Managed || w.Workspace != workspace) {
			return config.Worker{}, errors.New("this project already has a worker bound to a different workspace")
		}
	}
	preflight := a.workerPreflight
	if preflight == nil {
		preflight = checkWorkerModel
	}
	if err := preflight(ctx, workerModel(cfg, profile)); err != nil {
		a.Diagnostics.Failure(diagnostics.Event{Component: "worker", Stage: "model_setup", ProjectID: projectID, Engine: workerModel(cfg, profile).Engine}, err)
		return config.Worker{}, err
	}
	manager, err := a.managedWorkers()
	if err != nil {
		return config.Worker{}, err
	}
	if _, err = manager.Prepare(ctx, project.ID, workspace, workerModel(cfg, profile)); err != nil {
		a.Diagnostics.Failure(diagnostics.Event{Component: "worker", Stage: "runtime_setup", ProjectID: projectID}, err)
		return config.Worker{}, err
	}
	cfg = a.Config()
	cfg.Workers = slices.Clone(cfg.Workers)
	found := false
	for i, w := range cfg.Workers {
		if w.ID == profile.ID {
			cfg.Workers[i] = profile
			found = true
			break
		}
	}
	if !found {
		cfg.Workers = append(cfg.Workers, profile)
	}
	if err = a.UpdateConfig(cfg); err != nil {
		return config.Worker{}, err
	}
	if err = a.Core.RecordActivity(ctx, project.ID, "worker.prepared", fmt.Sprintf("Private worker prepared for %s; no project work started", project.Title)); err != nil {
		return config.Worker{}, err
	}
	return profile, nil
}

func workerModel(cfg config.Config, profile config.Worker) config.Model {
	if profile.ModelProfile != nil {
		return *profile.ModelProfile
	}
	return cfg.WorkerModel
}

func checkWorkerModel(ctx context.Context, model config.Model) error {
	if model.Engine != "codex" && model.Engine != "claude" {
		return model.Validate()
	}
	options, err := engine.DiscoverModels(ctx, engine.Config{Engine: model.Engine, CodexBin: model.CodexBin, CodexHome: model.CodexHome, ClaudeBin: model.ClaudeBin, ClaudeHome: model.ClaudeHome})
	if err != nil {
		return err
	}
	for _, option := range options {
		if option.ID != model.Model {
			continue
		}
		if model.Effort == "" {
			return nil
		}
		for _, effort := range option.Efforts {
			if effort.ID == model.Effort {
				return nil
			}
		}
		return errors.New("the selected worker model does not offer that effort; choose an available effort in Settings")
	}
	return errors.New("the selected worker model is not available to this CLI login; choose an available worker model in Settings")
}
