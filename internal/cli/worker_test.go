package cli

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/integrations/worker"
	"github.com/shhac/crew-assistant/internal/quota"
	"github.com/shhac/lib-agent-harness/session"
)

func quotaFixture(used float64) session.QuotaSnapshot {
	observation := session.Observation{Quality: session.Measured, ObservedAt: time.Now()}
	return session.QuotaSnapshot{Observation: observation, Complete: true, Windows: []session.QuotaWindow{{Observation: observation, ID: "codex/primary", Scope: "codex", UsedPercent: &used}}}
}

// A separately operated broker is measured by the same account policy as a
// managed one. Its configuration is its own, but the rules are not a copy.
func TestStandaloneBrokerHonoursTheConfiguredHeadroomPolicy(t *testing.T) {
	cfg := config.Default()
	profile := cfg.WorkerModel
	for _, tc := range []struct {
		name        string
		used        float64
		unavailable bool
		onMissing   string
		threshold   int
		held        bool
		kind        string
	}{
		{"headroom", 10, false, "allow", 90, false, ""},
		{"consumed", 95, false, "allow", 90, true, worker.HoldSubscriptionQuota},
		{"disabled", 95, false, "allow", 0, false, ""},
		{"unavailable allows by default", 0, true, "allow", 90, false, ""},
		{"unavailable pauses when asked", 0, true, "pause", 90, true, worker.HoldTelemetryUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := cfg
			policy.Limits.WorkerUsage.CodexMaxUsedPercent = tc.threshold
			policy.Limits.WorkerUsage.OnUnavailable = tc.onMissing
			inspected := 0
			meter := &quota.Meter{Inspect: func(context.Context, session.Options) (session.Inspection, error) {
				inspected++
				if tc.unavailable {
					return session.Inspection{}, errors.New("CLI unavailable")
				}
				return session.Inspection{Quota: quotaFixture(tc.used)}, nil
			}}
			err := standaloneAdmission(policy, profile, meter)(context.Background())
			var held *worker.HoldError
			if errors.As(err, &held) != tc.held {
				t.Fatalf("admission result %v", err)
			}
			if tc.threshold == 0 && inspected != 0 {
				t.Fatal("a disabled gate inspected the CLI login")
			}
			if tc.held && held.Hold.Kind != tc.kind {
				t.Fatalf("wrong hold kind %q", held.Hold.Kind)
			}
			// An account that can still be read later is looked at again; only a
			// decision the owner owns stops on its own.
			if tc.held && (!held.Hold.Recheckable() || held.Hold.NextCheckAt.IsZero()) {
				t.Fatalf("a measurement hold was not left recheckable: %+v", held.Hold)
			}
		})
	}
}

// An engine whose subscription cannot be inspected locally is not silently
// exempt. It is unmeasured, and follows the same unavailable-usage policy the
// daemon applies, so "pause" really does mean nothing runs unmeasured.
func TestStandaloneBrokerTreatsUninspectableEnginesAsUnmeasured(t *testing.T) {
	cfg := config.Default()
	profile := cfg.WorkerModel
	profile.Engine = "openai-compatible"
	meter := &quota.Meter{Inspect: func(context.Context, session.Options) (session.Inspection, error) {
		t.Fatal("an uninspectable engine contacted a CLI login")
		return session.Inspection{}, nil
	}}
	if err := standaloneAdmission(cfg, profile, meter)(context.Background()); err != nil {
		t.Fatal("the default allow policy blocked an unmeasured engine", err)
	}
	cfg.Limits.WorkerUsage.OnUnavailable = "pause"
	err := standaloneAdmission(cfg, profile, meter)(context.Background())
	var held *worker.HoldError
	if !errors.As(err, &held) || held.Hold.Kind != worker.HoldTelemetryUnavailable {
		t.Fatalf("pause policy let an unmeasured engine run: %v", err)
	}
	if !held.Hold.Recheckable() {
		t.Fatal("flipping the policy back would never be noticed")
	}
}

// Cancelling supervision is cancellation, not an unreadable account: a shutdown
// must not be recorded as a resource condition for the owner to resolve.
func TestStandaloneAdmissionPropagatesCancellation(t *testing.T) {
	cfg := config.Default()
	cfg.Limits.WorkerUsage.OnUnavailable = "pause"
	meter := &quota.Meter{Inspect: func(ctx context.Context, _ session.Options) (session.Inspection, error) {
		return session.Inspection{}, ctx.Err()
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := standaloneAdmission(cfg, cfg.WorkerModel, meter)(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation became a resource decision: %v", err)
	}
	var held *worker.HoldError
	if errors.As(err, &held) {
		t.Fatal("cancellation was reported as a hold the owner must resolve")
	}
}
