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
	"slices"
	"strconv"
	"strings"

	"github.com/shhac/lib-agent-cli/creds"
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
func (c Config) SmallModels() ([]Harness, error) {
	var order []string
	switch c.Model.Engine {
	case "codex":
		order = []string{"codex", "claude"}
	case "claude":
		order = []string{"claude", "codex"}
	default:
		return nil, fmt.Errorf("no approved small model for the %s engine", c.Model.Engine)
	}
	models := make([]Harness, 0, len(order))
	for _, engine := range order {
		approved := approvedSmallModels[engine]
		m := c.Harness(engine, approved.model, approved.effort)
		m.MaxTokens = 128
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

// Model is the assistant's own model choice; the engine it names is reached
// as Engines says.
type Model struct {
	Engine    string `json:"engine"`
	Model     string `json:"model"`
	Effort    string `json:"effort"`
	MaxTokens int    `json:"max_tokens"`
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

func defaultModel() Model {
	return Model{Engine: "codex", Model: "gpt-6-astra", Effort: "high", MaxTokens: 4096}
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

// Load reads the config file, in the current layout or an earlier one. Keys
// it doesn't know are kept in the file and reported by UnknownKeys, never an
// error: a file written by a newer version still loads.
func Load(path string) (Config, error) {
	c := Default()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if data, err = ConvertLegacyJSON(data); err != nil {
		return c, fmt.Errorf("decode config: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if err = dec.Decode(&c); err != nil {
		return c, fmt.Errorf("decode config: %w", err)
	}
	if err = dec.Decode(new(any)); err != io.EOF {
		return c, errors.New("config must contain one JSON object")
	}
	c.Assistant.Theme = NormalizeTheme(c.Assistant.Theme)
	return c, c.Validate()
}

// Save writes c over the config file, keeping what it doesn't model: notes
// and keys from a newer version. It holds the file's lock, so the daemon, the
// CLI and the dashboard never write over each other, and it finishes moving
// a file in an earlier layout to this one first, so no earlier key outlives
// the save.
func Save(path string, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	s := Document(path)
	return s.WithLock(func() error {
		if _, err := upgradeLocked(path); err != nil {
			return err
		}
		return s.Save(c)
	})
}

// Upgrade rewrites a config file in an earlier layout into the current one,
// once, under the file's lock, keeping everything else in it. It reports
// whether it rewrote anything.
func Upgrade(path string) (bool, error) {
	changed := false
	err := Document(path).WithLock(func() (err error) {
		changed, err = upgradeLocked(path)
		return err
	})
	return changed, err
}

func upgradeLocked(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return false, fmt.Errorf("decode config: %w", err)
	}
	if !convertLegacy(doc) {
		return false, nil
	}
	return true, writeFile(path, doc)
}

// UnknownKeys are the keys in the config file the current layout doesn't
// have, each with where it moved when it was renamed.
func UnknownKeys(path string) []UnknownKey {
	var out []UnknownKey
	for _, k := range Document(path).UnknownKeys(Config{}) {
		to, renamed := RenamedKey(k.Path)
		out = append(out, UnknownKey{Path: k.Path, Value: k.Value, Renamed: renamed, To: to})
	}
	return out
}

// UnknownKey is a key the config file holds that the current layout doesn't.
type UnknownKey struct {
	Path, Value string
	// Renamed says it is an earlier layout's key; To is where it went, or
	// "" when it was dropped.
	Renamed bool
	To      string
}

// String says what became of the key, for the owner to act on.
func (k UnknownKey) String() string {
	switch {
	case k.Renamed && k.To == "":
		return k.Path + " is no longer a setting; remove it with crew-assistant config unset " + k.Path
	case k.Renamed:
		return k.Path + " is now " + k.To
	}
	return k.Path + " is not a setting this version knows, so it has no effect; remove it with crew-assistant config unset " + k.Path
}

// Document is the config file as a document, for reading and removing keys
// by path, including ones this version doesn't know.
func Document(path string) creds.Store { return creds.Store{Path: path, Overlay: true} }

func writeFile(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
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
	if err = f.Chmod(0600); err != nil {
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

// EngineNames are the engines the assistant can run on.
var EngineNames = []string{"codex", "claude", "openai-compatible"}

// Efforts are the reasoning efforts a model may be asked for; empty is the
// model's own default.
var Efforts = []string{"", "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}

// Validate checks the model choice. The selected engine checks provider model
// capabilities before inference rather than guessing from model name prefixes.
func (m Model) Validate() error {
	if !slices.Contains(EngineNames, m.Engine) {
		return errors.New("engine must be codex, claude or openai-compatible")
	}
	if !slices.Contains(Efforts, m.Effort) {
		return errors.New("effort must be empty, none, minimal, low, medium, high, xhigh, max or ultra")
	}
	if m.MaxTokens < 128 || m.MaxTokens > 131072 {
		return errors.New("max_tokens must be between 128 and 131072")
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
