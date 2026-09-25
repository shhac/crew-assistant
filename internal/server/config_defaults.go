package server

import "github.com/shhac/crew-assistant/internal/config"

type engineDefaults struct {
	Bin  string `json:"bin"`
	Home string `json:"home"`
}

// configDefaults are what a blank engine setting means, for the dashboard to
// show in place of the blank.
func configDefaults() map[string]any {
	engines := map[string]engineDefaults{}
	for _, name := range config.CLIEngineNames {
		bin, home := config.DefaultBinary(name)
		engines[name] = engineDefaults{bin, home}
	}
	baseURL, _ := config.Engines{}.Endpoint()
	return map[string]any{
		"engines":          engines,
		"usage_floor":      config.DefaultUsageFloor,
		"on_unknown_usage": config.OnUnknownUsageAllow,
		"openai_base_url":  baseURL,
	}
}
