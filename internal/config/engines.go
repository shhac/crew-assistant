package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Engines are the CLIs and the endpoint everything that calls a model runs
// through: the assistant, the team roles, the small models, the painter and
// the usage meter. Which model and effort to use is chosen elsewhere; this is
// how to reach an engine and how much of its subscription may be used.
type Engines struct {
	Codex            CLIEngine  `json:"codex"`
	Claude           CLIEngine  `json:"claude"`
	OpenAICompatible HTTPEngine `json:"openai-compatible"`
}

// CLIEngine is a native CLI and the login home it uses.
type CLIEngine struct {
	// Bin is the executable; empty is the engine's own name on PATH.
	Bin string `json:"bin,omitempty"`
	// Home is the CLI's login home; empty is the default for the engine.
	Home string `json:"home,omitempty"`
	// UsageFloor holds team roles back while this little of a usage window
	// is left.
	UsageFloor UsageFloor `json:"usage_floor,omitzero"`
	// OnUnknownUsage is what roles do while usage can't be read: allow, the
	// default, or pause.
	OnUnknownUsage string `json:"on_unknown_usage,omitempty"`
}

// UsageFloor is the share of each usage window, in percent, that team roles
// leave unused. Nil is the default; 0 turns that window's floor off.
type UsageFloor struct {
	FiveHourPercent *int `json:"5h_percent,omitempty"`
	WeekPercent     *int `json:"1w_percent,omitempty"`
}

// HTTPEngine is an OpenAI-compatible endpoint, for an assistant that uses an
// API rather than a CLI subscription. An empty APIKeyEnv sends no key, for an
// endpoint on this machine that needs none.
type HTTPEngine struct {
	BaseURL   string `json:"base_url"`
	APIKeyEnv string `json:"api_key_env"`
}

// DefaultUsageFloor is the share of a usage window roles leave unused when
// the owner hasn't set one.
const DefaultUsageFloor = 10

const (
	defaultBaseURL   = "https://api.openai.com/v1"
	defaultAPIKeyEnv = "OPENAI_API_KEY"
)

// CLI is the configured CLI for engine, and whether engine is one.
func (e Engines) CLI(engine string) (CLIEngine, bool) {
	switch engine {
	case "codex":
		return e.Codex, true
	case "claude":
		return e.Claude, true
	}
	return CLIEngine{}, false
}

// Binary is the executable an engine runs as and the login home it uses,
// with the defaults filled in.
func (e Engines) Binary(engine string) (binary, home string) {
	cli, _ := e.CLI(engine)
	binary, home = cli.Bin, cli.Home
	if binary == "" {
		binary = engine
	}
	if home == "" {
		switch engine {
		case "codex":
			home = DefaultCodexHome()
		case "claude":
			home = DefaultClaudeHome()
		}
	}
	return binary, home
}

// Floors are the share of an engine's 5-hour and weekly windows roles leave
// unused, with the defaults filled in; supported is false for an engine
// whose usage can't be read locally.
func (e Engines) Floors(engine string) (fiveHour, week int, supported bool) {
	cli, ok := e.CLI(engine)
	if !ok {
		return 0, 0, false
	}
	return percentOr(cli.UsageFloor.FiveHourPercent), percentOr(cli.UsageFloor.WeekPercent), true
}

// PauseOnUnknownUsage says whether roles on engine wait while its usage
// can't be read, rather than carry on.
func (e Engines) PauseOnUnknownUsage(engine string) bool {
	cli, _ := e.CLI(engine)
	return cli.OnUnknownUsage == "pause"
}

// Endpoint is the OpenAI-compatible endpoint and the environment variable
// holding its key.
func (e Engines) Endpoint() (baseURL, apiKeyEnv string) {
	baseURL = e.OpenAICompatible.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return baseURL, e.OpenAICompatible.APIKeyEnv
}

func percentOr(p *int) int {
	if p == nil {
		return DefaultUsageFloor
	}
	return *p
}

// Harness is a model on an engine together with what reaches it: the CLI
// and login for a CLI engine, the endpoint for an API. It is what a caller
// needs to run the model.
type Harness struct {
	Engine    string
	Model     string
	Effort    string
	MaxTokens int
	Bin, Home string
	BaseURL   string
	APIKeyEnv string
}

// Harness is model and effort on engine, reached through this config.
func (c Config) Harness(engine, model, effort string) Harness {
	h := Harness{Engine: engine, Model: model, Effort: effort, MaxTokens: c.Model.MaxTokens}
	if _, ok := c.Engines.CLI(engine); ok {
		h.Bin, h.Home = c.Engines.Binary(engine)
		return h
	}
	h.BaseURL, h.APIKeyEnv = c.Engines.Endpoint()
	return h
}

// AssistantHarness is the assistant's own model.
func (c Config) AssistantHarness() Harness {
	return c.Harness(c.Model.Engine, c.Model.Model, c.Model.Effort)
}

func (e Engines) validate() error {
	for _, name := range []string{"codex", "claude"} {
		cli, _ := e.CLI(name)
		if cli.Home != "" && (!filepath.IsAbs(cli.Home) || strings.ContainsRune(cli.Home, '\x00')) {
			return fmt.Errorf("engines.%s.home must be an absolute directory path", name)
		}
		if strings.ContainsRune(cli.Bin, '\x00') {
			return fmt.Errorf("engines.%s.bin must be a command name or path", name)
		}
		for window, p := range map[string]*int{"5h_percent": cli.UsageFloor.FiveHourPercent, "1w_percent": cli.UsageFloor.WeekPercent} {
			if p != nil && (*p < 0 || *p > 100) {
				return fmt.Errorf("engines.%s.usage_floor.%s must be between 0 and 100; 0 turns it off", name, window)
			}
		}
		switch cli.OnUnknownUsage {
		case "", "allow", "pause":
		default:
			return fmt.Errorf("engines.%s.on_unknown_usage must be allow or pause", name)
		}
	}
	if e.OpenAICompatible.BaseURL != "" {
		if err := validateEndpoint(e.OpenAICompatible.BaseURL); err != nil {
			return fmt.Errorf("engines.openai-compatible.base_url: %w", err)
		}
	}
	if env := e.OpenAICompatible.APIKeyEnv; env != "" && !envName.MatchString(env) {
		return errors.New("engines.openai-compatible.api_key_env must be an environment variable name")
	}
	return nil
}
