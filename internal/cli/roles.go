package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/roles"
	harness "github.com/shhac/lib-agent-harness"
)

// roleSandboxChecks reports, for each CLI team roles can run on, whether
// they can run under its sandbox. It runs the same proof a role does before starting,
// and none of it performs inference.
func roleSandboxChecks(ctx context.Context, cfg config.Config, statePath string) []map[string]any {
	checks := []map[string]any{}
	work, err := os.MkdirTemp("", "crew-assistant-doctor-")
	if err != nil {
		return checks
	}
	defer os.RemoveAll(work)
	for _, e := range harness.Engines() {
		engine := string(e)
		if e.Transport() == harness.APITransport {
			ok, reason := config.RoleSupport(engine)
			if ok {
				reason = "Offered; its sandbox is checked when each turn starts"
			}
			checks = append(checks, map[string]any{"name": "team roles on " + engine, "ok": ok, "hint": reason})
			continue
		}
		if !config.Supports(engine, config.UseRoles) {
			continue
		}
		spec := roles.Spec{Engine: engine, RuntimeHome: filepath.Join(filepath.Dir(statePath), "roles", engine), WorkDir: work, Write: true}
		spec.Binary, spec.Home = cfg.Engines.Binary(engine)
		name := "team roles on " + engine
		if _, lookupErr := exec.LookPath(spec.Binary); lookupErr != nil {
			checks = append(checks, map[string]any{"name": name, "ok": false, "hint": "not installed; projects cannot use " + engine + " for a role"})
			continue
		}
		checkCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		verifyErr := roles.VerifySandbox(checkCtx, spec)
		cancel()
		hint := "sandbox verified: workspace-only writes, no network"
		if verifyErr != nil {
			hint = verifyErr.Error()
		}
		checks = append(checks, map[string]any{"name": name, "ok": verifyErr == nil, "hint": hint})
	}
	return checks
}
