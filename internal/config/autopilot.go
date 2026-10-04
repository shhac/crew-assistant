package config

import (
	"errors"
	"github.com/shhac/crew-assistant/internal/autopilot"
)

var ErrAutopilotConflict = errors.New("autopilot setting changed; reload it before saving")

// ErrAutopilotUnchanged identifies a rejection before any persistence begins.
// Callers must retain their existing admission state for these errors.
var ErrAutopilotUnchanged = errors.New("autopilot settings unchanged")

// SetAutopilotMode is a locked, row-scoped compare-and-swap. Empty resets
// to the catalog default without deleting future settings from the document.
func SetAutopilotMode(path, id string, mode autopilot.Mode, expected uint64) (Config, error) {
	var out Config
	err := Document(path).WithLock(func() error {
		c, err := Load(path)
		if err != nil {
			return err
		}
		if _, err = c.Autopilot.EffectiveMode(id); err != nil {
			return errors.Join(ErrAutopilotUnchanged, err)
		}
		if c.Autopilot.Revisions[id] != expected {
			return errors.Join(ErrAutopilotUnchanged, ErrAutopilotConflict)
		}
		if c.Autopilot.Modes == nil {
			c.Autopilot.Modes = map[string]autopilot.Mode{}
		}
		if c.Autopilot.Revisions == nil {
			c.Autopilot.Revisions = map[string]uint64{}
		}
		c.Autopilot.Modes[id] = mode
		c.Autopilot.Revisions[id]++
		if err = c.Validate(); err != nil {
			return errors.Join(ErrAutopilotUnchanged, err)
		}
		if _, err = upgradeLocked(path); err != nil {
			return err
		}
		if err = Document(path).Save(c); err != nil {
			return err
		}
		out = c
		return nil
	})
	return out, err
}
