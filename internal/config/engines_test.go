package config

import (
	"os"
	"path/filepath"
	"testing"
)

func intp(n int) *int { return &n }

func TestEnginesFillTheirDefaults(t *testing.T) {
	c := Default()
	if bin, home := c.Engines.Binary("codex"); bin != "codex" || home != DefaultCodexHome() {
		t.Fatalf("codex %s %s", bin, home)
	}
	if five, week := c.Engines.Floors("claude"); five != DefaultUsageFloor || week != DefaultUsageFloor {
		t.Fatalf("floors %d %d", five, week)
	}
	if five, week := c.Engines.Floors("openai-compatible"); five != 0 || week != 0 {
		t.Fatal("an API engine has no usage to floor")
	}
	c.Engines.Claude.UsageFloor.WeekPercent = intp(0)
	if _, week := c.Engines.Floors("claude"); week != 0 {
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
