package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/diagnostics"
	"github.com/shhac/crew-assistant/internal/engine"
	"github.com/shhac/crew-assistant/internal/integrations/github"
	"github.com/shhac/crew-assistant/internal/media/gitrepo"
)

// wakeCheckEvery is how often the watcher looks at branches, times and
// expiries. A task's status is watched as it changes, not polled.
const wakeCheckEvery = 15 * time.Second

// WakeMeWhen registers one of the assistant's wakes. The baseline is read now,
// so a change is measured from what the assistant could see when it asked.
func (a *App) WakeMeWhen(ctx context.Context, in engine.WakeArgs) (core.Wake, error) {
	return a.registerWake(ctx, core.WakeAssistant, "", in)
}

func (a *App) registerWake(ctx context.Context, owner, taskID string, in engine.WakeArgs) (core.Wake, error) {
	timeout := time.Duration(0)
	if in.Timeout != "" {
		d, err := time.ParseDuration(in.Timeout)
		if err != nil || d <= 0 {
			return core.Wake{}, errors.New("timeout is a duration such as 30m or 6h, or empty for a day")
		}
		timeout = d
	}
	wake := core.WakeInput{Owner: owner, TaskID: taskID, On: in.On, Target: in.Target, Match: in.Match, Prompt: in.Prompt, ProjectID: in.ProjectID, Timeout: timeout}
	snap, err := a.Core.Snapshot(ctx)
	if err != nil {
		return core.Wake{}, err
	}
	switch in.On {
	case core.WakeOnTask:
		t, ok := findTask(snap, "", in.Target)
		if !ok {
			return core.Wake{}, fmt.Errorf("there is no task %q", in.Target)
		}
		wake.Baseline, wake.ProjectID = t.Status, t.ProjectID
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
		pr, err := a.viewPR(ctx, in.Target)
		if err != nil {
			return core.Wake{}, err
		}
		wake.Baseline = prValue(in.On, pr)
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

// viewPR reads a pull request named as owner/name#number.
func (a *App) viewPR(ctx context.Context, target string) (github.PR, error) {
	ref, err := github.ParsePRRef(target)
	if err != nil {
		return github.PR{}, err
	}
	return a.github.View(ctx, ref.Repo, ref.Number)
}

func prValue(on string, pr github.PR) string {
	if on == core.WakeOnReview {
		return prReviewValue(pr)
	}
	return prChecksValue(pr)
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
		f, ok := a.observe(ctx, snap, w, now)
		if !ok {
			continue
		}
		if _, err := a.Core.FireWake(ctx, w.ID, f.observed, f.event, f.timedOut); err != nil && !errors.Is(err, core.ErrConflict) {
			return err
		}
		if w.Owner != core.WakeAssistant {
			if err := a.wakeTask(ctx, w.TaskID, f.event); err != nil {
				return err
			}
		}
	}
	return nil
}

// firing is a change the watcher saw.
type firing struct {
	observed, event string
	timedOut        bool
}

// observe looks at what w waits on and reports a change that fires it.
func (a *App) observe(ctx context.Context, snap core.Snapshot, w core.Wake, now time.Time) (firing, bool) {
	if now.After(w.ExpiresAt) {
		return firing{observed: w.Baseline, event: "timed out with no change", timedOut: true}, true
	}
	switch w.On {
	case core.WakeOnTime:
		at, err := time.Parse(time.RFC3339, w.Target)
		return firing{observed: "reached", event: "the time came"}, err == nil && !now.Before(at)
	case core.WakeOnChecks, core.WakeOnReview:
		// GitHub is asked about each pull request at most once a minute.
		if last, ok := a.prSeen.Load(w.On + w.Target); ok && now.Sub(last.(time.Time)) < time.Minute {
			return firing{}, false
		}
		a.prSeen.Store(w.On+w.Target, now)
		pr, err := a.viewPR(ctx, w.Target)
		if err != nil {
			return firing{}, false
		}
		value := prValue(w.On, pr)
		return firing{observed: value, event: fmt.Sprintf("pull request %s: %s is now %s", w.Target, strings.TrimPrefix(w.On, "pr_"), value)}, w.FiresOn(value)
	case core.WakeOnBranch:
		dir, err := projectRepo(snap, w.ProjectID)
		if err != nil {
			return firing{}, false
		}
		tip, err := gitrepo.BranchTip(ctx, dir, w.Target)
		if err != nil {
			return firing{}, false
		}
		return firing{observed: tip, event: fmt.Sprintf("%s moved from %s to %s", w.Target, short(w.Baseline), short(tip))}, w.FiresOn(tip)
	}
	return firing{}, false
}

// wakeTask sends a task asleep on something outside the team back to
// landing, to look again.
func (a *App) wakeTask(ctx context.Context, taskID, event string) error {
	_, err := a.Core.UpdateTask(ctx, taskID, func(t *core.Task, _ *core.Project) (string, error) {
		if t.Status == core.TaskAwaiting {
			t.Status, t.Detail = core.TaskLanding, "Looking again: "+event
		}
		return "", nil
	})
	if err != nil && !errors.Is(err, core.ErrNotFound) {
		return err
	}
	a.nudgeLoop()
	return nil
}

// wakeBlock is what an implementer may end its reply with.
type wakeBlock struct {
	WakeMeWhen []engine.WakeArgs `json:"wake_me_when"`
	Cancel     []string          `json:"cancel"`
}

// splitWakeBlock takes a trailing ```wake block off the implementer's reply.
func splitWakeBlock(text string) (string, string) {
	start := strings.LastIndex(text, "```wake")
	if start < 0 {
		return text, ""
	}
	rest := text[start+len("```wake"):]
	end := strings.Index(rest, "```")
	if end < 0 {
		return text, ""
	}
	return strings.TrimSpace(text[:start] + rest[end+3:]), strings.TrimSpace(rest[:end])
}

// applyWakeBlock registers and cancels the implementer's wakes. Problems are
// kept for its next round rather than dropped.
func (a *App) applyWakeBlock(ctx context.Context, p core.Project, t core.Task, block string) []string {
	if block == "" {
		return nil
	}
	var in wakeBlock
	if err := json.Unmarshal([]byte(block), &in); err != nil {
		return []string{"the wake block was not valid JSON: " + err.Error()}
	}
	var problems []string
	for _, handle := range in.Cancel {
		if _, err := a.Core.CancelWake(ctx, handle, t.ID); err != nil {
			problems = append(problems, fmt.Sprintf("cancelling %s: %v", handle, err))
		}
	}
	for _, req := range in.WakeMeWhen {
		req.ProjectID = p.ID
		if req.Target == "this" {
			if !proposed(t) || t.Playbook == nil {
				problems = append(problems, "there is no pull request yet, so \"this\" names nothing")
				continue
			}
			req.Target = github.PRRef{Repo: t.Playbook.Land.GitHub, Number: t.Proposal.Number}.String()
		}
		if _, err := a.registerWake(ctx, core.WakeTask, t.ID, req); err != nil {
			problems = append(problems, fmt.Sprintf("waiting on %s %s: %v", req.On, req.Target, err))
		}
	}
	return problems
}

// wakePrompt tells the implementer what woke it, what it is still waiting
// on, and how to ask for more.
func (a *App) wakePrompt(ctx context.Context, t core.Task, woken []core.Wake) (string, error) {
	snap, err := a.Core.Snapshot(ctx)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if len(woken) > 0 {
		b.WriteString("\n\nWake-ups you asked for have come:\n" + core.WakeReport(woken, time.Now()) + "\n")
	}
	var waiting []string
	for _, w := range snap.Wakes {
		if w.TaskID == t.ID && w.Owner == core.WakeTask && w.Status == core.WakeWaiting {
			waiting = append(waiting, fmt.Sprintf("%s (%s %s, until %s)", w.ID, w.On, w.Target, w.ExpiresAt.UTC().Format(time.RFC3339)))
		}
	}
	if len(waiting) > 0 {
		b.WriteString("\nYou are still waiting on: " + strings.Join(waiting, "; ") + ".\n")
	}
	if len(t.WakeErrors) > 0 {
		b.WriteString("\nYour last wake block had problems: " + strings.Join(t.WakeErrors, "; ") + ".\n")
	}
	if t.Playbook != nil && t.Playbook.Land.Way() == core.LandPullRequest {
		b.WriteString("\nIf something outside this change matters later, you can end your reply with a wake block and be woken in a later round, with your own note:\n```wake\n{\"wake_me_when\": [{\"on\": \"pr_checks\", \"target\": \"this\", \"match\": \"\", \"prompt\": \"what to do then\", \"timeout\": \"2h\"}], \"cancel\": []}\n```\non can be pr_checks or pr_review (target this), branch (a branch name), time (RFC 3339 or a duration) or task (a task id). Cancel handles you no longer need.\n")
	}
	return b.String(), nil
}
