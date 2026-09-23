package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
)

// WorkerModelSelection intentionally excludes connection and login details.
type WorkerModelSelection struct {
	Engine string `json:"engine"`
	Model  string `json:"model"`
	Effort string `json:"effort"`
}

type WorkerDetail struct {
	ID               string                `json:"id"`
	Name             string                `json:"name"`
	ProjectID        string                `json:"project_id"`
	Workspace        string                `json:"workspace"`
	Managed          bool                  `json:"managed"`
	Capabilities     []string              `json:"capabilities"`
	Model            *WorkerModelSelection `json:"model"`
	ModelStatus      string                `json:"model_status"`
	SettingsEditable bool                  `json:"settings_editable"`
	Detail           string                `json:"detail"`
}

type WorkerUpdate struct {
	Name   string `json:"name"`
	Engine string `json:"engine"`
	Model  string `json:"model"`
	Effort string `json:"effort"`
}

func projectWorkerBusy(snapshot core.Snapshot, projectID string) bool {
	for _, agent := range snapshot.Agents {
		if agent.ProjectID == projectID && agent.Status != "completed" && agent.Status != "cancelled" {
			return true
		}
	}
	return false
}

func workerDetail(cfg config.Config, profile config.Worker, busy, demo bool) WorkerDetail {
	d := WorkerDetail{ID: profile.ID, Name: profile.Name, ProjectID: profile.ProjectID, Workspace: profile.Workspace, Managed: profile.Managed, Capabilities: slices.Clone(profile.Capabilities), ModelStatus: "unavailable", Detail: "This external worker manages its own model settings."}
	if profile.Managed {
		m := workerModel(cfg, profile)
		d.Model = &WorkerModelSelection{Engine: m.Engine, Model: m.Model, Effort: m.Effort}
		d.ModelStatus = "configured"
		d.SettingsEditable = !busy && !demo
		d.Detail = "Configured model for the next assignment; preparing a worker does not start project work."
		if busy {
			d.Detail = "Finish or cancel existing project work before changing worker settings."
		}
		if demo {
			d.Detail = "Worker settings are read-only in the demo."
		}
	}
	return d
}

// WorkerDetails describes registered workers without starting brokers or model processes.
func (a *App) WorkerDetails(ctx context.Context, projectID string) ([]WorkerDetail, error) {
	snapshot, err := a.Core.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	found := projectID == ""
	for _, p := range snapshot.Projects {
		if p.ID == projectID {
			found = true
			break
		}
	}
	if !found {
		return nil, core.ErrNotFound
	}
	cfg := a.Config()
	result := []WorkerDetail{}
	for _, profile := range cfg.Workers {
		if projectID != "" && profile.ProjectID != projectID {
			// Shared external profiles belong here only once this project has
			// used them; registration alone does not enroll every project.
			used := slices.ContainsFunc(snapshot.Agents, func(agent core.Agent) bool {
				return agent.ProjectID == projectID && agent.ProfileID == profile.ID
			})
			if !used {
				continue
			}
		}
		result = append(result, workerDetail(cfg, profile, projectWorkerBusy(snapshot, profile.ProjectID), a.Demo))
	}
	return result, nil
}

// UpdateWorker changes only the display name and model selection of an idle,
// project-bound managed worker. Its login and execution authority are retained.
func (a *App) UpdateWorker(ctx context.Context, projectID, profileID string, in WorkerUpdate) (WorkerDetail, error) {
	if a.Demo {
		return WorkerDetail{}, errors.New("worker settings are read-only in the demo")
	}
	a.prepareMu.Lock()
	defer a.prepareMu.Unlock()
	original := a.Config()
	index := slices.IndexFunc(original.Workers, func(w config.Worker) bool { return w.ID == profileID && w.ProjectID == projectID })
	if index < 0 {
		return WorkerDetail{}, core.ErrNotFound
	}
	profile := original.Workers[index]
	if !profile.Managed {
		return WorkerDetail{}, errors.New("external worker settings are managed by their runtime")
	}
	in.Name, in.Engine, in.Model, in.Effort = strings.TrimSpace(in.Name), strings.TrimSpace(in.Engine), strings.TrimSpace(in.Model), strings.TrimSpace(in.Effort)
	if in.Name == "" || len(in.Name) > 200 {
		return WorkerDetail{}, errors.New("choose a worker name of at most 200 characters")
	}
	if in.Model == "" || len(in.Model) > 200 {
		return WorkerDetail{}, errors.New("choose an available worker model")
	}
	if in.Engine != "codex" && in.Engine != "claude" {
		return WorkerDetail{}, errors.New("choose a local Codex or Claude model")
	}
	model := workerModel(original, profile)
	model.Engine, model.Model, model.Effort = in.Engine, in.Model, in.Effort
	if err := model.Validate(); err != nil {
		return WorkerDetail{}, err
	}
	snapshot, err := a.Core.Snapshot(ctx)
	if err != nil {
		return WorkerDetail{}, err
	}
	found := false
	for _, p := range snapshot.Projects {
		if p.ID == projectID {
			found = true
			break
		}
	}
	if !found {
		return WorkerDetail{}, core.ErrNotFound
	}
	if projectWorkerBusy(snapshot, projectID) {
		return WorkerDetail{}, fmt.Errorf("finish or cancel existing project work before changing worker settings: %w", core.ErrConflict)
	}
	preflight := a.workerPreflight
	if preflight == nil {
		preflight = checkWorkerModel
	}
	if err := preflight(ctx, model); err != nil {
		return WorkerDetail{}, errors.New("the selected worker model or effort could not be verified; check the CLI login and choose an available model")
	}
	// Discovery can be slow. Recheck durable work and compare only the settings
	// used above, preserving unrelated configuration edits made during discovery.
	snapshot, err = a.Core.Snapshot(ctx)
	if err != nil {
		return WorkerDetail{}, err
	}
	if projectWorkerBusy(snapshot, projectID) {
		return WorkerDetail{}, fmt.Errorf("project work was commissioned while editing: %w", core.ErrConflict)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	current := a.cfg
	at := slices.IndexFunc(current.Workers, func(w config.Worker) bool { return w.ID == profileID })
	if at < 0 || !reflect.DeepEqual(current.Workers[at], profile) || workerModel(current, current.Workers[at]) != workerModel(original, profile) {
		return WorkerDetail{}, fmt.Errorf("worker settings changed; reload before saving: %w", core.ErrConflict)
	}
	updated := profile
	updated.Name, updated.ModelProfile = in.Name, &model
	current.Workers = slices.Clone(current.Workers)
	current.Workers[at] = updated
	if err := current.Validate(); err != nil {
		return WorkerDetail{}, err
	}
	if model != workerModel(original, profile) {
		a.managedMu.Lock()
		manager := a.managed
		a.managedMu.Unlock()
		if manager != nil {
			resetter, ok := manager.(interface{ ResetIdle(string) error })
			if !ok {
				return WorkerDetail{}, errors.New("this worker runtime cannot safely change model settings while connected")
			}
			if err := resetter.ResetIdle(projectID); err != nil {
				return WorkerDetail{}, err
			}
		}
	}
	if err := a.updateConfigLocked(current); err != nil {
		return WorkerDetail{}, err
	}
	return workerDetail(current, updated, false, false), nil
}
