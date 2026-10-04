package cli

import (
	"github.com/shhac/crew-assistant/internal/autopilot"
	"github.com/shhac/crew-assistant/internal/config"
	libcli "github.com/shhac/lib-agent-cli/cli"
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
	return keys
}
