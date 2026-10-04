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

	"github.com/shhac/crew-assistant/internal/access"
	"github.com/shhac/crew-assistant/internal/app"
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/diagnostics"
	"github.com/shhac/crew-assistant/internal/lifecycle"
	"github.com/shhac/crew-assistant/internal/sample"
	"github.com/shhac/crew-assistant/internal/server"
	"github.com/shhac/crew-assistant/internal/statepath"
	"github.com/shhac/crew-assistant/internal/upgrade"
	"github.com/spf13/cobra"
)

func registerServe(root *cobra.Command, o *options) {
	var addr, mode string
	var port int
	var demo, open, noDispatch bool
	cmd := &cobra.Command{Use: "serve", Short: "Run the assistant daemon and embedded dashboard", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		// Only a demo on a temporary state gets the fictional sample; a demo
		// pointed at a state of its own shows that state as it is.
		sampleDir := ""
		if demo {
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
		signals := make(chan os.Signal, 2)
		signal.Notify(signals, lifecycle.Signals...)
		defer signal.Stop(signals)
		stop, cancelAll := lifecycle.Watch(cmd.Context(), signals, func(s string) { fmt.Fprintln(cmd.ErrOrStderr(), s) })
		defer cancelAll()
		host, err := prepareUpgradeStart(o, stop, demo)
		if err != nil {
			return err
		}
		defer host.lock.Unlock()
		o.upgradeHost = host
		defer func() { o.upgradeHost = nil }()
		cfg, err := loadServeConfig(o.configPath, demo)
		if err != nil {
			if host.start.Probation {
				return host.engine.Fail("the new version could not load its configuration")
			}
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
		if demo {
			cfg.Dashboard.Tailscale = "off"
		}
		if err = cfg.Validate(); err != nil {
			if host.start.Probation {
				return host.engine.Fail("the new version could not validate its configuration")
			}
			return err
		}
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
func serve(stop lifecycle.Stop, o *options, cfg config.Config, demo bool, sampleDir string, open, noDispatch bool) (resultErr error) {
	stop, cancel := stop.WithCancel()
	defer cancel()
	host := o.upgradeHost
	if host == nil {
		var err error
		host, err = prepareUpgradeStart(o, stop, demo)
		if err != nil {
			return err
		}
		defer host.lock.Unlock()
	}
	defer func() {
		if host.start.Probation && resultErr != nil && !errors.Is(resultErr, upgrade.ErrRolledBack) {
			if stop.Reason() == "signal" {
				_ = host.engine.StopProbation()
				resultErr = nil
			} else {
				r, err := upgrade.ReadRecord(host.engine.Path)
				if err == nil && r != nil && r.Step == upgrade.Probation {
					resultErr = host.engine.Fail("the new version failed during startup")
				}
			}
		}
	}()
	defer func() {
		if host.listenerFile != nil {
			_ = host.listenerFile.Close()
		}
		_ = os.Unsetenv(upgradeListenerEnv)
	}()
	if host.start.Message != "" && currentUpgradeLaunchdLabel() == "" {
		fmt.Fprintln(os.Stderr, host.start.Message)
	}
	if host.engine != nil {
		r, err := upgrade.ReadRecord(host.engine.Path)
		if err != nil {
			return err
		}
		if r != nil && r.Address != "" && (host.start.Probation || host.start.Recovery || r.Pinned) {
			cfg.Dashboard.Addr = r.Address
		}
	}
	listener, err := inheritedUpgradeListener()
	if err == nil && listener == nil {
		listener, err = net.Listen("tcp", cfg.Dashboard.Addr)
	}
	if err != nil {
		return err
	}
	defer listener.Close()
	cfg.Dashboard.Addr = listener.Addr().String()
	localURL := "http://" + cfg.Dashboard.Addr
	store, err := core.Open(o.statePath)
	if err != nil {
		if host.start.Probation {
			return host.engine.Fail("the new version could not open or migrate state")
		}
		return err
	}
	defer store.Close()
	service := core.NewService(store, cfg)
	if host.engine != nil {
		r, err := upgrade.ReadRecord(host.engine.Path)
		if err != nil {
			return err
		}
		if err := service.ReconcileUpgradeRequests(stop.Force, o.version, r); err != nil {
			return err
		}
	}
	if host.engine != nil {
		r, err := upgrade.ReadRecord(host.engine.Path)
		if err != nil {
			return err
		}
		if r != nil && r.Step == upgrade.Abandoned {
			if err := service.ReofferAbandonedUpgrade(stop.Force, r.To); err != nil {
				return err
			}
		}
	}
	if sampleDir != "" {
		if err = sample.Seed(stop.Force, service, sampleDir); err != nil {
			return fmt.Errorf("the demo sample: %w", err)
		}
	}
	appConfigPath := o.configPath
	if demo {
		appConfigPath = filepath.Join(o.runtimeDir(), "demo-config.json")
	}
	var checker *upgrade.Checker
	if upgrade.Unavailable(o.version, demo) == "" {
		checker = host.checker
		if checker == nil {
			checker = upgrade.New(o.version, nil, nil)
		}
	}
	opts := app.Options{Demo: demo, Diagnostics: o.diagnostics, DrawWithCodex: true, Version: o.version, Checker: checker}
	if host.engine != nil {
		opts.UpgradeEngine = host.engine
		if host.prefix != "" {
			opts.RequestUpgrade = func(version string, automatic bool) error {
				return host.request(o, cfg.Dashboard.Addr, version, automatic)
			}
		}
	}
	a := app.New(service, cfg, appConfigPath, opts)
	a.SetUpgrading(host.start.Probation || host.start.Recovery)
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
	if err = statepath.WriteFileAtomic(filepath.Join(o.runtimeDir(), "daemon.json"), b); err != nil {
		return err
	}
	defer os.Remove(filepath.Join(o.runtimeDir(), "daemon.json"))
	httpServer := &http.Server{Handler: server.New(a, auth), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20, BaseContext: func(net.Listener) context.Context { return stop.Force }}
	if host.engine != nil {
		host.wire(o, a.Config, store, auth, listener, httpServer)
	}
	errs := make(chan error, 1)

	if host.engine != nil {
		if host.start.Failed {
			r, err := upgrade.ReadRecord(host.engine.Path)
			if err != nil {
				return err
			}
			if err = service.RecordUpgradeFailure(stop.Force, *r); err != nil {
				return err
			}
			if err = host.engine.MarkFailureOpened(r.StartedAt); err != nil {
				return err
			}
		}
		go func() { errs <- httpServer.Serve(listener) }()
		if host.start.Recovery {
			r, err := upgrade.ReadRecord(host.engine.Path)
			if err != nil {
				return err
			}
			if r == nil {
				return errors.New("rollback recovery record is missing")
			}
			bound := time.Until(r.Deadline)
			if bound <= 0 {
				return errors.New("restored startup exceeded its health-check deadline")
			}
			probe := host.probe
			if probe == nil {
				probe = probeUpgradeAPI
			}
			if err := host.engine.ConfirmRecovery(stop.Graceful, bound, func(ctx context.Context) error {
				return probe(ctx, localURL, filepath.Join(o.runtimeDir(), "admin-token"))
			}); err != nil {
				if stop.Stopping() {
					_ = host.engine.StopProbation()
					_ = host.engine.Close()
					return nil
				}
				return err
			}
			a.SetUpgrading(false)
			_ = os.Remove(filepath.Join(o.runtimeDir(), "upgrade-sessions.json"))
		}
		if host.start.Probation {
			if err = auth.SaveHandover(o.runtimeDir()); err != nil {
				return host.engine.Fail("browser session handover failed")
			}
			r, err := upgrade.ReadRecord(host.engine.Path)
			if err != nil {
				return err
			}
			bound := time.Until(r.Deadline)
			if bound <= 0 {
				return host.engine.Fail("the new version exceeded its health-check deadline")
			}
			probe := host.probe
			if probe == nil {
				probe = probeUpgradeAPI
			}
			if err = host.engine.CheckHealth(stop.Graceful, bound, func(ctx context.Context) error {
				return probe(ctx, localURL, filepath.Join(o.runtimeDir(), "admin-token"))
			}); err != nil {
				return err
			}
			if stop.Stopping() {
				_ = host.engine.Close()
				return nil
			}
			a.SetUpgrading(false)
			_ = os.Remove(filepath.Join(o.runtimeDir(), "upgrade-sessions.json"))
		}
	} else {
		go func() { errs <- httpServer.Serve(listener) }()
	}
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
	if !stop.Stopping() {
		cancel()
	}
	var drainErr error
	if host.engine != nil && stop.Reason() == "upgrade" {
		drainErr = awaitUpgradeDrain(stop, done, host.engine.Path, a.RecordUpgradeDrain, a.ObserveUpgradeDrain)
	} else {
		drainErr = stop.Await(done)
	}
	if host.engine != nil {
		r, readErr := upgrade.ReadRecord(host.engine.Path)
		if readErr != nil {
			return readErr
		}
		if r != nil && r.Step == upgrade.Draining {
			if drainErr == nil && serveErr == nil && stop.Reason() == "upgrade" {
				a.SetUpgrading(true)
				installCtx, endInstall := upgradeInstallContext(stop)
				defer endInstall()
				err := host.engine.FinishDrain(installCtx, stop.Reason())
				if errors.Is(err, context.Canceled) && stop.Reason() == "signal" {
					return nil
				}
				return err
			}
			if err := host.engine.FinishDrain(context.Background(), "signal"); err != nil {
				return err
			}
		}
	}
	cancel()
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	shutdownErr := httpServer.Shutdown(shutdown)
	if shutdownErr != nil {
		_ = httpServer.Close()
	}
	return errors.Join(serveErr, shutdownErr, drainErr)
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

// Progress reporting cannot bypass Await: admitted turns keep Force until the
// owner actually forces a stop, even if the journal disappears or is unreadable.
func awaitUpgradeDrain(stop lifecycle.Stop, done <-chan struct{}, path string, initial, observe func(context.Context, bool) error) error {
	r, progressErr := upgrade.ReadRecord(path)
	if progressErr == nil && r == nil {
		progressErr = errors.New("upgrade progress record is missing")
	}
	var observed chan error
	var cancel context.CancelFunc
	if progressErr == nil {
		progressErr = initial(stop.Force, r.Automatic)
		if progressErr == nil {
			ctx, end := context.WithCancel(stop.Force)
			cancel = end
			observed = make(chan error, 1)
			go func() { observed <- observe(ctx, r.Automatic) }()
		}
	}
	drainErr := stop.Await(done)
	if cancel != nil {
		cancel()
		err := <-observed
		if err != nil && !errors.Is(err, context.Canceled) {
			progressErr = errors.Join(progressErr, err)
		}
	}
	return errors.Join(drainErr, progressErr)
}
