package app

import (
	"context"
	"sync"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/engine"
	"github.com/shhac/crew-assistant/internal/quota"
	harness "github.com/shhac/lib-agent-harness"
)

// usageWait bounds how long the owner waits on a usage reading; a slower
// one is shown on the next look.
const usageWait = 10 * time.Second

// EngineUsage is what one engine's login reports it has left.
type EngineUsage struct {
	Engine string `json:"engine"`
	// Provider and Label name an API provider, which reports no usage and
	// is listed only while it rests for its rate limit.
	Provider string `json:"provider,omitempty"`
	Label    string `json:"label,omitempty"`
	quota.Remaining
	// RateLimitedUntil is when loading captions and suggestions try this
	// engine again after it refused them for its rate limit.
	RateLimitedUntil *time.Time `json:"rate_limited_until,omitempty"`
}

// Usage is what each engine that reports usage windows has left, read
// without a model call. The demo reads no login, and while the daemon stops only the
// last reading is shown, since nothing new starts a CLI.
func (a *App) Usage(ctx context.Context) []EngineUsage {
	engines := config.EnginesFor(config.UseUsage)
	out := make([]EngineUsage, len(engines))
	stopping := a.Stopping()
	var wg sync.WaitGroup
	for i, name := range engines {
		out[i].Engine = name
		switch {
		case a.Demo:
			out[i].Remaining = quota.Remaining{Level: quota.LevelUnknown, Windows: []quota.Window{}, Missing: "not checked in the demo"}
		case stopping:
			last, ok := a.Work.LastUsage(name)
			if !ok && last.Level != quota.LevelExhausted {
				last.Missing = "not checked while stopping"
			}
			out[i].Remaining = last
		default:
			wg.Go(func() { out[i].Remaining = a.Work.Usage(ctx, name, usageWait) })
		}
	}
	wg.Wait()
	for i := range out {
		if until := a.small.rateLimitedUntil(out[i].Engine); !until.IsZero() {
			out[i].RateLimitedUntil = &until
		}
	}
	api, cfg := string(harness.OpenAICompatible), a.Config()
	for _, p := range cfg.Engines.APIProviders() {
		// A provider shows resting only on the route it has now: one
		// edited to another address or key is another account.
		until := a.small.rateLimitedUntil(restKey(cfg.HarnessOn(api, p.ID, "", "")))
		if until.IsZero() {
			continue
		}
		out = append(out, EngineUsage{Engine: api, Provider: p.ID, Label: p.Label(), Remaining: quota.Remaining{Level: quota.LevelUnknown, Windows: []quota.Window{}}, RateLimitedUntil: &until})
	}
	return out
}

// restRateLimited rests the API provider a chat request ran on after it
// stayed rate-limited through its retries, so suggestions and loading lines
// leave it alone too and the sidebar says so, and names the provider in the
// error. It is the provider ec reached, named as cfg, the config ec was made
// from, names it. A CLI's limits show in its usage instead.
func (a *App) restRateLimited(cfg config.Config, ec engine.Config, err error) error {
	if facts, ok := harness.ErrorFacts(err); !ok || facts.Cause != harness.CauseRateLimited || ec.APIProvider == "" {
		return err
	}
	a.small.rested(config.Harness{Engine: ec.Engine(), APIProvider: ec.APIProvider, BaseURL: modelEndpoint(ec), APIKeyEnv: ec.APIKeyEnv}, err)
	label := ec.APIProvider
	if p, ok := cfg.Engines.APIProvider(ec.APIProvider); ok {
		label = p.Label()
	}
	return &providerRateLimit{provider: label, err: err}
}

// providerRateLimit is an API provider that stayed rate-limited.
type providerRateLimit struct {
	provider string
	err      error
}

func (e *providerRateLimit) Error() string { return e.provider + ": " + e.err.Error() }
func (e *providerRateLimit) Unwrap() error { return e.err }
