package config

import (
	"slices"
	"strings"
	"testing"

	harness "github.com/shhac/lib-agent-harness"
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
		UseBrowser:   {"codex", "claude"},
		UseLoopback:  {"claude"},
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

// Team roles use an API provider only once the harness claims a sandboxed
// session with the daemon's tools for it; until then, the owner is told why.
func TestRolesOnAnAPIProviderWaitForTheHarness(t *testing.T) {
	ok, reason := RoleSupport("openai-compatible")
	if ok || !strings.Contains(reason, "An API provider can't run team roles yet") {
		t.Fatalf("API provider: %v %q", ok, reason)
	}
	if err := CheckRoleEngine("openai-compatible"); err == nil || !strings.Contains(err.Error(), "engine must be codex or claude") || !strings.Contains(err.Error(), reason) {
		t.Fatalf("check %v", err)
	}
	if ok, reason := RoleSupport("claude"); !ok || reason != "" {
		t.Fatalf("claude %v %q", ok, reason)
	}
	if err := CheckRoleEngine("gemini"); err == nil || err.Error() != "engine must be codex or claude" {
		t.Fatalf("unknown engine %v", err)
	}
	real := roleSupport
	t.Cleanup(func() { roleSupport = real })
	roleSupport = func(e harness.Engine, op harness.Operation, f harness.Feature) harness.Capability {
		if e == harness.OpenAICompatible && op == harness.Session && (f == harness.Sandbox || f == harness.Tools) {
			return harness.Capability{Availability: harness.Composed}
		}
		return real(e, op, f)
	}
	if ok, reason := RoleSupport("openai-compatible"); !ok || reason != "" {
		t.Fatalf("a harness that claims it: %v %q", ok, reason)
	}
	if !slices.Contains(EnginesFor(UseRoles), "openai-compatible") || CheckRoleEngine("openai-compatible") != nil {
		t.Fatal("roles weren't offered the API once the harness claimed it")
	}
	// Tools alone aren't enough: every role works in a sandbox.
	roleSupport = func(e harness.Engine, op harness.Operation, f harness.Feature) harness.Capability {
		if e == harness.OpenAICompatible && f == harness.Tools {
			return harness.Capability{Availability: harness.Composed}
		}
		return real(e, op, f)
	}
	if ok, _ := RoleSupport("openai-compatible"); ok {
		t.Fatal("an API without a sandbox was offered to roles")
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
