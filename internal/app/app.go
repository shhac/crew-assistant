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
	"github.com/shhac/crew-assistant/internal/upgrade"
	"github.com/shhac/crew-assistant/internal/work"
)

type App struct {
	Autopilot        *core.AutopilotCoordinator
	version          string
	checker          *upgrade.Checker
	updateError      string // A state write failure cannot rely on that state being writable.
	Diagnostics      *diagnostics.Logger
	small            *smallModels // Loading captions and suggestions.
	connectionClient connections.Client
	Core             *core.Service
	mu               sync.RWMutex
	cfg              config.Config
	// applyConfiguration permits testing persistence/application divergence.
	// Nil uses the service's ordinary checked configuration path.
	applyConfiguration func(config.Config) error
	// Disk connection settings at startup distinguish CLI overrides from edits
	// that still require a daemon restart.
	diskDashboard config.Dashboard
	// slackConfig is the connection this boot uses. Edits are saved for restart.
	slackConfig config.Slack
	configPath  string
	Demo        bool
	chat        chan struct{}
	chatWake    chan struct{}
	chatRunning atomic.Bool
	chatFailed  atomic.Bool
	chatWaiters sync.Map
	chatInvoker func(context.Context, engine.Config, engine.Request, engine.ToolExecutor) (engine.Result, error)
	// sessions holds the model session the assistant's conversation runs on.
	sessions             chatSessions
	prerequisiteComplete smallCompletion
	summarize            smallCompletion // Writes the conversation's summaries.
	statuses             map[string]core.Integration
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
	stop          lifecycle.Stop
	autopilotStop atomic.Pointer[lifecycle.Stop]
	// chatClosed is set once the chat queue takes no more turns, so a
	// message queued after it hears so rather than waiting for an answer.
	chatClosed       atomic.Bool
	dispatchDisabled atomic.Bool // Fixed by Run before integrations accept messages.
	upgrading        atomic.Bool
	upgradeEngine    *upgrade.Engine
	requestUpgrade   func(string, bool) error
	upgradeClock     upgrade.Clock
}

// Options are what the daemon decides about the app it builds.
type Options struct {
	Version        string
	Checker        *upgrade.Checker
	UpgradeEngine  *upgrade.Engine
	RequestUpgrade func(string, bool) error
	UpgradeClock   upgrade.Clock
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
	a := &App{Diagnostics: opts.Diagnostics, connectionClient: connections.New(), Core: s, cfg: cfg, slackConfig: cfg.Slack, configPath: path, Demo: opts.Demo, chat: make(chan struct{}, 1), chatWake: make(chan struct{}, 1), summarize: engine.Complete, stop: lifecycle.Now(context.Background()), statuses: map[string]core.Integration{}, drawing: map[string]drawing{}, small: newSmallModels(func() string { return s.StateDirectory() })}
	a.diskDashboard = cfg.Dashboard
	if disk, err := config.Load(path); err == nil {
		a.diskDashboard = disk.Dashboard
	}
	a.Autopilot = core.NewAutopilotCoordinator(s, func() error {
		if a.dispatchDisabled.Load() || a.Demo {
			return errors.New("autopilot dispatch is disabled")
		}
		if a.upgrading.Load() {
			return errors.New("upgrade admission is closed")
		}
		if stop := a.autopilotStop.Load(); stop != nil && stop.Stopping() {
			return errStoppingRefused
		}
		return nil
	})
	a.Work = work.New(s, a.Config, opts.Demo)
	a.Autopilot.OnPerformed(func(action core.AutopilotAction) {
		if action.Action.Kind == "resolve-choice" {
			a.Work.Nudge()
		}
	})
	a.prerequisiteComplete = engine.Complete
	if !opts.Demo && s != nil {
		s.SetPrerequisiteComparer(a.comparePrerequisites)
	}
	a.version, a.checker = opts.Version, opts.Checker
	a.upgradeEngine, a.requestUpgrade, a.upgradeClock = opts.UpgradeEngine, opts.RequestUpgrade, opts.UpgradeClock
	if opts.RequestUpgrade != nil && !opts.Demo {
		s.OnUpgradeRequested(func(version string) error { return a.RequestUpgrade(version, false) })
	}
	if a.checker != nil && !a.Demo {
		s.OnReleaseRecorded(a.releaseRecorded)
	}
	a.Work.Diagnostics = opts.Diagnostics
	a.small.outOfUsage, a.small.recheck, a.small.busy = a.Work.OutOfUsage, a.Work.RecheckUsage, a.Work.Interactive
	if opts.DrawWithCodex && !opts.Demo {
		a.Painter = codexPainter{a}
	}
	return a
}

// Avatars keeps the pictures Codex draws.
func (a *App) Avatars() avatars.Store { return avatars.NewStore(a.Core.StateDirectory()) }

func (a *App) Config() config.Config { a.mu.RLock(); defer a.mu.RUnlock(); return a.cfg }
func (a *App) UpdateConfig(cfg config.Config) error {
	return a.Autopilot.SerializeSettings(func() error {
		if err := a.validateSlackProject(context.Background(), cfg.Slack); err != nil {
			return errors.Join(config.ErrAutopilotUnchanged, err)
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.updateConfigLocked(cfg)
	})
}

// updateConfigLocked requires a.mu to preserve atomic read-modify-write updates.
func (a *App) updateConfigLocked(cfg config.Config) error {
	cfg.Assistant.Theme = config.NormalizeTheme(cfg.Assistant.Theme)
	if err := a.checkConfigLocked(cfg); err != nil {
		return errors.Join(config.ErrAutopilotUnchanged, err)
	}
	if err := config.Save(a.configPath, cfg); err != nil {
		if errors.Is(err, config.ErrAutopilotConflict) {
			return errors.Join(config.ErrAutopilotUnchanged, err)
		}
		return err
	}
	return a.applyConfigLocked(cfg)
}

// checkConfigLocked refuses a config the running daemon can't take on:
// an invalid one, or one that moves the dashboard, which only a restart can.
// Slack edits are saved here, with the old connection used until restart.
func (a *App) checkConfigLocked(cfg config.Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	oldNetwork, _ := json.Marshal(a.cfg.Dashboard)
	newNetwork, _ := json.Marshal(cfg.Dashboard)
	if !bytes.Equal(oldNetwork, newNetwork) {
		return errors.New("dashboard connection changes require stopping the daemon and editing its config")
	}
	return nil
}

func (a *App) applyConfigLocked(cfg config.Config) error {
	apply := a.applyConfiguration
	if apply == nil {
		apply = a.Core.UpdateConfig
	}
	if err := apply(cfg); err != nil {
		return err
	}
	upgradeChanged := a.cfg.Upgrade != cfg.Upgrade
	a.cfg = cfg
	if upgradeChanged && a.checker != nil && !a.Demo {
		a.checker.Wake()
	}
	return nil
}

// ReloadConfig takes on the config file as it now is, when something other
// than the dashboard changed it, such as `crew-assistant config set`, so a new
// limit or model applies without a restart. It reports whether anything
// changed.
func (a *App) ReloadConfig() (bool, error) {
	var changed bool
	err := a.Autopilot.SerializeSettings(func() error {
		var err error
		changed, err = a.reloadConfig()
		return err
	})
	return changed, err
}
func (a *App) reloadConfig() (bool, error) {
	cfg, err := config.Load(a.configPath)
	if err != nil {
		return false, err
	}
	cfg.Assistant.Theme = config.NormalizeTheme(cfg.Assistant.Theme)
	if err := a.validateSlackProject(context.Background(), cfg.Slack); err != nil {
		return false, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	// Unchanged disk connection settings retain CLI overrides. Actual disk
	// connection edits still go through the restart-required check below.
	baseline, _ := json.Marshal(a.diskDashboard)
	diskNetwork, _ := json.Marshal(cfg.Dashboard)
	if bytes.Equal(baseline, diskNetwork) {
		cfg.Dashboard = a.cfg.Dashboard
	}
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

// configuredIntegrations is what the config says of each integration. The
// connections it marks in ignoreLive have their import switched off, so a live
// status left over from an earlier import no longer applies.
func configuredIntegrations(cfg config.Config) ([]core.Integration, map[string]bool) {
	list := []core.Integration{{ID: "model", Name: "Assistant model", Status: "not_configured", Detail: "Choose your assistant in Settings"}, {ID: "slack", Name: "Slack bot messaging", Status: "not_configured", Detail: "Sends and receives owner direct messages. Configure owner identity and Socket Mode credentials"}}
	list[1].ProjectID = cfg.Slack.ProjectID
	if cfg.Slack.OwnerUserID != "" {
		list[1].Status = "configured"
		list[1].Detail = "Owner direct messages in workspace " + cfg.Slack.WorkspaceID
	}
	if seated, ok := cfg.Seated(); ok && seated.Model.Model != "" {
		list[0].Status = "configured"
		list[0].Detail = strings.Join([]string{seated.Model.Engine, seated.Model.Model, seated.Model.Effort}, " / ")
	} else if ok {
		list[0].Detail = "Choose " + seated.Name + "'s model on the Team page"
	}
	ignoreLive := map[string]bool{}
	if cfg.LegacyLinearImportEnabled() {
		list = append(list, core.Integration{ID: "linear", Name: "Linear assignment import", Status: "configured", Detail: "Optional import from selected teams; local projects remain independent"})
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
		list = append(list, core.Integration{ID: "connection:" + c.ID, Name: c.Name, Status: state, Detail: detail})
	}
	return list, ignoreLive
}

func withLiveStatuses(list []core.Integration, live map[string]core.Integration, ignoreLive map[string]bool) []core.Integration {
	if status, ok := live["chat"]; ok {
		list = append(list, status)
	}
	for i, st := range list {
		if status, ok := live[st.ID]; ok && !ignoreLive[st.ID] {
			// A live status reports state, not relationships; keep the link
			// the configuration established.
			status.ProjectID = st.ProjectID
			list[i] = status
		}
	}
	return list
}

func (a *App) Snapshot(ctx context.Context) (core.Snapshot, error) {
	s, err := a.Core.Snapshot(ctx)
	if err != nil {
		return s, err
	}
	cfg := a.Config()
	s.Assistant.Engine = cfg.AssistantHarness().Engine
	s.Update.Running, s.Update.Mode = a.version, cfg.Upgrade.Mode
	s.Update.Unavailable = upgrade.Unavailable(a.version, a.Demo)
	s.Stopping = a.Stopping()
	if a.upgradeEngine != nil {
		r, readErr := upgrade.ReadRecord(a.upgradeEngine.Path)
		if readErr != nil {
			return s, readErr
		}
		if r != nil {
			s.Upgrade = &core.UpgradeProgress{Step: r.Step, From: r.From, To: r.To, Since: r.StepAt, WaitingOn: r.WaitingOn}
			if r.Pinned {
				version := r.To
				if r.PinTo != "" {
					version = r.PinTo
				}
				s.Rollback = &core.RollbackStatus{From: r.From, To: version, Failure: r.Failure, Clear: "crew-assistant upgrade clear-rollback"}
			}
		}
	}
	if a.Work != nil {
		s.Turns = a.Work.Turns()
	}
	integrations, ignoreLive := configuredIntegrations(cfg)
	if cfg.Slack != a.slackConfig {
		integrations[1].Status = "restart_required"
		integrations[1].Detail = "Saved. Restart crew-assistant to use these settings; the current connection stays in use until then."
		ignoreLive["slack"] = true
	}
	for _, p := range s.Projects {
		if p.Linear != nil && p.Linear.Rules.PickUp {
			integrations = append(integrations, core.Integration{ID: "linear-project:" + p.ID, Name: p.Title + " Linear pick-up", ProjectID: p.ID, Status: "configured"})
		}
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.updateError != "" {
		s.Update.Error = a.updateError
	}
	s.Integrations = withLiveStatuses(integrations, a.statuses, ignoreLive)
	if s.Assistant.ID != "" {
		d := a.drawing[drawingKey(s.Assistant.ID)]
		s.Assistant.Drawing, s.Assistant.DrawError = d.busy, d.failure
	}
	for i := range s.Assistants {
		d := a.drawing[drawingKey(s.Assistants[i].ID)]
		s.Assistants[i].Drawing, s.Assistants[i].DrawError = d.busy, d.failure
	}
	for i := range s.Members {
		d := a.drawing[s.Members[i].ID]
		s.Members[i].Drawing, s.Members[i].DrawError = d.busy, d.failure
	}
	return s, nil
}
