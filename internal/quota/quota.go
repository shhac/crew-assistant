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
// that window, and any request already in flight, can overshoot the threshold
// by the work those calls consume. Callers must document that tolerance rather
// than describe the gate as exact.
const CacheAge = time.Minute

// Identity keys telemetry by login, not project or model: several workers may
// share one subscription. Account metadata and credentials are never persisted.
type Identity struct{ Engine, Binary, Home string }

// IdentityFor selects the engine-specific binary and login home.
func IdentityFor(model config.Model) Identity {
	key := Identity{Engine: model.Engine, Binary: model.CodexBin, Home: model.CodexHome}
	if model.Engine == "claude" {
		key.Binary, key.Home = model.ClaudeBin, model.ClaudeHome
	}
	return key
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
func (m *Meter) Read(ctx context.Context, model config.Model) session.QuotaSnapshot {
	key := IdentityFor(model)
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

// Threshold reports the configured percentage for an engine. Supported is false
// for engines whose subscription allowance cannot be inspected locally.
func Threshold(policy config.RoleUsage, engine string) (percent int, supported bool) {
	switch engine {
	case "codex":
		return policy.CodexMaxUsedPercent, true
	case "claude":
		return policy.ClaudeMaxUsedPercent, true
	}
	return 0, false
}

// Verdict separates the three distinct answers a headroom check can give:
// measured headroom, a measured hold, and no usable measurement at all.
type Verdict struct {
	Held     bool
	Known    bool
	Detail   string
	ResetsAt time.Time
}

// Evaluate applies the threshold to the windows that govern this model. A
// window whose own observation is stale, invalid or already past its reset is
// treated as missing, which makes the whole snapshot unknown rather than free.
func Evaluate(q session.QuotaSnapshot, model config.Model, threshold int, now time.Time) Verdict {
	if q.IsStale(now, 2*CacheAge) {
		return Verdict{}
	}
	missing, peak, seen := !q.Complete, 0.0, false
	for _, w := range q.Windows {
		if !Applies(w, model) {
			continue
		}
		seen = true
		if w.IsStale(now, 2*CacheAge) || w.UsedPercent == nil || math.IsNaN(*w.UsedPercent) || math.IsInf(*w.UsedPercent, 0) || *w.UsedPercent < 0 || (w.ResetsAt != nil && !w.ResetsAt.After(now)) {
			missing = true
			continue
		}
		used := *w.UsedPercent
		peak = max(peak, used)
		if used >= float64(threshold) {
			out := Verdict{Held: true, Known: true, Detail: fmt.Sprintf("%s %s is %.1f%% consumed (limit %d%%)", model.Engine, w.ID, used, threshold)}
			if w.ResetsAt != nil {
				out.ResetsAt = w.ResetsAt.UTC()
				out.Detail += "; resets " + out.ResetsAt.Format(time.RFC3339)
			}
			return out
		}
	}
	return Verdict{Known: seen && !missing, Detail: fmt.Sprintf("%s peak applicable usage %.1f%%", model.Engine, peak)}
}

// Applies ignores unrelated pools (for example Codex code reviews, or Claude
// Sonnet when an Opus worker is selected). Unknown-only snapshots stay
// unavailable, never zero.
func Applies(w session.QuotaWindow, model config.Model) bool {
	scope, name := strings.ToLower(w.Scope), strings.ToLower(model.Model)
	if model.Engine == "codex" {
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
	return strings.HasPrefix(w.ID, "model:") && strings.EqualFold(w.Scope, model.Model)
}
