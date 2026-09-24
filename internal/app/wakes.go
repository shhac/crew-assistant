package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/diagnostics"
	"github.com/shhac/crew-assistant/internal/engine"
	"github.com/shhac/crew-assistant/internal/media/gitrepo"
)

// wakeCheckEvery is how often the watcher looks at branches, times and
// expiries. A task's status is watched as it changes, not polled.
const wakeCheckEvery = 15 * time.Second

// WakeMeWhen registers one of the assistant's wakes. The baseline is read now,
// so a change is measured from what the assistant could see when it asked.
func (a *App) WakeMeWhen(ctx context.Context, in engine.WakeArgs) (core.Wake, error) {
	timeout := time.Duration(0)
	if in.Timeout != "" {
		d, err := time.ParseDuration(in.Timeout)
		if err != nil || d <= 0 {
			return core.Wake{}, errors.New("timeout is a duration such as 30m or 6h, or empty for a day")
		}
		timeout = d
	}
	wake := core.WakeInput{Owner: core.WakeAssistant, On: in.On, Target: in.Target, Match: in.Match, Prompt: in.Prompt, ProjectID: in.ProjectID, Timeout: timeout}
	snap, err := a.Core.Snapshot(ctx)
	if err != nil {
		return core.Wake{}, err
	}
	switch in.On {
	case core.WakeOnTask:
		for _, t := range snap.Tasks {
			if t.ID == in.Target {
				wake.Baseline, wake.ProjectID = t.Status, t.ProjectID
			}
		}
		if wake.Baseline == "" {
			return core.Wake{}, fmt.Errorf("there is no task %q", in.Target)
		}
	case core.WakeOnBranch:
		dir, err := projectRepo(snap, in.ProjectID)
		if err != nil {
			return core.Wake{}, err
		}
		if wake.Baseline, err = gitrepo.BranchTip(ctx, dir, in.Target); err != nil {
			return core.Wake{}, err
		}
	case core.WakeOnTime:
		at, err := wakeTime(in.Target, time.Now())
		if err != nil {
			return core.Wake{}, err
		}
		wake.Target, wake.Baseline = at.Format(time.RFC3339), "not yet"
		if wake.Timeout == 0 || wake.Timeout < time.Until(at) {
			wake.Timeout = time.Until(at) + time.Minute
		}
	case core.WakeOnChecks, core.WakeOnReview:
		return core.Wake{}, errors.New("waiting on pull requests is not built yet")
	}
	return a.Core.RegisterWake(ctx, wake)
}

// OpenWakes lists every wake still waiting or not yet delivered.
func (a *App) OpenWakes(ctx context.Context) ([]core.Wake, error) {
	snap, err := a.Core.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	out := []core.Wake{}
	for _, w := range snap.Wakes {
		if w.Status == core.WakeWaiting || w.Status == core.WakeFired {
			out = append(out, w)
		}
	}
	return out, nil
}

// wakeTime reads a time as RFC 3339 or as a duration from now.
func wakeTime(target string, now time.Time) (time.Time, error) {
	if at, err := time.Parse(time.RFC3339, target); err == nil {
		return at.UTC(), nil
	}
	if d, err := time.ParseDuration(target); err == nil && d > 0 {
		return now.Add(d).UTC(), nil
	}
	return time.Time{}, errors.New("a time is RFC 3339, such as 2026-09-24T15:00:00Z, or a duration such as 30m")
}

func projectRepo(snap core.Snapshot, projectID string) (string, error) {
	p, ok := findProject(snap, projectID)
	if !ok {
		return "", fmt.Errorf("there is no project %q", projectID)
	}
	if p.Playbook != nil && p.Playbook.Repo != "" {
		return p.Playbook.Repo, nil
	}
	if len(p.Directories) > 0 {
		return p.Directories[0], nil
	}
	return "", errors.New("that project has no repository")
}

// runWakes watches what cannot tell the daemon it changed: branches, the
// clock, and every wake's expiry.
func (a *App) runWakes(ctx context.Context) {
	tick := time.NewTicker(wakeCheckEvery)
	defer tick.Stop()
	for {
		if err := a.checkWakes(ctx, time.Now()); err != nil && ctx.Err() == nil {
			a.Diagnostics.Failure(diagnostics.Event{Component: "daemon", Stage: "wakes"}, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (a *App) checkWakes(ctx context.Context, now time.Time) error {
	waiting, err := a.Core.Waiting(ctx)
	if err != nil {
		return err
	}
	snap, err := a.Core.Snapshot(ctx)
	if err != nil {
		return err
	}
	for _, w := range waiting {
		observed, event, fired, timedOut := "", "", false, false
		switch {
		case now.After(w.ExpiresAt):
			observed, event, fired, timedOut = w.Baseline, "timed out with no change", true, true
		case w.On == core.WakeOnTime:
			at, err := time.Parse(time.RFC3339, w.Target)
			if err == nil && !now.Before(at) {
				observed, event, fired = "reached", "the time came", true
			}
		case w.On == core.WakeOnBranch:
			dir, err := projectRepo(snap, w.ProjectID)
			if err != nil {
				continue
			}
			tip, err := gitrepo.BranchTip(ctx, dir, w.Target)
			if err != nil || (w.Match == "" && tip == w.Baseline) || (w.Match != "" && tip != w.Match) {
				continue
			}
			observed, event, fired = tip, fmt.Sprintf("%s moved from %s to %s", w.Target, short(w.Baseline), short(tip)), true
		}
		if !fired {
			continue
		}
		if _, err := a.Core.FireWake(ctx, w.ID, observed, event, timedOut); err != nil && !errors.Is(err, core.ErrConflict) {
			return err
		}
		if w.Owner != core.WakeAssistant {
			a.nudgeLoop()
		}
	}
	return nil
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
