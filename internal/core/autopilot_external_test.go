package core

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/autopilot"
)

func TestAutopilotExternalIntentReplayAndReconciliation(t *testing.T) {
	s, cfg := fixture(t)
	p := newProject(t, s)
	p, err := s.SetOperatorPermission(testContext, p.ID, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Autopilot.Modes = map[string]autopilot.Mode{autopilot.Operator: autopilot.Act}
	s.UpdateConfig(cfg)
	c := NewAutopilotCoordinator(s, nil)
	calls := 0
	adapter := ExternalAction{
		Check: func(Snapshot, ConcreteAction) error { return nil },
		Execute: func(ctx context.Context, a AutopilotAction) (ExternalResult, error) {
			durable, err := c.Action(ctx, a.ID)
			if err != nil || durable.Status != "uncertain" {
				t.Fatal("effect started before durable intent")
			}
			calls++
			return ExternalResult{}, errors.New("synthetic secret must not be recorded")
		},
		Reconcile: func(context.Context, AutopilotAction) (ExternalResult, error) {
			return ExternalResult{Outcome: "performed", Evidence: "Synthetic exact target was found"}, nil
		},
	}
	if err := c.RegisterExternal("synthetic-publication", adapter); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Register("ci-failures", "v1", []string{"synthetic-publication"}, func(Snapshot, ConcreteAction) error { return nil }); err == nil {
		t.Fatal("nonoperator gained outward authority")
	}
	f, err := c.Register(autopilot.Operator, "v1", []string{"synthetic-publication"}, func(Snapshot, ConcreteAction) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	action := ConcreteAction{Kind: "synthetic-publication", ProjectID: p.ID, PermissionRevision: p.OperatorPermission.Revision, Args: json.RawMessage(`{}`)}
	a, err := f.Submit(testContext, "source", "synthetic intent", action)
	if err != nil || a.Status != "uncertain" || calls != 1 {
		t.Fatalf("intent: %+v %v calls=%d", a, err, calls)
	}
	replay, err := f.Submit(testContext, "source", "synthetic intent", action)
	if err != nil || replay.Status != "uncertain" || calls != 1 {
		t.Fatal("uncertain effect repeated")
	}
	// Revoking authority prevents new execution but must not suppress facts.
	if _, err := s.SetOperatorPermission(testContext, p.ID, false, 1); err != nil {
		t.Fatal(err)
	}
	completed, err := c.ReconcileAutomatic(testContext, a.ID, 1)
	if err != nil || completed.Status != "performed" || calls != 1 {
		t.Fatal("reconciliation repeated effect or lost evidence")
	}
	history, err := c.History(testContext, AutopilotHistoryQuery{})
	if err != nil || history.Entries[0].ActorKind != AutopilotAssistant || history.Entries[0].Actor != a.AssistantID {
		t.Fatalf("automatic recovery attributed to owner: %+v %v", history, err)
	}
	if _, err := c.OwnerAction(testContext, a.ID, 1, "undo", nil); err == nil {
		t.Fatal("unsupported external undo accepted")
	}
}

func TestAutopilotExternalOutcomeFailureNeverRepeatsEffect(t *testing.T) {
	s, cfg := fixture(t)
	p := newProject(t, s)
	p, err := s.SetOperatorPermission(testContext, p.ID, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Autopilot.Modes = map[string]autopilot.Mode{autopilot.Operator: autopilot.Act}
	s.UpdateConfig(cfg)
	c := NewAutopilotCoordinator(s, nil)
	calls := 0
	c.RegisterExternal("fake-effect", ExternalAction{Check: func(Snapshot, ConcreteAction) error { return nil }, Execute: func(context.Context, AutopilotAction) (ExternalResult, error) {
		calls++
		_, err := s.store.db.Exec(`CREATE TRIGGER fail_external_receipt BEFORE INSERT ON autopilot_audit BEGIN SELECT RAISE(ABORT,'injected'); END`)
		if err != nil {
			t.Fatal(err)
		}
		return ExternalResult{Outcome: "performed", Evidence: "Synthetic effect completed"}, nil
	}, Reconcile: func(context.Context, AutopilotAction) (ExternalResult, error) {
		return ExternalResult{Outcome: "performed", Evidence: "Synthetic inspection confirmed effect"}, nil
	}})
	f, err := c.Register(autopilot.Operator, "v1", []string{"fake-effect"}, func(Snapshot, ConcreteAction) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	action := ConcreteAction{Kind: "fake-effect", ProjectID: p.ID, PermissionRevision: p.OperatorPermission.Revision, Args: json.RawMessage(`{}`)}
	a, err := f.Submit(testContext, "source", "reason", action)
	if err == nil || a.Status != "uncertain" {
		t.Fatal("false completion after receipt failure")
	}
	_, err = s.store.db.Exec("DROP TRIGGER fail_external_receipt")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.Submit(testContext, "source", "reason", action)
	if err != nil || replay.Status != "uncertain" || calls != 1 {
		t.Fatal("lost-response retry repeated publication")
	}
	at := s.now().Add(2 * time.Minute)
	s.now = func() time.Time { return at }
	reconciled, err := c.Reconcile(testContext, a.ID, 1)
	if err != nil || reconciled.Status != "performed" || calls != 1 {
		t.Fatal("reconciliation failed")
	}
	history, err := c.History(testContext, AutopilotHistoryQuery{})
	if err != nil || history.Entries[0].ActorKind != AutopilotOwner || history.Entries[0].Actor != "owner" {
		t.Fatalf("owner reconciliation attribution: %+v %v", history, err)
	}
}
