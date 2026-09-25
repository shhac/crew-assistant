package app

import (
	"strings"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/engine"
)

// EngineConfig is how the engine reaches a model: its CLI and login, or the
// endpoint and the key it reads.
func EngineConfig(h config.Harness) engine.Config {
	ec := engine.Config{Engine: h.Engine, Model: h.Model, Effort: h.Effort, MaxOutputTokens: h.MaxTokens}
	switch h.Engine {
	case "codex":
		ec.CodexBin, ec.CodexHome = h.Bin, h.Home
	case "claude":
		ec.ClaudeBin, ec.ClaudeHome = h.Bin, h.Home
	default:
		ec.Endpoint, ec.APIKeyEnv = strings.TrimRight(h.BaseURL, "/")+"/chat/completions", h.APIKeyEnv
	}
	return ec
}
