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
	Slack       Slack        `json:"slack"`
	Linear      Linear       `json:"linear"`
	Limits      Limits       `json:"limits"`
	Connections []Connection `json:"connections"`
}
type Chat struct {
	LoadingPhrases LoadingPhrases `json:"loading_phrases"`
}

// LoadingPhrases always use the approved small models; there is no model or
// effort to choose.
type LoadingPhrases struct {
	Enabled bool `json:"enabled"`
}

// approvedSmallModels are the only models loading captions and next-message
// suggestions may use, one per CLI engine. Luna runs at low effort; Haiku 4.5
// has no effort setting, so none is sent for it.
var approvedSmallModels = map[string]struct{ model, effort string }{
	"codex":  {"gpt-6-luna", "low"},
	"claude": {"haiku", ""},
}

// SmallModels lists the approved small models to try in order: the
// assistant's own CLI engine first, then the other one. Each uses that CLI's
// configured login. An API assistant has no CLI of its own to start from, so it
// gets none rather than a guessed engine.
func (c Config) SmallModels() ([]Model, error) {
	var order []string
	switch c.Model.Engine {
	case "codex":
		order = []string{"codex", "claude"}
	case "claude":
		order = []string{"claude", "codex"}
	default:
		return nil, fmt.Errorf("no approved small model for the %s engine", c.Model.Engine)
	}
	models := make([]Model, 0, len(order))
	for _, engine := range order {
		m := c.Model
		approved := approvedSmallModels[engine]
		m.Engine, m.Model, m.Effort, m.MaxTokens = engine, approved.model, approved.effort, 128
		models = append(models, m)
	}
	return models, nil
}

// ApprovedSmallModel reports whether engine/model is one of the approved pair.
func ApprovedSmallModel(engine, model string) bool {
	approved, ok := approvedSmallModels[engine]
	return ok && approved.model == model
}

// Themes are the dashboard's appearance: the system's choice, light or dark.
const (
	ThemeSystem = "system"
	ThemeLight  = "light"
	ThemeDark   = "dark"
)

// NormalizeTheme reads the dark palettes earlier versions offered as following
// the system: they were never a choice of dark, only the one look there was.
// A config file or an open dashboard from before still saves.
func NormalizeTheme(theme string) string {
	switch theme {
	case "graphite-sage", "ink-blue", "charcoal-amber", "":
		return ThemeSystem
	}
	return theme
}

type Assistant struct {
	Name        string `json:"name"`
	Personality string `json:"personality"`
	Theme       string `json:"theme"`
	Avatar      Avatar `json:"avatar"`
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
	// RoleUsage holds team roles back while their subscription is nearly used.
	RoleUsage RoleUsage `json:"role_usage"`
	// MaxModelTurns and MaxModelCallsPerDay bound the assistant's own
	// conversation and tool loop.
	MaxModelCallsPerDay int `json:"max_model_calls_per_day"`
	MaxModelTurns       int `json:"max_model_turns"`
}

// RoleUsage holds team-role turns while a native CLI subscription is nearly
// used up.
// A zero threshold disables that engine's usage gate.
type RoleUsage struct {
	CodexMaxUsedPercent  int    `json:"codex_max_used_percent"`
	ClaudeMaxUsedPercent int    `json:"claude_max_used_percent"`
	OnUnavailable        string `json:"on_unavailable"`
}

type FilePaths struct {
	Config string
	State  string
}

func Default() Config {
	return Config{
		Chat:        Chat{LoadingPhrases: LoadingPhrases{Enabled: true}},
		Assistant:   Assistant{Name: DefaultAssistantName, Personality: "Calm, concise and proactive. Bring clear recommendations and evidence; handle the chasing.", Theme: ThemeSystem, Avatar: Avatar{Shape: "orb", Background: "#16211e", Accent: "#a8c5a8"}},
		Dashboard:   Dashboard{Addr: "127.0.0.1:8340", Tailscale: "off", TailscalePort: 8443, AllowedUsers: []string{}},
		Model:       defaultModel(),
		Connections: []Connection{},
		Slack:       Slack{BotTokenEnv: "SLACK_BOT_TOKEN", AppTokenEnv: "SLACK_APP_TOKEN"},
		Linear:      Linear{APIKeyEnv: "LINEAR_API_KEY", TeamIDs: []string{}},
		Limits:      Limits{RoleUsage: RoleUsage{CodexMaxUsedPercent: 90, ClaudeMaxUsedPercent: 90, OnUnavailable: "allow"}, MaxModelCallsPerDay: 100, MaxModelTurns: 8},
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
	if data, err = dropLoadingModelChoice(data, sections); err != nil {
		return c, err
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
	c.Assistant.Theme = NormalizeTheme(c.Assistant.Theme)
	return c, c.Validate()
}

// dropLoadingModelChoice removes the loading-phrase model and effort that
// earlier versions saved. Loading phrases now use only the approved small
// models, so a saved choice is discarded rather than honoured, and the file
// still loads. The next save writes it without them.
func dropLoadingModelChoice(data []byte, sections map[string]json.RawMessage) ([]byte, error) {
	var chat map[string]json.RawMessage
	if raw, ok := sections["chat"]; !ok || json.Unmarshal(raw, &chat) != nil {
		return data, nil
	}
	var phrases map[string]json.RawMessage
	if raw, ok := chat["loading_phrases"]; !ok || json.Unmarshal(raw, &phrases) != nil {
		return data, nil
	}
	_, model := phrases["model"]
	_, effort := phrases["effort"]
	if !model && !effort {
		return data, nil
	}
	delete(phrases, "model")
	delete(phrases, "effort")
	var err error
	if chat["loading_phrases"], err = json.Marshal(phrases); err != nil {
		return nil, err
	}
	if sections["chat"], err = json.Marshal(chat); err != nil {
		return nil, err
	}
	return json.Marshal(sections)
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
	if strings.TrimSpace(c.Assistant.Name) == "" || len(c.Assistant.Name) > 80 {
		return errors.New("assistant.name must contain 1–80 characters")
	}
	if len(c.Assistant.Personality) > 4000 {
		return errors.New("assistant.personality must not exceed 4000 characters")
	}
	switch c.Assistant.Theme {
	case ThemeSystem, ThemeLight, ThemeDark:
	default:
		return errors.New("assistant.theme must be system, light or dark")
	}
	if err := c.Assistant.Avatar.Validate(); err != nil {
		return fmt.Errorf("assistant.avatar: %w", err)
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
	if c.Limits.RoleUsage.CodexMaxUsedPercent < 0 || c.Limits.RoleUsage.CodexMaxUsedPercent > 100 {
		return errors.New("limits.role_usage.codex_max_used_percent must be between 0 and 100; 0 disables the limit")
	}
	if c.Limits.RoleUsage.ClaudeMaxUsedPercent < 0 || c.Limits.RoleUsage.ClaudeMaxUsedPercent > 100 {
		return errors.New("limits.role_usage.claude_max_used_percent must be between 0 and 100; 0 disables the limit")
	}
	if c.Limits.RoleUsage.OnUnavailable != "allow" && c.Limits.RoleUsage.OnUnavailable != "pause" {
		return errors.New("limits.role_usage.on_unavailable must be allow or pause")
	}
	if err := c.Model.Validate(); err != nil {
		return fmt.Errorf("model: %w", err)
	}
	envs := []string{c.Model.APIKeyEnv, c.Slack.BotTokenEnv, c.Slack.AppTokenEnv, c.Linear.APIKeyEnv}
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
