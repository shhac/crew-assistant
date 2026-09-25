package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"time"

	"github.com/gofrs/flock"
	"github.com/shhac/crew-assistant/internal/access"
	"github.com/shhac/crew-assistant/internal/app"
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/diagnostics"
	"github.com/shhac/crew-assistant/internal/lifecycle"
	"github.com/shhac/crew-assistant/internal/sample"
	"github.com/shhac/crew-assistant/internal/server"
	"github.com/spf13/cobra"
)

func registerServe(root *cobra.Command, o *options) {
	var addr, mode string
	var port int
	var demo, open, noDispatch bool
	cmd := &cobra.Command{Use: "serve", Short: "Run the assistant daemon and embedded dashboard", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadServeConfig(o.configPath, demo)
		if err != nil {
			return err
		}
		if addr != "" {
			cfg.Dashboard.Addr = addr
		}
		if mode != "" {
			cfg.Dashboard.Tailscale = mode
		}
		if port != 0 {
			cfg.Dashboard.TailscalePort = port
		}
		// Only a demo on a temporary state gets the fictional sample; a demo
		// pointed at a state of its own shows that state as it is.
		sampleDir := ""
		if demo {
			cfg.Dashboard.Tailscale = "off"
			if !cmd.Flags().Changed("state") && !root.PersistentFlags().Changed("state") {
				dir, err := os.MkdirTemp("", "crew-assistant-demo-")
				if err != nil {
					return err
				}
				defer os.RemoveAll(dir)
				o.statePath = filepath.Join(dir, "state.db")
				sampleDir = filepath.Join(dir, "sample")
			}
		}
		if err = cfg.Validate(); err != nil {
			return err
		}
		signals := make(chan os.Signal, 2)
		signal.Notify(signals, lifecycle.Signals...)
		defer signal.Stop(signals)
		stop, cancelAll := lifecycle.Watch(cmd.Context(), signals, func(s string) { fmt.Fprintln(cmd.ErrOrStderr(), s) })
		defer cancelAll()
		o.diagnostics = diagnostics.New(cmd.ErrOrStderr())
		return serve(stop, o, cfg, demo, sampleDir, open, noDispatch)
	}}
	cmd.Flags().StringVar(&addr, "http", "", "Local dashboard address (loopback only)")
	cmd.Flags().StringVar(&mode, "tailscale", "", "Private dashboard access: off or serve")
	cmd.Flags().IntVar(&port, "tailscale-port", 0, "Tailscale HTTPS port: 443, 8443 or 10000")
	cmd.Flags().BoolVar(&demo, "demo", false, "Use fictional sample data; disable all external integrations")
	cmd.Flags().BoolVar(&open, "open", false, "Open the dashboard with a one-use sign-in link")
	cmd.Flags().BoolVar(&noDispatch, "no-dispatch", false, "Do not start or resume workers during this boot")
	root.AddCommand(cmd)
}
// serve runs the daemon until stop. The dashboard stays up while the work in
// progress finishes, so the owner can watch it finish.
func serve(stop lifecycle.Stop, o *options, cfg config.Config, demo bool, sampleDir string, open, noDispatch bool) error {
	graceful, endGraceful := context.WithCancel(stop.Graceful)
	defer endGraceful()
	force, endForce := context.WithCancel(stop.Force)
	defer endForce()
	stop = lifecycle.Stop{Graceful: graceful, Force: force}
	if err := os.MkdirAll(filepath.Dir(o.statePath), 0700); err != nil {
		return err
	}
	lock := flock.New(o.statePath + ".lock")
	ok, err := lock.TryLock()
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("another daemon owns this state file")
	}
	defer lock.Unlock()
	listener, err := net.Listen("tcp", cfg.Dashboard.Addr)
	if err != nil {
		return err
	}
	defer listener.Close()
	cfg.Dashboard.Addr = listener.Addr().String()
	localURL := "http://" + cfg.Dashboard.Addr
	store, err := core.Open(o.statePath)
	if err != nil {
		return err
	}
	defer store.Close()
	service := core.NewService(store, cfg)
	if sampleDir != "" {
		if err = sample.Seed(stop.Force, service, sampleDir); err != nil {
			return fmt.Errorf("the demo sample: %w", err)
		}
	}
	appConfigPath := o.configPath
	if demo {
		appConfigPath = filepath.Join(o.runtimeDir(), "demo-config.json")
	}
	a := app.New(service, cfg, appConfigPath, app.Options{Demo: demo, Diagnostics: o.diagnostics, DrawWithCodex: true})
	publicURL := ""
	if cfg.Dashboard.Tailscale == "serve" {
		var cleanup func() error
		publicURL, cleanup, err = access.DefaultTailscale().Start(stop.Force, cfg.Dashboard.TailscalePort, cfg.Dashboard.Addr, filepath.Join(o.runtimeDir(), "tailscale-route.json"))
		if err != nil {
			return err
		}
		defer func() {
			if err := cleanup(); err != nil {
				fmt.Fprintln(os.Stderr, "Tailscale cleanup:", err)
			}
		}()
	}
	auth, err := server.NewAuth(o.runtimeDir(), localURL, publicURL, cfg.Dashboard.AllowedUsers)
	if err != nil {
		return err
	}
	url := localURL
	if publicURL != "" {
		url = publicURL
	}
	info := runtimeInfo{URL: url, LocalURL: localURL, PID: os.Getpid(), Demo: demo}
	b, _ := json.Marshal(info)
	if err = os.WriteFile(filepath.Join(o.runtimeDir(), "daemon.json"), b, 0600); err != nil {
		return err
	}
	defer os.Remove(filepath.Join(o.runtimeDir(), "daemon.json"))
	httpServer := &http.Server{Handler: server.New(a, auth), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20, BaseContext: func(net.Listener) context.Context { return stop.Force }}
	errs := make(chan error, 1)
	go func() { errs <- httpServer.Serve(listener) }()
	done := make(chan struct{})
	loopErrors := make(chan error, 1)
	go func() {
		defer close(done)
		err := a.Run(stop, noDispatch)
		a.Diagnostics.Failure(diagnostics.Event{Component: "daemon", Stage: "supervision_loop"}, err)
		loopErrors <- err
	}()
	_ = o.emit(struct {
		Version        string   `json:"version"`
		URL            string   `json:"url"`
		State          string   `json:"state"`
		Demo           bool     `json:"demo"`
		Login          string   `json:"login"`
		ConfigProblems []string `json:"config_problems,omitempty"`
	}{o.version, url, o.statePath, demo, "crew-assistant --state " + o.statePath + " dashboard open", configProblems(o.configPath)})
	if open {
		if err := openDashboard(o, false); err != nil {
			fmt.Fprintln(os.Stderr, "Dashboard open:", err)
		}
	}
	var serveErr error
	select {
	case <-stop.Graceful.Done():
	case serveErr = <-loopErrors:
	case serveErr = <-errs:
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
	}
	// A failure of the loop or the listener stops everything at once. Every
	// exit ends request and integration contexts before closing their shared
	// store.
	asked := stop.Stopping()
	endGraceful()
	if !asked {
		endForce()
	}
	loopErr := waitForRun(done, stop.Force)
	endForce()
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	shutdownErr := httpServer.Shutdown(shutdown)
	if shutdownErr != nil {
		_ = httpServer.Close()
	}
	return errors.Join(serveErr, shutdownErr, loopErr)
}

// waitForRun waits for the work in progress: all of it after a stop, and
// only briefly once the stop is forced, so a wedged step can't hold up an
// exit the owner asked to force.
func waitForRun(done <-chan struct{}, force context.Context) error {
	select {
	case <-done:
		return nil
	case <-force.Done():
	}
	select {
	case <-done:
		return nil
	case <-time.After(lifecycle.ForceDrain):
		return fmt.Errorf("the work in progress did not stop within %s", lifecycle.ForceDrain)
	}
}
func registerDashboard(root *cobra.Command, o *options) {
	d := &cobra.Command{Use: "dashboard", Short: "Open the running dashboard"}
	var printOnly bool
	c := &cobra.Command{Use: "open", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error { return openDashboard(o, printOnly) }}
	c.Flags().BoolVar(&printOnly, "print", false, "Print the URL and one-use sign-in code instead of opening a browser")
	d.AddCommand(c)
	root.AddCommand(d)
}
func openDashboard(o *options, printOnly bool) error {
	info, err := o.runtime()
	if err != nil {
		return err
	}
	code, err := server.Pair(o.runtimeDir())
	if err != nil {
		return err
	}
	if printOnly {
		return o.emit(map[string]string{"url": info.URL, "code": code, "expires": "5 minutes"})
	}
	target := info.URL + "/#token=" + code
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	if err = cmd.Run(); err != nil {
		return errors.New("could not open browser; run dashboard open --print for a sign-in code")
	}
	return nil
}

// loadServeConfig reads the config the daemon starts with, first rewriting
// a file in an earlier layout once. A demo leaves the owner's file as it is.
func loadServeConfig(path string, demo bool) (config.Config, error) {
	if !demo {
		if _, err := config.Upgrade(path); err != nil {
			return config.Config{}, err
		}
	}
	return config.Load(path)
}
