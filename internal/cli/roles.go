package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/lib-agent-harness/session"
)

// roleSandboxChecks reports, for each installed CLI, whether team roles can
// run under its sandbox. It runs the same proof a role does before starting,
// and none of it performs inference.
func roleSandboxChecks(ctx context.Context, cfg config.Config, statePath string) []map[string]any {
	checks := []map[string]any{}
	work, err := os.MkdirTemp("", "crew-assistant-doctor-")
	if err != nil {
		return checks
	}
	defer os.RemoveAll(work)
	for _, engine := range []session.Engine{session.Codex, session.Claude} {
		o := session.Options{Engine: engine, WorkDir: work, Sandbox: &session.Sandbox{Write: true}}
		o.Binary, o.Home = cfg.Model.EngineBinary(string(engine))
		if engine == session.Codex {
			o.RuntimeHome = filepath.Join(filepath.Dir(statePath), "roles", "codex")
		}
		name := "team roles on " + string(engine)
		if _, lookupErr := exec.LookPath(o.Binary); lookupErr != nil {
			checks = append(checks, map[string]any{"name": name, "ok": false, "hint": "not installed; projects cannot use " + string(engine) + " for a role"})
			continue
		}
		checkCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		verifyErr := session.VerifySandbox(checkCtx, o)
		cancel()
		hint := "sandbox verified: workspace-only writes, no network"
		if verifyErr != nil {
			hint = verifyErr.Error()
		}
		checks = append(checks, map[string]any{"name": name, "ok": verifyErr == nil, "hint": hint})
	}
	return checks
}
