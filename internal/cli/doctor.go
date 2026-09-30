package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/spf13/cobra"

	"github.com/shhac/crew-assistant/internal/config"
	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/account"
	"github.com/shhac/lib-agent-harness/completion"
)

type check = map[string]any

func registerDoctor(root *cobra.Command, o *options) {
	root.AddCommand(&cobra.Command{Use: "doctor", Short: "Check configuration and credential availability without calling providers", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(o.configPath)
		if err != nil {
			return err
		}
		problems := configProblems(o.configPath)
		seated := cfg.AssistantHarness()
		checks := []check{{"name": "config", "ok": true}, {"name": "config keys", "ok": len(problems) == 0, "problems": problems}, {"name": "model", "ok": seated.Model != "", "assistant": cfg.Assistant.Seat, "engine": seated.Engine, "model": seated.Model, "effort": seated.Effort}}
		for _, connection := range cfg.Connections {
			_, lookupErr := exec.LookPath(connection.Tool)
			hint := "Account credentials are managed by this CLI; choose its existing profiles in Settings."
			if connection.Tool == "agent-notion" {
				hint = "Uses the CLI default account and native authentication; no profile is needed."
			}
			checks = append(checks, check{"name": connection.Name + " CLI executable", "tool": connection.Tool, "ok": lookupErr == nil, "queries_supported": true, "profiles": connection.Profiles, "hint": hint})
		}
		refs := []string{}
		if cfg.Slack.OwnerUserID != "" {
			refs = append(refs, cfg.Slack.BotTokenEnv, cfg.Slack.AppTokenEnv)
		}
		if cfg.LegacyLinearImportEnabled() {
			refs = append(refs, cfg.Linear.APIKeyEnv)
		}
		assistant, err := assistantEngineChecks(cmd.Context(), cfg)
		if err != nil {
			return err
		}
		checks = append(checks, assistant...)
		if _, ok := cfg.Engines.CLI(seated.Engine); !ok && seated.Engine != "" {
			refs = append(refs, seated.APIKeyEnv)
		}
		checks = append(checks, roleSandboxChecks(cmd.Context(), cfg, o.statePath)...)
		for _, ref := range refs {
			if ref != "" {
				checks = append(checks, check{"name": ref, "ok": os.Getenv(ref) != "", "hint": "set in the daemon environment if using this integration"})
			}
		}
		return o.emit(map[string]any{"checks": checks})
	}})
}

// assistantEngineChecks prove the seated assistant's CLI is installed and
// signed in, without inference. An API engine, or an empty seat, has nothing
// to check here.
func assistantEngineChecks(ctx context.Context, cfg config.Config) ([]check, error) {
	h := cfg.AssistantHarness()
	if harness.Engine(h.Engine).Transport() != harness.CLITransport {
		return nil, nil
	}
	checks := []check{}
	// Codex reads instruction files from its home, so the assistant's must
	// be one of its own.
	if harness.Engine(h.Engine) == harness.Codex {
		isolationErr := completion.ValidateCodexHome(h.Home)
		isolationHint := "Run crew-assistant model login to sign into the configured engines.codex.home."
		if isolationErr != nil {
			isolationHint = isolationErr.Error()
		}
		checks = append(checks, check{"name": "codex instruction isolation", "ok": isolationErr == nil, "hint": isolationHint})
	}
	return append(checks, loginChecks(ctx, h, account.Inspect)...), nil
}

// loginChecks find the engine's executable and, when it is there and the
// harness can read its account, ask whether it is signed in.
func loginChecks(ctx context.Context, h config.Harness, inspect func(context.Context, harness.Provider) (harness.AccountReport, error)) []check {
	label := config.EngineLabel(h.Engine)
	_, lookupErr := exec.LookPath(h.Bin)
	checks := []check{{"name": h.Engine + " executable", "ok": lookupErr == nil, "hint": fmt.Sprintf("Install the %s CLI or set engines.%s.bin.", label, h.Engine)}}
	if lookupErr != nil || !harness.Support(harness.Engine(h.Engine), harness.Account, harness.Login).Usable() {
		return checks
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	report, _ := inspect(ctx, h.Provider())
	signedIn := report.Account.LoggedIn != nil && *report.Account.LoggedIn
	return append(checks, check{"name": h.Engine + " login", "ok": signedIn, "hint": "Sign in once with crew-assistant model login; team roles on this CLI share the login. No inference was invoked."})
}
