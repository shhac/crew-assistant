package work

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/quota"
)

// holdForUsage waits a task out while the role's subscription is past the
// owner's threshold. A hold is a wait, not a failure: nothing is retried or
// counted against the task, and it resumes by itself when the window resets
// or the threshold is raised.
func (lp *Loop) holdForUsage(ctx context.Context, t core.Task, r core.Role) (bool, error) {
	wait, detail := lp.usageWait(ctx, r)
	if wait.IsZero() {
		return false, nil
	}
	_, err := lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.RetryAt, t.Detail, t.HeldFor = wait, detail, r.Engine
		return "", nil
	})
	return true, err
}

// releaseUsageHolds lets work go that waits on a usage limit which no longer
// holds it: the owner raised the limit, or the engine's usage fell. Usage is
// read through the shared meter, so looking costs no more than a minute's
// cached reading.
func (lp *Loop) releaseUsageHolds(ctx context.Context, snap core.Snapshot) error {
	now := time.Now()
	for _, t := range snap.Tasks {
		engine := heldFor(t)
		if engine == "" || !t.RetryAt.After(now) || t.Finished() {
			continue
		}
		if wait, _ := lp.usageWait(ctx, core.Role{Engine: engine}); !wait.IsZero() {
			continue
		}
		if _, err := lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
			t.RetryAt, t.HeldFor, t.Detail = time.Time{}, "", ""
			return "", nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// UsageWait is when an engine may be used again, if the owner's usage limit
// holds it now, and why.
func (lp *Loop) UsageWait(ctx context.Context, engine string) (time.Time, string) {
	return lp.usageWait(ctx, core.Role{Engine: engine})
}

// Usage is what an engine's login reports it has left, read through the
// meter team work is held by, so the two never disagree. It answers within
// wait: a slower reading goes on to fill the meter, and meanwhile the last
// one is used, or none.
func (lp *Loop) Usage(ctx context.Context, engine string, wait time.Duration) quota.Remaining {
	h := lp.Config().Harness(engine, "", "")
	read := make(chan quota.Reading, 1)
	go func() { read <- lp.meter.Observe(context.WithoutCancel(ctx), h) }()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	var r quota.Reading
	select {
	case r = <-read:
	case <-timer.C:
		if last, ok := lp.meter.Cached(h); ok {
			r = last
		} else {
			r.Err = context.DeadlineExceeded
		}
	case <-ctx.Done():
		r.Err = ctx.Err()
	}
	return lp.describeUsage(engine, h, r)
}

// LastUsage is Usage from the last reading alone, starting no look, as
// while the daemon stops; false when there is no reading.
func (lp *Loop) LastUsage(engine string) (quota.Remaining, bool) {
	h := lp.Config().Harness(engine, "", "")
	r, ok := lp.meter.Cached(h)
	return lp.describeUsage(engine, h, r), ok
}

func (lp *Loop) describeUsage(engine string, h config.Harness, r quota.Reading) quota.Remaining {
	fiveHour, week := lp.Config().Engines.Floors(engine)
	out := quota.Describe(r, h, quota.Floors{FiveHour: fiveHour, Week: week}, time.Now())
	// With nothing measured now, a login last measured spent is still
	// taken as spent, as the small models take it.
	if len(out.Windows) == 0 && lp.meter.Spent(h) {
		out.Level, out.Missing = quota.LevelExhausted, "out of usage when last checked; "+out.Missing
	}
	return out
}

// OutOfUsage says the engine's login was out of usage when last measured,
// by the same engine-wide windows the sidebar shows, so the two agree: a
// pool only one model draws on counts for neither. Asking never waits: a
// login due a look again gets one in the background.
func (lp *Loop) OutOfUsage(h config.Harness) bool {
	return lp.meter.OutOfUsage(engineWide(h))
}

// RecheckUsage reads the engine's login again in the background after one
// of its models refused a request. A CLI's refusal doesn't say whether the
// account ran out, but its usage does, so a refusal for that shows as out of
// usage in the sidebar and to the fallback alike.
func (lp *Loop) RecheckUsage(h config.Harness) {
	go lp.meter.Observe(context.Background(), engineWide(h))
}

// engineWide is the engine's login without a model, as the sidebar reads it.
func engineWide(h config.Harness) config.Harness {
	return config.Harness{Engine: h.Engine, Bin: h.Bin, Home: h.Home}
}

// usageWait is when the role may run again, and why, while its subscription
// has less left than the owner's floor; zero when it may run now.
func (lp *Loop) usageWait(ctx context.Context, r core.Role) (time.Time, string) {
	cfg := lp.Config()
	fiveHour, week := cfg.Engines.Floors(r.Engine)
	floors := quota.Floors{FiveHour: fiveHour, Week: week}
	if floors.Off() {
		return time.Time{}, ""
	}
	h := cfg.Harness(r.Engine, r.Model, r.Effort)
	now := time.Now()
	verdict := quota.Evaluate(lp.meter.Read(ctx, h), h, floors, now)
	switch {
	case verdict.Held:
		wait := verdict.ResetsAt
		if !wait.After(now) {
			wait = now.Add(10 * time.Minute)
		}
		return wait, fmt.Sprintf("Waiting for %s usage to reset (%s)", engineName(r.Engine), verdict.Detail)
	case !verdict.Known && cfg.Engines.PauseOnUnknownUsage(r.Engine):
		return now.Add(5 * time.Minute), "Waiting until " + engineName(r.Engine) + " usage can be checked"
	}
	return time.Time{}, ""
}

// heldFor is the engine whose usage limit holds a task. A hold made before
// holds were marked is known by what it says.
func heldFor(t core.Task) string {
	if t.HeldFor != "" {
		return t.HeldFor
	}
	for _, engine := range config.CLIEngineNames {
		if strings.HasPrefix(t.Detail, "Waiting for "+engineName(engine)+" usage to reset") {
			return engine
		}
	}
	return ""
}

func engineName(engine string) string {
	if engine == "codex" {
		return "Codex"
	}
	if engine == "claude" {
		return "Claude"
	}
	return engine
}
