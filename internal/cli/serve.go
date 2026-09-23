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
	"syscall"
	"time"

	"github.com/gofrs/flock"
	"github.com/shhac/crew-assistant/internal/access"
	"github.com/shhac/crew-assistant/internal/app"
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/diagnostics"
	"github.com/shhac/crew-assistant/internal/server"
	"github.com/spf13/cobra"
)

func registerServe(root *cobra.Command, o *options) {
	var addr, mode string
	var port int
	var demo, open, noDispatch bool
	cmd := &cobra.Command{Use: "serve", Short: "Run the assistant daemon and embedded dashboard", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(o.configPath)
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
		if demo {
			cfg.Dashboard.Tailscale = "off"
			if !cmd.Flags().Changed("state") && !root.PersistentFlags().Changed("state") {
				dir, err := os.MkdirTemp("", "crew-assistant-demo-")
				if err != nil {
					return err
				}
				defer os.RemoveAll(dir)
				o.statePath = filepath.Join(dir, "state.db")
			}
		}
		if err = cfg.Validate(); err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		o.diagnostics = diagnostics.New(cmd.ErrOrStderr())
		return serve(ctx, o, cfg, demo, open, noDispatch)
	}}
	cmd.Flags().StringVar(&addr, "http", "", "Local dashboard address (loopback only)")
	cmd.Flags().StringVar(&mode, "tailscale", "", "Private dashboard access: off or serve")
	cmd.Flags().IntVar(&port, "tailscale-port", 0, "Tailscale HTTPS port: 443, 8443 or 10000")
	cmd.Flags().BoolVar(&demo, "demo", false, "Use fictional sample data; disable all external integrations")
	cmd.Flags().BoolVar(&open, "open", false, "Open the dashboard with a one-use sign-in link")
	cmd.Flags().BoolVar(&noDispatch, "no-dispatch", false, "Do not start or resume workers during this boot")
	root.AddCommand(cmd)
}
func serve(ctx context.Context, o *options, cfg config.Config, demo, open, noDispatch bool) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
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
	appConfigPath := o.configPath
	if demo {
		appConfigPath = filepath.Join(o.runtimeDir(), "demo-config.json")
	}
	a := app.New(service, cfg, appConfigPath, demo)
	a.Diagnostics = o.diagnostics
	if noDispatch {
		a.SetNoDispatch()
	}
	publicURL := ""
	if cfg.Dashboard.Tailscale == "serve" {
		var cleanup func() error
		publicURL, cleanup, err = access.DefaultTailscale().Start(ctx, cfg.Dashboard.TailscalePort, cfg.Dashboard.Addr, filepath.Join(o.runtimeDir(), "tailscale-route.json"))
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
	httpServer := &http.Server{Handler: server.New(a, auth), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20, BaseContext: func(net.Listener) context.Context { return ctx }}
	errs := make(chan error, 1)
	go func() { errs <- httpServer.Serve(listener) }()
	loopCtx := ctx
	done := make(chan struct{})
	loopErrors := make(chan error, 1)
	go func() {
		defer close(done)
		err := a.Run(loopCtx, noDispatch)
		a.Diagnostics.Failure(diagnostics.Event{Component: "daemon", Stage: "supervision_loop"}, err)
		loopErrors <- err
	}()
	_ = o.emit(map[string]any{"url": url, "state": o.statePath, "demo": demo, "login": "crew-assistant --state " + o.statePath + " dashboard open"})
	if open {
		if err := openDashboard(o, false); err != nil {
			fmt.Fprintln(os.Stderr, "Dashboard open:", err)
		}
	}
	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-loopErrors:
	case serveErr = <-errs:
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
	}
	// Every exit cancels request and integration contexts before closing their
	// shared store. A listener failure follows the same drain path as SIGTERM.
	cancel()
	shutdown, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	shutdownErr := httpServer.Shutdown(shutdown)
	if shutdownErr != nil {
		_ = httpServer.Close()
	}
	var loopErr error
	select {
	case <-done:
	case <-shutdown.Done():
		loopErr = errors.New("integration shutdown exceeded its grace period")
	}
	return errors.Join(serveErr, shutdownErr, loopErr)
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
