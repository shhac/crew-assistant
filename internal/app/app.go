// Package app composes the coordination model with the deterministic core.
package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/diagnostics"
	"github.com/shhac/crew-assistant/internal/engine"
	"github.com/shhac/crew-assistant/internal/integrations/connections"
	"github.com/shhac/crew-assistant/internal/quota"
)

type App struct {
	Diagnostics      *diagnostics.Logger // Set before starting the daemon.
	workerDiscover   func(context.Context, engine.Config) ([]engine.ModelOption, error)
	workerUsage      quota.Meter
	loadingComplete  func(context.Context, engine.Config, []engine.Message, []engine.Tool) (engine.Message, engine.Usage, error)
	loadingDiscover  func(context.Context, engine.Config) ([]engine.ModelOption, error)
	workerPreflight  func(context.Context, config.Model) error
	managedMu        sync.Mutex
	managed          managedWorkerService
	prepareMu        sync.Mutex
	connectionClient connections.Client
	dispatchDisabled atomic.Bool
	Core             *core.Service
	mu               sync.RWMutex
	cfg              config.Config
	configPath       string
	Demo             bool
	chat             chan struct{}
	chatWake         chan struct{}
	chatRunning      atomic.Bool
	chatFailed       atomic.Bool
	chatWaiters      sync.Map
	chatInvoker      func(context.Context, engine.Config, engine.Request, engine.ToolExecutor) (engine.Result, error)
	statuses         map[string]core.Integration
	artifactKey      artifactSecret
}

func New(s *core.Service, cfg config.Config, path string, demo bool) *App {
	return &App{connectionClient: connections.New(), Core: s, cfg: cfg, configPath: path, Demo: demo, chat: make(chan struct{}, 1), chatWake: make(chan struct{}, 1), statuses: map[string]core.Integration{}}
}
func (a *App) Config() config.Config { a.mu.RLock(); defer a.mu.RUnlock(); return a.cfg }
func (a *App) UpdateConfig(cfg config.Config) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.updateConfigLocked(cfg)
}

// updateConfigLocked requires a.mu to preserve atomic read-modify-write updates.
func (a *App) updateConfigLocked(cfg config.Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	oldNetwork, _ := json.Marshal(a.cfg.Dashboard)
	newNetwork, _ := json.Marshal(cfg.Dashboard)
	oldSlack, _ := json.Marshal(a.cfg.Slack)
	newSlack, _ := json.Marshal(cfg.Slack)
	if !bytes.Equal(oldNetwork, newNetwork) || !bytes.Equal(oldSlack, newSlack) {
		return errors.New("dashboard and Slack connection changes require stopping the daemon and editing its config")
	}
	if err := config.Save(a.configPath, cfg); err != nil {
		return err
	}
	if err := a.Core.UpdateConfig(cfg); err != nil {
		return err
	}
	a.cfg = cfg
	return nil
}
func (a *App) Status(id, name, state, detail string) {
	a.mu.Lock()
	a.statuses[id] = core.Integration{ID: id, Name: name, Status: state, Detail: detail}
	a.mu.Unlock()
}
func (a *App) Snapshot(ctx context.Context) (core.Snapshot, error) {
	s, err := a.Core.Snapshot(ctx)
	if err != nil {
		return s, err
	}
	cfg := a.Config()
	s.Integrations = []core.Integration{{ID: "model", Name: "Assistant model", Status: "not_configured", Detail: "Choose a model in Settings"}, {ID: "slack", Name: "Slack bot messaging", Status: "not_configured", Detail: "Sends and receives owner direct messages. Configure owner identity and Socket Mode credentials"}, {ID: "workers", Name: "Worker runtimes", Status: "not_configured", Detail: "Ask your assistant to prepare a worker for a project"}}
	if cfg.Model.Model != "" {
		s.Integrations[0].Status = "configured"
		s.Integrations[0].Detail = strings.Join([]string{cfg.Model.Engine, cfg.Model.Model, cfg.Model.Effort}, " / ")
	}
	if len(cfg.Workers) > 0 {
		s.Integrations[2].Status = "configured"
		s.Integrations[2].Detail = fmt.Sprintf("%d approved profiles", len(cfg.Workers))
	}
	ignoreLive := map[string]bool{}
	if cfg.LegacyLinearImportEnabled() {
		s.Integrations = append(s.Integrations, core.Integration{ID: "linear", Name: "Linear assignment import", Status: "configured", Detail: "Optional import from selected teams; local projects remain independent"})
	}
	for _, c := range cfg.Connections {
		state, detail := "configured", "Reading only, through CLI accounts: "+strings.Join(c.Profiles, ", ")
		if c.Tool == "lin" && !c.ImportAssignments {
			detail += "; optional resource, assignment import off"
			ignoreLive["connection:"+c.ID] = true
		}
		if c.Tool == "agent-notion" {
			if len(c.Profiles) == 0 {
				state, detail = "configured", "Reading only, through the CLI default account"
			} else {
				state, detail = "unavailable", "Choose the CLI default account for Notion"
			}
		}
		s.Integrations = append(s.Integrations, core.Integration{ID: "connection:" + c.ID, Name: c.Name, Status: state, Detail: detail})
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if status, ok := a.statuses["chat"]; ok {
		s.Integrations = append(s.Integrations, status)
	}
	for _, profile := range cfg.Workers {
		if status, ok := a.statuses["worker-usage:"+profile.ID]; ok {
			status.ProjectID = profile.ProjectID
			s.Integrations = append(s.Integrations, status)
		}
	}
	for i, st := range s.Integrations {
		if live, ok := a.statuses[st.ID]; ok && !ignoreLive[st.ID] {
			// A live status reports state, not relationships; keep the link
			// the configuration established.
			live.ProjectID = st.ProjectID
			s.Integrations[i] = live
		}
	}
	return s, nil
}
func (a *App) context(ctx context.Context) (json.RawMessage, []engine.Message, error) {
	return a.chatContext(ctx, "")
}
func (a *App) chatContext(ctx context.Context, currentMessageID string) (json.RawMessage, []engine.Message, error) {
	s, err := a.Snapshot(ctx)
	if err != nil {
		return nil, nil, err
	}
	history := []engine.Message{}
	start := chatCheckpointStart(s)
	if s.ChatCheckpoint.ThroughID != "" && start == 0 {
		return nil, nil, errors.New("conversation checkpoint source is missing; original dialogue preserved")
	}
	for _, m := range s.Messages[start:] {
		if m.ID != currentMessageID && (m.Role == "user" || m.Role == "assistant") {
			history = append(history, engine.Message{Role: m.Role, Content: m.Content})
		}
	}
	s.Messages = []core.Message{}
	if len(s.Activity) > 40 {
		s.Activity = s.Activity[len(s.Activity)-40:]
	}
	profiles := []WorkerDetail{}
	cfg := a.Config()
	for _, p := range cfg.Workers {
		profiles = append(profiles, workerDetail(cfg, p, projectWorkerBusy(s, p.ProjectID), a.Demo))
	}
	raw, err := json.Marshal(struct {
		State               core.Snapshot       `json:"state"`
		Profiles            []WorkerDetail      `json:"worker_profiles"`
		Authority           ExecutionAuthority  `json:"execution_authority"`
		Connections         []config.Connection `json:"connections"`
		ConversationSummary core.ChatCheckpoint `json:"conversation_summary_untrusted"`
	}{s, profiles, a.executionAuthority(s.Paused), cfg.Connections, s.ChatCheckpoint})
	return raw, history, err
}
func args(raw json.RawMessage, v any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("invalid trailing arguments")
	}
	return nil
}
func (a *App) Execute(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	switch name {
	case "prepare_worker":
		var in struct {
			ProjectID string `json:"project_id"`
			Workspace string `json:"workspace"`
		}
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		profile, err := a.PrepareWorker(ctx, in.ProjectID, in.Workspace)
		if err != nil {
			return nil, err
		}
		snapshot, err := a.Core.Snapshot(ctx)
		if err != nil {
			return nil, err
		}
		return workerDetail(a.Config(), profile, projectWorkerBusy(snapshot, profile.ProjectID), a.Demo), nil
	case "list_worker_models":
		var in struct {
			WorkerProfile string `json:"worker_profile"`
			Engine        string `json:"engine"`
		}
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		return a.listWorkerModels(ctx, in.WorkerProfile, in.Engine)
	case "configure_worker":
		var in struct {
			ProjectID     string `json:"project_id"`
			WorkerProfile string `json:"worker_profile"`
			Name          string `json:"name"`
			Engine        string `json:"engine"`
			Model         string `json:"model"`
			Effort        string `json:"effort"`
		}
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		return a.UpdateWorker(ctx, in.ProjectID, in.WorkerProfile, WorkerUpdate{Name: in.Name, Engine: in.Engine, Model: in.Model, Effort: in.Effort})
	case "list_connections", "query_connection":
		return a.runConnectionTool(ctx, name, raw)
	case "control_agent":
		return nil, errors.New("worker controls require the current owner conversation")
	case "inspect_agent":
		var in engine.InspectAgentArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		return a.InspectAgent(ctx, in.AgentID)
	case "message_agent":
		var in struct {
			AgentID string `json:"agent_id"`
			Message string `json:"message"`
		}
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		snap, err := a.Core.Snapshot(ctx)
		if err != nil {
			return nil, err
		}
		for _, agent := range snap.Agents {
			if agent.ID == in.AgentID {
				return a.SendAgent(ctx, agent, in.Message)
			}
		}
		return nil, core.ErrNotFound

	case "read_state":
		if err := args(raw, &struct{}{}); err != nil {
			return nil, err
		}
		state, _, err := a.context(ctx)
		if err != nil {
			return nil, err
		}
		return state, nil
	case "create_project":
		var in engine.CreateProjectArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		return a.Core.CreateProject(ctx, core.ProjectInput{Directories: in.Directories, Title: in.Title, Description: in.Objective, AcceptanceCriteria: strings.Join(in.AcceptanceCriteria, "\n")})
	case "update_project":
		var in engine.UpdateProjectArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		return a.Core.RefineProjectWithDirectories(ctx, in.ProjectID, in.Objective, strings.Join(in.AcceptanceCriteria, "\n"), in.Directories)
	case "create_work_item", "queue_work_item", "unqueue_work_item", "steer_work_item", "accept_work_item":
		return a.workItemTool(ctx, name, raw)
	case "delegate":
		var in engine.DelegateArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		return a.commissionWorker(ctx, core.DelegateInput{WorkItemID: in.WorkItemID, ProjectID: in.ProjectID, ParentID: in.ParentID, ProfileID: in.WorkerProfile, Role: in.Role, Task: in.Objective, AcceptanceCriteria: strings.Join(in.AcceptanceCriteria, "\n")})
	case "ask_decision":
		var in engine.DecisionArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		why := in.Why
		if len(in.Evidence) > 0 {
			why += "\nEvidence: " + strings.Join(in.Evidence, "; ")
		}
		return a.Core.CreateDecision(ctx, core.DecisionInput{WorkItemID: in.WorkItemID, ProjectID: in.ProjectID, Title: in.Question, Context: why, Recommendation: in.Recommendation, Choices: in.Options})
	case "remember_preference":
		var in engine.PreferenceArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		return a.Core.Remember(ctx, in.Key, in.Value)
	case "report_status":
		var in engine.StatusArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		return map[string]bool{"recorded": true}, a.Core.RecordActivity(ctx, in.ProjectID, "assistant.update", in.Summary+evidenceText(in.Evidence))
	case "complete_project":
		var in struct {
			ProjectID string   `json:"project_id"`
			Evidence  []string `json:"evidence"`
		}
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		return map[string]bool{"completed": true}, a.Core.CompleteProject(ctx, in.ProjectID, in.Evidence)
	default:
		return nil, errors.New("unavailable coordination action")
	}
}
func evidenceText(e []string) string {
	if len(e) == 0 {
		return ""
	}
	return "\nEvidence: " + strings.Join(e, "; ")
}

// SetNoDispatch is a boot-only restriction; it cannot be removed by live configuration.
func (a *App) SetNoDispatch() { a.dispatchDisabled.Store(true) }
