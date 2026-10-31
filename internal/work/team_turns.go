package work

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/diagnostics"
	"github.com/shhac/crew-assistant/internal/roles"
	"github.com/shhac/lib-agent-harness/session"
)

type teamAccountingFailure struct {
	run, accounting error
	result          roles.Result
	// record makes the same accounting writes again.
	record func(context.Context) error
}

type accountingFailureKey struct{}
type accountingFailureState struct{ failure *teamAccountingFailure }

func (e *teamAccountingFailure) Error() string {
	return fmt.Sprintf("recording team turn accounting: %v", e.accounting)
}
func (e *teamAccountingFailure) Unwrap() []error {
	if e.run == nil {
		return []error{e.accounting}
	}
	return []error{e.run, e.accounting}
}

// runAttempt records exactly one runner invocation. Accounting retries below
// repeat only the durable write, never the invocation.
func (lp *Loop) runAttempt(ctx context.Context, spec roles.Spec) (roles.Result, error) {
	id := rand.Text()
	if spec.LaunchDir == "" {
		spec.LaunchDir = lp.launchDir(id)
	}
	_, token, _ := core.FenceOf(ctx)
	admission := core.TeamTurn{ID: id, ProjectID: spec.ProjectID, TaskID: spec.TaskID,
		ClaimToken: token, LaunchDir: spec.LaunchDir, Role: spec.Role, Seat: spec.Seat,
		MemberID: spec.MemberID, MemberName: spec.MemberName, Engine: spec.Engine, Model: spec.Model,
		PreviousID: spec.PreviousID, RetryCause: spec.RetryCause, AdmittedAt: time.Now(),
		UntrackedLaunch: len(spec.Tools) == 0 || spec.Engine == "openai-compatible"}
	if err := lp.Core.AdmitTeamTurn(ctx, admission); err != nil {
		return roles.Result{}, fmt.Errorf("recording team turn admission: %w", err)
	}
	if observer, ok := spec.Observer.(interface{ Attempt(string) }); ok {
		observer.Attempt(id)
	}
	opening, accepted := spec.Opening, spec.Accepted
	var unavailable []core.TeamTurnTool
	report := spec.ToolReport
	spec.ToolReport = func(tools []session.WorkbenchTool) {
		unavailable = nil
		for _, tool := range tools {
			unavailable = append(unavailable, core.TeamTurnTool{Name: tool.Name, Reason: tool.Capability.Reason})
		}
		if report != nil {
			report(tools)
		}
	}
	var actualOpening *core.TeamTurnOpening
	var actualAccepted *time.Time
	spec.Opening = func(o session.Opened, ref session.Ref) error {
		reason := spec.FreshReason
		if reason == "" {
			reason = core.FreshNoThread
		}
		switch o.Fresh {
		case session.FreshIncompatible:
			reason = core.FreshHarnessIncompatible
		case session.FreshUnavailable:
			reason = core.FreshHarnessUnavailable
		}
		if o.Resumed {
			reason = ""
		}
		at := time.Now()
		if actualOpening != nil {
			at = actualOpening.At
		}
		observed := core.TeamTurnOpening{At: at, Resumed: o.Resumed, FreshReason: reason, SessionID: ref.ID, UnavailableTools: unavailable}
		if actualOpening == nil {
			actualOpening = &observed
		}
		if err := lp.Core.OpenTeamTurn(ctx, id, observed); err != nil {
			return err
		}
		if opening != nil {
			return opening(o, ref)
		}
		return nil
	}
	spec.Accepted = func() error {
		at := time.Now()
		if actualAccepted == nil {
			actualAccepted = &at
		} else {
			at = *actualAccepted
		}
		if err := lp.Core.AcceptTeamTurn(ctx, id, at); err != nil {
			return err
		}
		if accepted != nil {
			return accepted()
		}
		return nil
	}
	result, runErr := lp.runner.Run(ctx, spec)
	result.AttemptID = id
	outcome := "completed"
	if runErr != nil {
		outcome = "failed"
	}
	if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
		outcome = "interrupted"
	}
	terminal := core.TeamTurnTerminal{At: time.Now(), Outcome: outcome, FailureStage: result.FailureStage,
		ProviderStatus: result.Provider.Status, ProviderTurnID: result.Provider.TurnID,
		NativeError: result.Provider.NativeError, CleanupConfirmed: result.CleanupConfirmed,
		Usage: result.Provider.Usage, Observed: result.Provider.Observed,
		CompactionUsage: result.Compaction.Usage, CompactionObserved: result.Compaction.Observed}
	record := func(ctx context.Context) error {
		if actualOpening != nil {
			if err := lp.Core.OpenTeamTurn(ctx, id, *actualOpening); err != nil {
				return err
			}
		}
		if actualAccepted != nil {
			if err := lp.Core.AcceptTeamTurn(ctx, id, *actualAccepted); err != nil {
				return err
			}
		}
		finish := lp.finishTeamTurn
		if finish == nil {
			finish = lp.Core.FinishTeamTurn
		}
		return finish(ctx, id, terminal)
	}
	accountCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), accountingWait)
	defer cancel()
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = record(accountCtx)
		if err == nil {
			return result, runErr
		}
		if errors.Is(err, core.ErrConflict) || errors.Is(err, core.ErrNotFound) {
			break
		}
		select {
		case <-accountCtx.Done():
			return result, accountingFailed(ctx, result, runErr, err, record)
		case <-time.After(time.Duration(attempt+1) * 50 * time.Millisecond):
		}
	}
	// runRole recognizes this separate accounting failure and stops immediate
	// fallback even when the runner also reported a browser refusal.
	return result, accountingFailed(ctx, result, runErr, err, record)
}

// Retain the result and signal the scheduler even when a caller handles
// the error locally, as PM, message and release paths may do.
func accountingFailed(ctx context.Context, result roles.Result, runErr, writeErr error, record func(context.Context) error) error {
	failure := &teamAccountingFailure{run: runErr, accounting: writeErr, result: result, record: record}
	if state, ok := ctx.Value(accountingFailureKey{}).(*accountingFailureState); ok {
		state.failure = failure
	}
	return failure
}

// Recovery runs before claims can be reused, including admissions whose
// process stopped before creating its launch directory.
func (lp *Loop) recoverTeamTurns(ctx context.Context, held map[string]string, confirmed map[string]bool) bool {
	turns, err := lp.Core.TeamTurns(ctx, core.TeamTurnFilter{NeedsRecovery: true})
	if err != nil {
		held[""] = "Team turn accounting could not be read"
		return true
	}
	running := false
	for _, t := range turns {
		gone, checked := confirmed[t.LaunchDir]
		if t.UntrackedLaunch {
			// Absence of a marker proves nothing for an untracked attempt,
			// including one sharing a directory with a tracked predecessor.
			gone, checked = false, true
		}
		if !checked && t.LaunchDir != "" {
			reclaim := lp.reclaim
			if reclaim == nil {
				reclaim = session.Reclaim
			}
			result, _ := reclaim(ctx, t.LaunchDir)
			gone = result.Confirmed
		}
		if err := lp.Core.RecoverTeamTurn(ctx, t.ID, gone, time.Now()); err != nil {
			gone = false
		}
		if !gone {
			held[t.ClaimToken] = "The previous turn's cleanup or accounting could not be confirmed"
			running = true
		}
	}
	return running
}

// accountingWait is how long a finished turn's accounting may take to be
// recorded. Each record rewrites the whole state, which on a busy machine
// took longer than the five seconds once allowed; a turn that runs out of
// time holds its member's claim until a restart, stalling every task that
// needs them.
const accountingWait = 2 * time.Minute

// accountingHold is why a claim is kept after its turn ended but the turn's
// accounting could not be recorded. The loop retries the accounting and lets
// the claim go once it is recorded; startup lets it go regardless.
const accountingHold = core.AccountingHold

// accountingRetry is a held claim's accounting, tried again until recorded.
type accountingRetry struct {
	task, project, token string
	record               func(context.Context) error
	due                  time.Time
	wait                 time.Duration
}

// keepAccounting has the loop try a failed turn's accounting again, from a
// little later, and let its held claim go once it is recorded.
func (lp *Loop) keepAccounting(c claimed, failure *teamAccountingFailure) {
	if failure.record == nil {
		return
	}
	lp.accountingRetries.Store(c.token, &accountingRetry{task: c.task, project: c.project, token: c.token,
		record: failure.record, due: lp.now().Add(accountingRetryWait), wait: accountingRetryWait})
}

// retryAccounting tries again each held claim's accounting that is due, on
// its own terms, whatever else the loop could not settle, such as a command
// sweep. A claim whose accounting is recorded now, or was already, goes.
func (lp *Loop) retryAccounting(ctx context.Context) {
	lp.accountingRetries.Range(func(key, value any) bool {
		r := value.(*accountingRetry)
		if lp.now().Before(r.due) {
			return true
		}
		writeCtx, cancel := context.WithTimeout(ctx, accountingWait)
		err := r.record(writeCtx)
		cancel()
		if err == nil || errors.Is(err, core.ErrConflict) || errors.Is(err, core.ErrNotFound) {
			if _, cleanup := lp.commandChecks.Load(r.token); cleanup {
				// Its command's cleanup is still owed: the sweep lets it go.
				err = lp.Core.HoldClaim(ctx, r.task, r.project, r.token, errCommandRecovery.Error())
			} else {
				err = lp.Core.ReleaseHeldClaim(ctx, r.task, r.project, r.token, accountingHold)
			}
			if err == nil {
				lp.accountingRetries.Delete(key)
				return true
			}
		}
		lp.Diagnostics.Failure(diagnostics.Event{Component: "daemon", Stage: "turn_accounting", ProjectID: r.project}, err)
		r.wait = min(2*r.wait, accountingRetryMax)
		r.due = lp.now().Add(r.wait)
		return true
	})
}

// A held claim's accounting is tried again after accountingRetryWait, then
// at doubling waits of up to accountingRetryMax.
const (
	accountingRetryWait = 30 * time.Second
	accountingRetryMax  = 10 * time.Minute
)
