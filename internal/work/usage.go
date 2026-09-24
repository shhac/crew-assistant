package work

import (
	"context"
	"fmt"
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
		t.RetryAt, t.Detail = wait, detail
		return "", nil
	})
	return true, err
}

// UsageWait is when an engine may be used again, if the owner's usage limit
// holds it now, and why.
func (lp *Loop) UsageWait(ctx context.Context, engine string) (time.Time, string) {
	return lp.usageWait(ctx, core.Role{Engine: engine})
}

// usageWait is when the role may run again, and why, while its subscription
// is past the owner's threshold; zero when it may run now.
func (lp *Loop) usageWait(ctx context.Context, r core.Role) (time.Time, string) {
	cfg := lp.Config()
	threshold, supported := quota.Threshold(cfg.Limits.RoleUsage, r.Engine)
	if !supported || threshold == 0 {
		return time.Time{}, ""
	}
	model := cfg.Model
	model.Engine, model.Model = r.Engine, r.Model
	now := time.Now()
	verdict := quota.Evaluate(lp.meter.Read(ctx, model), model, threshold, now)
	switch {
	case verdict.Held:
		wait := verdict.ResetsAt
		if !wait.After(now) {
			wait = now.Add(10 * time.Minute)
		}
		return wait, fmt.Sprintf("Waiting for %s usage to reset (%s)", engineName(r.Engine), verdict.Detail)
	case !verdict.Known && cfg.Limits.RoleUsage.OnUnavailable == "pause":
		return now.Add(5 * time.Minute), "Waiting until " + engineName(r.Engine) + " usage can be checked"
	}
	return time.Time{}, ""
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
