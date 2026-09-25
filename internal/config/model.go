package config

import (
	"errors"
	"fmt"
	"slices"
)

// Model is the assistant's own model choice; the engine it names is reached
// as Engines says.
type Model struct {
	Engine    string `json:"engine"`
	Model     string `json:"model"`
	Effort    string `json:"effort"`
	MaxTokens int    `json:"max_tokens"`
}

func defaultModel() Model {
	return Model{Engine: "codex", Model: "gpt-6-astra", Effort: "high", MaxTokens: 4096}
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
