package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/shhac/crew-assistant/internal/autopilot"
	"github.com/shhac/crew-assistant/internal/config"
	libcli "github.com/shhac/lib-agent-cli/cli"
	"io"
)

// Mode keys use the same narrow updates as the dedicated autopilot command,
// rather than the whole-config binding used by unrelated settings.
func autopilotConfigKeys(o *options) []libcli.ConfigKey {
	var keys []libcli.ConfigKey
	for _, function := range autopilot.Catalog() {
		id := function.ID
		set := func(mode autopilot.Mode) error {
			cfg, err := config.Load(o.configPath)
			if err != nil {
				return err
			}
			return o.setAutopilotMode(id, mode, cfg.Autopilot.Revisions[id])
		}
		keys = append(keys, libcli.ConfigKey{Name: "autopilot.modes." + id, Description: function.Label + ": off, suggest or act; available in " + function.AvailableIn, Values: []string{"off", "suggest", "act"}, Get: func() (string, bool) {
			cfg, err := config.Load(o.configPath)
			if err != nil {
				return "", false
			}
			mode, err := cfg.Autopilot.EffectiveMode(id)
			if err != nil {
				return "", false
			}
			return string(mode), cfg.Autopilot.Modes[id] != ""
		}, Set: func(value string) error { return set(autopilot.Mode(value)) }, Unset: func() error { return set("") }})
	}
	keys = append(keys, libcli.ConfigKey{Name: "autopilot.daily_digest", Description: "Owner-only daily digest settings: enabled, at (HH:MM), revision", Get: func() (string, bool) {
		cfg, err := config.Load(o.configPath)
		if err != nil {
			return "", false
		}
		data, err := json.Marshal(cfg.Autopilot.DailyDigest)
		return string(data), err == nil
	}, Set: func(value string) error {
		var d autopilot.DailyDigest
		decoder := json.NewDecoder(bytes.NewBufferString(value))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&d); err != nil {
			return err
		}
		if decoder.Decode(new(any)) != io.EOF {
			return errors.New("expected one digest settings value")
		}
		return o.setAutopilotDigest(d)
	}, Unset: func() error {
		cfg, err := config.Load(o.configPath)
		if err != nil {
			return err
		}
		return o.setAutopilotDigest(autopilot.DailyDigest{Revision: cfg.Autopilot.DailyDigest.Revision})
	}})
	return keys
}
