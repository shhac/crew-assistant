package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigAtomicPrivateRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nested", "config.json")
	c := Default()
	c.Assistant.Name = "Juniper"
	if err := Save(p, c); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	got, err := Load(p)
	if err != nil || got.Assistant.Name != "Juniper" {
		t.Fatal(got, err)
	}
	c.Limits.MaxModelTurns = 0
	if err = Save(p, c); err == nil {
		t.Fatal("invalid replacement accepted")
	}
	got, err = Load(p)
	if err != nil || got.Limits.MaxModelTurns != 8 {
		t.Fatal("invalid save changed file")
	}
}
func TestUnknownFieldsAndTrailingJSONRejected(t *testing.T) {
	for _, body := range []string{`{"unexpected":true}`, `{} {}`, `{"limits":{"max_agents":4}}`, `{"workers":[]}`, `{"worker_model":{}}`} {
		p := filepath.Join(t.TempDir(), "config.json")
		os.WriteFile(p, []byte(body), 0600)
		if _, err := Load(p); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}
func TestBoundariesCannotBeConfiguredAway(t *testing.T) {
	for _, mutate := range []func(*Config){func(c *Config) { c.Dashboard.Addr = "0.0.0.0:8340" }, func(c *Config) { c.Dashboard.Tailscale = "funnel" }, func(c *Config) { c.Dashboard.Tailscale = "serve" }, func(c *Config) { c.Model.BaseURL = "https://secret:password@example.com" }, func(c *Config) { c.Model.BaseURL = "http://example.com" }, func(c *Config) { c.Model.APIKeyEnv = "raw token!" }} {
		c := Default()
		mutate(&c)
		if err := c.Validate(); err == nil {
			t.Fatal("unsafe configuration accepted")
		}
	}
}
func TestXDGPaths(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "configuration"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	p, err := Paths()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.Config, filepath.Join(root, "configuration")) || !strings.HasPrefix(p.State, filepath.Join(root, "state")) {
		t.Fatal(p)
	}
}

func TestLegacyAPIConfigRetainsProviderAndBillingPath(t *testing.T) {
	for _, body := range []string{`{"model":{"model":"existing-model","base_url":"https://provider.example/v1","api_key_env":"EXISTING_KEY"}}`, `{"model":{}}`} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if got.Model.Engine != "openai-compatible" || got.Model.Effort != "" {
			t.Fatalf("legacy profile unexpectedly migrated: %+v", got)
		}
		if strings.Contains(body, "existing-model") && (got.Model.Model != "existing-model" || got.Model.APIKeyEnv != "EXISTING_KEY" || got.Model.BaseURL != "https://provider.example/v1") {
			t.Fatal(got.Model)
		}
		if body == `{"model":{}}` && got.Model.Model != "" {
			t.Fatal("unconfigured legacy model enabled inference")
		}
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"assistant":{"name":"Juniper"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || got.Model.Engine != "codex" || got.Model.Model != "gpt-6-astra" || got.Model.Effort != "high" {
		t.Fatal(got.Model, err)
	}
}
func TestInvalidEngineAndEffortRejected(t *testing.T) {
	for _, mutate := range []func(*Config){
		func(c *Config) { c.Model.Engine = "unknown" },
		func(c *Config) { c.Model.Effort = "maximumish" },
		func(c *Config) { c.Model.CodexBin = "" },
	} {
		c := Default()
		mutate(&c)
		if err := c.Validate(); err == nil {
			t.Fatal("invalid engine configuration accepted")
		}
	}
}

func TestPathsUseReverseDNSNamespaceUnderXDGRoots(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	got, err := Paths()
	if err != nil || got.Config != filepath.Join(root, "config", "app.paulie.crew-assistant", "config.json") || got.State != filepath.Join(root, "state", "app.paulie.crew-assistant", "state.db") {
		t.Fatal(got, err)
	}
}
func TestConnectionProfilesAndIdentityValidation(t *testing.T) {
	c := Default()
	c.Connections = []Connection{{ID: "work", Name: "Work", Tool: "lin", Profiles: []string{"first", "second"}}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Config){func(c *Config) { c.Connections[0].Profiles = []string{"first", "first"} }, func(c *Config) { c.Connections[0].Tool = "sh" }, func(c *Config) { c.Assistant.Avatar.Accent = "url(https://example.com)" }, func(c *Config) { c.Assistant.Theme = "arbitrary" }} {
		d := Default()
		d.Connections = []Connection{{ID: "work", Name: "Work", Tool: "lin", Profiles: []string{"first"}}}
		mutate(&d)
		if err := d.Validate(); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
}

func TestConfiguredCodexHomesDefaultWithoutAmbientEnvironment(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)
	t.Setenv("CODEX_HOME", filepath.Join(root, "unrelated-codex"))
	c := Default()
	want := filepath.Join(root, Namespace, "codex")
	if c.Model.CodexHome != want {
		t.Fatal(c.Model.CodexHome)
	}
	c.Model.CodexHome = filepath.Join(root, "assistant")
	path := filepath.Join(root, "config.json")
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || got.Model.CodexHome != c.Model.CodexHome {
		t.Fatal(got, err)
	}
	c.Model.CodexHome = "relative/path"
	if err := c.Validate(); err == nil {
		t.Fatal("relative home accepted")
	}
}

func TestNotionDefaultAccountConfigRoundTrip(t *testing.T) {
	cfg := Default()
	cfg.Connections = []Connection{{ID: "notion", Name: "Documents", Tool: "agent-notion", Profiles: []string{}}}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || len(got.Connections) != 1 || len(got.Connections[0].Profiles) != 0 {
		t.Fatal(got, err)
	}
	cfg.Connections[0].Profiles = []string{"work"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("unusable named Notion selection accepted")
	}
	for _, tool := range []string{"lin", "agent-slack", "agent-fathom"} {
		cfg.Connections[0].Tool = tool
		cfg.Connections[0].Profiles = nil
		if err := cfg.Validate(); err == nil {
			t.Fatal("implicit account selection accepted", tool)
		}
	}
}

func TestAssignmentImportOptInDefaultsAndRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	// Upgrading an old config must not silently enroll every visible assignment.
	if err := os.WriteFile(p, []byte(`{"connections":[{"id":"work","name":"Work","tool":"lin","profiles":["company"]}],"linear":{"team_ids":["team"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Linear.ImportAssignments || cfg.Connections[0].ImportAssignments {
		t.Fatal("old config implicitly enabled imports")
	}
	cfg.Linear.ImportAssignments = true
	cfg.Connections[0].ImportAssignments = true
	if err := Save(p, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil || !got.Linear.ImportAssignments || !got.Connections[0].ImportAssignments {
		t.Fatal("opt-in lost", err)
	}
	for _, tool := range []string{"agent-notion", "agent-slack", "agent-fathom"} {
		cfg.Connections[0].Tool = tool
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "assignment import") {
			t.Fatalf("unsupported import %s: %v", tool, err)
		}
	}
}

func TestLoadingModelInheritsCLIAccountAndCanBeDisabled(t *testing.T) {
	c := Default()
	c.Model.CodexHome = "/synthetic/account"
	m, ok := c.LoadingModel()
	if !ok || m.Engine != "codex" || m.Model != "gpt-5.6-luna" || m.Effort != "low" || m.CodexHome != c.Model.CodexHome {
		t.Fatal(m, ok)
	}
	c.Model.Engine = "claude"
	m, ok = c.LoadingModel()
	if !ok || m.Model != "haiku" || m.ClaudeHome != c.Model.ClaudeHome {
		t.Fatal(m, ok)
	}
	c.Chat.LoadingPhrases.Model = "chosen-small-model"
	m, _ = c.LoadingModel()
	if m.Model != "chosen-small-model" {
		t.Fatal(m)
	}
	c.Model.Engine = "openai-compatible"
	if _, ok = c.LoadingModel(); ok {
		t.Fatal("API loading request enabled")
	}
	c.Model.Engine = "codex"
	c.Chat.LoadingPhrases.Enabled = false
	if _, ok = c.LoadingModel(); ok {
		t.Fatal("disabled loading request enabled")
	}
}
