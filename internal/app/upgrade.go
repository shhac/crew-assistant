package app

import (
	"context"
	"strings"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/diagnostics"
	"github.com/shhac/crew-assistant/internal/upgrade"
)

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
