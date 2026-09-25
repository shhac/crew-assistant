package app

import (
	"reflect"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/engine"
)

func TestEngineConfigReachesEachEngineItsOwnWay(t *testing.T) {
	for _, tc := range []struct {
		h    config.Harness
		want engine.Config
	}{
		{config.Harness{Engine: "codex", Model: "m", Effort: "low", MaxTokens: 256, Bin: "/opt/codex", Home: "/synthetic/codex"},
			engine.Config{Engine: "codex", Model: "m", Effort: "low", MaxOutputTokens: 256, CodexBin: "/opt/codex", CodexHome: "/synthetic/codex"}},
		{config.Harness{Engine: "claude", Model: "m", Bin: "/opt/claude", Home: "/synthetic/claude"},
			engine.Config{Engine: "claude", Model: "m", ClaudeBin: "/opt/claude", ClaudeHome: "/synthetic/claude"}},
		{config.Harness{Engine: "openai-compatible", Model: "m", BaseURL: "https://api.example.test/v1/", APIKeyEnv: "EXAMPLE_KEY"},
			engine.Config{Engine: "openai-compatible", Model: "m", Endpoint: "https://api.example.test/v1/chat/completions", APIKeyEnv: "EXAMPLE_KEY"}},
	} {
		if got := EngineConfig(tc.h); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", tc.h.Engine, got, tc.want)
		}
	}
}
