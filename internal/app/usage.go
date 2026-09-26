package app

import (
	"context"
	"sync"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/quota"
)

// usageWait bounds how long the owner waits on a usage reading; a slower
// one is shown on the next look.
const usageWait = 10 * time.Second

// EngineUsage is what one engine's login reports it has left.
type EngineUsage struct {
	Engine string `json:"engine"`
	quota.Remaining
	// RateLimitedUntil is when loading captions and suggestions try this
	// engine again after it refused them for its rate limit.
	RateLimitedUntil *time.Time `json:"rate_limited_until,omitempty"`
}

// Usage is what each CLI engine's login reports it has left, read without a
// model call. The demo reads no login, and while the daemon stops only the
// last reading is shown, since nothing new starts a CLI.
func (a *App) Usage(ctx context.Context) []EngineUsage {
	out := make([]EngineUsage, len(config.CLIEngineNames))
	stopping := a.Stopping()
	var wg sync.WaitGroup
	for i, name := range config.CLIEngineNames {
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
	return out
}
