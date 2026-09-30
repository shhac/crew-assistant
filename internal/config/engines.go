package config

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	harness "github.com/shhac/lib-agent-harness"
)

// Engines are the CLIs and the endpoint everything that calls a model runs
// through: the assistant, the team roles, the small models, the painter and
// the usage meter. Which model and effort to use is chosen elsewhere; this is
// how to reach an engine and how much of its subscription may be used.
type Engines struct {
	Codex            CLIEngine  `json:"codex"`
	Claude           CLIEngine  `json:"claude"`
	Grok             CLIEngine  `json:"grok,omitzero"`
	OpenAICompatible HTTPEngine `json:"openai-compatible"`
	// Providers are further named OpenAI-compatible endpoints beside the
	// one above, such as OpenRouter next to a model on this machine.
	Providers []Provider `json:"providers,omitzero"`
}

// Provider is a named OpenAI-compatible endpoint. A model on the API names
// the one it runs on by id; no id is the endpoint under
// engines.openai-compatible.
type Provider struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	HTTPEngine
}

// LegacyProvider is the id engines.openai-compatible has among the
// providers.
const LegacyProvider = "openai-compatible"

// MaxProviders is the most named providers a config keeps.
const MaxProviders = 16

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
	// RoleRuns is an optional safety cap on how many team role turns may run
	// on the engine at once, across every project; nil is no cap, and each
	// team member working one step at a time is the only bound.
	RoleRuns *int `json:"role_runs,omitempty"`
}

// MaxRoleRuns is the highest safety cap on role turns the owner may set.
const MaxRoleRuns = 8

// RoleRuns is the safety cap on team role turns running on engine at once,
// or 0 when the owner has set none.
func (e Engines) RoleRuns(engine string) int {
	cli, _ := e.CLI(engine)
	if cli.RoleRuns == nil {
		return 0
	}
	return *cli.RoleRuns
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
	// EffortParameter is where the endpoint reads a reasoning effort:
	// reasoning_effort, the default, as OpenAI and xAI do, or
	// reasoning.effort, as gateways such as OpenRouter do.
	EffortParameter string `json:"effort_parameter,omitempty"`
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
var CLIEngineNames = cliEngineNames()

func cliEngineNames() []string {
	var names []string
	for _, e := range harness.Engines() {
		if e.Transport() == harness.CLITransport {
			names = append(names, string(e))
		}
	}
	return names
}

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
	case "grok":
		return &e.Grok
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

// APIProviders are the endpoints a model on the API can run on:
// engines.openai-compatible first, as LegacyProvider, then the named ones.
// Each has its defaults filled in.
func (e Engines) APIProviders() []Provider {
	legacy := Provider{ID: LegacyProvider, HTTPEngine: e.OpenAICompatible}
	legacy.BaseURL, legacy.APIKeyEnv = e.Endpoint()
	return append([]Provider{legacy}, e.Providers...)
}

// APIProvider is the endpoint with this id; empty is
// engines.openai-compatible.
func (e Engines) APIProvider(id string) (Provider, bool) {
	for _, p := range e.APIProviders() {
		if p.ID == cmp.Or(id, LegacyProvider) {
			return p, true
		}
	}
	return Provider{}, false
}

// Label is how the owner sees a provider named: its name, or else its
// address's host.
func (p Provider) Label() string {
	if p.Name != "" {
		return p.Name
	}
	if u, err := url.Parse(p.BaseURL); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return EngineLabel(string(harness.OpenAICompatible))
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
	// APIProvider is the id of the endpoint an API model runs on; empty for
	// a CLI.
	APIProvider string
	BaseURL     string
	APIKeyEnv   string
	// EffortParameter is where the endpoint reads an effort.
	EffortParameter string
}

// Provider is where the harness reaches the model: the CLI and its login,
// or the endpoint with a credential read from APIKeyEnv at each request, so
// the key is never held in configuration. An empty APIKeyEnv sends none,
// which the harness allows only on this machine.
func (h Harness) Provider() harness.Provider {
	p := harness.Provider{Engine: harness.Engine(h.Engine)}
	if p.Engine.Transport() == harness.CLITransport {
		p.CLI = harness.CLI{Binary: h.Bin, Home: h.Home}
		return p
	}
	if p.Engine.Transport() != harness.APITransport {
		return p
	}
	p.API = harness.API{BaseURL: h.BaseURL, Dialect: harness.OpenAIChatCompletions, EffortParameter: harness.EffortParameter(cmp.Or(h.EffortParameter, string(harness.EffortReasoningEffort)))}
	if h.APIKeyEnv == "" {
		p.API.Unauthenticated = true
		return p
	}
	p.API.Credentials = environmentCredential(h.APIKeyEnv)
	return p
}

// environmentCredential reads the key when a request is made, never before.
func environmentCredential(name string) harness.CredentialSource {
	return func(context.Context) (string, error) {
		if value := os.Getenv(name); value != "" {
			return value, nil
		}
		return "", fmt.Errorf("model credential environment variable %s is not set", name)
	}
}

// Harness is model and effort on engine, reached through this config. It
// may use as many tokens as the seated assistant's replies may.
func (c Config) Harness(engine, model, effort string) Harness {
	return c.HarnessOn(engine, "", model, effort)
}

// HarnessOn is Harness with an API model on the provider with this id;
// empty is engines.openai-compatible. A CLI engine has no provider.
func (c Config) HarnessOn(engine, provider, model, effort string) Harness {
	h := Harness{Engine: engine, Model: model, Effort: effort, MaxTokens: defaultModel().MaxTokens}
	if seated, ok := c.Seated(); ok {
		h.MaxTokens = seated.Model.MaxTokens
	}
	if _, ok := c.Engines.CLI(engine); ok {
		h.Bin, h.Home = c.Engines.Binary(engine)
		return h
	}
	// Validation keeps a saved provider known; one that isn't reaches no
	// endpoint rather than another one.
	p, _ := c.Engines.APIProvider(provider)
	h.APIProvider, h.BaseURL, h.APIKeyEnv, h.EffortParameter = cmp.Or(provider, LegacyProvider), p.BaseURL, p.APIKeyEnv, p.EffortParameter
	return h
}

// ProfileHarness is an assistant profile's own model.
func (c Config) ProfileHarness(p AssistantProfile) Harness {
	h := c.HarnessOn(p.Model.Engine, p.Model.Provider, p.Model.Model, p.Model.Effort)
	h.MaxTokens = p.Model.MaxTokens
	return h
}

// AssistantHarness is the seated assistant's own model; with no one in the
// seat it names no engine.
func (c Config) AssistantHarness() Harness {
	seated, ok := c.Seated()
	if !ok {
		return Harness{}
	}
	return c.ProfileHarness(seated)
}

func (e Engines) validate() error {
	for _, name := range CLIEngineNames {
		if err := e.CLIRef(name).validate("engines." + name); err != nil {
			return err
		}
	}
	if err := e.OpenAICompatible.validate("engines.openai-compatible"); err != nil {
		return err
	}
	if len(e.Providers) > MaxProviders {
		return fmt.Errorf("at most %d engines.providers are supported", MaxProviders)
	}
	for i, p := range e.Providers {
		prefix := fmt.Sprintf("engines.providers[%d]", i)
		if !profileID.MatchString(p.ID) || p.ID == LegacyProvider {
			return fmt.Errorf("%s.id must be 1–64 lower-case letters, digits or hyphens, other than %s", prefix, LegacyProvider)
		}
		if slices.ContainsFunc(e.Providers[:i], func(earlier Provider) bool { return earlier.ID == p.ID }) {
			return fmt.Errorf("two engines.providers have the id %s", p.ID)
		}
		if strings.TrimSpace(p.Name) == "" || len(p.Name) > 80 {
			return fmt.Errorf("%s.name must contain 1–80 characters", prefix)
		}
		if p.BaseURL == "" {
			return fmt.Errorf("%s.base_url is required", prefix)
		}
		if err := p.validate(prefix); err != nil {
			return err
		}
	}
	return nil
}

// validateProvider checks that a model's provider is one of the endpoints,
// and that only a model on the API names one.
func (e Engines) validateProvider(engine, provider string) error {
	if provider == "" {
		return nil
	}
	if engine != string(harness.OpenAICompatible) {
		return errors.New("provider is only for a model on another API")
	}
	if _, ok := e.APIProvider(provider); !ok {
		return fmt.Errorf("provider %s is not one of engines.providers", provider)
	}
	return nil
}

func (h HTTPEngine) validate(prefix string) error {
	if h.BaseURL != "" {
		if err := validateEndpoint(h.BaseURL); err != nil {
			return fmt.Errorf("%s.base_url: %w", prefix, err)
		}
	}
	if env := h.APIKeyEnv; env != "" && !envName.MatchString(env) {
		return fmt.Errorf("%s.api_key_env must be an environment variable name", prefix)
	}
	switch harness.EffortParameter(h.EffortParameter) {
	case "", harness.EffortReasoningEffort, harness.EffortReasoningObject:
		return nil
	}
	return fmt.Errorf("%s.effort_parameter must be %s or %s", prefix, harness.EffortReasoningEffort, harness.EffortReasoningObject)
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
	if cli.RoleRuns != nil && (*cli.RoleRuns < 1 || *cli.RoleRuns > MaxRoleRuns) {
		return fmt.Errorf("%s.role_runs must be between 1 and %d", prefix, MaxRoleRuns)
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
