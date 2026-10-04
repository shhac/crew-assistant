// Package autopilot describes saved function choices, not execution authority.
package autopilot

import "fmt"

type Mode string

const (
	Off      Mode = "off"
	Suggest  Mode = "suggest"
	Act      Mode = "act"
	Operator      = "landing-release-operator"
)

type Function struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Group       string `json:"group"`
	AvailableIn string `json:"available_in"`
	DefaultMode Mode   `json:"default_mode"`
	Available   bool   `json:"available"`
}

// Catalog returns an independent copy. No actual function is available yet.
func Catalog() []Function {
	return []Function{
		{"authorised-research", "More authorised research", "routine", "CA-99", Suggest, false},
		{"proposed-split", "Proposed split", "routine", "CA-99", Suggest, false},
		{"owner-step", "Record an owner step", "routine", "CA-99", Suggest, false},
		{"narrow-edge-cases", "Accept narrow edge cases", "routine", "CA-99", Suggest, false},
		{"settled-prerequisite", "Confirm a settled prerequisite", "routine", "CA-99", Suggest, false},
		{"cross-project-patterns", "Cross-project patterns", "health", "CA-100", Suggest, false},
		{"ci-failures", "CI failures", "health", "CA-100", Suggest, false},
		{"flaky-tests", "Flaky tests", "health", "CA-100", Suggest, false},
		{"engine-usage", "Engine usage", "health", "CA-100", Suggest, false},
		{"repeated-friction", "Repeated friction", "health", "CA-100", Suggest, false},
		{Operator, "Landing and release operator", "landing", "CA-101", Off, false},
	}
}

type Settings struct {
	Revisions map[string]uint64 `json:"revisions,omitempty"`
	Modes     map[string]Mode   `json:"modes,omitempty"`
}

func (s Settings) Validate() error {
	for id, mode := range s.Modes {
		if id == "" {
			return fmt.Errorf("autopilot function ID is empty")
		}
		switch mode {
		case "", Off, Suggest, Act:
		default:
			return fmt.Errorf("invalid autopilot mode %q for %s", mode, id)
		}
	}
	return nil
}

// EffectiveMode rejects unknown functions; future saved settings have no effect.
func (s Settings) EffectiveMode(id string) (Mode, error) {
	for _, f := range Catalog() {
		if f.ID != id {
			continue
		}
		mode := s.Modes[id]
		switch mode {
		case "":
			return f.DefaultMode, nil
		case Off, Suggest, Act:
			return mode, nil
		default:
			return Off, fmt.Errorf("invalid autopilot mode %q", mode)
		}
	}
	return Off, fmt.Errorf("unknown autopilot function %q", id)
}
