// Package config defines owner-controlled configuration. Credentials are environment references.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const Namespace = "app.paulie.crew-assistant"
const DefaultAssistantName = "Milo"

type Config struct {
	Chat        Chat         `json:"chat"`
	Assistant   Assistant    `json:"assistant"`
	Dashboard   Dashboard    `json:"dashboard"`
	Model       Model        `json:"model"`
	WorkerModel Model        `json:"worker_model"`
	Slack       Slack        `json:"slack"`
	Linear      Linear       `json:"linear"`
	Limits      Limits       `json:"limits"`
	Workers     []Worker     `json:"workers"`
	Connections []Connection `json:"connections"`
}
type Chat struct {
	LoadingPhrases LoadingPhrases `json:"loading_phrases"`
}
type LoadingPhrases struct {
	Enabled bool   `json:"enabled"`
	Model   string `json:"model"`
	Effort  string `json:"effort"`
}

// LoadingModel shares the assistant's selected local CLI/account; cosmetic text
// never switches to an API provider. An empty model follows the engine default.
func (c Config) LoadingModel() (Model, bool) {
	m := c.Model
	if !c.Chat.LoadingPhrases.Enabled || (m.Engine != "codex" && m.Engine != "claude") {
		return Model{}, false
	}
	m.Model, m.Effort, m.MaxTokens = c.Chat.LoadingPhrases.Model, c.Chat.LoadingPhrases.Effort, 128
	if m.Model == "" {
		m.Model = "gpt-5.6-luna"
		if m.Engine == "claude" {
			m.Model = "haiku"
		}
	}
	return m, true
}

type Assistant struct {
	Name        string `json:"name"`
	Personality string `json:"personality"`
	Theme       string `json:"theme"`
	Avatar      Avatar `json:"avatar"`
}
type Avatar struct {
	Shape      string `json:"shape"`
	Background string `json:"background"`
	Accent     string `json:"accent"`
}
type Connection struct {
	ImportAssignments bool     `json:"import_assignments"`
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Tool              string   `json:"tool"`
	Profiles          []string `json:"profiles"`
}
type Dashboard struct {
	Addr          string   `json:"addr"`
	Tailscale     string   `json:"tailscale"`
	TailscalePort int      `json:"tailscale_port"`
	AllowedUsers  []string `json:"allowed_users"`
}
type Model struct {
	ClaudeBin  string `json:"claude_bin"`
	ClaudeHome string `json:"claude_home"`
	Engine     string `json:"engine"`
	Effort     string `json:"effort"`
	CodexBin   string `json:"codex_bin"`
	CodexHome  string `json:"codex_home"`
	BaseURL    string `json:"base_url"`
	Model      string `json:"model"`
	APIKeyEnv  string `json:"api_key_env"`
	MaxTokens  int    `json:"max_tokens"`
}
type Slack struct {
	BotTokenEnv string `json:"bot_token_env"`
	AppTokenEnv string `json:"app_token_env"`
	OwnerUserID string `json:"owner_user_id"`
}
type Linear struct {
	ImportAssignments bool     `json:"import_assignments"`
	APIKeyEnv         string   `json:"api_key_env"`
	TeamIDs           []string `json:"team_ids"`
}
type Limits struct {
	WorkerUsage WorkerUsage `json:"worker_usage"`
	// WorkerTokenBudget caps the tokens one worker assignment may consume,
	// counted from the provider's own reported usage across its whole life,
	// including context summaries and resumes. Zero disables it. This is a
	// per-assignment budget, not the shared-account headroom in WorkerUsage.
	WorkerTokenBudget   int64 `json:"worker_token_budget"`
	MaxModelCallsPerDay int   `json:"max_model_calls_per_day"`
	// MaxModelTurns and MaxModelCallsPerDay bound the assistant's own
	// conversation and tool loop. They are not worker lifetime budgets.
	MaxModelTurns  int `json:"max_model_turns"`
	MaxAgents      int `json:"max_agents"`
	MaxDepth       int `json:"max_depth"`
	MaxRecoveries  int `json:"max_recoveries"`
	CheckInMinutes int `json:"check_in_minutes"`
}

// WorkerUsage controls admission of new work against native CLI subscription quotas.
// A zero threshold disables that engine's usage gate.
type WorkerUsage struct {
	CodexMaxUsedPercent  int    `json:"codex_max_used_percent"`
	ClaudeMaxUsedPercent int    `json:"claude_max_used_percent"`
	OnUnavailable        string `json:"on_unavailable"`
}

type Worker struct {
	ModelProfile *Model   `json:"model_profile,omitempty"`
	Managed      bool     `json:"managed,omitempty"`
	Workspace    string   `json:"workspace,omitempty"`
	ProjectID    string   `json:"project_id,omitempty"`
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Endpoint     string   `json:"endpoint"`
	APIKeyEnv    string   `json:"api_key_env"`
	Capabilities []string `json:"capabilities"`
}
type FilePaths struct {
	Config string
	State  string
}

func Default() Config {
	return Config{
		Chat:        Chat{LoadingPhrases: LoadingPhrases{Enabled: true, Effort: "low"}},
		Assistant:   Assistant{Name: DefaultAssistantName, Personality: "Calm, concise and proactive. Bring clear recommendations and evidence; handle the chasing.", Theme: "graphite-sage", Avatar: Avatar{Shape: "orb", Background: "#16211e", Accent: "#a8c5a8"}},
		Dashboard:   Dashboard{Addr: "127.0.0.1:8340", Tailscale: "off", TailscalePort: 8443, AllowedUsers: []string{}},
		Model:       defaultModel(),
		WorkerModel: defaultWorkerModel(),
		Connections: []Connection{},
		Slack:       Slack{BotTokenEnv: "SLACK_BOT_TOKEN", AppTokenEnv: "SLACK_APP_TOKEN"},
		Linear:      Linear{APIKeyEnv: "LINEAR_API_KEY", TeamIDs: []string{}},
		Limits:      Limits{WorkerUsage: WorkerUsage{CodexMaxUsedPercent: 90, ClaudeMaxUsedPercent: 90, OnUnavailable: "allow"}, MaxModelCallsPerDay: 100, MaxModelTurns: 8, MaxAgents: 4, MaxDepth: 3, MaxRecoveries: 2, CheckInMinutes: 30}, Workers: []Worker{},
	}
}

func defaultModel() Model {
	return Model{ClaudeBin: "claude", ClaudeHome: DefaultClaudeHome(), Engine: "codex", Model: "gpt-6-astra", Effort: "high", CodexBin: "codex", CodexHome: DefaultCodexHome(), BaseURL: "https://api.openai.com/v1", APIKeyEnv: "OPENAI_API_KEY", MaxTokens: 4096}
}

// DefaultCodexHome is app-owned state, independent of an ambient CODEX_HOME.
func DefaultCodexHome() string {
	root := os.Getenv("XDG_STATE_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		root = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(root, Namespace, "codex")
}

func DefaultClaudeHome() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

func defaultWorkerModel() Model {
	m := defaultModel()
	m.Model = "gpt-5.6-terra"
	return m
}

func Paths() (FilePaths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return FilePaths{}, err
	}
	configRoot := os.Getenv("XDG_CONFIG_HOME")
	if configRoot == "" {
		configRoot = filepath.Join(home, ".config")
	}
	stateRoot := os.Getenv("XDG_STATE_HOME")
	if stateRoot == "" {
		stateRoot = filepath.Join(home, ".local", "state")
	}
	return FilePaths{Config: filepath.Join(configRoot, Namespace, "config.json"), State: filepath.Join(stateRoot, Namespace, "state.db")}, nil
}
func Load(path string) (Config, error) {
	c := Default()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	// Existing API configurations must retain their provider and billing path.
	var sections map[string]json.RawMessage
	if err := json.Unmarshal(data, &sections); err != nil {
		return c, fmt.Errorf("decode config: %w", err)
	}
	legacyModel := false
	if raw, exists := sections["model"]; exists {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return c, fmt.Errorf("decode model config: %w", err)
		}
		_, explicitEngine := fields["engine"]
		legacyModel = !explicitEngine
		if legacyModel {
			c.Model.Engine = "openai-compatible"
			c.Model.Model = ""
			c.Model.Effort = ""
		}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&c); err != nil {
		return c, fmt.Errorf("decode config: %w", err)
	}
	if err = dec.Decode(new(any)); err != io.EOF {
		return c, errors.New("config must contain one JSON object")
	}
	if _, exists := sections["worker_model"]; legacyModel && !exists {
		c.WorkerModel = c.Model
		c.WorkerModel.MaxTokens = 4096 // Historical worker flag default, independent of the PA cap.
	}
	return c, c.Validate()
}
func Save(path string, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".config-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (c Config) Validate() error {
	if len(c.Chat.LoadingPhrases.Model) > 200 || strings.ContainsAny(c.Chat.LoadingPhrases.Model, "\r\n\x00") {
		return errors.New("chat loading model must be a short model identifier")
	}
	switch c.Chat.LoadingPhrases.Effort {
	case "", "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra":
	default:
		return errors.New("chat loading effort is not recognized")
	}

	if strings.TrimSpace(c.Assistant.Name) == "" || len(c.Assistant.Name) > 80 {
		return errors.New("assistant.name must contain 1–80 characters")
	}
	if len(c.Assistant.Personality) > 4000 {
		return errors.New("assistant.personality must not exceed 4000 characters")
	}
	switch c.Assistant.Theme {
	case "graphite-sage", "ink-blue", "charcoal-amber":
	default:
		return errors.New("assistant.theme must be graphite-sage, ink-blue or charcoal-amber")
	}
	switch c.Assistant.Avatar.Shape {
	case "orb", "spark", "leaf":
	default:
		return errors.New("assistant.avatar.shape must be orb, spark or leaf")
	}
	hex := regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	if !hex.MatchString(c.Assistant.Avatar.Background) || !hex.MatchString(c.Assistant.Avatar.Accent) {
		return errors.New("assistant avatar colors must be #RRGGBB")
	}
	if err := validateConnections(c.Connections); err != nil {
		return err
	}
	host, port, err := net.SplitHostPort(c.Dashboard.Addr)
	if err != nil {
		return errors.New("dashboard.addr must be a loopback host:port")
	}
	portNumber, portErr := strconv.Atoi(port)
	if portErr != nil || portNumber < 1 || portNumber > 65535 {
		return errors.New("dashboard.addr port must be between 1 and 65535")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("dashboard.addr must use a loopback IP")
	}
	if c.Dashboard.Tailscale != "off" && c.Dashboard.Tailscale != "serve" {
		return errors.New("dashboard.tailscale must be off or serve; public Funnel is not supported")
	}
	if c.Dashboard.TailscalePort != 443 && c.Dashboard.TailscalePort != 8443 && c.Dashboard.TailscalePort != 10000 {
		return errors.New("dashboard.tailscale_port must be 443, 8443 or 10000")
	}
	if c.Dashboard.Tailscale == "serve" && len(c.Dashboard.AllowedUsers) == 0 {
		return errors.New("dashboard.allowed_users is required for Tailscale Serve")
	}
	if c.Limits.MaxModelCallsPerDay < 1 || c.Limits.MaxModelCallsPerDay > 100000 {
		return errors.New("limits.max_model_calls_per_day must be between 1 and 100000")
	}
	if c.Limits.MaxModelTurns < 1 || c.Limits.MaxModelTurns > 32 {
		return errors.New("limits.max_model_turns must be between 1 and 32")
	}
	if c.Limits.WorkerTokenBudget < 0 || (c.Limits.WorkerTokenBudget > 0 && c.Limits.WorkerTokenBudget < 1000) || c.Limits.WorkerTokenBudget > 1_000_000_000_000 {
		return errors.New("worker token budget must be 0 to disable it, or at least 1000 tokens")
	}
	if c.Limits.MaxAgents < 1 || c.Limits.MaxAgents > 64 {
		return errors.New("limits.max_agents must be between 1 and 64")
	}
	if c.Limits.MaxDepth < 1 || c.Limits.MaxDepth > 10 {
		return errors.New("limits.max_depth must be between 1 and 10")
	}
	if c.Limits.MaxRecoveries < 0 || c.Limits.MaxRecoveries > 10 {
		return errors.New("limits.max_recoveries must be between 0 and 10")
	}
	if c.Limits.CheckInMinutes < 1 || c.Limits.CheckInMinutes > 1440 {
		return errors.New("limits.check_in_minutes must be between 1 and 1440")
	}
	if c.Limits.WorkerUsage.CodexMaxUsedPercent < 0 || c.Limits.WorkerUsage.CodexMaxUsedPercent > 100 {
		return errors.New("limits.worker_usage.codex_max_used_percent must be between 0 and 100; 0 disables the limit")
	}
	if c.Limits.WorkerUsage.ClaudeMaxUsedPercent < 0 || c.Limits.WorkerUsage.ClaudeMaxUsedPercent > 100 {
		return errors.New("limits.worker_usage.claude_max_used_percent must be between 0 and 100; 0 disables the limit")
	}
	if c.Limits.WorkerUsage.OnUnavailable != "allow" && c.Limits.WorkerUsage.OnUnavailable != "pause" {
		return errors.New("limits.worker_usage.on_unavailable must be allow or pause")
	}
	if err := c.Model.Validate(); err != nil {
		return fmt.Errorf("model: %w", err)
	}
	if err := c.WorkerModel.Validate(); err != nil {
		return fmt.Errorf("worker_model: %w", err)
	}
	if c.WorkerModel.MaxTokens > 32768 {
		return errors.New("worker_model.max_tokens must not exceed the worker broker limit of 32768")
	}
	envs := []string{c.Model.APIKeyEnv, c.WorkerModel.APIKeyEnv, c.Slack.BotTokenEnv, c.Slack.AppTokenEnv, c.Linear.APIKeyEnv}
	ids := map[string]bool{}
	for _, w := range c.Workers {
		if w.ID == "" || ids[w.ID] {
			return errors.New("workers must have unique nonempty IDs")
		}
		ids[w.ID] = true
		if w.ModelProfile != nil {
			if !w.Managed {
				return errors.New("model_profile applies only to managed workers")
			}
			if err := w.ModelProfile.Validate(); err != nil {
				return fmt.Errorf("worker model profile: %w", err)
			}
			if w.ModelProfile.MaxTokens > 32768 {
				return errors.New("worker model output limit exceeds 32768")
			}
			envs = append(envs, w.ModelProfile.APIKeyEnv)
		}
		if w.Managed {
			if w.ProjectID == "" || !filepath.IsAbs(w.Workspace) || w.Endpoint != "" || w.APIKeyEnv != "" {
				return errors.New("managed workers require a project and absolute workspace; endpoints and credentials are automatic")
			}
			if len(w.Capabilities) == 0 {
				return errors.New("managed workers need implementation or review capability")
			}
			for _, cap := range w.Capabilities {
				if cap != "implement" && cap != "review" {
					return errors.New("managed workers only implement or review")
				}
			}
		} else if err = validateEndpoint(w.Endpoint); err != nil {
			return fmt.Errorf("worker %s endpoint: %w", w.ID, err)
		}
		for _, cap := range w.Capabilities {
			if cap != "coordinate" && cap != "implement" && cap != "review" && cap != "research" {
				return fmt.Errorf("worker %s has prohibited or unknown capability %q", w.ID, cap)
			}
		}
		envs = append(envs, w.APIKeyEnv)
	}
	for _, e := range envs {
		if e != "" && !envName.MatchString(e) {
			return errors.New("credential references must be environment variable names")
		}
	}
	return nil
}

// Validate checks configuration syntax. The selected engine checks provider model
// capabilities before inference rather than guessing from model name prefixes.
func (m Model) Validate() error {
	if m.Engine != "codex" && m.Engine != "claude" && m.Engine != "openai-compatible" {
		return errors.New("engine must be codex, claude or openai-compatible")
	}
	switch m.Effort {
	case "", "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra":
	default:
		return errors.New("effort must be empty, none, minimal, low, medium, high, xhigh, max or ultra")
	}
	if m.Engine == "codex" && strings.TrimSpace(m.CodexBin) == "" {
		return errors.New("codex_bin is required for the codex engine")
	}
	if m.Engine == "codex" && (!filepath.IsAbs(m.CodexHome) || strings.ContainsRune(m.CodexHome, '\x00')) {
		return errors.New("codex_home must be an absolute directory path; use the app default or an existing dedicated Codex home")
	}
	if m.Engine == "claude" && (strings.TrimSpace(m.ClaudeBin) == "" || !filepath.IsAbs(m.ClaudeHome) || strings.ContainsRune(m.ClaudeHome, '\x00')) {
		return errors.New("claude requires an executable and absolute CLI home for its shared login")
	}
	if m.MaxTokens < 128 || m.MaxTokens > 131072 {
		return errors.New("max_tokens must be between 128 and 131072")
	}
	if err := validateEndpoint(m.BaseURL); err != nil {
		return fmt.Errorf("base_url: %w", err)
	}
	return nil
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

func validateConnections(cs []Connection) error {
	if len(cs) > 32 {
		return errors.New("at most 32 connections are supported")
	}
	ids := map[string]bool{}
	for _, c := range cs {
		if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`).MatchString(c.ID) || ids[c.ID] {
			return errors.New("connections require unique IDs of 1–64 letters, digits, underscores or hyphens")
		}
		ids[c.ID] = true
		if strings.TrimSpace(c.Name) == "" || len(c.Name) > 80 {
			return errors.New("connection name must contain 1–80 characters")
		}
		switch c.Tool {
		case "lin", "agent-slack", "agent-notion", "agent-fathom":
		default:
			return errors.New("unsupported connection CLI")
		}
		if c.ImportAssignments && c.Tool != "lin" {
			return errors.New("assignment import is only supported for Linear connections")
		}
		if c.Tool == "agent-notion" {
			if len(c.Profiles) != 0 {
				return errors.New("Notion uses the CLI default account; profiles must be empty")
			}
			continue
		}
		if len(c.Profiles) < 1 || len(c.Profiles) > 32 {
			return errors.New("select 1–32 existing profiles per connection")
		}
		seen := map[string]bool{}
		for _, p := range c.Profiles {
			if strings.TrimSpace(p) == "" || len(p) > 128 || strings.ContainsAny(p, "\r\n\x00") || seen[p] {
				return errors.New("connection profiles must be unique nonempty aliases")
			}
			seen[p] = true
		}
	}
	return nil
}

// LegacyLinearImportEnabled keeps query-only CLI connections from falling back
// to a legacy account with different ownership or scope.
func (c Config) LegacyLinearImportEnabled() bool {
	if !c.Linear.ImportAssignments || len(c.Linear.TeamIDs) == 0 {
		return false
	}
	for _, connection := range c.Connections {
		if connection.Tool == "lin" {
			return false
		}
	}
	return true
}
