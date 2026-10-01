package config

import (
	"fmt"
	"strings"
	"unicode"
)

// Browser is whether a member, QA or the assistant may use the browser its
// engine ships, and which of the browsers connected to it. An empty name is
// the one the extension connects by default. It is the owner's real browser,
// with its logins.
type Browser struct {
	On   bool   `json:"on,omitempty"`
	Name string `json:"name,omitempty"`
}

const maxBrowserName = 100

// Trimmed is the setting as it is kept: the browser's name without the
// spaces around it.
func (b Browser) Trimmed() Browser {
	return Browser{On: b.On, Name: strings.TrimSpace(b.Name)}
}

// Validate checks a browser setting for a session on engine.
func (b Browser) Validate(engine string) error {
	if len(b.Name) > maxBrowserName || strings.ContainsFunc(b.Name, unicode.IsControl) {
		return fmt.Errorf("a browser name must be one line of at most %d characters", maxBrowserName)
	}
	if !b.On || Supports(engine, UseBrowser) {
		return nil
	}
	return fmt.Errorf("%s can't use the browser; choose %s, or switch the browser off first", EngineLabel(engine), engineLabels(EnginesFor(UseBrowser)))
}

func engineLabels(engines []string) string {
	labels := make([]string, len(engines))
	for i, e := range engines {
		labels[i] = EngineLabel(e)
	}
	if len(labels) == 0 {
		return "an engine that can"
	}
	return strings.Join(labels, " or ")
}
