package cli

import (
	"context"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/spf13/cobra"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/engine"
)

type check = map[string]any

func registerDoctor(root *cobra.Command, o *options) {
	root.AddCommand(&cobra.Command{Use: "doctor", Short: "Check configuration and credential availability without calling providers", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(o.configPath)
		if err != nil {
			return err
		}
		problems := configProblems(o.configPath)
		checks := []check{{"name": "config", "ok": true}, {"name": "config keys", "ok": len(problems) == 0, "problems": problems}, {"name": "model", "ok": cfg.Model.Model != "", "engine": cfg.Model.Engine, "model": cfg.Model.Model, "effort": cfg.Model.Effort}}
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
		if _, ok := cfg.Engines.CLI(cfg.Model.Engine); !ok {
			_, apiKeyEnv := cfg.Engines.Endpoint()
			refs = append(refs, apiKeyEnv)
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

// assistantEngineChecks prove the assistant's CLI is installed and signed
// in, without inference. An API engine has nothing to check here.
func assistantEngineChecks(ctx context.Context, cfg config.Config) ([]check, error) {
	bin, home := cfg.Engines.Binary(cfg.Model.Engine)
	switch cfg.Model.Engine {
	case "codex":
		isolationErr := engine.ValidateCodexHome(home)
		isolationHint := "Run crew-assistant model login to sign into the configured engines.codex.home."
		if isolationErr != nil {
			isolationHint = isolationErr.Error()
		}
		checks := []check{{"name": "codex instruction isolation", "ok": isolationErr == nil, "hint": isolationHint}}
		// Match the inference transport: use Codex's stored login, not
		// unrelated provider keys inherited from the daemon environment.
		login, err := loginChecks(ctx, "codex", bin, func() ([]string, error) { return engine.CodexEnvironment(home) }, []string{"login", "status"},
			"install Codex or set engines.codex.bin",
			"run crew-assistant model login; no inference was invoked")
		return append(checks, login...), err
	case "claude":
		return loginChecks(ctx, "claude", bin, func() ([]string, error) { return engine.ClaudeEnvironment(home) }, []string{"auth", "status"},
			"Install Claude CLI to use its existing subscription login.",
			"Sign in once with crew-assistant model login; workers using this CLI home share the login.")
	}
	return nil, nil
}

// loginChecks find the engine's executable and, when it is there, ask it
// whether it is signed in.
func loginChecks(ctx context.Context, name, bin string, environment func() ([]string, error), status []string, installHint, loginHint string) ([]check, error) {
	binary, lookupErr := exec.LookPath(bin)
	checks := []check{{"name": name + " executable", "ok": lookupErr == nil, "hint": installHint}}
	if lookupErr != nil {
		return checks, nil
	}
	env, err := environment()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	probe := exec.CommandContext(ctx, binary, status...)
	probe.Env = env
	probe.Stdout, probe.Stderr = io.Discard, io.Discard
	return append(checks, check{"name": name + " login", "ok": probe.Run() == nil, "hint": loginHint}), nil
}
