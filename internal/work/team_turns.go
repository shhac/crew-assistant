package work

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/roles"
	"github.com/shhac/lib-agent-harness/session"
)

type teamAccountingFailure struct {
	run, accounting error
	result          roles.Result
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
		observed := core.TeamTurnOpening{At: at, Resumed: o.Resumed, FreshReason: reason, SessionID: ref.ID}
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
	accountCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), accountingWait)
	defer cancel()
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = nil
		if actualOpening != nil {
			err = lp.Core.OpenTeamTurn(accountCtx, id, *actualOpening)
		}
		if err == nil && actualAccepted != nil {
			err = lp.Core.AcceptTeamTurn(accountCtx, id, *actualAccepted)
		}
		if err == nil {
			finish := lp.finishTeamTurn
			if finish == nil {
				finish = lp.Core.FinishTeamTurn
			}
			err = finish(accountCtx, id, terminal)
		}
		if err == nil {
			return result, runErr
		}
		if errors.Is(err, core.ErrConflict) || errors.Is(err, core.ErrNotFound) {
			break
		}
		select {
		case <-accountCtx.Done():
			return result, accountingFailed(ctx, result, runErr, err)
		case <-time.After(time.Duration(attempt+1) * 50 * time.Millisecond):
		}
	}
	// runRole recognizes this separate accounting failure and stops immediate
	// fallback even when the runner also reported a browser refusal.
	return result, accountingFailed(ctx, result, runErr, err)
}

// Retain the result and signal the scheduler even when a caller handles
// the error locally, as PM, message and release paths may do.
func accountingFailed(ctx context.Context, result roles.Result, runErr, writeErr error) error {
	failure := &teamAccountingFailure{run: runErr, accounting: writeErr, result: result}
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
// accounting could not be recorded; startup releases it.
const accountingHold = "Held: terminal team turn accounting could not be recorded"
