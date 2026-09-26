package quota

import (
	"context"
	"errors"
	"io/fs"
	"os/exec"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/lib-agent-harness/session"
)

// Recheck is how often a login that had nothing left is looked at again, to
// learn when it can be used again.
const Recheck = 5 * time.Minute

// Level is how much an engine has left, as the owner sees it.
type Level string

const (
	LevelUnknown Level = "unknown"
	LevelOK      Level = "ok"
	// LevelLow is less left than the owner's floor, which holds team work.
	LevelLow Level = "low"
	// LevelExhausted is a window with nothing left.
	LevelExhausted Level = "exhausted"
)

var levelOrder = map[Level]int{LevelUnknown: 0, LevelOK: 1, LevelLow: 2, LevelExhausted: 3}

// Window is what one of an engine's usage windows has left.
type Window struct {
	Name         string     `json:"name"`
	LeftPercent  float64    `json:"left_percent"`
	FloorPercent int        `json:"floor_percent"`
	ResetsAt     *time.Time `json:"resets_at,omitempty"`
	Level        Level      `json:"level"`
}

// Remaining is what a login reports it has left, only as the CLI measured it.
type Remaining struct {
	Level   Level    `json:"level"`
	Windows []Window `json:"windows"`
	// ResetsAt is when the level eases: the last reset among the windows
	// that set it, when each of them said.
	ResetsAt *time.Time `json:"resets_at,omitempty"`
	Overage  bool       `json:"using_overage,omitempty"`
	// Missing says in plain words why nothing was measured.
	Missing string `json:"missing,omitempty"`
}

// Describe is what a reading says a model's login has left. Only windows the
// CLI measured, recently and validly, are shown; with none, it says why
// rather than guessing. A window is low below the floor that would hold team
// work, and exhausted by the rule Exhausted uses.
func Describe(r Reading, h config.Harness, floors Floors, now time.Time) Remaining {
	out := Remaining{Level: LevelUnknown, Windows: []Window{}}
	switch {
	case errors.Is(r.Err, exec.ErrNotFound) || errors.Is(r.Err, fs.ErrNotExist):
		out.Missing = "not installed"
		return out
	case r.LoggedIn != nil && !*r.LoggedIn:
		out.Missing = "not signed in"
		return out
	}
	if !r.Quota.IsStale(now, 2*CacheAge) {
		for _, w := range r.Quota.Windows {
			if !Applies(w, h) || !usable(w, now) {
				continue
			}
			floor, label := floors.For(w)
			name := w.Name
			if name == "" {
				name = strings.TrimSuffix(label, " usage")
			}
			win := Window{Name: name, LeftPercent: max(0, 100-*w.UsedPercent), FloorPercent: floor, Level: LevelOK}
			if w.ResetsAt != nil {
				at := w.ResetsAt.UTC()
				win.ResetsAt = &at
			}
			switch {
			case spent(w):
				win.Level = LevelExhausted
			case floor > 0 && win.LeftPercent < float64(floor):
				win.Level = LevelLow
			}
			out.Windows = append(out.Windows, win)
		}
	}
	if len(out.Windows) == 0 {
		switch {
		case errors.Is(r.Err, context.DeadlineExceeded):
			out.Missing = "usage check timed out"
		case r.Err != nil:
			out.Missing = "usage check failed"
		default:
			out.Missing = "usage not reported"
		}
		return out
	}
	for _, w := range out.Windows {
		if levelOrder[w.Level] > levelOrder[out.Level] {
			out.Level = w.Level
		}
	}
	if out.Level != LevelOK {
		for _, w := range out.Windows {
			if w.Level != out.Level {
				continue
			}
			if w.ResetsAt == nil {
				out.ResetsAt = nil
				break
			}
			if out.ResetsAt == nil || w.ResetsAt.After(*out.ResetsAt) {
				out.ResetsAt = w.ResetsAt
			}
		}
	}
	out.Overage = r.Quota.UsingOverage != nil && *r.Quota.UsingOverage
	return out
}

// Exhausted says a snapshot leaves a model's login with nothing in a window
// that governs it. It is the one rule for "out of usage": the sidebar shows
// it and the small models skip an engine by it.
func Exhausted(q session.QuotaSnapshot, h config.Harness) bool {
	for _, w := range q.Windows {
		if Applies(w, h) && spent(w) {
			return true
		}
	}
	return false
}

func spent(w session.QuotaWindow) bool {
	return !w.Invalidated && validPercent(w) && *w.UsedPercent >= 100
}
