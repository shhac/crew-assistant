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
	var profile string
	models := &cobra.Command{Use: "model", Short: "Manage the configured model runtime"}
	login := &cobra.Command{Use: "login", Short: "Sign in using the configured runtime home", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Load(o.configPath)
		if err != nil {
			return err
		}
		selected := cfg.Model
		switch profile {
		case "assistant":
		case "worker":
			selected = cfg.WorkerModel
		default:
			return errors.New("profile must be assistant or worker")
		}
		child, err := prepareModelLogin(cmd.Context(), selected)
		if err != nil {
			return err
		}
		child.Stdin, child.Stdout, child.Stderr = cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()
		home := selected.CodexHome
		if selected.Engine == "claude" {
			home = selected.ClaudeHome
		}
		fmt.Fprintln(cmd.ErrOrStderr(), "Signing into", selected.Engine, "home:", home)
		if err = child.Run(); err != nil {
			return fmt.Errorf("%s login did not complete: %w", selected.Engine, err)
		}
		return nil
	}}
	login.Flags().StringVar(&profile, "profile", "assistant", "Model profile: assistant or worker")
	models.AddCommand(login)
	root.AddCommand(models)
}

// Login is an owner-invoked CLI action, never an assistant model tool. Codex owns
// its credentials and refresh flow; we only select the directory and process.
func prepareModelLogin(ctx context.Context, profile config.Model) (*exec.Cmd, error) {
	if profile.Engine == "claude" {
		if err := profile.Validate(); err != nil {
			return nil, err
		}
		bin, err := exec.LookPath(profile.ClaudeBin)
		if err != nil {
			return nil, errors.New("Claude CLI is not installed")
		}
		bin, err = filepath.Abs(bin)
		if err != nil {
			return nil, err
		}
		if err = os.MkdirAll(profile.ClaudeHome, 0700); err != nil {
			return nil, err
		}
		env, err := engine.ClaudeEnvironment(profile.ClaudeHome)
		if err != nil {
			return nil, err
		}
		child := exec.CommandContext(ctx, bin, "auth", "login", "--claudeai")
		child.Dir, child.Env = profile.ClaudeHome, env
		return child, nil
	}
	if profile.Engine != "codex" {
		return nil, errors.New("model login supports Codex and Claude CLI; API credentials stay with the configured provider")
	}
	if err := profile.Validate(); err != nil {
		return nil, err
	}
	bin, err := exec.LookPath(profile.CodexBin)
	if err != nil {
		return nil, errors.New("Codex executable not found; install Codex or set this profile's codex_bin")
	}
	bin, err = filepath.Abs(bin)
	if err != nil {
		return nil, fmt.Errorf("resolve Codex executable: %w", err)
	}
	if err = os.MkdirAll(profile.CodexHome, 0700); err != nil {
		return nil, fmt.Errorf("create configured Codex home: %w", err)
	}
	if err = engine.ValidateCodexHome(profile.CodexHome); err != nil {
		return nil, err
	}
	env, err := engine.CodexEnvironment(profile.CodexHome)
	if err != nil {
		return nil, err
	}
	child := exec.CommandContext(ctx, bin, "login")
	child.Dir, child.Env = profile.CodexHome, env
	return child, nil
}
