// Package cli exposes the family CLI and daemon entry point.
package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/diagnostics"
	"github.com/shhac/crew-assistant/internal/upgrade"
	libcli "github.com/shhac/lib-agent-cli/cli"
	_ "github.com/shhac/lib-agent-cli/yaml"
	output "github.com/shhac/lib-agent-output"
	"github.com/spf13/cobra"
)

type options struct {
	upgradeIdentity       func(int) string // Injected process inspection for upgrade tests.
	diagnostics           *diagnostics.Logger
	configPath, statePath string
	envFile               string
	globals               *libcli.Globals
	// version is the running build, said on startup so a log shows which
	// daemon it came from.
	version string
	// transport replaces the network in tests; nil talks to the daemon.
	transport         http.RoundTripper
	upgradeHost       *upgradeHost
	upgradeExecutable func() (string, error)
	upgradeExecer     func(string, []string) error
	upgradeStarter    func(string, []string, string) error // Injected process starter; nil uses detached OS processes.
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
			if err := before(cmd, args); err != nil {
				return err
			}
		}
		return loadCommandEnvFile(cmd, o.envFile)
	}
	root.PersistentFlags().StringVar(&o.configPath, "config", paths.Config, "Configuration file")
	root.PersistentFlags().StringVar(&o.statePath, "state", paths.State, "Durable SQLite state file")
	root.PersistentFlags().StringVar(&o.envFile, "env-file", "", "Load a private dotenv file for serve or doctor; existing environment takes precedence")
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
	root.AddCommand(decisionsCommand(o))
	root.AddCommand(configCommand(o))
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
	registerDoctor(root, o)
	registerServe(root, o)
	registerDashboard(root, o)
	registerModel(root, o)
	registerTask(root, o)
	registerEngine(root, o)
	registerUpgrade(root, o)
	registerCompletions(root, o)
	return root
}
func (o *options) emit(v any) error { return libcli.EmitItem(os.Stdout, o.globals.Format, v) }
func (o *options) runtimeDir() string {
	if path, err := upgrade.CanonicalPath(o.statePath); err == nil {
		return path + ".runtime"
	}
	return o.statePath + ".runtime"
}

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
	var result any
	if err := o.requestInto(method, path, value, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (o *options) requestInto(method, path string, value, out any) error {
	return o.requestBody(method, path, value, func(r io.Reader) error { return json.NewDecoder(io.LimitReader(r, 4<<20)).Decode(out) })
}

func (o *options) requestBody(method, path string, value any, consume func(io.Reader) error) error {
	info, err := o.runtime()
	if err != nil {
		return err
	}
	token, err := os.ReadFile(filepath.Join(o.runtimeDir(), "admin-token"))
	if err != nil {
		return err
	}
	var body io.Reader
	if value != nil {
		b, err := json.Marshal(value)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, info.LocalURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+string(token))
	req.Header.Set("X-Requested-With", "crew-assistant")
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: o.transport, Timeout: 10 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("could not reach daemon; check crew-assistant serve and the selected --state path")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 400 {
		return consume(resp.Body)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	return daemonError(resp.StatusCode, data)
}

// daemonError is the daemon's own message when it sent one. A reply that isn't
// JSON at all says why it couldn't be read instead.
func daemonError(status int, data []byte) error {
	var reply any
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&reply); err != nil {
		return err
	}
	if m, ok := reply.(map[string]any); ok {
		return fmt.Errorf("%v", m["error"])
	}
	return fmt.Errorf("daemon returned HTTP %d", status)
}
func (o *options) saveConfig(c config.Config) error {
	if err := canonicalUpgradeOptions(o); err != nil {
		return err
	}
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
