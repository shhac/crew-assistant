package quota

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/lib-agent-harness/session"
)

func fixture(used float64) session.QuotaSnapshot {
	observation := session.Observation{Quality: session.Measured, ObservedAt: time.Now()}
	return session.QuotaSnapshot{Observation: observation, Complete: true, Windows: []session.QuotaWindow{{Observation: observation, ID: "codex/primary", Scope: "codex", UsedPercent: &used}}}
}

func TestEvaluateThreshold(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name string
		used float64
		held bool
	}{{"below", 89.9, false}, {"exactly", 90, true}, {"over", 110, true}, {"zero", 0, false}} {
		t.Run(tc.name, func(t *testing.T) {
			v := Evaluate(fixture(tc.used), config.Model{Engine: "codex"}, 90, now)
			if v.Held != tc.held || !v.Known {
				t.Fatalf("held=%v known=%v", v.Held, v.Known)
			}
		})
	}
	for _, tc := range []string{"absent", "stale", "invalidated", "nil percentage", "expired reset", "partial", "unrelated"} {
		t.Run(tc, func(t *testing.T) {
			q := fixture(20)
			switch tc {
			case "absent":
				q = session.QuotaSnapshot{}
			case "stale":
				q.ObservedAt = now.Add(-3 * time.Minute)
			case "invalidated":
				q.Invalidated = true
			case "nil percentage":
				q.Windows[0].UsedPercent = nil
			case "expired reset":
				expired := now.Add(-time.Second)
				q.Windows[0].ResetsAt = &expired
			case "partial":
				q.Complete = false
			case "unrelated":
				q.Windows[0].Scope = "code-review"
			}
			v := Evaluate(q, config.Model{Engine: "codex"}, 90, now)
			if v.Held || v.Known {
				t.Fatalf("held=%v known=%v", v.Held, v.Known)
			}
		})
	}
	q := fixture(10)
	high := fixture(95).Windows[0]
	high.ID = "codex/secondary"
	q.Windows = append(q.Windows, high)
	if !Evaluate(q, config.Model{Engine: "codex"}, 90, now).Held {
		t.Fatal("weekly allowance ignored")
	}
	q.Complete = false
	if !Evaluate(q, config.Model{Engine: "codex"}, 90, now).Held {
		t.Fatal("known exhausted window lost in partial snapshot")
	}
}

// A measured hold reports the reset the owner is waiting for, so the daemon can
// describe an ordinary wait instead of an unexplained stop.
func TestHeldVerdictCarriesReset(t *testing.T) {
	now := time.Now()
	q := fixture(95)
	reset := now.Add(2 * time.Hour)
	q.Windows[0].ResetsAt = &reset
	v := Evaluate(q, config.Model{Engine: "codex"}, 90, now)
	if !v.Held || !v.ResetsAt.Equal(reset.UTC()) {
		t.Fatalf("reset lost: %+v", v)
	}
}

func TestModelScopes(t *testing.T) {
	for _, tc := range []struct {
		engine, model, scope, id string
		applies                  bool
	}{
		{"codex", "gpt-6-astra", "codex", "codex/primary", true},
		{"codex", "gpt-6-astra", "gpt-6-astra", "model/primary", true},
		{"codex", "gpt-6-astra", "code-review", "review/primary", false},
		{"claude", "claude-opus-5", "five_hour", "five_hour", true},
		{"claude", "claude-opus-5", "seven_day_opus", "seven_day_opus", true},
		{"claude", "claude-opus-5", "seven_day_sonnet", "seven_day_sonnet", false},
		{"claude", "claude-opus-5", "Opus 5", "model:Opus 5", true},
		{"claude", "claude-opus-5", "Sonnet", "model:Sonnet", false},
	} {
		if got := Applies(session.QuotaWindow{ID: tc.id, Scope: tc.scope}, config.Model{Engine: tc.engine, Model: tc.model}); got != tc.applies {
			t.Errorf("%+v got %v", tc, got)
		}
	}
}

func TestThresholdSupportedEngines(t *testing.T) {
	policy := config.RoleUsage{CodexMaxUsedPercent: 90, ClaudeMaxUsedPercent: 75}
	if p, ok := Threshold(policy, "codex"); p != 90 || !ok {
		t.Fatal("codex threshold", p, ok)
	}
	if p, ok := Threshold(policy, "claude"); p != 75 || !ok {
		t.Fatal("claude threshold", p, ok)
	}
	if _, ok := Threshold(policy, "openai-compatible"); ok {
		t.Fatal("unsupported engine reported a subscription threshold")
	}
}

func TestCacheUsesEngineBinaryAndHomeNotModel(t *testing.T) {
	var options []session.Options
	meter := Meter{Inspect: func(_ context.Context, o session.Options) (session.Inspection, error) {
		options = append(options, o)
		return session.Inspection{Quota: fixture(90)}, errors.New("account unavailable but quota succeeded")
	}}
	m := config.Default().Model
	for i := 0; i < 2; i++ {
		if !meter.Read(context.Background(), m).Known() {
			t.Fatal("partial inspection discarded quota")
		}
	}
	m.Model = "different-model"
	meter.Read(context.Background(), m)
	m.CodexHome = "/synthetic/another-home"
	meter.Read(context.Background(), m)
	m.CodexBin = "another-codex"
	meter.Read(context.Background(), m)
	m.Engine = "claude"
	m.ClaudeBin = "synthetic-claude"
	m.ClaudeHome = "/synthetic/claude-home"
	meter.Read(context.Background(), m)
	if len(options) != 4 || options[3].Engine != session.Claude || options[3].Binary != m.ClaudeBin || options[3].Home != m.ClaudeHome {
		t.Fatalf("wrong identities: %+v", options)
	}
}

func TestRefreshDoesNotRetainFailedTelemetry(t *testing.T) {
	calls := 0
	meter := Meter{Inspect: func(context.Context, session.Options) (session.Inspection, error) {
		calls++
		if calls == 1 {
			return session.Inspection{Quota: fixture(5)}, nil
		}
		return session.Inspection{}, errors.New("failed refresh")
	}}
	model := config.Default().Model
	if !meter.Read(context.Background(), model).Known() {
		t.Fatal("initial quota absent")
	}
	meter.mu.Lock()
	for key, e := range meter.entries {
		e.fetched = time.Now().Add(-2 * CacheAge)
		meter.entries[key] = e
	}
	meter.mu.Unlock()
	if meter.Read(context.Background(), model).Known() || calls != 2 {
		t.Fatal("failed refresh presented stale quota as current")
	}
}
