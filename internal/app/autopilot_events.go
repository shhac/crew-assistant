package app

import (
	"context"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/lifecycle"
)

func (a *App) runAutopilotEvents(stop lifecycle.Stop) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for !stop.Stopping() {
		now := time.Now()
		err := a.Core.RecordAutopilotHeartbeat(stop.Force, now)
		if err == nil {
			err = a.Autopilot.ConsumeEvents(stop.Force, now, stop.Stopping)
		}
		if err == nil {
			_, err = a.Autopilot.RecordDigest(stop.Force, now, time.Local)
		}
		a.autopilotStatus("autopilot-events", err, "Event delivery failed")
		select {
		case <-stop.Graceful.Done():
			return
		case <-tick.C:
		case <-a.Core.AutopilotNudges():
		}
	}
}

// Notifications use a bounded durable retry reservation, not uncertain-operation
// claims. Only successful presentation commits the notified cursor.
func (a *App) autopilotNotice(ctx context.Context, key string, boundary int64, now time.Time, text string, send func(context.Context, string) error) (bool, error) {
	ready, err := a.Autopilot.BeginNotification(ctx, key, now)
	if err != nil || !ready {
		return false, err
	}
	sendErr := send(ctx, text)
	finishErr := a.Autopilot.FinishNotification(ctx, key, boundary, sendErr == nil)
	if sendErr != nil {
		return false, sendErr
	}
	return finishErr == nil, finishErr
}

func (a *App) autopilotStatus(id string, err error, detail string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		a.statuses[id] = core.Integration{ID: id, Name: "Autopilot", Status: "error", Detail: detail + ": " + err.Error()}
	} else {
		delete(a.statuses, id)
	}
	var failures []string
	for _, key := range []string{"autopilot-events", "autopilot-notifications", "autopilot-digests", "autopilot-summary", "autopilot-acknowledgement"} {
		if failure, ok := a.statuses[key]; ok {
			failures = append(failures, failure.Detail)
		}
	}
	if len(failures) == 0 {
		delete(a.statuses, "autopilot")
	} else {
		a.statuses["autopilot"] = core.Integration{ID: "autopilot", Name: "Autopilot", Status: "error", Detail: strings.Join(failures, "; ")}
	}
}

func (a *App) notifyAutopilotActions(ctx context.Context, send func(context.Context, string) error) {
	a.notifyAutopilotActionsAt(ctx, time.Now(), send)
}
func (a *App) notifyAutopilotActionsAt(ctx context.Context, now time.Time, send func(context.Context, string) error) {
	after, err := a.Autopilot.Progress(ctx, "notified")
	if err != nil {
		a.autopilotStatus("autopilot-notifications", err, "Unable to read notification progress")
		return
	}
	summary, err := a.Autopilot.Summary(ctx, core.SummaryQuery{After: after, ChangesOnly: true})
	if err != nil {
		a.autopilotStatus("autopilot-notifications", err, "Unable to read notification summary")
		return
	}
	if summary.Boundary > after {
		done, err := a.autopilotNotice(ctx, "actions", summary.Boundary, now, summary.Text(), send)
		if done || err != nil {
			a.autopilotStatus("autopilot-notifications", err, "Action notification failed")
		}
	} else {
		a.autopilotStatus("autopilot-notifications", nil, "")
	}
}

func (a *App) notifyAutopilotDigests(ctx context.Context, send func(context.Context, string) error) {
	a.notifyAutopilotDigestsAt(ctx, time.Now(), time.Local, send)
}

func (a *App) notifyAutopilotDigestsAt(ctx context.Context, now time.Time, location *time.Location, send func(context.Context, string) error) {
	if !a.Config().Autopilot.DailyDigest.Enabled {
		a.autopilotStatus("autopilot-digests", nil, "")
		return
	}
	// Only today is eligible: enabling a channel must not backfill old digests.
	digests, err := a.Autopilot.Digests(ctx, now.In(location).AddDate(0, 0, 1).Format("2006-01-02"), 1)
	if err != nil {
		a.autopilotStatus("autopilot-digests", err, "Unable to read daily digests")
		return
	}
	if len(digests) == 0 || digests[0].LocalDate != now.In(location).Format("2006-01-02") {
		a.autopilotStatus("autopilot-digests", nil, "")
		return
	}
	for i := len(digests) - 1; i >= 0; i-- {
		d := digests[i]
		if d.LocalDate != now.In(location).Format("2006-01-02") {
			continue
		}
		summary, err := a.Autopilot.Summary(ctx, core.SummaryQuery{After: d.From, Boundary: d.Boundary, FixedBoundary: true})
		if err != nil {
			a.autopilotStatus("autopilot-digests", err, "Unable to read digest summary")
			return
		}
		done, err := a.autopilotNotice(ctx, "digest:"+d.LocalDate, d.Boundary, now, "Daily digest "+d.LocalDate+"\n"+summary.Text(), send)
		if done || err != nil {
			a.autopilotStatus("autopilot-digests", err, "Digest notification failed")
		}
		if err != nil {
			return
		}
	}
}

func (a *App) notifyAutopilot(ctx context.Context, send func(context.Context, string) error) {
	a.notifyAutopilotActions(ctx, send)
	a.notifyAutopilotDigests(ctx, send)
}
