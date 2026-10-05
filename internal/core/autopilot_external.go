package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ExternalAction opts into durable intent, fenced dispatch and inspection.
// Replace validates owner replacement arguments; nil explicitly refuses override.
type ExternalAction struct {
	Check     func(Snapshot, ConcreteAction) error
	Execute   func(context.Context, AutopilotAction) (ExternalResult, error)
	Reconcile func(context.Context, AutopilotAction) (ExternalResult, error)
	Replace   func(Snapshot, ConcreteAction, ConcreteAction) error
}

type ExternalResult struct {
	Outcome  string `json:"outcome"`
	Evidence string `json:"evidence"`
}

func (c *AutopilotCoordinator) RegisterExternal(kind string, adapter ExternalAction) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if kind == "" || adapter.Check == nil || adapter.Execute == nil || adapter.Reconcile == nil {
		return errors.New("external adapter requires checks, execution and reconciliation")
	}
	if _, ok := c.actions[kind]; ok {
		return ErrConflict
	}
	c.actions[kind] = LocalAction{Check: adapter.Check, external: &adapter}
	return nil
}

// executeExternal belongs only to the caller that committed the new intent.
// Callback admission precedes that commit; replay never dispatches again.
func (c *AutopilotCoordinator) executeExternal(ctx context.Context, a AutopilotAction) (AutopilotAction, error) {
	defer c.service.releaseExternal(a)
	c.mu.Lock()
	adapter := c.actions[a.Action.Kind].external
	if c.callbackContext != nil {
		ctx = c.callbackContext
	}
	allowed := false
	err := c.service.store.updateTransaction(context.WithoutCancel(ctx), func(v *Snapshot, conn *sql.Conn) error {
		current, err := readAutopilotAction(context.WithoutCancel(ctx), conn, "id", a.ID)
		if err != nil {
			return err
		}
		if current.Status != "uncertain" || !c.ownsDispatch(a, current) || !current.Dispatch.ExpiresAt.After(c.service.now()) {
			return ErrConflict
		}
		// Drain cannot revoke an already admitted callback. Recheck mutable policy,
		// target and authority, while ignoring runtime gates already passed.
		checkErr := c.checkPolicy(*v, a)
		if c.blocked {
			checkErr = errors.New("autopilot settings require reconciliation")
		}
		if checkErr != nil {
			a.Status, a.Detail, a.Dispatch = "refused", checkErr.Error(), nil
			return writeAutopilotAction(context.WithoutCancel(ctx), conn, a, a.Executor, a.ExecutorKind, c.service.now().UTC())
		}
		allowed = true
		return nil
	})
	c.mu.Unlock()
	if err != nil || !allowed {
		return a, err
	}
	stopRenew := c.renewDispatch(a)
	defer stopRenew()
	result, callbackErr := adapter.Execute(ctx, a)
	if callbackErr != nil {
		result = ExternalResult{Outcome: "uncertain", Evidence: "Completion could not be confirmed; check what happened"}
	}
	return c.externalResult(context.WithoutCancel(ctx), a, result, a.Executor, a.ExecutorKind)
}

func (c *AutopilotCoordinator) externalResult(ctx context.Context, a AutopilotAction, result ExternalResult, actor string, kind AutopilotActorKind) (AutopilotAction, error) {
	// Even a malformed adapter receipt must leave an attributable durable outcome.
	if result.Evidence == "" || len(result.Evidence) > 8192 ||
		(result.Outcome != "performed" && result.Outcome != "failed" && result.Outcome != "uncertain") {
		result = ExternalResult{Outcome: "uncertain", Evidence: "Adapter returned an invalid receipt; inspect completion"}
	}
	err := c.service.store.updateTransaction(ctx, func(_ *Snapshot, conn *sql.Conn) error {
		current, err := readAutopilotAction(ctx, conn, "id", a.ID)
		if err != nil {
			return err
		}
		owned := c.ownsDispatch(a, current)
		// An expired inspector cannot settle another inspector's live claim.
		// Only a definite executor receipt can supersede live inspection; an
		// unconfirmed receipt supplies evidence without revoking ownership.
		preserveDispatch := !owned && current.Dispatch != nil && current.Dispatch.ExpiresAt.After(c.service.now()) &&
			(a.Dispatch == nil || a.Dispatch.Purpose != "execute" || result.Outcome == "uncertain")
		switch {
		case preserveDispatch:
			if current.Status == "conflict" {
				current.Detail = conflictEvidence(current.Detail, "Late "+result.Outcome+" receipt: "+result.Evidence)
			} else {
				current.Detail = boundedEvidence(current.Detail, "Late "+result.Outcome+" receipt: "+result.Evidence)
			}
		case owned && current.Status == "conflict" && result.Outcome == "uncertain":
			current.Detail = conflictEvidence(current.Detail, "Unconfirmed inspection: "+result.Evidence)
		case owned:
			current.Status, current.Detail = result.Outcome, result.Evidence
		case current.Status == "uncertain":
			// A definite executor receipt supersedes an in-flight inspection.
			current.Status, current.Detail = result.Outcome, result.Evidence
		case result.Outcome == "uncertain":
			if current.Status == "conflict" {
				current.Detail = conflictEvidence(current.Detail, "Unconfirmed late receipt: "+result.Evidence)
			} else {
				current.Detail = boundedEvidence(current.Detail, "Unconfirmed late receipt: "+result.Evidence)
			}
		case current.Status == result.Outcome:
			current.Detail = boundedEvidence(current.Detail, "Confirmed: "+result.Evidence)
		case current.Status == "conflict":
			current.Detail = conflictEvidence(current.Detail, result.Outcome+": "+result.Evidence)
		default:
			current.Detail = boundedEvidence(current.Status+": "+current.Detail, result.Outcome+": "+result.Evidence)
			current.Status = "conflict"
		}
		if !preserveDispatch {
			current.Dispatch = nil
			if !owned {
				current.DispatchToken++
			}
		}
		if a.Dispatch != nil && a.Dispatch.Purpose == "execute" {
			current.Executor, current.ExecutorKind = a.Executor, a.ExecutorKind
		}
		a = current
		return writeAutopilotAction(ctx, conn, a, actor, kind, c.service.now().UTC())
	})
	if err != nil {
		current, readErr := c.Action(context.WithoutCancel(ctx), a.ID)
		if readErr == nil {
			return current, err
		}
		return AutopilotAction{}, errors.Join(err, readErr)
	}
	return a, nil
}

func boundedEvidence(first, second string) string {
	if len(first) > 4096 {
		first = first[:4096]
	}
	if len(second) > 4096 {
		second = second[:4096]
	}
	return first + "; " + second
}

// Keep both original bounded conflict receipts (4096 + separator + 4096).
// Later inconclusive evidence may replace earlier annotations, never receipts.
func conflictEvidence(conflict, annotation string) string {
	if len(conflict) > 8194 {
		conflict = conflict[:8194]
	}
	if len(annotation) > 4096 {
		annotation = annotation[:4096]
	}
	return conflict + "; " + annotation
}

// Reconciliation only inspects; modes and authority cannot suppress facts.
func (c *AutopilotCoordinator) Reconcile(ctx context.Context, id string, revision uint64) (AutopilotAction, error) {
	return c.reconcile(ctx, id, revision, AutopilotOwner)
}
func (c *AutopilotCoordinator) ReconcileAutomatic(ctx context.Context, id string, revision uint64) (AutopilotAction, error) {
	return c.reconcile(ctx, id, revision, AutopilotAssistant)
}

func (c *AutopilotCoordinator) reconcile(ctx context.Context, id string, revision uint64, kind AutopilotActorKind) (AutopilotAction, error) {
	c.mu.Lock()
	var a AutopilotAction
	var adapter *ExternalAction
	actor := "owner"
	admitted := false
	err := c.service.store.updateTransaction(ctx, func(_ *Snapshot, conn *sql.Conn) error {
		var err error
		a, err = readAutopilotAction(ctx, conn, "id", id)
		if err != nil {
			return err
		}
		if a.Revision != revision {
			return ErrConflict
		}
		if a.Status != "uncertain" && a.Status != "conflict" {
			return nil
		}
		if a.Status == "conflict" && kind != AutopilotOwner {
			return fmt.Errorf("%w: conflict requires owner reconciliation", ErrConflict)
		}
		if a.Dispatch != nil && a.Dispatch.ExpiresAt.After(c.service.now()) {
			return fmt.Errorf("%w: dispatch in progress", ErrConflict)
		}
		f := c.functions[a.Function]
		adapter = c.actions[a.Action.Kind].external
		if f == nil || f.version != a.RuleVersion || !f.allowed[a.Action.Kind] || adapter == nil {
			return errors.New("reconciliation implementation unavailable")
		}
		if kind == AutopilotAssistant {
			actor = a.AssistantID
		}
		if c.closed || c.blocked {
			return autopilotDeferred{ErrConflict}
		}
		if c.admission != nil {
			if err := c.admission(); err != nil {
				return autopilotDeferred{err}
			}
		}
		c.grantDispatch(&a, "reconcile", kind)
		if err := c.service.admitExternal(a); err != nil {
			return err
		}
		admitted = true
		return writeAutopilotAction(ctx, conn, a, actor, kind, c.service.now().UTC())
	})
	if c.callbackContext != nil {
		ctx = c.callbackContext
	}
	c.mu.Unlock()
	if admitted {
		defer c.service.releaseExternal(a)
	}
	if err != nil || !admitted {
		return a, err
	}
	stopRenew := c.renewDispatch(a)
	defer stopRenew()
	result, inspectErr := adapter.Reconcile(ctx, a)
	if inspectErr != nil {
		result = ExternalResult{Outcome: "uncertain", Evidence: "Completion could not be reconciled; inspect completion"}
	}
	return c.externalResult(context.WithoutCancel(ctx), a, result, actor, kind)
}
