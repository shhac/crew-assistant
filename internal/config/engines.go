package config

import (
	"cmp"
	"errors"
	"fmt"
	"net"
	"net/url"
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

// What team roles do while an engine's usage can't be read.
const (
	OnUnknownUsageAllow = "allow"
	OnUnknownUsagePause = "pause"
)

// CLIEngineNames are the engines reached through a native CLI and its login.
var CLIEngineNames = []string{"codex", "claude"}

const (
	defaultBaseURL   = "https://api.openai.com/v1"
	defaultAPIKeyEnv = "OPENAI_API_KEY"
)

// CLIRef is the settings of engine's CLI, nil when engine isn't one.
func (e *Engines) CLIRef(engine string) *CLIEngine {
	switch engine {
	case "codex":
		return &e.Codex
	case "claude":
		return &e.Claude
	}
	return nil
}

// CLI is the configured CLI for engine, and whether engine is one.
func (e Engines) CLI(engine string) (CLIEngine, bool) {
	cli := e.CLIRef(engine)
	if cli == nil {
		return CLIEngine{}, false
	}
	return *cli, true
}

// DefaultBinary is what a blank bin and home mean for engine.
func DefaultBinary(engine string) (binary, home string) {
	switch engine {
	case "codex":
		return engine, DefaultCodexHome()
	case "claude":
		return engine, DefaultClaudeHome()
	}
	return engine, ""
}

// Binary is the executable an engine runs as and the login home it uses,
// with the defaults filled in.
func (e Engines) Binary(engine string) (binary, home string) {
	cli, _ := e.CLI(engine)
	binary, home = DefaultBinary(engine)
	return cmp.Or(cli.Bin, binary), cmp.Or(cli.Home, home)
}

// Floors are the share of an engine's 5-hour and weekly windows roles leave
// unused, with the defaults filled in. An engine whose usage can't be read
// locally has none.
func (e Engines) Floors(engine string) (fiveHour, week int) {
	cli, ok := e.CLI(engine)
	if !ok {
		return 0, 0
	}
	return percentOr(cli.UsageFloor.FiveHourPercent), percentOr(cli.UsageFloor.WeekPercent)
}

// PauseOnUnknownUsage says whether roles on engine wait while its usage
// can't be read, rather than carry on.
func (e Engines) PauseOnUnknownUsage(engine string) bool {
	cli, _ := e.CLI(engine)
	return cli.OnUnknownUsage == OnUnknownUsagePause
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
	for _, name := range CLIEngineNames {
		if err := e.CLIRef(name).validate("engines." + name); err != nil {
			return err
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

func (cli *CLIEngine) validate(prefix string) error {
	if cli.Home != "" && (!filepath.IsAbs(cli.Home) || strings.ContainsRune(cli.Home, '\x00')) {
		return fmt.Errorf("%s.home must be an absolute directory path", prefix)
	}
	if strings.ContainsRune(cli.Bin, '\x00') {
		return fmt.Errorf("%s.bin must be a command name or path", prefix)
	}
	for _, floor := range []struct {
		window  string
		percent *int
	}{{"5h_percent", cli.UsageFloor.FiveHourPercent}, {"1w_percent", cli.UsageFloor.WeekPercent}} {
		if floor.percent != nil && (*floor.percent < 0 || *floor.percent > 100) {
			return fmt.Errorf("%s.usage_floor.%s must be between 0 and 100; 0 turns it off", prefix, floor.window)
		}
	}
	switch cli.OnUnknownUsage {
	case "", OnUnknownUsageAllow, OnUnknownUsagePause:
		return nil
	}
	return fmt.Errorf("%s.on_unknown_usage must be allow or pause", prefix)
}

func validateEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("use an absolute URL without credentials, query or fragment")
	}
	if u.Scheme == "https" {
		return nil
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme == "http" && (u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())) {
		return nil
	}
	return errors.New("HTTPS is required except on loopback")
}
