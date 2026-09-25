package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/engine"
	"github.com/spf13/cobra"
)

func registerModel(root *cobra.Command, o *options) {
	var engineName string
	models := &cobra.Command{Use: "model", Short: "Manage the configured model runtime"}
	login := &cobra.Command{Use: "login", Short: "Sign in using the configured runtime home", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Load(o.configPath)
		if err != nil {
			return err
		}
		// Team roles use the same CLI homes as the assistant, one per engine, so
		// signing in to an engine's home serves both.
		selected := cfg.AssistantHarness()
		switch engineName {
		case "":
		case "codex", "claude":
			selected = cfg.Harness(engineName, "", "")
		default:
			return errors.New("engine must be codex or claude")
		}
		child, err := prepareModelLogin(cmd.Context(), selected)
		if err != nil {
			return err
		}
		child.Stdin, child.Stdout, child.Stderr = cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()
		fmt.Fprintln(cmd.ErrOrStderr(), "Signing into", selected.Engine, "home:", selected.Home)
		if err = child.Run(); err != nil {
			return fmt.Errorf("%s login did not complete: %w", selected.Engine, err)
		}
		return nil
	}}
	login.Flags().StringVar(&engineName, "engine", "", "Sign in to this engine's home instead of the assistant's: codex or claude")
	models.AddCommand(login)
	root.AddCommand(models)
}

// Login is an owner-invoked CLI action, never an assistant model tool. Codex owns
// its credentials and refresh flow; we only select the directory and process.
func prepareModelLogin(ctx context.Context, h config.Harness) (*exec.Cmd, error) {
	if h.Engine != "codex" && h.Engine != "claude" {
		return nil, errors.New("model login supports Codex and Claude CLI; API credentials stay with the configured provider")
	}
	if !filepath.IsAbs(h.Home) {
		return nil, fmt.Errorf("engines.%s.home must be an absolute directory path", h.Engine)
	}
	if h.Engine == "claude" {
		bin, err := exec.LookPath(h.Bin)
		if err != nil {
			return nil, errors.New("Claude CLI is not installed")
		}
		bin, err = filepath.Abs(bin)
		if err != nil {
			return nil, err
		}
		if err = os.MkdirAll(h.Home, 0700); err != nil {
			return nil, err
		}
		env, err := engine.ClaudeEnvironment(h.Home)
		if err != nil {
			return nil, err
		}
		child := exec.CommandContext(ctx, bin, "auth", "login", "--claudeai")
		child.Dir, child.Env = h.Home, env
		return child, nil
	}
	bin, err := exec.LookPath(h.Bin)
	if err != nil {
		return nil, errors.New("Codex executable not found; install Codex or set engines.codex.bin")
	}
	bin, err = filepath.Abs(bin)
	if err != nil {
		return nil, fmt.Errorf("resolve Codex executable: %w", err)
	}
	if err = os.MkdirAll(h.Home, 0700); err != nil {
		return nil, fmt.Errorf("create configured Codex home: %w", err)
	}
	if err = engine.ValidateCodexHome(h.Home); err != nil {
		return nil, err
	}
	env, err := engine.CodexEnvironment(h.Home)
	if err != nil {
		return nil, err
	}
	child := exec.CommandContext(ctx, bin, "login")
	child.Dir, child.Env = h.Home, env
	return child, nil
}
