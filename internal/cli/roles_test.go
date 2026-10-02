package cli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
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
			if !strings.Contains(hint, "checked when each turn starts") {
				t.Fatal(check)
			}
		} else if hint != reason {
			t.Fatal(check)
		}
		return
	}
	t.Fatal("API role support missing from doctor")
}
