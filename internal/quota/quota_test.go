package quota

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	harness "github.com/shhac/lib-agent-harness"
)

func fixture(used float64) harness.QuotaSnapshot {
	observation := harness.Observation{Quality: harness.Measured, ObservedAt: time.Now()}
	five := int64(300)
	return harness.QuotaSnapshot{Observation: observation, Complete: true, Windows: []harness.QuotaWindow{{Observation: observation, ID: "codex/primary", Kind: harness.QuotaSession, Scope: "codex", UsedPercent: &used, WindowMinutes: &five}}}
}

var codex = config.Harness{Engine: "codex"}
var tenAndTen = Floors{FiveHour: 10, Week: 10}

func TestEvaluateFloors(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name string
		used float64
		held bool
	}{{"above the floor", 89.9, false}, {"exactly at it", 90, false}, {"below it", 90.5, true}, {"over", 110, true}, {"unused", 0, false}} {
		t.Run(tc.name, func(t *testing.T) {
			v := Evaluate(fixture(tc.used), codex, tenAndTen, now)
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
				q = harness.QuotaSnapshot{}
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
				q.Windows[0].ID, q.Windows[0].Scope = "review/primary", "review"
			}
			v := Evaluate(q, codex, tenAndTen, now)
			if v.Held || v.Known {
				t.Fatalf("held=%v known=%v", v.Held, v.Known)
			}
		})
	}
	q := fixture(10)
	week := int64(10080)
	high := fixture(95).Windows[0]
	high.ID, high.Kind, high.WindowMinutes = "codex/secondary", harness.QuotaWeekly, &week
	q.Windows = append(q.Windows, high)
	if v := Evaluate(q, codex, tenAndTen, now); !v.Held || v.Detail != "codex weekly usage has 5% left (floor 10%)" {
		t.Fatalf("weekly allowance ignored: %+v", v)
	}
	if Evaluate(q, codex, Floors{FiveHour: 10, Week: 0}, now).Held {
		t.Fatal("a weekly floor of 0 still held")
	}
	if !Evaluate(q, codex, Floors{FiveHour: 0, Week: 6}, now).Held || !Evaluate(q, codex, Floors{FiveHour: 99, Week: 4}, now).Held {
		t.Fatal("each window should answer to its own floor")
	}
	q.Complete = false
	if !Evaluate(q, codex, tenAndTen, now).Held {
		t.Fatal("known exhausted window lost in partial snapshot")
	}
	// A window of the engine's own whose period isn't recognized answers to
	// the stricter floor.
	q = fixture(85)
	q.Windows[0].Kind, q.Windows[0].WindowMinutes = harness.QuotaOther, nil
	if !Evaluate(q, codex, Floors{FiveHour: 5, Week: 20}, now).Held {
		t.Fatal("a window of unknown length took the looser floor")
	}
}

// A measured hold reports the reset the owner is waiting for, so the daemon can
// describe an ordinary wait instead of an unexplained stop.
func TestHeldVerdictCarriesReset(t *testing.T) {
	now := time.Now()
	q := fixture(95)
	reset := now.Add(2 * time.Hour)
	q.Windows[0].ResetsAt = &reset
	v := Evaluate(q, codex, tenAndTen, now)
	if !v.Held || !v.ResetsAt.Equal(reset.UTC()) {
		t.Fatalf("reset lost: %+v", v)
	}
}

func TestModelScopes(t *testing.T) {
	session, weekly, model, other := harness.QuotaSession, harness.QuotaWeekly, harness.QuotaWeeklyModel, harness.QuotaOther
	for _, tc := range []struct {
		engine, model string
		window        harness.QuotaWindow
		applies       bool
	}{
		{"codex", "gpt-6-astra", harness.QuotaWindow{ID: "codex/primary", Scope: "codex", Kind: session}, true},
		{"codex", "gpt-6-astra", harness.QuotaWindow{ID: "default/primary", Scope: "default", Kind: other}, true},
		{"codex", "gpt-6-astra", harness.QuotaWindow{ID: "model/primary", Scope: "gpt-6-astra", Model: "gpt-6-astra", Kind: model}, true},
		{"codex", "gpt-6-luna", harness.QuotaWindow{ID: "model/primary", Scope: "gpt-6-astra", Model: "gpt-6-astra", Kind: model}, false},
		{"codex", "gpt-6-astra", harness.QuotaWindow{ID: "review/primary", Scope: "review", Kind: session}, false},
		{"claude", "claude-opus-5", harness.QuotaWindow{ID: "five_hour", Scope: "five_hour", Kind: session}, true},
		{"claude", "claude-opus-5", harness.QuotaWindow{ID: "seven_day", Scope: "seven_day", Kind: weekly}, true},
		{"claude", "claude-opus-5", harness.QuotaWindow{ID: "seven_day_opus", Scope: "seven_day_opus", Kind: model, Model: "opus"}, true},
		{"claude", "claude-opus-5", harness.QuotaWindow{ID: "seven_day_sonnet", Scope: "seven_day_sonnet", Kind: model, Model: "sonnet"}, false},
		{"claude", "claude-opus-5", harness.QuotaWindow{ID: "model:Opus 5", Scope: "Opus 5", Kind: model, Model: "Opus 5"}, true},
		{"claude", "opus", harness.QuotaWindow{ID: "model:Opus 5", Scope: "Opus 5", Kind: model, Model: "Opus 5"}, true},
		{"claude", "claude-opus-5", harness.QuotaWindow{ID: "model:Sonnet", Scope: "Sonnet", Kind: model, Model: "Sonnet"}, false},
		{"claude", "", harness.QuotaWindow{ID: "seven_day_opus", Scope: "seven_day_opus", Kind: model, Model: "opus"}, false},
		{"claude", "claude-opus-5", harness.QuotaWindow{ID: "iguana_necktie", Scope: "iguana_necktie", Kind: other}, false},
		{"claude", "claude-opus-5", harness.QuotaWindow{ID: "limits/session", Scope: "session", Kind: session}, true},
	} {
		if got := Applies(tc.window, config.Harness{Engine: tc.engine, Model: tc.model}); got != tc.applies {
			t.Errorf("%+v got %v", tc, got)
		}
	}
}

func TestCacheUsesEngineBinaryAndHomeNotModel(t *testing.T) {
	var providers []harness.Provider
	meter := Meter{Inspect: func(_ context.Context, p harness.Provider) (harness.AccountReport, error) {
		providers = append(providers, p)
		return harness.AccountReport{Quota: fixture(90)}, errors.New("account unavailable but quota succeeded")
	}}
	m := config.Default().AssistantHarness()
	for i := 0; i < 2; i++ {
		if !meter.Read(context.Background(), m).Known() {
			t.Fatal("partial inspection discarded quota")
		}
	}
	m.Model = "different-model"
	meter.Read(context.Background(), m)
	m.Home = "/synthetic/another-home"
	meter.Read(context.Background(), m)
	m.Bin = "another-codex"
	meter.Read(context.Background(), m)
	m.Engine, m.Bin, m.Home = "claude", "synthetic-claude", "/synthetic/claude-home"
	meter.Read(context.Background(), m)
	if len(providers) != 4 || providers[3].Engine != harness.Claude || providers[3].CLI.Binary != m.Bin || providers[3].CLI.Home != m.Home {
		t.Fatalf("wrong identities: %+v", providers)
	}
}

func TestRefreshDoesNotRetainFailedTelemetry(t *testing.T) {
	calls := 0
	meter := Meter{Inspect: func(context.Context, harness.Provider) (harness.AccountReport, error) {
		calls++
		if calls == 1 {
			return harness.AccountReport{Quota: fixture(5)}, nil
		}
		return harness.AccountReport{}, errors.New("failed refresh")
	}}
	model := config.Default().AssistantHarness()
	if !meter.Read(context.Background(), model).Known() {
		t.Fatal("initial quota absent")
	}
	meter.mu.Lock()
	for key, e := range meter.entries {
		e.last.At = time.Now().Add(-2 * CacheAge)
		meter.entries[key] = e
	}
	meter.mu.Unlock()
	if meter.Read(context.Background(), model).Known() || calls != 2 {
		t.Fatal("failed refresh presented stale quota as current")
	}
}
