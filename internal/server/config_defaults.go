package server

import (
	"github.com/shhac/crew-assistant/internal/config"
	harness "github.com/shhac/lib-agent-harness"
)

type engineDefaults struct {
	Bin        string `json:"bin"`
	Home       string `json:"home"`
	BridgeHome string `json:"browser_bridge_home,omitempty"`
}

// configDefaults are what a blank engine setting means, for the dashboard to
// show in place of the blank.
func configDefaults() map[string]any {
	engines := map[string]engineDefaults{}
	for _, name := range config.CLIEngineNames {
		bin, home := config.DefaultBinary(name)
		engines[name] = engineDefaults{Bin: bin, Home: home, BridgeHome: config.Engines{}.BridgeHome(name)}
	}
	baseURL, _ := config.Engines{}.Endpoint()
	return map[string]any{
		"upgrade":          config.DefaultUpgrade(),
		"engines":          engines,
		"choices":          engineChoices(),
		"usage_floor":      config.DefaultUsageFloor,
		"on_unknown_usage": config.OnUnknownUsageAllow,
		"openai_base_url":  baseURL,
	}
}

// engineChoice is what an engine can be chosen for, so the dashboard offers
// an engine wherever the harness can run it and nowhere else.
type engineChoice struct {
	Engine    string `json:"engine"`
	Label     string `json:"label"`
	CLI       bool   `json:"cli"`
	Assistant bool   `json:"assistant"`
	Roles     bool   `json:"roles"`
	// RolesReason is why team roles can't run on it, for the owner to read
	// beside the choice.
	RolesReason string `json:"roles_reason,omitempty"`
	Small       bool   `json:"small"`
	Compact     bool   `json:"compact"`
	Usage       bool   `json:"usage"`
	Models      bool   `json:"models"`
	Efforts     bool   `json:"efforts"`
	// Browser is whether QA on it can use the browser the engine ships.
	Browser bool `json:"browser"`
}

func engineChoices() []engineChoice {
	choices := []engineChoice{}
	for _, e := range harness.Engines() {
		name := string(e)
		roles, rolesReason := config.RoleSupport(name)
		choices = append(choices, engineChoice{
			Engine: name, Label: config.EngineLabel(name), CLI: e.Transport() == harness.CLITransport,
			Assistant: config.Supports(name, config.UseAssistant), Roles: roles, RolesReason: rolesReason,
			Small: config.Supports(name, config.UseSmall), Compact: config.Supports(name, config.UseCompact),
			Usage: config.Supports(name, config.UseUsage), Models: config.Supports(name, config.UseModels),
			Efforts: config.Supports(name, config.UseEfforts), Browser: config.Supports(name, config.UseBrowser),
		})
	}
	return choices
}

// providerChoice is an API provider a model on another API can run on.
type providerChoice struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// providerChoices are the saved API providers, engines.openai-compatible
// first.
func providerChoices(cfg config.Config) []providerChoice {
	choices := []providerChoice{}
	for _, p := range cfg.Engines.APIProviders() {
		choices = append(choices, providerChoice{ID: p.ID, Label: p.Label()})
	}
	return choices
}
