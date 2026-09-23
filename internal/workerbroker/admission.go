package workerbroker

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/shhac/crew-assistant/internal/engine"
	"github.com/shhac/crew-assistant/internal/integrations/worker"
)

// reserveWorkerModelCall is the one place a worker's model work is authorized.
// It runs immediately before a turn is handed to the coding session, so a
// refusal here spends nothing and a failure earlier adds no unknown usage.
//
// Order matters: the local budget is checked before the account is inspected,
// so a run that already has to stop does not cost an inspection; and both
// checks complete before any pending accounting is written, so a refused call
// never leaves a reservation behind.
func (b *Broker) reserveWorkerModelCall(ctx context.Context, id, stage string, request *string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := b.snapshot(id)
	if err != nil {
		return err
	}
	budget := b.tokenBudget()
	// An unresolved reservation means the previous request's consumption was
	// never established — it may still be in flight, or its settlement may have
	// failed to persist. Authorizing another one would build on a ledger we
	// cannot vouch for, so this holds whether or not a budget is configured.
	if hold := unresolvedHold(current); hold != nil {
		return b.refuse(id, *hold)
	}
	if hold := budgetHold(current, budget); hold != nil {
		return b.refuse(id, *hold)
	}
	if b.cfg.Admit != nil {
		if admitErr := b.cfg.Admit(ctx); admitErr != nil {
			var held *worker.HoldError
			if errors.As(admitErr, &held) {
				return b.refuse(id, held.Hold)
			}
			return admitErr
		}
	}
	reservation := uid()
	if err = b.update(id, func(run *storedRun) error {
		if run.Run.Status != "running" || run.PendingStatus == "paused" || run.PendingStatus == "cancelled" {
			return errInterrupted
		}
		// Re-check under the lock: an owner budget change or a settled call may
		// have landed between the snapshot and here.
		if hold := unresolvedHold(*run); hold != nil {
			return &worker.HoldError{Hold: *hold}
		}
		if hold := budgetHold(*run, budget); hold != nil {
			return &worker.HoldError{Hold: *hold}
		}
		run.ModelCalls++
		run.UsageLedger = true
		run.Run.ResourceHold = nil
		run.PendingUsage = &pendingUsage{RequestID: reservation, Stage: stage, StartedAt: now()}
		publishUsage(run, budget)
		run.Run.UpdatedAt = now()
		return nil
	}); err != nil {
		var held *worker.HoldError
		if errors.As(err, &held) {
			return b.refuse(id, held.Hold)
		}
		return err
	}
	*request = reservation
	return nil
}

// refuse records the hold before returning, because completion transports wrap
// admission errors in an opaque type the caller cannot unwrap. The execution
// loop only needs to recognize that this was a hold, not carry its details.
func (b *Broker) refuse(id string, hold worker.ResourceHold) error {
	b.holdOnResources(id, hold)
	return &worker.HoldError{Hold: hold}
}

// holdOnResources preserves the run exactly as it stands. No request was made
// and no response was received, so the transcript, any queued owner message and
// any unresolved operation stay untouched for the next admitted attempt. Owner
// pause and stop still win.
func (b *Broker) holdOnResources(id string, hold worker.ResourceHold) {
	_ = b.update(id, func(r *storedRun) error {
		if r.Run.Status == "cancelled" || r.PendingStatus == "cancelled" || r.PendingStatus == "paused" {
			return nil
		}
		held := hold
		r.Run.ResourceHold = &held
		// A hold is not a provider rejection: it schedules no retry, spends no
		// recovery allowance and leaves the failure classification alone.
		r.Run.RetryAt = time.Time{}
		r.PendingStatus = "usage_wait"
		r.PendingSummary = hold.Reason
		r.Run.Summary = "Finalizing isolated execution and collecting evidence"
		r.Run.UpdatedAt = now()
		return nil
	})
}

func (b *Broker) tokenBudget() int64 {
	if b.cfg.TokenBudget == nil {
		return 0
	}
	budget := b.cfg.TokenBudget()
	if budget < 0 {
		return 0
	}
	return budget
}

// budgetHold answers whether the next request may be made under the configured
// token budget. Zero disables it entirely, which is the only way past a run
// whose consumption cannot be established: raising a budget cannot retroactively
// measure what a provider never reported.
func budgetHold(run storedRun, budget int64) *worker.ResourceHold {
	if budget <= 0 {
		return nil
	}
	if run.UsageUnknownCalls > 0 {
		return &worker.ResourceHold{Kind: worker.HoldUsageUnknown, OwnerAction: true, Reason: fmt.Sprintf("Token budget enforcement stopped: %d earlier model call(s) reported no usage, so this worker's consumption cannot be established. Raising the budget cannot measure them. Decide explicitly whether to continue with the budget disabled (limits.worker_token_budget = 0) and resume.", run.UsageUnknownCalls)}
	}
	if used := run.UsageInputTokens + run.UsageOutputTokens; used >= budget {
		return &worker.ResourceHold{Kind: worker.HoldTokenBudget, OwnerAction: true, Reason: fmt.Sprintf("Worker token budget reached: %d of %d tokens used. Raise limits.worker_token_budget, or set it to 0, then resume this assignment explicitly to continue with its saved context.", used, budget)}
	}
	return nil
}

// unresolvedHold refuses further work while a reservation is still open. The
// owner has to look, because the alternative is continuing to spend against a
// record that is known to be incomplete.
func unresolvedHold(run storedRun) *worker.ResourceHold {
	if run.PendingUsage == nil {
		return nil
	}
	return &worker.ResourceHold{Kind: worker.HoldUsageUnknown, OwnerAction: true, Reason: "A previous model request's consumption was never recorded, so this worker's usage cannot be established. Inspect the preserved work and the assistant's diagnostics, then resume explicitly to continue; the unrecorded request is counted as unknown consumption."}
}

// settleUsage closes one reservation. Matching by request ID means a repeated
// or late settlement changes nothing, and a reservation belonging to an earlier
// process is left for restart reconciliation to convert into uncertainty.
//
// Its error must reach the caller: a settlement that did not persist leaves
// consumption unrecorded, and continuing to execute proposals on top of that
// would spend against a ledger the broker cannot vouch for.
func (b *Broker) settleUsage(id, request string, usage engine.Usage) error {
	if request == "" {
		return nil
	}
	budget := b.tokenBudget()
	return b.update(id, func(run *storedRun) error {
		if run.PendingUsage == nil || run.PendingUsage.RequestID != request {
			return nil
		}
		run.PendingUsage = nil
		if input, output, ok := countable(usage); ok && addable(run, input, output) {
			run.UsageInputTokens += input
			run.UsageOutputTokens += output
		} else {
			// A figure that is absent, negative or too large to add is not a
			// measurement. Recording it as unknown keeps the total honest instead
			// of repairing it into something indistinguishable from a real one.
			run.UsageUnknownCalls++
		}
		publishUsage(run, budget)
		return nil
	})
}

// countable accepts only a complete, non-negative report.
func countable(usage engine.Usage) (input, output int64, ok bool) {
	if !usage.Known || usage.InputTokens < 0 || usage.OutputTokens < 0 {
		return 0, 0, false
	}
	return int64(usage.InputTokens), int64(usage.OutputTokens), true
}

// addable rejects a total that would wrap. Each column has to stay
// representable, and so does their sum: the budget and everything shown to the
// owner add them together, so two individually-valid columns that overflow
// combined are just as unusable. An assignment whose accounting has grown past
// what can be represented is unknown, not zero and not negative.
func addable(run *storedRun, input, output int64) bool {
	if input > math.MaxInt64-run.UsageInputTokens || output > math.MaxInt64-run.UsageOutputTokens {
		return false
	}
	return run.UsageInputTokens+input <= math.MaxInt64-(run.UsageOutputTokens+output)
}

// publishUsage keeps the reported ledger identical to the persisted one. The
// budget travels with it so the owner sees the limit their worker is measured
// against, not just a number of tokens.
func publishUsage(run *storedRun, budget int64) {
	run.Run.Usage = worker.Usage{InputTokens: run.UsageInputTokens, OutputTokens: run.UsageOutputTokens, UnknownCalls: run.UsageUnknownCalls, TokenBudget: budget}
}

// reconcileUsage runs once per run at broker startup. A surviving reservation
// may have been billed, and calls recorded before this ledger existed were
// never measured at all; both become explicit unknown consumption rather than
// disappearing into an apparently free history.
func reconcileUsage(run *storedRun, budget int64) {
	if !run.UsageLedger {
		run.UsageLedger = true
		run.UsageUnknownCalls += run.ModelCalls
	}
	if run.PendingUsage != nil {
		run.PendingUsage = nil
		run.UsageUnknownCalls++
	}
	publishUsage(run, budget)
}
