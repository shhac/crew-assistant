package app

import (
	"github.com/shhac/crew-assistant/internal/autopilot"
	"github.com/shhac/crew-assistant/internal/config"
)

func (a *App) SetAutopilotMode(id string, mode autopilot.Mode, expected uint64) (config.Config, error) {
	var out config.Config
	err := a.Autopilot.SerializeSettings(func() error {
		a.mu.Lock()
		defer a.mu.Unlock()
		cfg, err := config.SetAutopilotMode(a.configPath, id, mode, expected)
		if err != nil {
			return err
		}
		// A row edit applies only those settings. The daemon's effective config
		// includes command-line overrides and may differ from the disk elsewhere.
		effective := a.cfg
		effective.Autopilot = cfg.Autopilot
		if err := a.checkConfigLocked(effective); err != nil {
			return err
		}
		if err := a.applyConfigLocked(effective); err != nil {
			return err
		}
		out = effective
		return nil
	})
	return out, err
}

func (a *App) SetAutopilotDigest(digest autopilot.DailyDigest, expected uint64) (config.Config, error) {
	var out config.Config
	err := a.Autopilot.SerializeSettings(func() error {
		a.mu.Lock()
		defer a.mu.Unlock()
		cfg, err := config.SetAutopilotDigest(a.configPath, digest, expected)
		if err != nil {
			return err
		}
		effective := a.cfg
		effective.Autopilot = cfg.Autopilot
		if err := a.checkConfigLocked(effective); err != nil {
			return err
		}
		if err := a.applyConfigLocked(effective); err != nil {
			return err
		}
		out = effective
		return nil
	})
	return out, err
}
