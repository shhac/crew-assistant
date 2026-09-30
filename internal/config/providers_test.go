package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func openRouter() Provider {
	return Provider{ID: "openrouter", Name: "OpenRouter", HTTPEngine: HTTPEngine{BaseURL: "https://openrouter.ai/api/v1", APIKeyEnv: "OPENROUTER_API_KEY", EffortParameter: "reasoning.effort"}}
}

func localModel() Provider {
	return Provider{ID: "local", Name: "Local model", HTTPEngine: HTTPEngine{BaseURL: "http://127.0.0.1:11434/v1"}}
}

// Named providers are saved and read back as they were, beside the one
// engines.openai-compatible setting.
func TestProvidersRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	c := Default()
	c.Engines.Providers = []Provider{openRouter(), localModel()}
	c.Assistants[0].Model = Model{Engine: "openai-compatible", Provider: "openrouter", Model: "deepseek/deepseek-r1:free", MaxTokens: 4096}
	c.Models.Suggestions = SmallModel{Engine: "openai-compatible", Provider: "local", Model: "llama3.2"}
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Engines.Providers, c.Engines.Providers) || got.Assistants[0].Model != c.Assistants[0].Model || got.Models.Suggestions != c.Models.Suggestions {
		t.Fatalf("got %+v %+v %+v", got.Engines.Providers, got.Assistants[0].Model, got.Models.Suggestions)
	}
	if unknown := UnknownKeys(path); len(unknown) != 0 {
		t.Fatalf("unknown keys %v", unknown)
	}
}

// A file with only the single engines.openai-compatible setting, as before
// providers, keeps working and shows it as the one provider.
func TestTheSingleAPISettingIsOneProvider(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"engines":{"openai-compatible":{"base_url":"https://api.x.ai/v1","api_key_env":"XAI_API_KEY","effort_parameter":"reasoning_effort"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	providers := c.Engines.APIProviders()
	if len(providers) != 1 {
		t.Fatalf("providers %+v", providers)
	}
	p := providers[0]
	if p.ID != LegacyProvider || p.BaseURL != "https://api.x.ai/v1" || p.APIKeyEnv != "XAI_API_KEY" || p.EffortParameter != "reasoning_effort" || p.Label() != "api.x.ai" {
		t.Fatalf("provider %+v %s", p, p.Label())
	}
	h := c.Harness("openai-compatible", "grok-5", "")
	if h.APIProvider != LegacyProvider || h.BaseURL != "https://api.x.ai/v1" || h.APIKeyEnv != "XAI_API_KEY" || h.EffortParameter != "reasoning_effort" {
		t.Fatalf("harness %+v", h)
	}
	if def := Default().Engines.APIProviders(); len(def) != 1 || def[0].BaseURL != defaultBaseURL || def[0].APIKeyEnv != defaultAPIKeyEnv {
		t.Fatalf("default providers %+v", def)
	}
}

// Each model on the API reaches the provider it names, and no provider is
// engines.openai-compatible.
func TestModelsReachTheirProvider(t *testing.T) {
	c := Default()
	c.Engines.Providers = []Provider{openRouter(), localModel()}
	c.Assistants[0].Model = Model{Engine: "openai-compatible", Provider: "openrouter", Model: "deepseek/deepseek-r1:free", Effort: "high", MaxTokens: 2048}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	h := c.AssistantHarness()
	if h.APIProvider != "openrouter" || h.BaseURL != "https://openrouter.ai/api/v1" || h.APIKeyEnv != "OPENROUTER_API_KEY" || h.EffortParameter != "reasoning.effort" || h.MaxTokens != 2048 {
		t.Fatalf("assistant %+v", h)
	}
	if p := h.Provider(); p.API.BaseURL != "https://openrouter.ai/api/v1" || string(p.API.EffortParameter) != "reasoning.effort" || p.API.Unauthenticated {
		t.Fatalf("provider %+v", p.API)
	}
	c.Models.Suggestions = SmallModel{Engine: "openai-compatible", Provider: "local", Model: "llama3.2"}
	small, err := c.SmallModels()
	if err != nil || len(small) != 1 {
		t.Fatal(small, err)
	}
	if s := small[0]; s.APIProvider != "local" || s.BaseURL != "http://127.0.0.1:11434/v1" || s.APIKeyEnv != "" || s.Model != "llama3.2" || s.MaxTokens != 128 || !s.Provider().API.Unauthenticated {
		t.Fatalf("suggestions %+v", s)
	}
	c.Models.Suggestions.Provider = ""
	if small, _ := c.SmallModels(); small[0].APIProvider != LegacyProvider || small[0].BaseURL != defaultBaseURL {
		t.Fatalf("no provider %+v", small[0])
	}
	if cli := c.HarnessOn("codex", "openrouter", "gpt", ""); cli.APIProvider != "" || cli.BaseURL != "" {
		t.Fatalf("a CLI reached a provider: %+v", cli)
	}
}

func TestProviderSettingsAreChecked(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*Config)
		want   string
	}{
		"a duplicate id": {func(c *Config) { c.Engines.Providers = []Provider{openRouter(), openRouter()} }, "two engines.providers"},
		"the reserved id": {func(c *Config) {
			p := openRouter()
			p.ID = LegacyProvider
			c.Engines.Providers = []Provider{p}
		}, "engines.providers[0].id"},
		"an id with spaces": {func(c *Config) {
			p := openRouter()
			p.ID = "Open Router"
			c.Engines.Providers = []Provider{p}
		}, "engines.providers[0].id"},
		"no name": {func(c *Config) {
			p := openRouter()
			p.Name = " "
			c.Engines.Providers = []Provider{p}
		}, "engines.providers[0].name"},
		"no address": {func(c *Config) {
			p := openRouter()
			p.BaseURL = ""
			c.Engines.Providers = []Provider{p}
		}, "engines.providers[0].base_url"},
		"plain HTTP off this machine": {func(c *Config) {
			p := localModel()
			p.BaseURL = "http://example.com/v1"
			c.Engines.Providers = []Provider{openRouter(), p}
		}, "engines.providers[1].base_url"},
		"a raw key": {func(c *Config) {
			p := openRouter()
			p.APIKeyEnv = "sk-or-v1 secret"
			c.Engines.Providers = []Provider{p}
		}, "engines.providers[0].api_key_env"},
		"an unknown effort key": {func(c *Config) {
			p := openRouter()
			p.EffortParameter = "effort"
			c.Engines.Providers = []Provider{p}
		}, "engines.providers[0].effort_parameter"},
		"an assistant on an unknown provider": {func(c *Config) {
			c.Assistants[0].Model = Model{Engine: "openai-compatible", Provider: "gone", Model: "m", MaxTokens: 4096}
		}, "provider gone"},
		"a CLI model naming a provider": {func(c *Config) {
			c.Engines.Providers = []Provider{openRouter()}
			c.Assistants[0].Model.Provider = "openrouter"
		}, "only for a model on another API"},
		"suggestions on an unknown provider": {func(c *Config) {
			c.Models.Suggestions = SmallModel{Engine: "openai-compatible", Provider: "gone", Model: "m"}
		}, "models.suggestions"},
		"a provider without a suggestions engine": {func(c *Config) {
			c.Models.Suggestions = SmallModel{Provider: "openrouter"}
		}, "models.suggestions"},
	} {
		c := Default()
		tc.mutate(&c)
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	c := Default()
	c.Engines.Providers = []Provider{openRouter(), localModel()}
	c.Assistants[0].Model = Model{Engine: "openai-compatible", Provider: LegacyProvider, Model: "gpt-5", MaxTokens: 4096}
	if err := c.Validate(); err != nil {
		t.Fatalf("valid providers refused: %v", err)
	}
}
