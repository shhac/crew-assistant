package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func intp(n int) *int { return &n }

func TestEnginesFillTheirDefaults(t *testing.T) {
	c := Default()
	if bin, home := c.Engines.Binary("codex"); bin != "codex" || home != DefaultCodexHome() {
		t.Fatalf("codex %s %s", bin, home)
	}
	if five, week, ok := c.Engines.Floors("claude"); !ok || five != DefaultUsageFloor || week != DefaultUsageFloor {
		t.Fatalf("floors %d %d %v", five, week, ok)
	}
	if _, _, ok := c.Engines.Floors("openai-compatible"); ok {
		t.Fatal("an API engine has no usage to floor")
	}
	c.Engines.Claude.UsageFloor.WeekPercent = intp(0)
	if _, week, _ := c.Engines.Floors("claude"); week != 0 {
		t.Fatal("0 should turn the weekly floor off")
	}
	if url, env := c.Engines.Endpoint(); url != "https://api.openai.com/v1" || env != "OPENAI_API_KEY" {
		t.Fatalf("endpoint %s %s", url, env)
	}
	c.Engines.Codex.Home = "/elsewhere"
	if h := c.Harness("codex", "gpt", "low"); h.Bin != "codex" || h.Home != "/elsewhere" || h.Model != "gpt" || h.MaxTokens != c.Model.MaxTokens {
		t.Fatalf("harness %+v", h)
	}
}

func TestAnEarlierLayoutLoadsAndIsRewrittenOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	old := `{
  "//": "the owner's note",
  "model": {"engine": "claude", "model": "opus", "effort": "", "max_tokens": 4096,
            "codex_bin": "codex", "codex_home": "` + DefaultCodexHome() + `",
            "claude_bin": "/opt/claude", "claude_home": "/Users/me/.claude-work",
            "base_url": "https://api.openai.com/v1", "api_key_env": "OPENAI_API_KEY"},
  "limits": {"max_model_turns": 7, "role_usage": {"codex_max_used_percent": 90, "claude_max_used_percent": 98, "on_unavailable": "pause"}},
  "chat": {"loading_phrases": {"enabled": true, "model": "x", "effort": "low"}},
  "future": {"setting": 1}
}`
	if err := os.WriteFile(path, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if bin, home := c.Engines.Binary("claude"); bin != "/opt/claude" || home != "/Users/me/.claude-work" || c.Engines.Codex.Home != "" || c.Engines.Codex.Bin != "" {
		t.Fatalf("engines %+v", c.Engines)
	}
	if five, week, _ := c.Engines.Floors("claude"); five != 2 || week != 2 {
		t.Fatalf("a 98%% limit should leave 2%%: %d %d", five, week)
	}
	if c.Engines.Codex.UsageFloor.FiveHourPercent != nil || !c.Engines.PauseOnUnknownUsage("codex") || c.Limits.MaxModelTurns != 7 {
		t.Fatalf("the old default should stay a default, and pause carry over: %+v %+v", c.Engines.Codex, c.Limits)
	}
	if changed, err := Upgrade(path); err != nil || !changed {
		t.Fatalf("upgrade %v %v", changed, err)
	}
	data, _ := os.ReadFile(path)
	for _, gone := range []string{"codex_bin", "role_usage", `"model": "x"`, "max_used_percent"} {
		if strings.Contains(string(data), gone) {
			t.Fatalf("%s is still in the rewritten file:\n%s", gone, data)
		}
	}
	for _, kept := range []string{"the owner's note", `"future"`} {
		if !strings.Contains(string(data), kept) {
			t.Fatalf("%s was lost:\n%s", kept, data)
		}
	}
	if changed, _ := Upgrade(path); changed {
		t.Fatal("a file in the current layout was rewritten")
	}
	again, err := Load(path)
	if err != nil || again.Engines.Claude.Bin != "/opt/claude" {
		t.Fatalf("the rewritten file reads differently: %+v %v", again.Engines, err)
	}
	unknown := UnknownKeys(path)
	if len(unknown) != 1 || unknown[0].Path != "future" || unknown[0].Renamed {
		t.Fatalf("unknown %+v", unknown)
	}
	if err := Save(path, again); err != nil {
		t.Fatal(err)
	}
	if data, _ = os.ReadFile(path); !strings.Contains(string(data), `"future"`) {
		t.Fatal("saving dropped a key from a newer version")
	}
}

func TestAnOldDisabledLimitStaysOff(t *testing.T) {
	doc := map[string]any{"limits": map[string]any{"role_usage": map[string]any{"codex_max_used_percent": 0.0}}}
	convertLegacy(doc)
	raw, _ := json.Marshal(doc)
	var c Config
	json.Unmarshal(raw, &c)
	if five, week, _ := c.Engines.Floors("codex"); five != 0 || week != 0 {
		t.Fatalf("a disabled limit became floors %d %d", five, week)
	}
}

func TestAKeyFromAnEarlierLayoutSaysWhereItWent(t *testing.T) {
	if to, ok := RenamedKey("model.codex_home"); !ok || to != "engines.codex.home" {
		t.Fatal(to, ok)
	}
	if _, ok := RenamedKey("model.engine"); ok {
		t.Fatal("a current key reported as renamed")
	}
	for key, want := range map[UnknownKey]string{
		{Path: "model.codex_home", Renamed: true, To: "engines.codex.home"}: "model.codex_home is now engines.codex.home",
		{Path: "chat.loading_phrases.model", Renamed: true}:                 "chat.loading_phrases.model is no longer a setting; remove it with crew-assistant config unset chat.loading_phrases.model",
		{Path: "futur"}: "futur is not a setting this version knows, so it has no effect; remove it with crew-assistant config unset futur",
	} {
		if key.String() != want {
			t.Errorf("%q", key.String())
		}
	}
}

func TestEngineSettingsAreChecked(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"a floor over 100":      func(c *Config) { c.Engines.Codex.UsageFloor.FiveHourPercent = intp(101) },
		"a negative floor":      func(c *Config) { c.Engines.Claude.UsageFloor.WeekPercent = intp(-1) },
		"a relative home":       func(c *Config) { c.Engines.Codex.Home = "relative/home" },
		"an unknown behaviour":  func(c *Config) { c.Engines.Claude.OnUnknownUsage = "deny" },
		"a URL with a password": func(c *Config) { c.Engines.OpenAICompatible.BaseURL = "https://u:p@example.com" },
		"plain HTTP":            func(c *Config) { c.Engines.OpenAICompatible.BaseURL = "http://example.com" },
		"a raw key":             func(c *Config) { c.Engines.OpenAICompatible.APIKeyEnv = "raw token!" },
	} {
		c := Default()
		mutate(&c)
		if c.Validate() == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// The example in the repository is a config this version reads in full.
func TestTheExampleConfigIsCurrent(t *testing.T) {
	path := filepath.Join("..", "..", "config.example.json")
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if unknown := UnknownKeys(path); len(unknown) != 0 {
		t.Fatalf("unknown keys in the example: %v", unknown)
	}
	data, _ := os.ReadFile(path)
	if converted, _ := ConvertLegacyJSON(data); string(converted) != string(data) {
		t.Fatal("the example is in an earlier layout")
	}
	if c.Engines.Claude.UsageFloor.WeekPercent == nil || *c.Engines.Claude.UsageFloor.WeekPercent != DefaultUsageFloor {
		t.Fatalf("floors %+v", c.Engines.Claude.UsageFloor)
	}
}
