// Package app composes the coordination model with the deterministic core.
package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/diagnostics"
	"github.com/shhac/crew-assistant/internal/engine"
	"github.com/shhac/crew-assistant/internal/integrations/connections"
	"github.com/shhac/crew-assistant/internal/integrations/github"
	"github.com/shhac/crew-assistant/internal/quota"
	"github.com/shhac/crew-assistant/internal/roles"
)

type App struct {
	Diagnostics      *diagnostics.Logger // Set before starting the daemon.
	small            *smallModels        // Loading captions and suggestions.
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
	runner           roles.Runner
	meter            *quota.Meter
	loopWake         chan struct{}
	// github reads and merges pull requests; githubURL is where git pushes.
	// Both are replaced in tests.
	github    github.Client
	githubURL func(repo string) string
	prSeen    sync.Map
}

func New(s *core.Service, cfg config.Config, path string, demo bool) *App {
	return &App{connectionClient: connections.New(), Core: s, cfg: cfg, configPath: path, Demo: demo, chat: make(chan struct{}, 1), chatWake: make(chan struct{}, 1), statuses: map[string]core.Integration{}, runner: roles.Native{}, meter: &quota.Meter{}, loopWake: make(chan struct{}, 1), small: newSmallModels(func() string { return s.StateDirectory() }), github: github.New(), githubURL: github.URL}
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
	s.Integrations = []core.Integration{{ID: "model", Name: "Assistant model", Status: "not_configured", Detail: "Choose a model in Settings"}, {ID: "slack", Name: "Slack bot messaging", Status: "not_configured", Detail: "Sends and receives owner direct messages. Configure owner identity and Socket Mode credentials"}}
	if cfg.Model.Model != "" {
		s.Integrations[0].Status = "configured"
		s.Integrations[0].Detail = strings.Join([]string{cfg.Model.Engine, cfg.Model.Model, cfg.Model.Effort}, " / ")
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
	s = assistantView(s)
	cfg := a.Config()
	raw, err := json.Marshal(struct {
		State               core.Snapshot       `json:"state"`
		Connections         []config.Connection `json:"connections"`
		ConversationSummary core.ChatCheckpoint `json:"conversation_summary_untrusted"`
	}{s, cfg.Connections, s.ChatCheckpoint})
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
	case "list_connections", "query_connection":
		return a.runConnectionTool(ctx, name, raw)
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
		return a.Core.CreateProject(ctx, core.ProjectInput{Directories: in.Directories, Title: in.Title, Template: in.Template, Brief: core.BriefInput{Goal: in.Goal, Audience: in.Audience, Constraints: in.Constraints, Criteria: in.Criteria}})
	case "update_brief":
		var in engine.UpdateBriefArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		project, err := a.Core.UpdateBrief(ctx, in.ProjectID, core.BriefInput{Goal: in.Goal, Audience: in.Audience, Constraints: in.Constraints, Criteria: in.Criteria})
		a.nudgeLoop()
		return project, err
	case "set_team":
		var in engine.SetTeamArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		return a.SetTeam(ctx, in.ProjectID, TeamChoice{Template: in.Template, WriterEngine: in.WriterEngine, ReviewerEngine: in.ReviewerEngine, MaxRounds: in.MaxRounds, DeliverTo: in.DeliverTo, Repo: in.Repo, BranchPrefix: in.BranchPrefix, Check: in.Check, Prepare: in.Prepare})
	case "set_landing":
		var in engine.SetLandingArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		return a.SetLanding(ctx, in.ProjectID, core.LandPolicy{Means: in.Means, Via: in.Via, Target: in.Target, Method: in.Method, GitHub: in.GitHub, Approve: in.Approve})
	case "land_task":
		var in engine.LandTaskArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		return a.LandTask(ctx, in.ProjectID, in.TaskID)
	case "wake_me_when":
		var in engine.WakeArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		return a.WakeMeWhen(ctx, WakeRequest(in))
	case "list_wakes":
		if err := args(raw, &struct{}{}); err != nil {
			return nil, err
		}
		return a.OpenWakes(ctx)
	case "cancel_wake":
		var in engine.WakeHandleArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		return a.Core.CancelWake(ctx, in.Handle, "")
	case "queue_task":
		var in engine.QueueTaskArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		queued, err := a.Core.QueueTask(ctx, in.ProjectID, core.TaskInput{Objective: in.Objective, Criteria: in.Criteria})
		a.nudgeLoop()
		return queued, err
	case "stop_task":
		var in engine.StopTaskArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		return a.StopTask(ctx, in.ProjectID, in.TaskID)
	case "resolve_decision":
		var in engine.ResolveDecisionArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		decision, err := a.Core.ResolveDecision(ctx, in.DecisionID, in.Answer)
		a.nudgeLoop()
		return decision, err
	case "ask_decision":
		var in engine.DecisionArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		why := in.Why
		if len(in.Evidence) > 0 {
			why += "\nEvidence: " + strings.Join(in.Evidence, "; ")
		}
		return a.Core.CreateDecision(ctx, core.DecisionInput{ProjectID: in.ProjectID, Title: in.Question, Context: why, Recommendation: in.Recommendation, Choices: in.Options})
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
