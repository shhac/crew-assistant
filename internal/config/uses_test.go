package config

import (
	"slices"
	"testing"
)

// What each engine is offered for is what the harness says it can do, so an
// engine it starts to support is offered with no change here.
func TestEnginesAreOfferedForWhatTheHarnessSupports(t *testing.T) {
	for use, want := range map[Use][]string{
		UseAssistant: {"codex", "claude", "grok", "openai-compatible"},
		UseRoles:     {"codex", "claude"},
		UseSmall:     {"codex", "claude", "grok", "openai-compatible"},
		UseCompact:   {"codex"},
		UseUsage:     {"codex", "claude"},
		UseModels:    {"codex", "claude", "grok", "openai-compatible"},
		UseEfforts:   {"codex", "claude", "grok"},
	} {
		if got := EnginesFor(use); !slices.Equal(got, want) {
			t.Errorf("%s: got %v, want %v", use, got, want)
		}
	}
	if Supports("", UseModels) || Supports("gemini", UseAssistant) {
		t.Fatal("an engine the harness doesn't know was offered")
	}
	if !slices.Equal(CLIEngineNames, []string{"codex", "claude", "grok"}) {
		t.Fatalf("CLI engines %v", CLIEngineNames)
	}
}

// Stored configs name engines by the harness's spelling, so every engine a
// saved assistant or small model may name is still accepted.
func TestSavedEngineNamesStayValid(t *testing.T) {
	for _, engine := range []string{"codex", "claude", "grok", "openai-compatible"} {
		m := Model{Engine: engine, Model: "m", MaxTokens: 4096}
		if err := m.Validate(); err != nil {
			t.Errorf("%s: %v", engine, err)
		}
	}
	if (Model{Engine: "gemini", Model: "m", MaxTokens: 4096}).Validate() == nil {
		t.Fatal("an engine that can't run the assistant was accepted for it")
	}
}
