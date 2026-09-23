package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/diagnostics"
	"github.com/shhac/crew-assistant/internal/integrations/worker"
	"github.com/shhac/crew-assistant/internal/quota"
	"github.com/shhac/crew-assistant/internal/workerbroker"
	"github.com/spf13/cobra"
)

// standaloneAdmission gives a separately operated broker the same subscription
// headroom policy the daemon applies, from the same shared meter, so one
// account is not measured by two different sets of rules. Its configuration is
// the process's own: a standalone broker has no live daemon settings feed.
func standaloneAdmission(cfg config.Config, profile config.Model, meter *quota.Meter) func(context.Context) error {
	return func(ctx context.Context) error {
		policy := cfg.Limits.WorkerUsage
		// An engine with no local subscription meter is unmeasured, not exempt:
		// it follows the same unavailable-usage policy the daemon applies, so
		// choosing "pause" means nothing runs unmeasured either way.
		unmeasured := func(reason string) error {
			if policy.OnUnavailable != "pause" {
				return nil
			}
			// A measurement problem can end on its own, so this stays recheckable
			// and names when to look again rather than demanding a decision.
			return &worker.HoldError{Hold: worker.ResourceHold{Kind: worker.HoldTelemetryUnavailable, Reason: "Worker paused: " + reason + ", and the configured policy is to pause when usage cannot be measured", NextCheckAt: time.Now().Add(quota.CacheAge).UTC()}}
		}
		threshold, supported := quota.Threshold(policy, profile.Engine)
		if !supported {
			return unmeasured("subscription usage is unavailable for the " + profile.Engine + " engine")
		}
		if threshold == 0 {
			return nil
		}
		snapshot := meter.Read(ctx, profile)
		// A cancelled inspection is cancellation, not an unreadable account.
		if err := ctx.Err(); err != nil {
			return err
		}
		verdict := quota.Evaluate(snapshot, profile, threshold, time.Now())
		if verdict.Held {
			return &worker.HoldError{Hold: worker.ResourceHold{Kind: worker.HoldSubscriptionQuota, Reason: "Worker paused: " + verdict.Detail, ResetsAt: verdict.ResetsAt, NextCheckAt: time.Now().Add(quota.CacheAge).UTC()}}
		}
		if !verdict.Known {
			return unmeasured("fresh subscription usage is unavailable for " + profile.Engine)
		}
		return nil
	}
}

func registerWorker(root *cobra.Command, o *options) {
	var workspace, project, image, socket, state, addr, tokenEnv, model, engineName, effort string
	var turns, tokens, concurrency int
	worker := &cobra.Command{Use: "worker", Short: "Run an isolated coding-worker broker for an approved project"}
	cmd := &cobra.Command{Use: "serve", Short: "Serve a private worker endpoint backed by an offline Docker sandbox", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(o.configPath)
		if err != nil {
			return err
		}
		profile := cfg.WorkerModel
		if cmd.Flags().Changed("engine") {
			profile.Engine = engineName
		}
		if cmd.Flags().Changed("model") {
			profile.Model = model
		}
		if cmd.Flags().Changed("effort") {
			profile.Effort = effort
		}
		if cmd.Flags().Changed("max-output-tokens") {
			profile.MaxTokens = tokens
		}
		cfg.WorkerModel = profile
		if err := cfg.Validate(); err != nil {
			return err
		}
		if state == "" {
			state = o.statePath + ".workers"
		}
		host, _, err := net.SplitHostPort(addr)
		if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
			return errors.New("worker address must be a loopback IP and port")
		}
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			return err
		}
		defer listener.Close()
		if cmd.Flags().Changed("max-turns") {
			// Never leave a retired option quietly enforcing a cap. Say what
			// replaced it, then run under the configured resource policy.
			fmt.Fprintln(cmd.ErrOrStderr(), "--max-turns no longer limits a worker: a cumulative model-call ceiling stopped long assignments that were doing useful work. Worker limits are now resource limits, in limits.worker_usage (subscription headroom, default 90%) and limits.worker_token_budget (per-assignment tokens, 0 disables). This broker is starting with those settings; the supplied value is ignored.")
		}
		meter := &quota.Meter{}
		broker, err := workerbroker.New(workerbroker.Config{Diagnostics: diagnostics.New(cmd.ErrOrStderr()), StateDir: state, Workspace: workspace, ProjectID: project, Image: image, DockerSocket: socket, Engine: profile.Engine, Effort: profile.Effort, CodexBin: profile.CodexBin, CodexHome: profile.CodexHome, ClaudeBin: profile.ClaudeBin, ClaudeHome: profile.ClaudeHome, ModelEndpoint: strings.TrimRight(profile.BaseURL, "/") + "/chat/completions", Model: profile.Model, APIKeyEnv: profile.APIKeyEnv, TokenEnv: tokenEnv, MaxOutputTokens: profile.MaxTokens, MaxConcurrent: concurrency,
			Admit:       standaloneAdmission(cfg, profile, meter),
			TokenBudget: func() int64 { return cfg.Limits.WorkerTokenBudget },
		})
		if err != nil {
			return err
		}
		defer broker.Close()
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		web := &http.Server{Handler: broker.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20, BaseContext: func(net.Listener) context.Context { return ctx }}
		runtimeDone := make(chan error, 1)
		httpDone := make(chan error, 1)
		go func() { runtimeDone <- broker.Run(ctx) }()
		go func() { httpDone <- web.Serve(listener) }()
		info := broker.Info()
		info["url"] = "http://" + listener.Addr().String()
		info["token_env"] = tokenEnv
		info["state"] = state
		_ = o.emit(info)
		runtimeStopped := false
		select {
		case <-ctx.Done():
		case err = <-runtimeDone:
			runtimeStopped = true
		case err = <-httpDone:
			if errors.Is(err, http.ErrServerClosed) {
				err = nil
			}
		}
		cancel()
		shutdown, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		if shutdownErr := web.Shutdown(shutdown); shutdownErr != nil {
			_ = web.Close()
			if err == nil {
				err = shutdownErr
			}
		}
		if !runtimeStopped {
			runtimeErr := <-runtimeDone
			if err == nil {
				err = runtimeErr
			}
		}
		return err
	}}
	cmd.Flags().StringVar(&workspace, "workspace", "", "Dedicated approved source workspace to copy (original remains untouched)")
	cmd.Flags().StringVar(&project, "project", "", "Exact assistant project ID this broker may work on")
	cmd.Flags().StringVar(&image, "image", "", "Locally installed image pinned by sha256 digest; never pulled automatically")
	cmd.Flags().StringVar(&socket, "docker-socket", "", "Local Docker Unix socket (defaults to /var/run/docker.sock)")
	cmd.Flags().StringVar(&state, "worker-state", "", "Private worker state directory (defaults beside assistant state)")
	cmd.Flags().StringVar(&addr, "http", "127.0.0.1:8350", "Loopback worker API address")
	cmd.Flags().StringVar(&tokenEnv, "token-env", "CREW_ASSISTANT_WORKER_TOKEN", "Environment variable containing the broker API token")
	cmd.Flags().StringVar(&model, "model", "", "Worker model (defaults to worker_model.model)")
	cmd.Flags().StringVar(&engineName, "engine", "", "Worker engine (defaults to worker_model.engine)")
	cmd.Flags().StringVar(&effort, "effort", "", "Reasoning effort (defaults to worker_model.effort)")
	cmd.Flags().IntVar(&turns, "max-turns", 0, "Retired: worker work is limited by subscription headroom and the optional token budget")
	_ = cmd.Flags().MarkDeprecated("max-turns", "worker limits are resource limits; configure limits.worker_usage and limits.worker_token_budget instead")
	cmd.Flags().IntVar(&tokens, "max-output-tokens", 4096, "Maximum output tokens per worker model request (otherwise worker_model.max_tokens)")
	cmd.Flags().IntVar(&concurrency, "max-concurrent", 1, "Maximum simultaneously executing local workers")
	_ = cmd.MarkFlagRequired("workspace")
	_ = cmd.MarkFlagRequired("project")
	_ = cmd.MarkFlagRequired("image")
	worker.AddCommand(cmd)
	worker.AddCommand(toolBridge())
	root.AddCommand(worker)
}
