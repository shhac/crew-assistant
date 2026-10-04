package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/diagnostics"
	"github.com/shhac/crew-assistant/internal/upgrade"
)

func (a *App) SetUpgrading(value bool) {
	a.upgrading.Store(value)
	if value {
		a.dispatchDisabled.Store(true)
	}
}
func (a *App) Upgrading() bool { return a.upgrading.Load() }

func (a *App) RequestUpgrade(version string, automatic bool) error {
	if automatic {
		_, err := a.tryAutomaticUpgrade(context.Background(), version, false)
		return err
	}
	_, err := a.Core.AdmitUpgrade(context.Background(), func(core.Snapshot, config.Config) (bool, error) {
		err := a.requestUpgradeNow(version, automatic)
		return err == nil, err
	})
	return err
}

// Called only while core's admission lock excludes new claims.
func (a *App) requestUpgradeNow(version string, automatic bool) error {
	if a.upgradeEngine != nil {
		r, err := upgrade.ReadRecord(a.upgradeEngine.Path)
		if err != nil {
			return err
		}
		if r != nil && upgrade.InProgress(r.Step) {
			return upgrade.ErrInProgress
		}
	}
	if a.requestUpgrade == nil || a.Demo {
		return errors.New("this install cannot self-upgrade; upgrade by hand")
	}
	return a.requestUpgrade(version, automatic)
}

func (a *App) ClearRollback() error {
	if a.upgradeEngine == nil {
		return errors.New("no rollback is in force")
	}
	r, err := upgrade.ReadRecord(a.upgradeEngine.Path)
	if err != nil {
		return err
	}
	if r != nil && upgrade.InProgress(r.Step) {
		return upgrade.ErrInProgress
	}
	if r == nil || !r.Pinned {
		return errors.New("no rollback is in force")
	}
	return a.RequestUpgrade(r.To, false)
}

// UpgradeWaiting names live role turns and chat replies, never queued work.
func (a *App) UpgradeWaiting(ctx context.Context) ([]upgrade.WaitingOn, error) {
	snapshot, err := a.Core.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return upgradeWaiting(snapshot, a.Work.Turns()), nil
}

func upgradeWaiting(snapshot core.Snapshot, turns []core.Turn) []upgrade.WaitingOn {
	labels := map[string]string{}
	for _, task := range snapshot.Tasks {
		labels[task.ID] = task.Label()
	}
	for _, project := range snapshot.Projects {
		labels[project.ID] = project.Title
	}
	waiting := []upgrade.WaitingOn{}
	pmReplies := map[string]core.PMChatMessage{}
	for _, reply := range snapshot.PMChats {
		if reply.Status == "working" {
			pmReplies[reply.ID] = reply
		}
	}
	observedReplies := map[string]bool{}
	for _, turn := range turns {
		if turn.MessageID != "" {
			started := turn.StartedAt
			reply := pmReplies[turn.MessageID]
			if reply.StartedAt != nil {
				started = *reply.StartedAt
			}
			waiting = append(waiting, upgrade.WaitingOn{Kind: "pm-chat", Ref: turn.MessageID, Label: labels[turn.ProjectID] + " (PM reply)", StartedAt: started})
			observedReplies[turn.MessageID] = true
			continue
		}
		ref := turn.TaskID
		if ref == "" {
			ref = turn.ProjectID
		}
		label := labels[ref]
		if label == "" {
			label = ref
		}
		waiting = append(waiting, upgrade.WaitingOn{Kind: turn.Role, Ref: ref, Label: label + " (" + turn.Seat + ")", StartedAt: turn.StartedAt})
	}
	for _, turn := range snapshot.ChatTurns {
		if turn.Status == "running" && turn.StartedAt != nil {
			waiting = append(waiting, upgrade.WaitingOn{Kind: "chat", Ref: turn.ID, Label: "assistant reply", StartedAt: *turn.StartedAt})
		}
	}
	for _, reply := range snapshot.PMChats {
		if reply.Status == "working" && reply.StartedAt != nil && !observedReplies[reply.ID] {
			waiting = append(waiting, upgrade.WaitingOn{Kind: "pm-chat", Ref: reply.ID, Label: labels[reply.ProjectID] + " (PM reply)", StartedAt: *reply.StartedAt})
		}
	}
	slices.SortFunc(waiting, func(a, b upgrade.WaitingOn) int { return strings.Compare(a.Kind+a.Ref, b.Kind+b.Ref) })
	return waiting
}

func (a *App) runAutomaticUpgrades(ctx context.Context) {
	clock := a.upgradeClock
	if clock == nil {
		clock = upgrade.SystemClock()
	}
	var version string
	var noticed time.Time
	for ctx.Err() == nil {
		snap, err := a.Core.Snapshot(ctx)
		candidate := ""
		if err == nil && a.Config().Upgrade.Mode == "automatic" && automaticEligible(snap, a.version, snap.Update.Available) {
			candidate = snap.Update.Available
		}
		if candidate != version {
			version, noticed = candidate, clock.Now()
		}
		if version != "" {
			admitted, requestErr := a.tryAutomaticUpgrade(ctx, version, clock.Now().Sub(noticed) >= 6*time.Hour)
			if requestErr == nil {
				if admitted {
					return
				}
			} else if !errors.Is(requestErr, upgrade.ErrInProgress) && !errors.Is(requestErr, upgrade.ErrFailedVersion) && !errors.Is(requestErr, upgrade.ErrStopping) {
				a.updateFailure(requestErr)
			}
		}
		timer := clock.NewTimer(time.Minute)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C():
		}
	}
}

func automaticEligible(s core.Snapshot, running, candidate string) bool {
	return core.ValidVersion(candidate) && s.Update.Available == candidate &&
		core.CompareVersions(candidate, running) > 0 && s.Update.Skipped != candidate &&
		(!core.ValidVersion(s.Update.Failed) || core.CompareVersions(candidate, s.Update.Failed) > 0)
}

func (a *App) tryAutomaticUpgrade(ctx context.Context, candidate string, force bool) (bool, error) {
	// Core holds config and state through admission. Do not take the app
	// mutex here: role admission already holds state while reading app config.
	return a.Core.AdmitUpgrade(ctx, func(s core.Snapshot, cfg config.Config) (bool, error) {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if cfg.Upgrade.Mode != "automatic" || !automaticEligible(s, a.version, candidate) {
			return false, nil
		}
		if !force && (core.UpgradeWorkClaimed(s) || len(upgradeWaiting(s, a.Work.Turns())) > 0) {
			return false, nil
		}
		err := a.requestUpgradeNow(candidate, true)
		return err == nil, err
	})
}

// ObserveUpgradeDrain remains alive while Run waits for its in-flight turns.
// Record only changes to the waiting set, not a stream of repeated notices.
func (a *App) ObserveUpgradeDrain(ctx context.Context, automatic bool) error {
	r, err := upgrade.ReadRecord(a.upgradeEngine.Path)
	if err != nil {
		return err
	}
	var previous []upgrade.WaitingOn
	if r != nil {
		previous = r.WaitingOn
	}
	for {
		waiting, err := a.UpgradeWaiting(ctx)
		if err != nil {
			return err
		}
		if !slices.Equal(waiting, previous) {
			if err = a.recordUpgradeWaiting(ctx, waiting, automatic); err != nil {
				return err
			}
			previous = waiting
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}
	}
}

func (a *App) RecordUpgradeDrain(ctx context.Context, automatic bool) error {
	waiting, err := a.UpgradeWaiting(ctx)
	if err != nil {
		return err
	}
	return a.recordUpgradeWaiting(ctx, waiting, automatic)
}

func (a *App) recordUpgradeWaiting(ctx context.Context, waiting []upgrade.WaitingOn, automatic bool) error {
	if err := a.upgradeEngine.UpdateWaiting(waiting); err != nil {
		return err
	}
	if !automatic {
		return nil
	}
	parts := []string{}
	for _, item := range waiting {
		label := item.Label
		if label == "" {
			label = item.Ref
		}
		parts = append(parts, fmt.Sprintf("%s %s (running %s)", item.Kind, label, time.Since(item.StartedAt).Round(time.Second)))
	}
	summary := "Automatic upgrade: running work has finished."
	if len(parts) > 0 {
		summary = "Automatic upgrade is waiting on " + strings.Join(parts, ", ") + "."
	}
	return a.Core.RecordActivity(ctx, "", "upgrade.draining", summary)
}

func (a *App) releaseRecorded(repo, version string) {
	if a.checker != nil && !a.Demo && strings.EqualFold(repo, a.Config().Upgrade.SourceRepo) {
		a.checker.Nudge(version)
	}
}
func (a *App) runUpdates(ctx context.Context) error {
	if a.Demo {
		return nil
	}
	return a.runUpdateChecks(ctx, func(ctx context.Context) error {
		return a.Core.ReconcileRunningUpdate(ctx, a.version)
	}, func(ctx context.Context, result upgrade.Result) error {
		return a.Core.RecordUpdateCheck(ctx, a.version, result)
	})
}

// Keep startup reconciliation pending until it succeeds, independently of the
// source result. A failed state write is retried on the next tick or nudge.
func (a *App) runUpdateChecks(ctx context.Context, reconcile func(context.Context) error, record func(context.Context, upgrade.Result) error) error {
	reconcileErr := reconcile(ctx)
	if ctx.Err() != nil {
		return nil
	}
	if reconcileErr != nil {
		a.updateFailure(reconcileErr)
	}
	if a.checker == nil {
		return nil
	}
	return a.checker.Run(ctx, func() config.UpgradeSettings { return a.Config().Upgrade }, func(ctx context.Context, result upgrade.Result) error {
		if reconcileErr != nil {
			reconcileErr = reconcile(ctx)
			if reconcileErr != nil {
				return reconcileErr
			}
		}
		if err := record(ctx, result); err != nil {
			return err
		}
		a.mu.Lock()
		a.updateError = ""
		a.mu.Unlock()
		return nil
	}, a.updateFailure)
}

func (a *App) updateFailure(err error) {
	a.Diagnostics.Failure(diagnostics.Event{Component: "daemon", Stage: "update_checks"}, err)
	a.mu.Lock()
	a.updateError = "The update check couldn't be saved. It will retry on the next scheduled check."
	a.mu.Unlock()
}
