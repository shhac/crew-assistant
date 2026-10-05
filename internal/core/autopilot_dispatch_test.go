package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/autopilot"
	"github.com/shhac/crew-assistant/internal/config"
)

func externalFixture(t *testing.T, mode autopilot.Mode, adapter ExternalAction) (*Service, *AutopilotCoordinator, *AutopilotFunction, ConcreteAction, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	cfg := config.Default()
	cfg.Autopilot.Modes = map[string]autopilot.Mode{autopilot.Operator: mode}
	s := NewService(store, cfg)
	p := newProject(t, s)
	p, err = s.SetOperatorPermission(testContext, p.ID, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	c, f := externalCoordinator(t, s, adapter)
	return s, c, f, ConcreteAction{Kind: "synthetic", ProjectID: p.ID, PermissionRevision: p.OperatorPermission.Revision, Args: json.RawMessage("{}")}, path
}
func externalCoordinator(t *testing.T, s *Service, adapter ExternalAction) (*AutopilotCoordinator, *AutopilotFunction) {
	t.Helper()
	c := NewAutopilotCoordinator(s, nil)
	if err := c.RegisterExternal("synthetic", adapter); err != nil {
		t.Fatal(err)
	}
	f, err := c.Register(autopilot.Operator, "v1", []string{"synthetic"}, func(Snapshot, ConcreteAction) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return c, f
}
func secondExternal(t *testing.T, s *Service, path string, adapter ExternalAction) (*Service, *AutopilotCoordinator) {
	t.Helper()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	other := NewService(store, s.configuration())
	c, _ := externalCoordinator(t, other, adapter)
	return other, c
}
func awaitExternal[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("external operation blocked")
		var zero T
		return zero
	}
}

func TestAutopilotDispatchLeaseIndependentCoordinators(t *testing.T) {
	entered, release := make(chan AutopilotAction, 1), make(chan struct{})
	var inspections atomic.Int32
	adapter := ExternalAction{Check: func(Snapshot, ConcreteAction) error { return nil },
		Execute: func(ctx context.Context, a AutopilotAction) (ExternalResult, error) {
			entered <- a
			<-release
			return ExternalResult{"performed", "executor receipt"}, nil
		},
		Reconcile: func(context.Context, AutopilotAction) (ExternalResult, error) {
			inspections.Add(1)
			return ExternalResult{"failed", "inspection"}, nil
		}}
	s, c, f, action, path := externalFixture(t, autopilot.Act, adapter)
	_, other := secondExternal(t, s, path, adapter)
	done := make(chan error, 1)
	go func() { _, err := f.Submit(testContext, "lease", "reason", action); done <- err }()
	a := awaitExternal(t, entered)
	if a.Dispatch == nil || a.Dispatch.Owner != c.instance || a.Dispatch.Token == 0 {
		t.Fatal("missing durable fence")
	}
	durable, _ := other.Action(testContext, a.ID)
	if durable.Dispatch == nil {
		t.Fatal("intent not durable")
	}
	before, _ := other.History(testContext, AutopilotHistoryQuery{})
	for _, reconcile := range []func(context.Context, string, uint64) (AutopilotAction, error){other.Reconcile, other.ReconcileAutomatic} {
		if _, err := reconcile(testContext, a.ID, 1); !errors.Is(err, ErrConflict) {
			t.Fatalf("live lease reconciled: %v", err)
		}
	}
	after, _ := other.History(testContext, AutopilotHistoryQuery{})
	if inspections.Load() != 0 || len(after.Entries) != len(before.Entries) {
		t.Fatal("live dispatch inspected or mutated")
	}
	close(release)
	if err := awaitExternal(t, done); err != nil {
		t.Fatal(err)
	}
	durable, _ = other.Action(testContext, a.ID)
	if durable.Status != "performed" || durable.Dispatch != nil {
		t.Fatal(durable)
	}
}

func TestAutopilotReconcileAfterLeaseExpiry(t *testing.T) {
	for _, kind := range []AutopilotActorKind{AutopilotOwner, AutopilotAssistant} {
		for _, outcome := range []string{"performed", "failed", "uncertain"} {
			t.Run(string(kind)+"/"+outcome, func(t *testing.T) {
				entered, release := make(chan AutopilotAction, 1), make(chan struct{})
				var at atomic.Int64
				at.Store(time.Now().UnixNano())
				clock := func() time.Time { return time.Unix(0, at.Load()) }
				adapter := ExternalAction{Check: func(Snapshot, ConcreteAction) error { return nil },
					Execute: func(context.Context, AutopilotAction) (ExternalResult, error) {
						<-release
						return ExternalResult{outcome, "executor evidence"}, nil
					},
					Reconcile: func(context.Context, AutopilotAction) (ExternalResult, error) {
						return ExternalResult{"failed", "inspection evidence"}, nil
					}}
				s, c, f, action, path := externalFixture(t, autopilot.Act, adapter)
				s.now = clock
				c.renewEvery = 0
				otherService, other := secondExternal(t, s, path, adapter)
				otherService.now = clock
				// Observe admission without depending on a callback-specific side effect.
				adapter.Execute = func(_ context.Context, a AutopilotAction) (ExternalResult, error) {
					entered <- a
					<-release
					return ExternalResult{outcome, "executor evidence"}, nil
				}
				c.actions["synthetic"] = LocalAction{Check: adapter.Check, external: &adapter}
				done := make(chan error, 1)
				go func() { _, err := f.Submit(testContext, "expired", "reason", action); done <- err }()
				a := awaitExternal(t, entered)
				at.Add(int64(2 * time.Minute))
				var reconciled AutopilotAction
				var err error
				if kind == AutopilotOwner {
					reconciled, err = other.Reconcile(testContext, a.ID, 1)
				} else {
					reconciled, err = other.ReconcileAutomatic(testContext, a.ID, 1)
				}
				if err != nil || reconciled.Status != "failed" {
					t.Fatal(reconciled, err)
				}
				close(release)
				if err := awaitExternal(t, done); err != nil {
					t.Fatal(err)
				}
				got, _ := other.Action(testContext, a.ID)
				want := "failed"
				if outcome == "performed" {
					want = "conflict"
				}
				if got.Status != want || !strings.Contains(got.Detail, "executor evidence") || !strings.Contains(got.Detail, "inspection evidence") {
					t.Fatal(got)
				}
				history, _ := other.History(testContext, AutopilotHistoryQuery{})
				if history.Entries[1].ActorKind != kind {
					t.Fatal("reconciliation attribution lost")
				}
				if want == "conflict" {
					if _, err := other.ReconcileAutomatic(testContext, a.ID, 1); !errors.Is(err, ErrConflict) {
						t.Fatal("autonomous reconciliation settled conflict", err)
					}
					adapter.Reconcile = func(context.Context, AutopilotAction) (ExternalResult, error) {
						return ExternalResult{}, errors.New("inspection unavailable")
					}
					other.actions["synthetic"] = LocalAction{Check: adapter.Check, external: &adapter}
					unconfirmed, err := other.Reconcile(testContext, a.ID, 1)
					if err != nil || unconfirmed.Status != "conflict" || !strings.Contains(unconfirmed.Detail, "executor evidence") || !strings.Contains(unconfirmed.Detail, "inspection evidence") {
						t.Fatal(unconfirmed, err)
					}
					adapter.Reconcile = func(context.Context, AutopilotAction) (ExternalResult, error) {
						return ExternalResult{"failed", "found absent"}, nil
					}
					summary, err := other.Summary(testContext, SummaryQuery{})
					if err != nil || summary.Counts["failed"] != 1 || summary.Counts["needs_owner"] != 1 {
						t.Fatal(summary, err)
					}
					settled, err := other.Reconcile(testContext, a.ID, 1)
					if err != nil || settled.Status != "failed" {
						t.Fatal("conflict cannot settle", settled, err)
					}
				}
			})
		}
	}
}

func TestAutopilotCallbackDoesNotHoldCoordinatorLock(t *testing.T) {
	entered, release := make(chan AutopilotAction, 1), make(chan struct{})
	adapter := ExternalAction{Check: func(Snapshot, ConcreteAction) error { return nil },
		Execute: func(_ context.Context, a AutopilotAction) (ExternalResult, error) {
			entered <- a
			<-release
			return ExternalResult{"performed", "finished"}, nil
		},
		Reconcile: func(context.Context, AutopilotAction) (ExternalResult, error) {
			return ExternalResult{"performed", "found"}, nil
		}}
	s, c, f, action, _ := externalFixture(t, autopilot.Suggest, adapter)
	local, err := c.Register("authorised-research", "v1", []string{"rename-project"}, func(Snapshot, ConcreteAction) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	proposals := []AutopilotAction{}
	for i := 0; i < 3; i++ {
		p := newProject(t, s)
		a, err := local.Submit(testContext, string(rune('a'+i)), "reason", renameProposal(p, "suggested"))
		if err != nil {
			t.Fatal(err)
		}
		proposals = append(proposals, a)
	}
	a, err := f.Submit(testContext, "blocked", "reason", action)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := c.OwnerAction(testContext, a.ID, 1, "approve", nil); done <- err }()
	awaitExternal(t, entered)
	ops := make(chan error, 1)
	go func() {
		c.Catalog()
		if err := c.SerializeSettings(func() error {
			cfg := s.configuration()
			cfg.Autopilot.Modes = map[string]autopilot.Mode{autopilot.Operator: autopilot.Suggest, "authorised-research": autopilot.Act}
			return s.UpdateConfig(cfg)
		}); err != nil {
			ops <- err
			return
		}
		for i, verb := range []string{"approve", "cancel", "override"} {
			var replacement *ConcreteAction
			if verb == "override" {
				copy := proposals[i].Action
				copy.Args = json.RawMessage(`{"title":"replacement"}`)
				replacement = &copy
			}
			if _, err := c.OwnerAction(testContext, proposals[i].ID, 1, verb, replacement); err != nil {
				ops <- err
				return
			}
		}
		if _, err := c.Pending(testContext, "", 50); err != nil {
			ops <- err
			return
		}
		_, err := c.History(testContext, AutopilotHistoryQuery{})
		ops <- err
	}()
	if err := awaitExternal(t, ops); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := awaitExternal(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestAutopilotExternalReplacement(t *testing.T) {
	for _, variant := range []string{"accepted", "refused", "unsupported", "stale-authority", "revoked", "changed-target", "stale-mode"} {
		t.Run(variant, func(t *testing.T) {
			var effects atomic.Int32
			var executed ConcreteAction
			adapter := ExternalAction{Check: func(Snapshot, ConcreteAction) error { return nil },
				Replace: func(Snapshot, ConcreteAction, ConcreteAction) error { return nil },
				Execute: func(_ context.Context, a AutopilotAction) (ExternalResult, error) {
					effects.Add(1)
					executed = a.Action
					return ExternalResult{"performed", "finished"}, nil
				},
				Reconcile: func(context.Context, AutopilotAction) (ExternalResult, error) {
					return ExternalResult{"performed", "found"}, nil
				}}
			if variant == "refused" {
				adapter.Replace = func(Snapshot, ConcreteAction, ConcreteAction) error { return errors.New("invalid replacement") }
			}
			if variant == "unsupported" {
				adapter.Replace = nil
			}
			s, c, f, action, _ := externalFixture(t, autopilot.Suggest, adapter)
			original, err := f.Submit(testContext, "proposal", "reason", action)
			if err != nil {
				t.Fatal(err)
			}
			replacement := action
			replacement.Args = json.RawMessage(`{"replacement":true}`)
			switch variant {
			case "stale-authority":
				replacement.PermissionRevision = 0
			case "revoked":
				_, err = s.SetOperatorPermission(testContext, action.ProjectID, false, 1)
				if err != nil {
					t.Fatal(err)
				}
			case "changed-target":
				replacement.TaskID = "other"
			case "stale-mode":
				cfg := s.configuration()
				cfg.Autopilot.Modes[autopilot.Operator] = autopilot.Act
				s.UpdateConfig(cfg)
				got, err := c.OwnerAction(testContext, original.ID, 1, "approve", nil)
				if err != nil || got.Status != "refused" || effects.Load() != 0 {
					t.Fatal(got, err)
				}
				return
			}
			got, err := c.OwnerAction(testContext, original.ID, 1, "override", &replacement)
			if variant != "accepted" {
				if err == nil || effects.Load() != 0 {
					t.Fatal("invalid replacement dispatched", got, err)
				}
				current, _ := c.Action(testContext, original.ID)
				if current.Status != "proposed" {
					t.Fatal("original lost")
				}
				if variant == "refused" || variant == "unsupported" || variant == "stale-authority" || variant == "revoked" {
					if got.Status != "failed" || got.Approver != "owner" || got.Replaces != "" {
						t.Fatal("failed attempt not attributed", got)
					}
				}
				if variant == "unsupported" && !strings.Contains(err.Error(), "does not support owner replacement") {
					t.Fatal("unclear refusal", err)
				}
				if variant != "revoked" {
					approved, err := c.OwnerAction(testContext, original.ID, 1, "approve", nil)
					if err != nil || approved.Status != "performed" || effects.Load() != 1 {
						t.Fatal("failed replacement made original unapprovable", approved, err)
					}
				}
				return
			}
			if err != nil || got.Status != "performed" || got.Replaces != original.ID || got.Revision != 2 || got.Executor != "owner" || string(executed.Args) != string(replacement.Args) {
				t.Fatal(got, err)
			}
			old, _ := c.Action(testContext, original.ID)
			if old.Status != "cancelled" || old.ReplacedBy != got.ID {
				t.Fatal(old)
			}
			if _, err := c.OwnerAction(testContext, original.ID, 1, "approve", nil); !errors.Is(err, ErrConflict) {
				t.Fatal("stale approval accepted", err)
			}
			replay, err := c.OwnerAction(testContext, original.ID, 1, "override", &replacement)
			if err != nil || replay.ID != got.ID || effects.Load() != 1 {
				t.Fatal("replacement replay dispatched", replay, err)
			}
		})
	}
}

func TestAutopilotExternalConcurrentOwnerActions(t *testing.T) {
	for _, verbs := range [][]string{{"approve", "override"}, {"cancel", "override"}, {"approve", "cancel", "override"}} {
		t.Run(strings.Join(verbs, "-"), func(t *testing.T) {
			var effects atomic.Int32
			adapter := ExternalAction{Check: func(Snapshot, ConcreteAction) error { return nil }, Replace: func(Snapshot, ConcreteAction, ConcreteAction) error { return nil },
				Execute: func(context.Context, AutopilotAction) (ExternalResult, error) {
					effects.Add(1)
					return ExternalResult{"performed", "finished"}, nil
				},
				Reconcile: func(context.Context, AutopilotAction) (ExternalResult, error) {
					return ExternalResult{"performed", "found"}, nil
				}}
			s, c, f, action, path := externalFixture(t, autopilot.Suggest, adapter)
			_, other := secondExternal(t, s, path, adapter)
			a, err := f.Submit(testContext, "race", "reason", action)
			if err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i, verb := range verbs {
				wg.Add(1)
				go func(i int, verb string) {
					defer wg.Done()
					<-start
					coordinator := c
					if i%2 == 1 {
						coordinator = other
					}
					var replacement *ConcreteAction
					if verb == "override" {
						copy := action
						copy.Args = json.RawMessage(`{"replacement":true}`)
						replacement = &copy
					}
					_, err := coordinator.OwnerAction(testContext, a.ID, 1, verb, replacement)
					if err != nil && !errors.Is(err, ErrConflict) {
						t.Error(err)
					}
				}(i, verb)
			}
			close(start)
			wg.Wait()
			if effects.Load() > 1 {
				t.Fatal("duplicate effect")
			}
			current, err := c.Action(testContext, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case current.ReplacedBy != "":
				replaced, err := c.Action(testContext, current.ReplacedBy)
				if err != nil || current.Status != "cancelled" || replaced.Status != "performed" || replaced.Replaces != current.ID || effects.Load() != 1 {
					t.Fatal(current, replaced, err)
				}
			case current.Status == "performed":
				if effects.Load() != 1 {
					t.Fatal("approval effect lost")
				}
			case current.Status == "cancelled":
				if effects.Load() != 0 {
					t.Fatal("cancelled action executed")
				}
			default:
				t.Fatal("no verb won", current)
			}
		})
	}
}

func TestAutopilotExternalDrainAndUpgradeAdmission(t *testing.T) {
	entered, release := make(chan AutopilotAction, 1), make(chan struct{})
	adapter := ExternalAction{Check: func(Snapshot, ConcreteAction) error { return nil },
		Execute: func(_ context.Context, a AutopilotAction) (ExternalResult, error) {
			entered <- a
			<-release
			return ExternalResult{"performed", "finished"}, nil
		},
		Reconcile: func(context.Context, AutopilotAction) (ExternalResult, error) {
			return ExternalResult{"performed", "found"}, nil
		}}
	s, c, f, action, _ := externalFixture(t, autopilot.Suggest, adapter)
	first, err := f.Submit(testContext, "first", "reason", action)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.Submit(testContext, "second", "reason", action)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := c.OwnerAction(testContext, first.ID, 1, "approve", nil); done <- err }()
	awaitExternal(t, entered)
	admitted, err := s.AdmitUpgrade(testContext, func(v Snapshot, _ config.Config) (bool, error) {
		if !UpgradeWorkClaimed(v) {
			t.Error("callback not counted")
		}
		return true, nil
	})
	if err != nil || !admitted {
		t.Fatal(admitted, err)
	}
	if _, err := c.OwnerAction(testContext, second.ID, 1, "approve", nil); err == nil {
		t.Fatal("draining admitted")
	}
	pending, _ := c.Action(testContext, second.ID)
	if pending.Status != "proposed" {
		t.Fatal("draining consumed suggestion", pending)
	}
	drained := make(chan struct{})
	go func() { s.DrainExternal(); close(drained) }()
	select {
	case <-drained:
		t.Fatal("drain returned during callback")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := awaitExternal(t, done); err != nil {
		t.Fatal(err)
	}
	awaitExternal(t, drained)
	got, _ := c.Action(testContext, first.ID)
	if got.Status != "performed" || len(s.ExternalCallbacks()) != 0 {
		t.Fatal("drain before receipt")
	}
}

func TestAutopilotLateReceiptDuringInspection(t *testing.T) {
	for _, outcome := range []string{"performed", "uncertain"} {
		t.Run(outcome, func(t *testing.T) {
			entered, release := make(chan AutopilotAction, 1), make(chan struct{})
			inspecting, finishInspect := make(chan struct{}), make(chan struct{})
			var at atomic.Int64
			at.Store(time.Now().UnixNano())
			clock := func() time.Time { return time.Unix(0, at.Load()) }
			adapter := ExternalAction{Check: func(Snapshot, ConcreteAction) error { return nil },
				Execute: func(_ context.Context, a AutopilotAction) (ExternalResult, error) {
					entered <- a
					<-release
					return ExternalResult{outcome, "executor evidence"}, nil
				},
				Reconcile: func(context.Context, AutopilotAction) (ExternalResult, error) {
					close(inspecting)
					<-finishInspect
					return ExternalResult{"failed", "inspection evidence"}, nil
				}}
			s, c, f, action, path := externalFixture(t, autopilot.Act, adapter)
			s.now = clock
			c.renewEvery = 0
			second, b := secondExternal(t, s, path, adapter)
			second.now = clock
			b.renewEvery = 0
			done := make(chan error, 1)
			go func() { _, err := f.Submit(testContext, "late", "reason", action); done <- err }()
			a := awaitExternal(t, entered)
			at.Add(int64(2 * time.Minute))
			inspected := make(chan error, 1)
			go func() { _, err := b.Reconcile(testContext, a.ID, 1); inspected <- err }()
			awaitExternal(t, inspecting)
			// Inspection runs without the coordinator lock too.
			catalog := make(chan struct{})
			go func() { b.Catalog(); close(catalog) }()
			awaitExternal(t, catalog)
			if _, err := c.OwnerAction(testContext, a.ID, 1, "cancel", nil); !errors.Is(err, ErrConflict) {
				t.Fatal(err)
			}
			if len(s.ExternalCallbacks()) != 1 {
				t.Fatal("refused cancellation released active executor")
			}
			close(release)
			if err := awaitExternal(t, done); err != nil {
				t.Fatal(err)
			}
			current, _ := b.Action(testContext, a.ID)
			if outcome == "performed" && (current.Status != "performed" || current.Dispatch != nil) {
				t.Fatal("late executor receipt rejected", current)
			}
			if outcome == "uncertain" && (current.Status != "uncertain" || current.Dispatch == nil || current.Dispatch.Owner != b.instance) {
				t.Fatal("unconfirmed executor revoked live inspection", current)
			}
			close(finishInspect)
			if err := awaitExternal(t, inspected); err != nil {
				t.Fatal(err)
			}
			current, _ = b.Action(testContext, a.ID)
			if outcome == "performed" && (current.Status != "conflict" || !strings.Contains(current.Detail, "executor evidence") || !strings.Contains(current.Detail, "inspection evidence")) {
				t.Fatal(current)
			}
			if outcome == "uncertain" && (current.Status != "failed" || current.Dispatch != nil) {
				t.Fatal("current inspection could not settle", current)
			}
		})
	}
}

func TestAutopilotAbandonedInspectionLease(t *testing.T) {
	var effects atomic.Int32
	inspecting, release := make(chan struct{}), make(chan struct{})
	adapter := ExternalAction{Check: func(Snapshot, ConcreteAction) error { return nil },
		Execute: func(context.Context, AutopilotAction) (ExternalResult, error) {
			effects.Add(1)
			return ExternalResult{"uncertain", "unconfirmed"}, nil
		},
		Reconcile: func(context.Context, AutopilotAction) (ExternalResult, error) {
			close(inspecting)
			<-release
			return ExternalResult{"performed", "late inspector"}, nil
		}}
	s, c, f, action, path := externalFixture(t, autopilot.Act, adapter)
	var at atomic.Int64
	at.Store(time.Now().UnixNano())
	clock := func() time.Time { return time.Unix(0, at.Load()) }
	s.now = clock
	c.renewEvery = 0
	a, err := f.Submit(testContext, "abandoned", "reason", action)
	if err != nil {
		t.Fatal(err)
	}
	other, b := secondExternal(t, s, path, adapter)
	other.now = clock
	b.renewEvery = 0
	thirdAdapter := adapter
	thirdAdapter.Reconcile = func(context.Context, AutopilotAction) (ExternalResult, error) {
		return ExternalResult{"failed", "replacement inspector"}, nil
	}
	third, d := secondExternal(t, s, path, thirdAdapter)
	third.now = clock
	done := make(chan error, 1)
	go func() { _, err := b.Reconcile(testContext, a.ID, 1); done <- err }()
	awaitExternal(t, inspecting)
	live, _ := d.Action(testContext, a.ID)
	if _, err := d.ReconcileAutomatic(testContext, a.ID, 1); !errors.Is(err, ErrConflict) {
		t.Fatal("live inspection stolen", err)
	}
	at.Add(int64(2 * time.Minute))
	settled, err := d.ReconcileAutomatic(testContext, a.ID, 1)
	if err != nil || settled.Status != "failed" || settled.DispatchToken <= live.DispatchToken {
		t.Fatal(settled, err)
	}
	close(release)
	if err := awaitExternal(t, done); err != nil {
		t.Fatal(err)
	}
	got, _ := d.Action(testContext, a.ID)
	if got.Status != "conflict" || effects.Load() != 1 {
		t.Fatal("abandoned inspection repeated effect", got)
	}
}

// A late inspector is evidence, not authority to terminate a newer live claim.
func TestAutopilotExpiredInspectorPreservesLiveReconciliation(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	newEntered, newRelease := make(chan struct{}), make(chan struct{})
	var once, newOnce sync.Once
	defer once.Do(func() { close(release) })
	defer newOnce.Do(func() { close(newRelease) })
	adapter := ExternalAction{
		Check: func(Snapshot, ConcreteAction) error { return nil },
		Execute: func(context.Context, AutopilotAction) (ExternalResult, error) {
			return ExternalResult{"uncertain", "executor could not confirm"}, nil
		},
		Reconcile: func(context.Context, AutopilotAction) (ExternalResult, error) {
			close(entered)
			<-release
			return ExternalResult{"failed", "expired inspector evidence"}, nil
		},
	}
	s, _, f, action, path := externalFixture(t, autopilot.Act, adapter)
	var at atomic.Int64
	at.Store(time.Now().UnixNano())
	clock := func() time.Time { return time.Unix(0, at.Load()) }
	s.now = clock
	a, err := f.Submit(testContext, "live-inspector", "reason", action)
	if err != nil {
		t.Fatal(err)
	}
	other, b := secondExternal(t, s, path, adapter)
	other.now, b.renewEvery = clock, 0
	newAdapter := adapter
	newAdapter.Reconcile = func(context.Context, AutopilotAction) (ExternalResult, error) {
		close(newEntered)
		<-newRelease
		return ExternalResult{"performed", "current inspector evidence"}, nil
	}
	third, c := secondExternal(t, s, path, newAdapter)
	third.now, c.renewEvery = clock, 0
	oldDone, newDone := make(chan error, 1), make(chan error, 1)
	go func() { _, err := b.Reconcile(testContext, a.ID, 1); oldDone <- err }()
	awaitExternal(t, entered)
	at.Add(int64(2 * time.Minute))
	go func() { _, err := c.Reconcile(testContext, a.ID, 1); newDone <- err }()
	awaitExternal(t, newEntered)
	live, err := c.Action(testContext, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	once.Do(func() { close(release) })
	if err := awaitExternal(t, oldDone); err != nil {
		t.Fatal(err)
	}
	got, err := b.Action(testContext, a.ID)
	if err != nil || got.Status != "uncertain" || got.Dispatch == nil ||
		got.Dispatch.Owner != live.Dispatch.Owner || got.Dispatch.Token != live.Dispatch.Token ||
		!strings.Contains(got.Detail, "expired inspector evidence") {
		t.Fatal("expired inspector revoked live ownership", got, err)
	}
	if _, err := b.ReconcileAutomatic(testContext, a.ID, 1); !errors.Is(err, ErrConflict) {
		t.Fatal("late receipt reopened dispatch", err)
	}
	newOnce.Do(func() { close(newRelease) })
	if err := awaitExternal(t, newDone); err != nil {
		t.Fatal(err)
	}
	got, err = c.Action(testContext, a.ID)
	if err != nil || got.Status != "performed" || got.Dispatch != nil {
		t.Fatal(got, err)
	}
	history, err := c.History(testContext, AutopilotHistoryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range history.Entries {
		if strings.Contains(entry.Action.Detail, "expired inspector evidence") {
			found = true
		}
	}
	if !found {
		t.Fatal("late inspector evidence lost from audit")
	}
}

func TestAutopilotDispatchRenewal(t *testing.T) {
	entered, release := make(chan AutopilotAction, 1), make(chan struct{})
	adapter := ExternalAction{Check: func(Snapshot, ConcreteAction) error { return nil },
		Execute: func(_ context.Context, a AutopilotAction) (ExternalResult, error) {
			entered <- a
			<-release
			return ExternalResult{"performed", "finished"}, nil
		},
		Reconcile: func(context.Context, AutopilotAction) (ExternalResult, error) {
			return ExternalResult{"failed", "absent"}, nil
		}}
	s, c, f, action, path := externalFixture(t, autopilot.Act, adapter)
	var at atomic.Int64
	at.Store(time.Now().UnixNano())
	clock := func() time.Time { return time.Unix(0, at.Load()) }
	s.now = clock
	c.renewEvery = time.Millisecond
	second, b := secondExternal(t, s, path, adapter)
	second.now = clock
	done := make(chan error, 1)
	go func() { _, err := f.Submit(testContext, "renew", "reason", action); done <- err }()
	a := awaitExternal(t, entered)
	if _, err := s.store.db.Exec(`ALTER TABLE autopilot_actions RENAME TO unavailable_actions`); err != nil {
		t.Fatal(err)
	}
	// Discard setup/event admission nudges before observing lease maintenance.
	select {
	case <-s.store.autopilotNudge:
	default:
	}
	// Several renewal ticks fail before storage recovers. Renewal must retry,
	// rather than treating a failed transaction as proof of lost ownership.
	time.Sleep(30 * time.Millisecond)
	if _, err := s.store.db.Exec(`ALTER TABLE unavailable_actions RENAME TO autopilot_actions`); err != nil {
		t.Fatal(err)
	}
	at.Add(int64(30 * time.Second))
	extended := false
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		current, err := b.Action(testContext, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Dispatch.ExpiresAt.After(a.Dispatch.ExpiresAt) {
			extended = true
			break
		}
	}
	if !extended {
		t.Fatal("lease not renewed")
	}
	select {
	case <-s.store.autopilotNudge:
		t.Fatal("lease renewal woke event consumer")
	default:
	}
	if _, err := b.Reconcile(testContext, a.ID, 1); !errors.Is(err, ErrConflict) {
		t.Fatal("renewed dispatch inspected", err)
	}
	close(release)
	if err := awaitExternal(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestAutopilotIntentFailureReleasesCallback(t *testing.T) {
	var effects atomic.Int32
	adapter := ExternalAction{Check: func(Snapshot, ConcreteAction) error { return nil },
		Execute: func(context.Context, AutopilotAction) (ExternalResult, error) {
			effects.Add(1)
			return ExternalResult{"performed", "finished"}, nil
		},
		Reconcile: func(context.Context, AutopilotAction) (ExternalResult, error) {
			return ExternalResult{"performed", "found"}, nil
		}}
	s, c, f, action, _ := externalFixture(t, autopilot.Suggest, adapter)
	a, err := f.Submit(testContext, "intent-failure", "reason", action)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.store.db.Exec(`CREATE TRIGGER fail_intent BEFORE INSERT ON autopilot_audit BEGIN SELECT RAISE(ABORT,'injected'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.OwnerAction(testContext, a.ID, 1, "approve", nil); err == nil {
		t.Fatal("intent failure ignored")
	}
	if effects.Load() != 0 || len(s.ExternalCallbacks()) != 0 {
		t.Fatal("uncommitted dispatch escaped or leaked")
	}
	current, _ := c.Action(testContext, a.ID)
	if current.Status != "proposed" || current.Dispatch != nil {
		t.Fatal(current)
	}
	drained := make(chan struct{})
	go func() { s.DrainExternal(); close(drained) }()
	awaitExternal(t, drained)
}

func TestAutopilotExternalStaleApproval(t *testing.T) {
	for _, change := range []string{"off", "mode-revision", "target", "authority"} {
		t.Run(change, func(t *testing.T) {
			var effects atomic.Int32
			adapter := ExternalAction{Check: func(v Snapshot, a ConcreteAction) error {
				if project(&v, a.ProjectID).TitleRevision != a.TargetVersion {
					return ErrConflict
				}
				return nil
			},
				Execute: func(context.Context, AutopilotAction) (ExternalResult, error) {
					effects.Add(1)
					return ExternalResult{"performed", "finished"}, nil
				},
				Reconcile: func(context.Context, AutopilotAction) (ExternalResult, error) {
					return ExternalResult{"performed", "found"}, nil
				}}
			s, c, f, action, _ := externalFixture(t, autopilot.Suggest, adapter)
			a, err := f.Submit(testContext, "stale", "reason", action)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "off", "mode-revision":
				cfg := s.configuration()
				if change == "off" {
					cfg.Autopilot.Modes[autopilot.Operator] = autopilot.Off
				} else {
					cfg.Autopilot.Revisions = map[string]uint64{autopilot.Operator: 1}
				}
				if err := s.UpdateConfig(cfg); err != nil {
					t.Fatal(err)
				}
			case "target":
				if err := s.store.update(testContext, func(v *Snapshot) error { project(v, action.ProjectID).TitleRevision++; return nil }); err != nil {
					t.Fatal(err)
				}
			case "authority":
				if _, err := s.SetOperatorPermission(testContext, action.ProjectID, true, 1); err != nil {
					t.Fatal(err)
				}
			}
			got, err := c.OwnerAction(testContext, a.ID, 1, "approve", nil)
			if err != nil || got.Status != "refused" || effects.Load() != 0 {
				t.Fatal("stale approval executed", got, err)
			}
		})
	}
}

// Stage the same intent transaction used by approval, then mutate policy before
// dispatch. This pins the post-commit recheck independently of goroutine timing.
func TestAutopilotExternalCommittedApprovalBecomesStale(t *testing.T) {
	for _, change := range []string{"mode", "target", "authority"} {
		t.Run(change, func(t *testing.T) {
			var effects atomic.Int32
			adapter := ExternalAction{Check: func(v Snapshot, a ConcreteAction) error {
				if project(&v, a.ProjectID).TitleRevision != a.TargetVersion {
					return ErrConflict
				}
				return nil
			}, Execute: func(context.Context, AutopilotAction) (ExternalResult, error) {
				effects.Add(1)
				return ExternalResult{"performed", "done"}, nil
			},
				Reconcile: func(context.Context, AutopilotAction) (ExternalResult, error) {
					return ExternalResult{"failed", "absent"}, nil
				}}
			s, c, f, action, _ := externalFixture(t, autopilot.Suggest, adapter)
			a, err := f.Submit(testContext, "committed", "reason", action)
			if err != nil {
				t.Fatal(err)
			}
			c.mu.Lock()
			err = s.store.updateTransaction(testContext, func(v *Snapshot, conn *sql.Conn) error {
				a.Approver = "owner"
				return c.perform(testContext, conn, v, &a, "owner", AutopilotOwner, false)
			})
			c.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if a.Status != "uncertain" || a.Dispatch == nil || len(s.ExternalCallbacks()) != 1 {
				t.Fatal("intent not committed", a)
			}
			switch change {
			case "mode":
				err = c.SerializeSettings(func() error {
					cfg := s.configuration()
					cfg.Autopilot.Modes[autopilot.Operator] = autopilot.Off
					return s.UpdateConfig(cfg)
				})
			case "target":
				err = s.store.update(testContext, func(v *Snapshot) error { project(v, action.ProjectID).TitleRevision++; return nil })
			case "authority":
				_, err = s.SetOperatorPermission(testContext, action.ProjectID, false, 1)
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := c.executeExternal(testContext, a)
			if err != nil || got.Status != "refused" || got.Dispatch != nil || effects.Load() != 0 || len(s.ExternalCallbacks()) != 0 {
				t.Fatal(got, err)
			}
		})

	}
}

func TestAutopilotClosedAdmissionPreservesActProposal(t *testing.T) {
	adapter := ExternalAction{Check: func(Snapshot, ConcreteAction) error { return nil },
		Execute: func(context.Context, AutopilotAction) (ExternalResult, error) {
			t.Fatal("closed admission executed")
			return ExternalResult{}, nil
		},
		Reconcile: func(context.Context, AutopilotAction) (ExternalResult, error) {
			return ExternalResult{"failed", "absent"}, nil
		}}
	s, c, f, action, _ := externalFixture(t, autopilot.Act, adapter)
	s.CloseExternalAdmission()
	a, err := f.Submit(testContext, "closed", "reason", action)
	if err != nil || a.Status != "proposed" || a.Dispatch != nil {
		t.Fatal(a, err)
	}
	stored, err := c.Action(testContext, a.ID)
	if err != nil || stored.Status != "proposed" {
		t.Fatal(stored, err)
	}
	s.DrainExternal()
	s.OpenExternalAdmission()
	// A second runtime can admit the preserved proposal.
	c.actions["synthetic"].external.Execute = func(context.Context, AutopilotAction) (ExternalResult, error) {
		return ExternalResult{"performed", "done"}, nil
	}
	got, err := c.OwnerAction(testContext, a.ID, 1, "approve", nil)
	if err != nil || got.Status != "performed" {
		t.Fatal(got, err)
	}
}

func TestAutopilotDeferredSubmitPreservesExternalProposal(t *testing.T) {
	for _, gate := range []string{"pause", "upgrade", "coordinator", "hook"} {
		t.Run(gate, func(t *testing.T) {
			adapter := ExternalAction{Check: func(Snapshot, ConcreteAction) error { return nil },
				Execute: func(context.Context, AutopilotAction) (ExternalResult, error) {
					t.Error("deferred action executed")
					return ExternalResult{}, nil
				},
				Reconcile: func(context.Context, AutopilotAction) (ExternalResult, error) { return ExternalResult{}, nil }}
			s, c, f, action, _ := externalFixture(t, autopilot.Act, adapter)
			switch gate {
			case "pause":
				if err := s.store.update(testContext, func(v *Snapshot) error { v.Paused = true; return nil }); err != nil {
					t.Fatal(err)
				}
			case "upgrade":
				s.upgradeDraining = true
			case "coordinator":
				c.Close()
			case "hook":
				c.admission = func() error { return ErrConflict }
			}
			a, err := f.Submit(testContext, "deferred", "reason", action)
			if err != nil || a.Status != "proposed" || a.Dispatch != nil || len(s.ExternalCallbacks()) != 0 {
				t.Fatal(a, err)
			}
			stored, err := c.Action(testContext, a.ID)
			if err != nil || stored.Status != "proposed" {
				t.Fatal(stored, err)
			}
		})
	}
}

func TestAutopilotInconclusiveConflictRetainsBothReceipts(t *testing.T) {
	adapter := ExternalAction{Check: func(Snapshot, ConcreteAction) error { return nil },
		Execute: func(context.Context, AutopilotAction) (ExternalResult, error) { return ExternalResult{}, nil },
		Reconcile: func(context.Context, AutopilotAction) (ExternalResult, error) {
			return ExternalResult{"uncertain", "inspection unavailable"}, nil
		}}
	s, c, f, action, _ := externalFixture(t, autopilot.Suggest, adapter)
	a, err := f.Submit(testContext, "conflict-evidence", "reason", action)
	if err != nil {
		t.Fatal(err)
	}
	first, second := strings.Repeat("a", 4096), strings.Repeat("b", 4096)
	a.Status, a.Detail = "conflict", boundedEvidence(first, second)
	err = s.store.updateTransaction(testContext, func(_ *Snapshot, conn *sql.Conn) error {
		return writeAutopilotAction(testContext, conn, a, "owner", AutopilotOwner, s.now())
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		got, err := c.Reconcile(testContext, a.ID, 1)
		if err != nil || got.Status != "conflict" || !strings.Contains(got.Detail, first) || !strings.Contains(got.Detail, second) {
			t.Fatal("conflict evidence lost", err)
		}
	}
}

func TestAutopilotExternalAdmissionRacesDrain(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var checks, effects atomic.Int32
	adapter := ExternalAction{Check: func(Snapshot, ConcreteAction) error {
		if checks.Add(1) == 2 {
			close(entered)
			<-release
		}
		return nil
	},
		Execute: func(context.Context, AutopilotAction) (ExternalResult, error) {
			effects.Add(1)
			return ExternalResult{"performed", "finished"}, nil
		},
		Reconcile: func(context.Context, AutopilotAction) (ExternalResult, error) {
			return ExternalResult{"performed", "found"}, nil
		}}
	s, c, f, action, _ := externalFixture(t, autopilot.Suggest, adapter)
	a, err := f.Submit(testContext, "drain-race", "reason", action)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := c.OwnerAction(testContext, a.ID, 1, "approve", nil); done <- err }()
	awaitExternal(t, entered)
	drained := make(chan struct{})
	go func() { s.DrainExternal(); close(drained) }()
	awaitExternal(t, drained)
	close(release)
	if err := awaitExternal(t, done); err == nil {
		t.Fatal("dispatch entered after drain")
	}
	got, _ := c.Action(testContext, a.ID)
	if got.Status != "proposed" || got.Dispatch != nil || effects.Load() != 0 {
		t.Fatal("refused intent persisted", got)
	}
}

func TestAutopilotReceiptFailureDoesNotHoldDrain(t *testing.T) {
	adapter := ExternalAction{Check: func(Snapshot, ConcreteAction) error { return nil },
		Reconcile: func(context.Context, AutopilotAction) (ExternalResult, error) {
			return ExternalResult{"performed", "found"}, nil
		}}
	var s *Service
	adapter.Execute = func(context.Context, AutopilotAction) (ExternalResult, error) {
		_, err := s.store.db.Exec(`CREATE TRIGGER fail_receipt BEFORE INSERT ON autopilot_audit BEGIN SELECT RAISE(ABORT,'injected'); END`)
		if err != nil {
			t.Error(err)
		}
		return ExternalResult{"performed", "finished"}, nil
	}
	var f *AutopilotFunction
	var action ConcreteAction
	s, _, f, action, _ = externalFixture(t, autopilot.Act, adapter)
	got, err := f.Submit(testContext, "receipt-failure", "reason", action)
	if err == nil || got.Status != "uncertain" || got.Dispatch == nil {
		t.Fatal(got, err)
	}
	replay, err := f.Submit(testContext, "receipt-failure", "reason", action)
	if err != nil || replay.ID != got.ID {
		t.Fatal("receipt retry dispatched", replay, err)
	}
	drained := make(chan struct{})
	go func() { s.DrainExternal(); close(drained) }()
	awaitExternal(t, drained)
	if len(s.ExternalCallbacks()) != 0 {
		t.Fatal("failed persistence blocked drain")
	}
}
