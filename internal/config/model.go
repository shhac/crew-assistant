package config

import (
	"errors"
	"fmt"
	"slices"
)

// Model is an assistant profile's model choice; the engine it names is
// reached as Engines says.
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

// Models are the models the daemon uses for its own small jobs, apart from
// the assistant's conversation.
type Models struct {
	// Suggestions writes suggested next messages and loading lines.
	Suggestions SmallModel `json:"suggestions"`
}

// SmallModel is any model for a small job, on a CLI login or the API. An
// empty engine is the approved small models, which is the default. Each
// call is bounded the same way whatever the engine: a short reply, a small
// context and no retries.
type SmallModel struct {
	Engine string `json:"engine"`
	Model  string `json:"model"`
	Effort string `json:"effort"`
}

func (m Models) validate() error {
	s := m.Suggestions
	if s.Engine == "" {
		if s.Model != "" || s.Effort != "" {
			return errors.New("models.suggestions: choose an engine for the model, or leave all three empty for the small models")
		}
		return nil
	}
	if !slices.Contains(EngineNames, s.Engine) {
		return errors.New("models.suggestions.engine must be codex, claude, openai-compatible or empty")
	}
	if s.Model == "" || len(s.Model) > 80 {
		return errors.New("models.suggestions.model must contain 1–80 characters")
	}
	if !slices.Contains(Efforts, s.Effort) {
		return errors.New("models.suggestions.effort must be empty, none, minimal, low, medium, high, xhigh, max or ultra")
	}
	return nil
}

// approvedSmallModels are what small jobs use unless the owner chooses a
// model for them, one per CLI engine. Luna runs at low effort; Haiku 4.5
// has no effort setting, so none is sent for it.
var approvedSmallModels = map[string]struct{ model, effort string }{
	"codex":  {"gpt-6-luna", "low"},
	"claude": {"haiku", ""},
}

// SmallModels lists the models to try in order for a small job. A model the
// owner chose is the only one. Otherwise they are the approved small models:
// the seated assistant's CLI engine first, then the other one, or Codex
// first while no one is seated. Each uses that CLI's configured login. An
// API assistant has no CLI of its own to start from, so it gets none rather
// than a guessed engine.
func (c Config) SmallModels() ([]Harness, error) {
	if chosen := c.Models.Suggestions; chosen.Engine != "" {
		m := c.Harness(chosen.Engine, chosen.Model, chosen.Effort)
		m.MaxTokens = 128
		return []Harness{m}, nil
	}
	order := []string{"codex", "claude"}
	if seated, ok := c.Seated(); ok {
		switch seated.Model.Engine {
		case "codex":
		case "claude":
			order = []string{"claude", "codex"}
		default:
			return nil, fmt.Errorf("no approved small model for the %s engine", seated.Model.Engine)
		}
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

// ApprovedSmallModel reports whether engine/model is one of the approved
// small models, the default for small jobs.
func ApprovedSmallModel(engine, model string) bool {
	approved, ok := approvedSmallModels[engine]
	return ok && approved.model == model
}
