package config

import (
	"encoding/json"
	"math"
)

// renamedKeys says where each key of an earlier layout went, so a file or a
// dashboard written before still works and a report of an unknown key can
// say where it moved.
var renamedKeys = map[string]string{
	"model.codex_bin":                           "engines.codex.bin",
	"model.codex_home":                          "engines.codex.home",
	"model.claude_bin":                          "engines.claude.bin",
	"model.claude_home":                         "engines.claude.home",
	"model.base_url":                            "engines.openai-compatible.base_url",
	"model.api_key_env":                         "engines.openai-compatible.api_key_env",
	"limits.role_usage.codex_max_used_percent":  "engines.codex.usage_floor",
	"limits.role_usage.claude_max_used_percent": "engines.claude.usage_floor",
	"limits.role_usage.on_unavailable":          "engines.<engine>.on_unknown_usage",
	"chat.loading_phrases.model":                "",
	"chat.loading_phrases.effort":               "",
}

// RenamedKey is where an earlier layout's key went, if it is one: "" when it
// was dropped.
func RenamedKey(path string) (string, bool) {
	to, ok := renamedKeys[path]
	return to, ok
}

// convertLegacy rewrites a config document in an earlier layout into the
// current one, in place, and reports whether it changed anything.
//
// A model section with no engine is an API configuration from before engines
// were chosen, and keeps its provider and billing path. The engines' binaries,
// homes and endpoint move to the engines section. A used-percent limit
// becomes the floor it leaves: 0 stays off, the old default of 90 becomes the
// default, and anything else leaves 100 minus it for both windows.
func convertLegacy(doc map[string]any) bool {
	changed := false
	if model, ok := doc["model"].(map[string]any); ok {
		if _, explicit := model["engine"]; !explicit {
			model["engine"] = "openai-compatible"
			if _, ok := model["model"]; !ok {
				model["model"] = ""
			}
			if _, ok := model["effort"]; !ok {
				model["effort"] = ""
			}
			changed = true
		}
		for _, move := range []struct{ from, engine, field string }{
			{"codex_bin", "codex", "bin"}, {"codex_home", "codex", "home"},
			{"claude_bin", "claude", "bin"}, {"claude_home", "claude", "home"},
			{"base_url", "openai-compatible", "base_url"}, {"api_key_env", "openai-compatible", "api_key_env"},
		} {
			value, ok := model[move.from]
			if !ok {
				continue
			}
			delete(model, move.from)
			changed = true
			s, _ := value.(string)
			// The endpoint's settings move as they are: an empty key means
			// none. Earlier versions saved every default, and a CLI's
			// default stays one.
			if move.engine == "openai-compatible" || (s != "" && s != legacyDefault(move.engine, move.field)) {
				section(section(doc, "engines"), move.engine)[move.field] = s
			}
		}
	}
	if limits, ok := doc["limits"].(map[string]any); ok {
		if usage, ok := limits["role_usage"].(map[string]any); ok {
			delete(limits, "role_usage")
			changed = true
			for _, engine := range []string{"codex", "claude"} {
				if used, ok := usage[engine+"_max_used_percent"].(float64); ok {
					if floor, set := floorFrom(used); set {
						section(section(section(doc, "engines"), engine), "usage_floor")["5h_percent"] = floor
						section(section(section(doc, "engines"), engine), "usage_floor")["1w_percent"] = floor
					}
				}
				if when, _ := usage["on_unavailable"].(string); when == "pause" {
					section(section(doc, "engines"), engine)["on_unknown_usage"] = when
				}
			}
		}
	}
	if chat, ok := doc["chat"].(map[string]any); ok {
		if phrases, ok := chat["loading_phrases"].(map[string]any); ok {
			for _, key := range []string{"model", "effort"} {
				if _, ok := phrases[key]; ok {
					delete(phrases, key)
					changed = true
				}
			}
		}
	}
	return changed
}

func legacyDefault(engine, field string) string {
	switch field {
	case "bin":
		return engine
	case "home":
		_, home := Engines{}.Binary(engine)
		return home
	}
	return ""
}

// floorFrom is the floor an old used-percent limit leaves, and whether it
// differs from the default.
func floorFrom(used float64) (int, bool) {
	switch {
	case used == 0:
		return 0, true
	case used == 90:
		return 0, false
	}
	return int(math.Max(0, math.Round(100-used))), true
}

// section is the object at key in parent, made if it isn't there.
func section(parent map[string]any, key string) map[string]any {
	if child, ok := parent[key].(map[string]any); ok {
		return child
	}
	child := map[string]any{}
	parent[key] = child
	return child
}

// ConvertLegacyJSON is a config body in any layout, in the current one: what
// an open dashboard from before an upgrade still sends.
func ConvertLegacyJSON(data []byte) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if !convertLegacy(doc) {
		return data, nil
	}
	return json.Marshal(doc)
}
