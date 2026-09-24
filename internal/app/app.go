// Package app composes the coordination model with the deterministic core.
package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/diagnostics"
	"github.com/shhac/crew-assistant/internal/engine"
	"github.com/shhac/crew-assistant/internal/integrations/connections"
	"github.com/shhac/crew-assistant/internal/work"
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
	// Work runs the teams' tasks and wakes agents.
	Work *work.Loop
}

func New(s *core.Service, cfg config.Config, path string, demo bool) *App {
	a := &App{connectionClient: connections.New(), Core: s, cfg: cfg, configPath: path, Demo: demo, chat: make(chan struct{}, 1), chatWake: make(chan struct{}, 1), statuses: map[string]core.Integration{}, small: newSmallModels(func() string { return s.StateDirectory() })}
	a.Work = work.New(s, a.Config, demo)
	return a
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

// SetNoDispatch is a boot-only restriction; it cannot be removed by live configuration.
func (a *App) SetNoDispatch() { a.dispatchDisabled.Store(true) }
