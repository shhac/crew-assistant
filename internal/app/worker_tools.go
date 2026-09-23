package app

import (
	"context"
	"errors"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/engine"
)

// ExecutionAuthority describes application permissions, independent of whether
// the inference CLI has native shell or agent tools (it deliberately has neither).
type ExecutionAuthority struct {
	CanCommission bool   `json:"can_commission"`
	Reason        string `json:"reason"`
}

func (a *App) executionAuthority(paused bool) ExecutionAuthority {
	switch {
	case a.Demo:
		return ExecutionAuthority{Reason: "Demo mode cannot commission workers."}
	case a.dispatchDisabled.Load():
		return ExecutionAuthority{Reason: "Worker dispatch was disabled when this daemon started."}
	case paused:
		return ExecutionAuthority{Reason: "Coordination is paused; resume it before commissioning work."}
	default:
		return ExecutionAuthority{CanCommission: true, Reason: "The delegate application tool can queue approved workers. The daemon starts them subject to scope, capacity and usage limits. Native CLI agent tools are not required."}
	}
}
func (a *App) commissionWorker(ctx context.Context, in core.DelegateInput) (core.Agent, error) {
	// Serialize new commissions against idle-profile reconfiguration. Once the
	// agent exists, both the core state and broker guard protect its model choice.
	a.prepareMu.Lock()
	defer a.prepareMu.Unlock()
	if a.Demo || a.dispatchDisabled.Load() {
		return core.Agent{}, errors.New(a.executionAuthority(false).Reason)
	}
	return a.Core.Delegate(ctx, in)
}

func (a *App) listWorkerModels(ctx context.Context, profileID, selectedEngine string) (any, error) {
	if a.Demo {
		return nil, errors.New("model discovery is unavailable in the demo")
	}
	profile, err := a.Core.GetProfile(profileID)
	if err != nil {
		return nil, err
	}
	if !profile.Managed {
		return nil, errors.New("external workers manage their own model selection")
	}
	model := workerModel(a.Config(), profile)
	if selectedEngine != "" {
		model.Engine = selectedEngine
	}
	if model.Engine != "codex" && model.Engine != "claude" {
		return nil, errors.New("choose a local CLI engine: codex or claude")
	}
	discover := a.workerDiscover
	if discover == nil {
		discover = engine.DiscoverModels
	}
	options, err := discover(ctx, engine.Config{Engine: model.Engine, CodexBin: model.CodexBin, CodexHome: model.CodexHome, ClaudeBin: model.ClaudeBin, ClaudeHome: model.ClaudeHome})
	if err != nil {
		return nil, errors.New("could not discover worker models; check the selected CLI installation and login")
	}
	return struct {
		Engine string               `json:"engine"`
		Models []engine.ModelOption `json:"models"`
	}{model.Engine, options}, nil
}
