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

	"github.com/shhac/crew-assistant/internal/avatars"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/diagnostics"
	"github.com/shhac/crew-assistant/internal/engine"
	"github.com/shhac/crew-assistant/internal/integrations/connections"
	"github.com/shhac/crew-assistant/internal/lifecycle"
	"github.com/shhac/crew-assistant/internal/work"
)

type App struct {
	Diagnostics      *diagnostics.Logger
	small            *smallModels // Loading captions and suggestions.
	connectionClient connections.Client
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
	// sessions holds the model session the assistant's conversation runs on.
	sessions  chatSessions
	summarize smallCompletion // Writes the conversation's summaries.
	statuses  map[string]core.Integration
	// drawing is each picture being drawn or that failed, by member id or
	// drawingAssistant. It is not stored: a restart forgets a drawing it
	// could not finish.
	drawing map[string]drawing
	// Work runs the teams' tasks and wakes agents.
	Work *work.Loop
	// Painter draws avatars; with none, as in demo mode, nothing is drawn.
	Painter  avatars.Painter
	paint    sync.Mutex // One drawing at a time.
	drawings sync.WaitGroup
	// drawingsClosed refuses new drawings once the run waits for them. It is
	// set under mu, which orders a drawing's Add before that Wait.
	drawingsClosed bool
	// stop is the daemon's run: until one starts, a stop that never comes.
	stop lifecycle.Stop
	// chatClosed is set once the chat queue takes no more turns, so a
	// message queued after it hears so rather than waiting for an answer.
	chatClosed atomic.Bool
}

// Options are what the daemon decides about the app it builds.
type Options struct {
	// Demo runs on sample data, with every model and integration off.
	Demo bool
	// Diagnostics records failures, the app's and its loop's; nil keeps the
	// default log.
	Diagnostics *diagnostics.Logger
	// DrawWithCodex has Codex draw faces. Without it nothing is drawn unless a
	// Painter is set.
	DrawWithCodex bool
}

func New(s *core.Service, cfg config.Config, path string, opts Options) *App {
	a := &App{Diagnostics: opts.Diagnostics, connectionClient: connections.New(), Core: s, cfg: cfg, configPath: path, Demo: opts.Demo, chat: make(chan struct{}, 1), chatWake: make(chan struct{}, 1), summarize: engine.Complete, stop: lifecycle.Now(context.Background()), statuses: map[string]core.Integration{}, drawing: map[string]drawing{}, small: newSmallModels(func() string { return s.StateDirectory() })}
	a.Work = work.New(s, a.Config, opts.Demo)
	a.Work.Diagnostics = opts.Diagnostics
	a.small.outOfUsage = a.Work.OutOfUsage
	if opts.DrawWithCodex && !opts.Demo {
		a.Painter = codexPainter{a}
	}
	return a
}

// Avatars keeps the pictures Codex draws.
func (a *App) Avatars() avatars.Store { return avatars.NewStore(a.Core.StateDirectory()) }

func (a *App) Config() config.Config { a.mu.RLock(); defer a.mu.RUnlock(); return a.cfg }
func (a *App) UpdateConfig(cfg config.Config) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.updateConfigLocked(cfg)
}

// updateConfigLocked requires a.mu to preserve atomic read-modify-write updates.
func (a *App) updateConfigLocked(cfg config.Config) error {
	cfg.Assistant.Theme = config.NormalizeTheme(cfg.Assistant.Theme)
	if err := a.checkConfigLocked(cfg); err != nil {
		return err
	}
	if err := config.Save(a.configPath, cfg); err != nil {
		return err
	}
	return a.applyConfigLocked(cfg)
}

// checkConfigLocked refuses a config the running daemon can't take on:
// an invalid one, or one that moves the dashboard or Slack, which only a
// restart can.
func (a *App) checkConfigLocked(cfg config.Config) error {
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
	return nil
}

func (a *App) applyConfigLocked(cfg config.Config) error {
	if err := a.Core.UpdateConfig(cfg); err != nil {
		return err
	}
	a.cfg = cfg
	return nil
}

// ReloadConfig takes on the config file as it now is, when something other
// than the dashboard changed it, such as `crew-assistant config set`, so a new
// limit or model applies without a restart. It reports whether anything
// changed.
func (a *App) ReloadConfig() (bool, error) {
	cfg, err := config.Load(a.configPath)
	if err != nil {
		return false, err
	}
	cfg.Assistant.Theme = config.NormalizeTheme(cfg.Assistant.Theme)
	a.mu.Lock()
	defer a.mu.Unlock()
	was, _ := json.Marshal(a.cfg)
	now, _ := json.Marshal(cfg)
	if bytes.Equal(was, now) {
		return false, nil
	}
	if err := a.checkConfigLocked(cfg); err != nil {
		return false, err
	}
	return true, a.applyConfigLocked(cfg)
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
	s.Stopping = a.Stopping()
	if a.Work != nil {
		s.Turns = a.Work.Turns()
	}
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
	d := a.drawing[drawingAssistant]
	s.Assistant.Drawing, s.Assistant.DrawError = d.busy, d.failure
	for i := range s.Members {
		d := a.drawing[s.Members[i].ID]
		s.Members[i].Drawing, s.Members[i].DrawError = d.busy, d.failure
	}
	return s, nil
}
