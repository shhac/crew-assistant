package work

import (
	"context"
	"fmt"
	"strings"
	"time"

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

// usageWait is when the role may run again, and why, while its subscription
// has less left than the owner's floor; zero when it may run now.
func (lp *Loop) usageWait(ctx context.Context, r core.Role) (time.Time, string) {
	cfg := lp.Config()
	fiveHour, week, supported := cfg.Engines.Floors(r.Engine)
	floors := quota.Floors{FiveHour: fiveHour, Week: week}
	if !supported || floors.Off() {
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
	for _, engine := range []string{"claude", "codex"} {
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
