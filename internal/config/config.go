// Package config defines owner-controlled configuration. Credentials are environment references.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const Namespace = "app.paulie.crew-assistant"
const DefaultAssistantName = "Milo"

type Config struct {
	Chat      Chat      `json:"chat"`
	Assistant Assistant `json:"assistant"`
	Dashboard Dashboard `json:"dashboard"`
	// Model is the assistant's own model; Engines are how every model is
	// reached.
	Model       Model        `json:"model"`
	Engines     Engines      `json:"engines"`
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

// Limits bound the assistant's own conversation and tool loop. How much of
// a subscription team roles may use is each engine's usage floor.
type Limits struct {
	MaxModelCallsPerDay int `json:"max_model_calls_per_day"`
	MaxModelTurns       int `json:"max_model_turns"`
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
		Engines:     Engines{OpenAICompatible: HTTPEngine{BaseURL: defaultBaseURL, APIKeyEnv: defaultAPIKeyEnv}},
		Connections: []Connection{},
		Slack:       Slack{BotTokenEnv: "SLACK_BOT_TOKEN", AppTokenEnv: "SLACK_APP_TOKEN"},
		Linear:      Linear{APIKeyEnv: "LINEAR_API_KEY", TeamIDs: []string{}},
		Limits:      Limits{MaxModelCallsPerDay: 100, MaxModelTurns: 8},
	}
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
	if err := c.Model.Validate(); err != nil {
		return fmt.Errorf("model: %w", err)
	}
	if err := c.Engines.validate(); err != nil {
		return err
	}
	envs := []string{c.Slack.BotTokenEnv, c.Slack.AppTokenEnv, c.Linear.APIKeyEnv}
	for _, e := range envs {
		if e != "" && !envName.MatchString(e) {
			return errors.New("credential references must be environment variable names")
		}
	}
	return nil
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
