package work

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
)

// Serialize pause writes and cache refreshes, without holding the gate lock
// while reading the store (claims acquire those locks in the opposite order).
func (lp *Loop) refreshEnginePauses(ctx context.Context) error {
	lp.enginePauseMu.Lock()
	defer lp.enginePauseMu.Unlock()
	return lp.loadEnginePauses(ctx, true)
}

func (lp *Loop) loadEnginePauses(ctx context.Context, lift bool) error {
	if lp.Core == nil {
		return errors.New("coordination state is unavailable")
	}
	snap, err := lp.Core.Snapshot(ctx)
	if err != nil {
		return err
	}
	now := lp.now()
	for _, p := range snap.EnginePauses {
		if lift && p.Until != nil && !p.Until.After(now) {
			if err := lp.Core.LiftEndedEnginePauses(ctx, now); err != nil {
				return err
			}
			snap, err = lp.Core.Snapshot(ctx)
			if err != nil {
				return err
			}
			break
		}
	}
	lp.gate.mu.Lock()
	lp.gate.pauses = snap.EnginePauses
	lp.gate.pausesLoaded = true
	lp.gate.signal()
	lp.gate.mu.Unlock()
	return nil
}

func (lp *Loop) PauseEngine(ctx context.Context, engine string, until *time.Time) error {
	lp.enginePauseMu.Lock()
	defer lp.enginePauseMu.Unlock()
	if err := lp.Core.PauseEngine(ctx, engine, until); err != nil {
		return err
	}
	lp.gate.mu.Lock()
	if lp.gate.pauses == nil {
		lp.gate.pauses = map[string]core.EnginePause{}
	}
	lp.gate.pauses[engine] = core.EnginePause{At: lp.now(), Until: until}
	lp.gate.signal()
	lp.gate.mu.Unlock()
	lp.Nudge()
	return nil
}

func (lp *Loop) ResumeEngine(ctx context.Context, engine string) error {
	lp.enginePauseMu.Lock()
	defer lp.enginePauseMu.Unlock()
	if err := lp.Core.ResumeEngine(ctx, engine); err != nil {
		return err
	}
	lp.gate.mu.Lock()
	delete(lp.gate.pauses, engine)
	lp.gate.signal()
	lp.gate.mu.Unlock()
	lp.Nudge()
	return nil
}

type EngineStatus struct {
	Engine string     `json:"engine"`
	State  string     `json:"state"`
	Until  *time.Time `json:"until,omitempty"`
	Reason string     `json:"reason,omitempty"`
}

func (lp *Loop) EngineStatus(ctx context.Context) ([]EngineStatus, error) {
	snap, err := lp.Core.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	engines := slices.Clone(config.EngineNames)
	for engine := range snap.EnginePauses {
		if !slices.Contains(engines, engine) {
			engines = append(engines, engine)
		}
	}
	slices.Sort(engines)
	out := make([]EngineStatus, 0, len(engines))
	for _, engine := range engines {
		s := EngineStatus{Engine: engine, State: "running"}
		if p, paused := snap.EnginePaused(engine, lp.now()); paused {
			s.State, s.Until = "paused", p.Until
		} else if until, detail := lp.floorWait(ctx, core.Role{Engine: engine}); !until.IsZero() {
			s.State, s.Until, s.Reason = "held", &until, detail
		}
		out = append(out, s)
	}
	return out, nil
}

// EnginePause reads the effective owner pause, including before Run has loaded
// admission. A failed initial read is an error, never permission to spend usage.
func (lp *Loop) EnginePause(ctx context.Context, engine string) (core.EnginePause, bool, error) {
	lp.gate.mu.Lock()
	loaded := lp.gate.pausesLoaded
	lp.gate.mu.Unlock()
	if !loaded {
		lp.enginePauseMu.Lock()
		lp.gate.mu.Lock()
		loaded = lp.gate.pausesLoaded
		lp.gate.mu.Unlock()
		var err error
		if !loaded {
			err = lp.loadEnginePauses(ctx, false)
		}
		lp.enginePauseMu.Unlock()
		if err != nil {
			return core.EnginePause{}, false, err
		}
	}
	lp.gate.mu.Lock()
	defer lp.gate.mu.Unlock()
	pause, paused := (core.Snapshot{EnginePauses: lp.gate.pauses}).EnginePaused(engine, lp.now())
	return pause, paused, nil
}
