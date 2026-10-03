package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
)

var errEnginePauseUnchanged = errors.New("engine pause unchanged")

type EnginePause struct {
	At    time.Time  `json:"at"`
	Until *time.Time `json:"until,omitempty"`
}

func (v Snapshot) EnginePaused(engine string, now time.Time) (EnginePause, bool) {
	p, ok := v.EnginePauses[engine]
	return p, ok && (p.Until == nil || p.Until.After(now))
}

func (s *Service) PauseEngine(ctx context.Context, engine string, until *time.Time) error {
	if !slices.Contains(config.EngineNames, engine) {
		return fmt.Errorf("unknown engine %q", engine)
	}
	return s.store.update(ctx, func(v *Snapshot) error {
		now := s.now().UTC()
		if until != nil && !until.After(now) {
			return fmt.Errorf("pause end must be in the future")
		}
		if v.EnginePauses == nil {
			v.EnginePauses = map[string]EnginePause{}
		}
		v.EnginePauses[engine] = EnginePause{At: now, Until: until}
		end := "until you resume"
		if until != nil {
			end = "until " + until.Local().Format("Mon 15:04")
		}
		record(v, now, "", "engine.paused", "You paused "+config.EngineLabel(engine)+" "+end)
		return nil
	})
}

func (s *Service) ResumeEngine(ctx context.Context, engine string) error {
	if !slices.Contains(config.EngineNames, engine) {
		return fmt.Errorf("unknown engine %q", engine)
	}
	err := s.store.update(ctx, func(v *Snapshot) error {
		if _, ok := v.EnginePaused(engine, s.now().UTC()); !ok {
			return errEnginePauseUnchanged
		}
		delete(v.EnginePauses, engine)
		record(v, s.now().UTC(), "", "engine.resumed", "You resumed "+config.EngineLabel(engine))
		return nil
	})
	if errors.Is(err, errEnginePauseUnchanged) {
		return nil
	}
	return err
}

func (s *Service) LiftEndedEnginePauses(ctx context.Context, now time.Time) error {
	err := s.store.update(ctx, func(v *Snapshot) error {
		changed := false
		for engine, p := range v.EnginePauses {
			if p.Until != nil && !p.Until.After(now) {
				delete(v.EnginePauses, engine)
				changed = true
				record(v, now.UTC(), "", "engine.pause_ended", config.EngineLabel(engine)+"'s pause ended at "+p.Until.Local().Format("Mon 15:04"))
			}
		}
		if !changed {
			return errEnginePauseUnchanged
		}
		return nil
	})
	if errors.Is(err, errEnginePauseUnchanged) {
		return nil
	}
	return err
}
