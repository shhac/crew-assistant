package app

import (
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/engine"
)

// EngineConfig is how the engine reaches a model: its CLI and login, or the
// endpoint and the key it reads.
func EngineConfig(h config.Harness) engine.Config {
	return engine.Config{Provider: h.Provider(), Model: h.Model, Effort: h.Effort, MaxOutputTokens: h.MaxTokens}
}
