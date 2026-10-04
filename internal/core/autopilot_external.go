package core

import (
	"context"
	"database/sql"
	"errors"
)

// ExternalAction explicitly opts into the persist-intent/reconcile contract.
// Only the operator function may bind it. CA-101 supplies actual host/publishing
// implementations; this framework has no command executor or publishing tool.
// Execute runs once after checked intent commits. Reconcile must only inspect
// evidence, never repeat the effect. An unavailable implementation cannot run.
type ExternalAction struct {
	Check     func(Snapshot, ConcreteAction) error
	Execute   func(context.Context, AutopilotAction) (ExternalResult, error)
	Reconcile func(context.Context, AutopilotAction) (ExternalResult, error)
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

// executeExternal is called only by the caller that committed a new intent.
// A replay sees the durable uncertain record and never calls Execute again.
// It runs under c.mu, ordering mode saves and shutdown against this admission.
func (c *AutopilotCoordinator) executeExternal(ctx context.Context, a AutopilotAction) (AutopilotAction, error) {
	adapter := c.actions[a.Action.Kind].external
	if adapter == nil {
		return a, errors.New("external action unavailable")
	}
	// Recheck target, gate and runtime after intent persistence, before dispatch.
	allowed := false
	err := c.service.store.updateTransaction(ctx, func(v *Snapshot, conn *sql.Conn) error {
		current, err := readAutopilotAction(ctx, conn, "id", a.ID)
		if err != nil {
			return err
		}
		if current.Status != "uncertain" {
			return ErrConflict
		}
		if err := c.check(*v, a); err != nil {
			a.Status, a.Detail = "refused", err.Error()
			return writeAutopilotAction(ctx, conn, a, a.Executor, a.ExecutorKind, c.service.now().UTC())
		}
		allowed = true
		return nil
	})
	if err != nil {
		return a, err
	}
	if !allowed {
		return a, nil
	}
	result, err := adapter.Execute(ctx, a)
	if err != nil {
		// Arbitrary adapter error text can contain host secrets. Keep only the
		// conservative outcome; reconciliation supplies safe, attributable evidence.
		result = ExternalResult{Outcome: "uncertain", Evidence: "Completion could not be confirmed; check what happened"}
	}
	return c.externalResult(context.WithoutCancel(ctx), a, result, a.Executor, a.ExecutorKind)
}

func (c *AutopilotCoordinator) externalResult(ctx context.Context, a AutopilotAction, result ExternalResult, actor string, kind AutopilotActorKind) (AutopilotAction, error) {
	if result.Evidence == "" || len(result.Evidence) > 8192 {
		return a, errors.New("bounded reconciliation evidence is required")
	}
	if result.Outcome != "performed" && result.Outcome != "failed" && result.Outcome != "uncertain" {
		return a, errors.New("invalid external outcome")
	}
	err := c.service.store.updateTransaction(ctx, func(_ *Snapshot, conn *sql.Conn) error {
		current, err := readAutopilotAction(ctx, conn, "id", a.ID)
		if err != nil {
			return err
		}
		if current.Status != "uncertain" || current.Revision != a.Revision {
			return ErrConflict
		}
		a.Status, a.Detail = result.Outcome, result.Evidence
		return writeAutopilotAction(ctx, conn, a, actor, kind, c.service.now().UTC())
	})
	if err != nil {
		// Never return a successful receipt when its persistence failed. Rereading
		// resolves a lost commit acknowledgement without re-executing the effect.
		current, readErr := c.Action(context.WithoutCancel(ctx), a.ID)
		if readErr == nil {
			return current, err
		}
		return AutopilotAction{}, errors.Join(err, readErr)
	}
	return a, nil
}

// Reconcile records inspected evidence, even after modes/gates are revoked.
// Revocation prevents execution, not recording what already happened.
func (c *AutopilotCoordinator) Reconcile(ctx context.Context, id string, revision uint64) (AutopilotAction, error) {
	return c.reconcile(ctx, id, revision, AutopilotOwner)
}

// ReconcileAutomatic attributes inspected recovery evidence to the assistant,
// without implying owner approval or granting execution authority.
func (c *AutopilotCoordinator) ReconcileAutomatic(ctx context.Context, id string, revision uint64) (AutopilotAction, error) {
	return c.reconcile(ctx, id, revision, AutopilotAssistant)
}

func (c *AutopilotCoordinator) reconcile(ctx context.Context, id string, revision uint64, kind AutopilotActorKind) (AutopilotAction, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	a, err := c.Action(ctx, id)
	if err != nil {
		return a, err
	}
	if a.Revision != revision {
		return a, ErrConflict
	}
	if a.Status != "uncertain" {
		return a, nil
	}
	f := c.functions[a.Function]
	adapter := c.actions[a.Action.Kind].external
	if f == nil || f.version != a.RuleVersion || !f.allowed[a.Action.Kind] || adapter == nil {
		return a, errors.New("reconciliation implementation unavailable")
	}
	result, err := adapter.Reconcile(ctx, a)
	if err != nil {
		return a, errors.New("completion could not be reconciled")
	}
	actor := "owner"
	if kind == AutopilotAssistant {
		actor = a.AssistantID
	}
	return c.externalResult(ctx, a, result, actor, kind)
}
