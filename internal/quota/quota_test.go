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
	five := int64(300)
	return session.QuotaSnapshot{Observation: observation, Complete: true, Windows: []session.QuotaWindow{{Observation: observation, ID: "codex/primary", Scope: "codex", UsedPercent: &used, WindowMinutes: &five}}}
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
			v := Evaluate(q, codex, tenAndTen, now)
			if v.Held || v.Known {
				t.Fatalf("held=%v known=%v", v.Held, v.Known)
			}
		})
	}
	q := fixture(10)
	week := int64(10080)
	high := fixture(95).Windows[0]
	high.ID, high.WindowMinutes = "codex/secondary", &week
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
	// A window that doesn't say how long it is answers to the stricter floor.
	q = fixture(85)
	q.Windows[0].WindowMinutes = nil
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
		if got := Applies(session.QuotaWindow{ID: tc.id, Scope: tc.scope}, config.Harness{Engine: tc.engine, Model: tc.model}); got != tc.applies {
			t.Errorf("%+v got %v", tc, got)
		}
	}
}

func TestCacheUsesEngineBinaryAndHomeNotModel(t *testing.T) {
	var options []session.Options
	meter := Meter{Inspect: func(_ context.Context, o session.Options) (session.Inspection, error) {
		options = append(options, o)
		return session.Inspection{Quota: fixture(90)}, errors.New("account unavailable but quota succeeded")
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
	if len(options) != 4 || options[3].Engine != session.Claude || options[3].Binary != m.Bin || options[3].Home != m.Home {
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
