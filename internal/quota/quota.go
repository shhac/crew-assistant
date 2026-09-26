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
	"errors"
	"fmt"
	"math"
	"os/exec"
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

// Reading is one look at a login's usage: what it reported, and what kept it
// from reporting more.
type Reading struct {
	Quota session.QuotaSnapshot
	// LoggedIn is nil when the CLI didn't say.
	LoggedIn *bool
	// Err is why the look failed, perhaps only in part.
	Err error
	At  time.Time
}

// Meter caches native account telemetry. Inspect is injectable; tests must
// never reach a real CLI login.
type Meter struct {
	inspecting sync.Mutex // One CLI inspection at a time.
	mu         sync.Mutex // Guards entries, never held during an inspection.
	Inspect    func(context.Context, session.Options) (session.Inspection, error)
	entries    map[Identity]entry
}

type entry struct {
	last Reading
	// measured is the newest valid observation of each window, by id. Only
	// a later one of the same window can say it has usage again; a failed
	// look, or one of another pool, can't.
	measured   map[string]session.QuotaWindow
	rechecking bool
}

// Read returns the cached or freshly observed allowance for a worker model.
func (m *Meter) Read(ctx context.Context, h config.Harness) session.QuotaSnapshot {
	return m.Observe(ctx, h).Quota
}

// Observe returns the cached or a fresh reading of a worker model's login.
func (m *Meter) Observe(ctx context.Context, h config.Harness) Reading {
	key := IdentityFor(h)
	m.inspecting.Lock()
	defer m.inspecting.Unlock()
	if r, ok := m.Cached(h); ok && time.Since(r.At) < CacheAge {
		return r
	}
	inspect := m.Inspect
	if inspect == nil {
		inspect = session.Inspect
	}
	bounded, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	// Account inspection may fail while quota succeeds. Keep usable quota even
	// when Inspect returns a partial error; never interpret an error as zero use.
	result, err := inspect(bounded, session.Options{Engine: session.Engine(key.Engine), Binary: key.Binary, Home: key.Home})
	if err != nil {
		// The harness says only that the CLI couldn't start; say why when
		// it isn't there at all.
		if _, missing := exec.LookPath(key.Binary); missing != nil {
			err = errors.Join(err, missing)
		}
	}
	r := Reading{Quota: result.Quota, LoggedIn: result.Account.LoggedIn, Err: err, At: time.Now()}
	if ctx.Err() == nil {
		m.update(key, func(e *entry) {
			e.last = r
			for _, w := range r.Quota.Windows {
				if w.Invalidated || !validPercent(w) {
					continue
				}
				if e.measured == nil {
					e.measured = make(map[string]session.QuotaWindow)
				}
				e.measured[w.ID] = w
			}
		})
	}
	return r
}

func (m *Meter) update(key Identity, change func(*entry)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.entries == nil {
		m.entries = make(map[Identity]entry)
	}
	e := m.entries[key]
	change(&e)
	m.entries[key] = e
}

// Cached is the last reading of a worker model's login, however old. It
// never inspects and never waits on an inspection under way.
func (m *Meter) Cached(h config.Harness) (Reading, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[IdentityFor(h)]
	return e.last, ok && !e.last.At.IsZero()
}

// Spent is OutOfUsage without ever starting a look.
func (m *Meter) Spent(h config.Harness) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.entries[IdentityFor(h)].spent(h)
}

func (e entry) spent(h config.Harness) bool {
	var q session.QuotaSnapshot
	for _, w := range e.measured {
		q.Windows = append(q.Windows, w)
	}
	return Exhausted(q, h)
}

// OutOfUsage says a window that governs the model had nothing left when it
// was last measured. Neither time, a reset time passing, a failed look nor
// a reading of another pool clears that; only a reading that measures usage
// left in that window does. It never waits: when the login was last looked at Recheck ago
// or more, it starts one bounded look in the background.
func (m *Meter) OutOfUsage(h config.Harness) bool {
	key := IdentityFor(h)
	m.mu.Lock()
	e := m.entries[key]
	out := e.spent(h)
	due := out && !e.rechecking && time.Since(e.last.At) >= Recheck
	if due {
		e.rechecking = true
		m.entries[key] = e
	}
	m.mu.Unlock()
	if due {
		go func() {
			defer m.update(key, func(e *entry) { e.rechecking = false })
			m.Observe(context.Background(), h)
		}()
	}
	return out
}

// Forget drops cached telemetry so the next admission observes the account
// again. Used when policy changes and by tests. What was last measured is
// kept: only a new measurement says a spent login has usage again.
func (m *Meter) Forget() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, e := range m.entries {
		e.last = Reading{}
		m.entries[key] = e
	}
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
		if !usable(w, now) {
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

// usable says a window's own observation is fresh, valid and not yet past
// its reset.
func usable(w session.QuotaWindow, now time.Time) bool {
	return !w.IsStale(now, 2*CacheAge) && validPercent(w) && (w.ResetsAt == nil || w.ResetsAt.After(now))
}

func validPercent(w session.QuotaWindow) bool {
	return w.UsedPercent != nil && !math.IsNaN(*w.UsedPercent) && !math.IsInf(*w.UsedPercent, 0) && *w.UsedPercent >= 0
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
