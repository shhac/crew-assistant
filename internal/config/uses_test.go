package config

import (
	"runtime"
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
		if (use == UseRoles && (runtime.GOOS == "darwin" || runtime.GOOS == "linux")) || (use == UseLoopback && runtime.GOOS == "darwin") {
			want = append(want, "openai-compatible")
		}
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

func TestRolesOnAnAPIProviderFollowTheHarness(t *testing.T) {
	real := roleSupport
	t.Cleanup(func() { roleSupport = real })
	for _, missing := range []harness.Feature{"", harness.Sandbox, harness.Tools, harness.WorkspaceRead, harness.WorkspaceWrite} {
		for _, platform := range []string{"Linux needs a proved command sandbox", "Windows has no command sandbox."} {
			roleSupport = func(e harness.Engine, op harness.Operation, f harness.Feature) harness.Capability {
				if e == harness.OpenAICompatible && op == harness.Session {
					if f == missing {
						return harness.Capability{Availability: harness.Unsupported, Reason: platform}
					}
					return harness.Capability{Availability: harness.Composed}
				}
				return real(e, op, f)
			}
			ok, reason := RoleSupport("openai-compatible")
			if missing == "" || missing == harness.WorkspaceRead || missing == harness.WorkspaceWrite {
				if !ok || reason != "" || CheckRoleEngine("openai-compatible") != nil {
					t.Fatalf("offered: %v %q", ok, reason)
				}
			} else {
				if strings.Contains(reason, "..") {
					t.Fatal(reason)
				}
				if ok || !strings.Contains(reason, platform) || !strings.Contains(reason, "Another API can't run team roles on this computer") {
					t.Fatalf("%s: %v %q", missing, ok, reason)
				}
				if err := CheckRoleEngine("openai-compatible"); err == nil || !strings.Contains(err.Error(), reason) {
					t.Fatal(err)
				}
			}
		}
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

func TestTemplateRoleEnginesStayOnCLIs(t *testing.T) {
	if slices.Contains(TemplateRoleEngines(), "openai-compatible") {
		t.Fatal("API offered to engine-only seats")
	}
	if err := CheckTemplateRoleEngine("openai-compatible"); err == nil || !strings.Contains(err.Error(), "provider and model") {
		t.Fatal(err)
	}
	for _, engine := range []string{"claude", "codex"} {
		if err := CheckTemplateRoleEngine(engine); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRoleModelRequiresAnIDOnlyOnAnAPI(t *testing.T) {
	for _, engine := range []string{"claude", "codex", "openai-compatible"} {
		for _, model := range []string{"", " \t ", "unlisted-model"} {
			err := CheckRoleModel(engine, model)
			wantError := engine == "openai-compatible" && strings.TrimSpace(model) == ""
			if (err != nil) != wantError {
				t.Fatalf("%s %q: %v", engine, model, err)
			}
		}
	}
}

func TestRoleFileToolsReportHarnessAbsence(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return
	}
	claims := RoleFileTools("openai-compatible")
	if len(claims) != 2 {
		t.Fatal(claims)
	}
	if claims[0].Feature != harness.WorkspaceRead || claims[1].Feature != harness.WorkspaceWrite {
		t.Fatal(claims)
	}
	for _, c := range claims {
		if c.Usable() || c.Reason != "workbench file tools are off until their workspace check is verified" {
			t.Fatal(c)
		}
	}
	if got := RoleFileTools("codex"); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestMissingCommandsOrCallerToolsRefusesEvenWithFileToolsOff(t *testing.T) {
	real := roleSupport
	t.Cleanup(func() { roleSupport = real })
	for _, missing := range []harness.Feature{harness.Sandbox, harness.Tools} {
		roleSupport = func(e harness.Engine, op harness.Operation, f harness.Feature) harness.Capability {
			if f == missing || f == harness.WorkspaceRead || f == harness.WorkspaceWrite {
				return harness.Capability{Availability: harness.Unsupported, Reason: "required capability absent"}
			}
			return harness.Capability{Availability: harness.Composed}
		}
		if ok, reason := RoleSupport("openai-compatible"); ok || !strings.Contains(reason, "required capability absent") {
			t.Fatalf("%v %s", ok, reason)
		}
	}
}
