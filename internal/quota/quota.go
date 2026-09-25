// Package quota holds one subscription-headroom policy for deciding whether
// a team role may start a turn now.
//
// This measures the native CLI login's own reported allowance windows. It is a
// headroom guard for a shared account, not a token count, a currency budget or
// a per-assignment cap; those are separate controls. An unavailable or stale
// observation is never interpreted as zero consumption.
package quota

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/lib-agent-harness/session"
)

// CacheAge bounds how long one account observation is reused. Admission during
// that window, and any request already in flight, can overshoot the floor
// by the work those calls consume. Callers must document that tolerance rather
// than describe the gate as exact.
const CacheAge = time.Minute

// Identity keys telemetry by login, not project or model: several workers may
// share one subscription. Account metadata and credentials are never persisted.
type Identity struct{ Engine, Binary, Home string }

// IdentityFor is the login a model on an engine is billed to.
func IdentityFor(h config.Harness) Identity {
	return Identity{Engine: h.Engine, Binary: h.Bin, Home: h.Home}
}

type entry struct {
	quota   session.QuotaSnapshot
	fetched time.Time
}

// Meter caches native account telemetry. Inspect is injectable; tests must
// never reach a real CLI login.
type Meter struct {
	mu      sync.Mutex
	Inspect func(context.Context, session.Options) (session.Inspection, error)
	entries map[Identity]entry
}

// Read returns the cached or freshly observed allowance for a worker model.
func (m *Meter) Read(ctx context.Context, h config.Harness) session.QuotaSnapshot {
	key := IdentityFor(h)
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	if e, ok := m.entries[key]; ok && now.Sub(e.fetched) < CacheAge {
		return e.quota
	}
	inspect := m.Inspect
	if inspect == nil {
		inspect = session.Inspect
	}
	bounded, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	// Account inspection may fail while quota succeeds. Keep usable quota even
	// when Inspect returns a partial error; never interpret an error as zero use.
	result, _ := inspect(bounded, session.Options{Engine: session.Engine(key.Engine), Binary: key.Binary, Home: key.Home})
	if ctx.Err() == nil {
		if m.entries == nil {
			m.entries = make(map[Identity]entry)
		}
		m.entries[key] = entry{result.Quota, time.Now()}
	}
	return result.Quota
}

// Forget drops cached telemetry so the next admission observes the account
// again. Used when policy changes and by tests.
func (m *Meter) Forget() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = nil
}

// Floors are the share of each window roles leave unused, in percent: the
// 5-hour floor for windows of a day or less, the weekly floor for longer
// ones. A floor of 0 turns that window's check off.
type Floors struct{ FiveHour, Week int }

// Off says no window is checked, so there is nothing to wait for either.
func (f Floors) Off() bool { return f.FiveHour == 0 && f.Week == 0 }

// For is the floor that governs a window. A window that doesn't say how long
// it is takes the stricter floor.
func (f Floors) For(w session.QuotaWindow) (floor int, label string) {
	switch {
	case w.WindowMinutes == nil:
		return max(f.FiveHour, f.Week), "usage"
	case *w.WindowMinutes <= 24*60:
		return f.FiveHour, "5-hour usage"
	}
	return f.Week, "weekly usage"
}

// Verdict separates the three distinct answers a headroom check can give:
// measured headroom, a measured hold, and no usable measurement at all.
type Verdict struct {
	Held     bool
	Known    bool
	Detail   string
	ResetsAt time.Time
}

// Evaluate holds a role when a window that governs its model has less left
// than that window's floor. A window whose own observation is stale, invalid
// or already past its reset is treated as missing, which makes the whole
// snapshot unknown rather than free.
func Evaluate(q session.QuotaSnapshot, h config.Harness, floors Floors, now time.Time) Verdict {
	if q.IsStale(now, 2*CacheAge) {
		return Verdict{}
	}
	missing, least, seen := !q.Complete, 100.0, false
	for _, w := range q.Windows {
		if !Applies(w, h) {
			continue
		}
		seen = true
		if w.IsStale(now, 2*CacheAge) || w.UsedPercent == nil || math.IsNaN(*w.UsedPercent) || math.IsInf(*w.UsedPercent, 0) || *w.UsedPercent < 0 || (w.ResetsAt != nil && !w.ResetsAt.After(now)) {
			missing = true
			continue
		}
		left := max(0, 100-*w.UsedPercent)
		least = min(least, left)
		floor, label := floors.For(w)
		if floor > 0 && left < float64(floor) {
			out := Verdict{Held: true, Known: true, Detail: fmt.Sprintf("%s %s has %.0f%% left (floor %d%%)", h.Engine, label, left, floor)}
			if w.ResetsAt != nil {
				out.ResetsAt = w.ResetsAt.UTC()
				out.Detail += "; resets " + out.ResetsAt.Format(time.RFC3339)
			}
			return out
		}
	}
	return Verdict{Known: seen && !missing, Detail: fmt.Sprintf("%s has %.0f%% left in its tightest window", h.Engine, least)}
}

// Applies ignores unrelated pools (for example Codex code reviews, or Claude
// Sonnet when an Opus worker is selected). Unknown-only snapshots stay
// unavailable, never zero.
func Applies(w session.QuotaWindow, h config.Harness) bool {
	scope, name := strings.ToLower(w.Scope), strings.ToLower(h.Model)
	if h.Engine == "codex" {
		return scope == "default" || scope == "codex" || scope == name
	}
	switch w.ID {
	case "five_hour", "seven_day":
		return true
	}
	for _, family := range []string{"opus", "sonnet", "haiku"} {
		if strings.Contains(name, family) && (w.ID == "seven_day_"+family || (strings.HasPrefix(w.ID, "model:") && strings.Contains(scope, family))) {
			return true
		}
	}
	return strings.HasPrefix(w.ID, "model:") && strings.EqualFold(w.Scope, h.Model)
}
