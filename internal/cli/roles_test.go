package cli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
	harness "github.com/shhac/lib-agent-harness"
)

func TestAPIRoleDoctorReportsOfferingWithoutClaimingAProof(t *testing.T) {
	cfg := config.Default()
	missing := filepath.Join(t.TempDir(), "not-installed")
	cfg.Engines.Codex.Bin, cfg.Engines.Claude.Bin, cfg.Engines.Grok.Bin = missing, missing, missing
	checks := roleSandboxChecks(context.Background(), cfg, filepath.Join(t.TempDir(), "state.db"))
	for _, check := range checks {
		if check["name"] != "team roles on openai-compatible" {
			continue
		}
		ok, reason := config.RoleSupport("openai-compatible")
		hint, _ := check["hint"].(string)
		if check["ok"] != ok || strings.Contains(hint, "sandbox verified") {
			t.Fatal(check)
		}
		if ok {
			if !strings.Contains(hint, "checked when each turn starts") || !strings.Contains(hint, absentRoleFileTools(config.RoleFileTools("openai-compatible"))) {
				t.Fatal(check)
			}
		} else if hint != reason {
			t.Fatal(check)
		}
		return
	}
	t.Fatal("API role support missing from doctor")
}

func TestAbsentRoleFileToolsKeepsEachClaimsNamesAndReason(t *testing.T) {
	read := config.RoleFileToolClaim{Feature: harness.WorkspaceRead, Capability: harness.Capability{Availability: harness.Unsupported, Reason: "read proof missing"}}
	write := config.RoleFileToolClaim{Feature: harness.WorkspaceWrite, Capability: harness.Capability{Availability: harness.Unsupported, Reason: "write proof missing"}}
	for _, tc := range []struct {
		claims []config.RoleFileToolClaim
		want   string
	}{
		{[]config.RoleFileToolClaim{read, write}, "read_file, search_files are off: read proof missing; edit_file is off: write proof missing"},
		{[]config.RoleFileToolClaim{read}, "read_file, search_files are off: read proof missing"},
		{[]config.RoleFileToolClaim{write}, "edit_file is off: write proof missing"},
	} {
		if got := absentRoleFileTools(tc.claims); got != tc.want {
			t.Fatalf("got %q, want %q", got, tc.want)
		}
	}
}
