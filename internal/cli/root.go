// Package cli exposes the family CLI and daemon entry point.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/diagnostics"
	"github.com/shhac/crew-assistant/internal/engine"
	libcli "github.com/shhac/lib-agent-cli/cli"
	_ "github.com/shhac/lib-agent-cli/yaml"
	output "github.com/shhac/lib-agent-output"
	"github.com/spf13/cobra"
)

type options struct {
	diagnostics           *diagnostics.Logger
	configPath, statePath string
	globals               *libcli.Globals
	// version is the running build, said on startup so a log shows which
	// daemon it came from.
	version string
}

func Run(version string) { libcli.Run(NewRoot(version)) }
func NewRoot(version string) *cobra.Command {
	paths, err := config.Paths()
	if err != nil {
		paths.Config = "config.json"
		paths.State = "state.db"
	}
	o := &options{configPath: paths.Config, statePath: paths.State, globals: &libcli.Globals{}, version: version}
	root := libcli.NewRoot(libcli.Options{Use: "crew-assistant", Short: "A personal assistant that coordinates agents and brings clear decisions", Version: version, Globals: o.globals, DefaultFormat: output.FormatNDJSON})
	pathErr := err
	before := root.PersistentPreRunE
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if pathErr != nil && !(cmd.Flags().Changed("config") && cmd.Flags().Changed("state")) {
			return pathErr
		}
		if before != nil {
			return before(cmd, args)
		}
		return nil
	}
	root.PersistentFlags().StringVar(&o.configPath, "config", paths.Config, "Configuration file")
	root.PersistentFlags().StringVar(&o.statePath, "state", paths.State, "Durable SQLite state file")
	init := &cobra.Command{Use: "init", Short: "Create private default configuration", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if _, err := os.Stat(o.configPath); err == nil {
			return errors.New("configuration already exists; use config set to change it")
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := config.Save(o.configPath, config.Default()); err != nil {
			return err
		}
		return o.emit(map[string]string{"config": o.configPath, "next": "Run crew-assistant model login, then crew-assistant serve --open; defaults are codex/gpt-6-astra/high"})
	}}
	root.AddCommand(init)
	root.AddCommand(&cobra.Command{Use: "status", Short: "Read daemon state", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		v, err := o.request("GET", "/api/state", nil)
		if err != nil {
			return err
		}
		return o.emit(v)
	}})
	root.AddCommand(&cobra.Command{Use: "chat <message>", Short: "Talk to your assistant", Args: cobra.MinimumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		v, err := o.request("POST", "/api/chat", map[string]string{"message": strings.Join(args, " ")})
		if err != nil {
			return err
		}
		return o.emit(v)
	}})
	for _, action := range []string{"pause", "resume"} {
		action := action
		root.AddCommand(&cobra.Command{Use: action, Short: action + " new coordination work", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
			v, err := o.request("POST", "/api/control", map[string]bool{"paused": action == "pause"})
			if err != nil {
				return err
			}
			return o.emit(v)
		}})
	}
	cfgcmd := &cobra.Command{Use: "config", Short: "Inspect and update typed configuration"}
	cfgcmd.AddCommand(&cobra.Command{Use: "path", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error { return o.emit(map[string]string{"path": o.configPath}) }})
	cfgcmd.AddCommand(&cobra.Command{Use: "show", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(o.configPath)
		if err != nil {
			return err
		}
		return o.emit(cfg)
	}})
	cfgcmd.AddCommand(&cobra.Command{Use: "validate", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		_, err := config.Load(o.configPath)
		if err != nil {
			return err
		}
		return o.emit(map[string]bool{"valid": true})
	}})
	cfgcmd.AddCommand(&cobra.Command{Use: "set <key.path> <value>", Short: "Set a string, JSON number, boolean, array or object", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		c, err := config.Load(o.configPath)
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(c)
		var object map[string]any
		_ = json.Unmarshal(raw, &object)
		parts := strings.Split(args[0], ".")
		target := object
		for _, p := range parts[:len(parts)-1] {
			next, ok := target[p].(map[string]any)
			if !ok {
				return errors.New("unknown configuration object")
			}
			target = next
		}
		key := parts[len(parts)-1]
		if _, ok := target[key]; !ok {
			return errors.New("unknown configuration key")
		}
		var value any
		if json.Unmarshal([]byte(args[1]), &value) != nil {
			value = args[1]
		}
		target[key] = value
		raw, _ = json.Marshal(object)
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if err = d.Decode(&c); err != nil {
			return err
		}
		if err = c.Validate(); err != nil {
			return err
		}
		if err = o.saveConfig(c); err != nil {
			return err
		}
		return o.emit(map[string]string{"updated": args[0]})
	}})
	root.AddCommand(cfgcmd)
	doctor := &cobra.Command{Use: "doctor", Short: "Check configuration and credential availability without calling providers", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(o.configPath)
		if err != nil {
			return err
		}
		checks := []map[string]any{{"name": "config", "ok": true}, {"name": "model", "ok": cfg.Model.Model != "", "engine": cfg.Model.Engine, "model": cfg.Model.Model, "effort": cfg.Model.Effort}}
		for _, connection := range cfg.Connections {
			_, lookupErr := exec.LookPath(connection.Tool)
			hint := "Account credentials are managed by this CLI; choose its existing profiles in Settings."
			if connection.Tool == "agent-notion" {
				hint = "Uses the CLI default account and native authentication; no profile is needed."
			}
			checks = append(checks, map[string]any{"name": connection.Name + " CLI executable", "tool": connection.Tool, "ok": lookupErr == nil, "queries_supported": true, "profiles": connection.Profiles, "hint": hint})
		}
		refs := []string{}
		if cfg.Slack.OwnerUserID != "" {
			refs = append(refs, cfg.Slack.BotTokenEnv, cfg.Slack.AppTokenEnv)
		}
		if cfg.LegacyLinearImportEnabled() {
			refs = append(refs, cfg.Linear.APIKeyEnv)
		}
		if cfg.Model.Engine == "codex" {
			isolationErr := engine.ValidateCodexHome(cfg.Model.CodexHome)
			isolationHint := "Run crew-assistant model login to sign into the configured model.codex_home."
			if isolationErr != nil {
				isolationHint = isolationErr.Error()
			}
			checks = append(checks, map[string]any{"name": "codex instruction isolation", "ok": isolationErr == nil, "hint": isolationHint})
			binary, lookupErr := exec.LookPath(cfg.Model.CodexBin)
			checks = append(checks, map[string]any{"name": "codex executable", "ok": lookupErr == nil, "hint": "install Codex or set model.codex_bin"})
			if lookupErr == nil {
				ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
				probe := exec.CommandContext(ctx, binary, "login", "status")
				// Match the inference transport: use Codex's stored login, not
				// unrelated provider keys inherited from the daemon environment.
				probe.Env, err = engine.CodexEnvironment(cfg.Model.CodexHome)
				if err != nil {
					cancel()
					return err
				}
				probe.Stdout, probe.Stderr = io.Discard, io.Discard
				loginErr := probe.Run()
				cancel()
				checks = append(checks, map[string]any{"name": "codex login", "ok": loginErr == nil, "hint": "run crew-assistant model login; no inference was invoked"})
			}
		} else if cfg.Model.Engine == "claude" {
			binary, lookupErr := exec.LookPath(cfg.Model.ClaudeBin)
			checks = append(checks, map[string]any{"name": "claude executable", "ok": lookupErr == nil, "hint": "Install Claude CLI to use its existing subscription login."})
			if lookupErr == nil {
				ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
				probe := exec.CommandContext(ctx, binary, "auth", "status")
				probe.Env, err = engine.ClaudeEnvironment(cfg.Model.ClaudeHome)
				if err != nil {
					cancel()
					return err
				}
				probe.Stdout, probe.Stderr = io.Discard, io.Discard
				loginErr := probe.Run()
				cancel()
				checks = append(checks, map[string]any{"name": "claude login", "ok": loginErr == nil, "hint": "Sign in once with crew-assistant model login; workers using this CLI home share the login."})
			}
		} else {
			refs = append(refs, cfg.Model.APIKeyEnv)
		}
		checks = append(checks, roleSandboxChecks(cmd.Context(), cfg, o.statePath)...)
		for _, ref := range refs {
			if ref != "" {
				checks = append(checks, map[string]any{"name": ref, "ok": os.Getenv(ref) != "", "hint": "set in the daemon environment if using this integration"})
			}
		}
		return o.emit(map[string]any{"checks": checks})
	}}
	operations := &cobra.Command{Use: "operations", Short: "Inspect interrupted operations without replaying them"}
	operations.AddCommand(&cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		v, err := o.request("GET", "/api/state", nil)
		if err != nil {
			return err
		}
		state, ok := v.(map[string]any)
		if !ok {
			return errors.New("invalid daemon state")
		}
		return o.emit(state["pending_operations"])
	}})
	operations.AddCommand(&cobra.Command{Use: "acknowledge <id> <inspection-note>", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		v, err := o.request("POST", "/api/operations/"+url.PathEscape(args[0])+"/acknowledge", map[string]string{"note": args[1]})
		if err != nil {
			return err
		}
		return o.emit(v)
	}})
	root.AddCommand(operations)
	root.AddCommand(doctor)
	registerServe(root, o)
	registerDashboard(root, o)
	registerModel(root, o)
	registerCompletions(root, o)
	return root
}
func (o *options) emit(v any) error   { return libcli.EmitItem(os.Stdout, o.globals.Format, v) }
func (o *options) runtimeDir() string { return o.statePath + ".runtime" }

type runtimeInfo struct {
	URL      string `json:"url"`
	LocalURL string `json:"local_url"`
	PID      int    `json:"pid"`
	Demo     bool   `json:"demo"`
}

func (o *options) runtime() (runtimeInfo, error) {
	var info runtimeInfo
	b, err := os.ReadFile(filepath.Join(o.runtimeDir(), "daemon.json"))
	if err != nil {
		return info, errors.New("daemon is not running for this state file; start crew-assistant serve")
	}
	if err = json.Unmarshal(b, &info); err != nil {
		return info, err
	}
	return info, nil
}
func (o *options) request(method, path string, value any) (any, error) {
	info, err := o.runtime()
	if err != nil {
		return nil, err
	}
	token, err := os.ReadFile(filepath.Join(o.runtimeDir(), "admin-token"))
	if err != nil {
		return nil, err
	}
	var body io.Reader
	if value != nil {
		b, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, info.LocalURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+string(token))
	req.Header.Set("X-Requested-With", "crew-assistant")
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("could not reach daemon; check crew-assistant serve and the selected --state path")
	}
	defer resp.Body.Close()
	var result any
	if err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&result); err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		if m, ok := result.(map[string]any); ok {
			return nil, fmt.Errorf("%v", m["error"])
		}
		return nil, fmt.Errorf("daemon returned HTTP %d", resp.StatusCode)
	}
	return result, nil
}
func (o *options) saveConfig(c config.Config) error {
	if err := os.MkdirAll(filepath.Dir(o.statePath), 0700); err != nil {
		return err
	}
	lock := flock.New(o.statePath + ".lock")
	ok, err := lock.TryLock()
	if err != nil {
		return err
	}
	if !ok {
		_, err = o.request("PUT", "/api/config", c)
		return err
	}
	defer lock.Unlock()
	return config.Save(o.configPath, c)
}
