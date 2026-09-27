package app

import (
	"context"
	"reflect"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/engine"
	harness "github.com/shhac/lib-agent-harness"
)

func TestEngineConfigReachesEachEngineItsOwnWay(t *testing.T) {
	for _, tc := range []struct {
		h    config.Harness
		want engine.Config
	}{
		{config.Harness{Engine: "codex", Model: "m", Effort: "low", MaxTokens: 256, Bin: "/opt/codex", Home: "/synthetic/codex"},
			engine.Config{Provider: harness.Provider{Engine: harness.Codex, CLI: harness.CLI{Binary: "/opt/codex", Home: "/synthetic/codex"}}, Model: "m", Effort: "low", MaxOutputTokens: 256}},
		{config.Harness{Engine: "claude", Model: "m", Bin: "/opt/claude", Home: "/synthetic/claude"},
			engine.Config{Provider: harness.Provider{Engine: harness.Claude, CLI: harness.CLI{Binary: "/opt/claude", Home: "/synthetic/claude"}}, Model: "m"}},
	} {
		if got := EngineConfig(tc.h); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", tc.h.Engine, got, tc.want)
		}
	}
}

// An API is reached at its base URL, with its key read from the environment
// only when a request is made, and never held in the config.
func TestEngineConfigReachesAnAPIWithAKeyReadWhenAsked(t *testing.T) {
	ec := EngineConfig(config.Harness{Engine: "openai-compatible", Model: "m", BaseURL: "https://api.example.test/v1", APIKeyEnv: "EXAMPLE_KEY"})
	api := ec.Provider.API
	if ec.Engine() != "openai-compatible" || ec.Model != "m" || api.BaseURL != "https://api.example.test/v1" || api.Dialect != harness.OpenAIChatCompletions || api.EffortParameter != harness.EffortReasoningEffort || ec.Provider.Problem() != "" {
		t.Fatalf("%+v", ec)
	}
	t.Setenv("EXAMPLE_KEY", "")
	if _, err := api.Credentials(context.Background()); err == nil {
		t.Fatal("an unset key was sent")
	}
	t.Setenv("EXAMPLE_KEY", "synthetic-key")
	if key, err := api.Credentials(context.Background()); err != nil || key != "synthetic-key" {
		t.Fatalf("%q %v", key, err)
	}
	local := EngineConfig(config.Harness{Engine: "openai-compatible", Model: "m", BaseURL: "http://127.0.0.1:8080/v1", EffortParameter: "reasoning.effort"})
	if !local.Provider.API.Unauthenticated || local.Provider.API.Credentials != nil || local.Provider.API.EffortParameter != harness.EffortReasoningObject || local.Provider.Problem() != "" {
		t.Fatalf("%+v", local.Provider.API)
	}
}
