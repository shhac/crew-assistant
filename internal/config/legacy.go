package config

import (
	"encoding/json"
	"math"
	"strings"
)

// renamedKeys says where each key of an earlier layout went, so a file or a
// dashboard written before still works and a report of an unknown key can
// say where it moved. The model section's moves and the dropped keys are
// carried out from this table; the usage limits change meaning as well as
// place, so convertRoleUsage does those, and the assistant's own settings
// become a profile together, so convertProfile does those.
var renamedKeys = map[string]string{
	"assistant.name":              profileKey + ".name",
	"assistant.personality":       profileKey + ".personality",
	"assistant.avatar":            profileKey + ".avatar",
	"model":                       profileKey + ".model, and its CLI and endpoint settings engines.<engine>",
	"model.engine":                profileKey + ".model.engine",
	"model.model":                 profileKey + ".model.model",
	"model.effort":                profileKey + ".model.effort",
	"model.max_tokens":            profileKey + ".model.max_tokens",
	"model.codex_bin":             "engines.codex.bin",
	"model.codex_home":            "engines.codex.home",
	"model.claude_bin":            "engines.claude.bin",
	"model.claude_home":           "engines.claude.home",
	"model.base_url":              "engines.openai-compatible.base_url",
	"model.api_key_env":           "engines.openai-compatible.api_key_env",
	"limits.role_usage":           "engines.<engine>.usage_floor and on_unknown_usage",
	"chat.loading_phrases.model":  "",
	"chat.loading_phrases.effort": "",
}

// RenamedKey is where an earlier layout's key went, if it is one: "" when it
// was dropped.
func RenamedKey(path string) (string, bool) {
	to, ok := renamedKeys[path]
	return to, ok
}

// convertLegacy rewrites a config document in an earlier layout into the
// current one, in place, and reports whether it changed anything.
func convertLegacy(doc map[string]any) bool {
	engine := defaultAPIEngine(doc)
	moved := moveRenamedKeys(doc)
	usage := convertRoleUsage(doc)
	profile := convertProfile(doc)
	// A model section whose settings all moved away is gone too.
	if model, ok := doc["model"].(map[string]any); ok && len(model) == 0 {
		delete(doc, "model")
	}
	return engine || moved || usage || profile
}

// profileKey is where the seated assistant's settings went: its profile.
const profileKey = "assistants[<seat>]"

// convertProfile makes the assistant of a layout from before profiles, its
// name, personality and avatar and the model section's choice, into the
// first profile, and seats it. What the file left out is the default. Its id
// comes from its name, so converting the same file again gives the same one.
func convertProfile(doc map[string]any) bool {
	if _, current := doc["assistants"]; current {
		return false
	}
	assistant, _ := doc["assistant"].(map[string]any)
	model, _ := doc["model"].(map[string]any)
	old, oldModel := map[string]any{}, map[string]any{}
	for _, key := range []string{"name", "personality", "avatar"} {
		if value, ok := assistant[key]; ok {
			old[key] = value
			delete(assistant, key)
		}
	}
	for _, key := range []string{"engine", "model", "effort", "max_tokens"} {
		if value, ok := model[key]; ok {
			oldModel[key] = value
			delete(model, key)
		}
	}
	if len(old) == 0 && len(oldModel) == 0 {
		return false
	}
	var profile map[string]any
	raw, _ := json.Marshal(DefaultProfile())
	_ = json.Unmarshal(raw, &profile)
	for key, value := range old {
		profile[key] = value
	}
	for key, value := range oldModel {
		profile["model"].(map[string]any)[key] = value
	}
	name, _ := profile["name"].(string)
	profile["id"] = ProfileID(name)
	doc["assistants"] = []any{profile}
	section(doc, "assistant")["seat"] = profile["id"]
	return true
}

// defaultAPIEngine reads a model section with no engine as an API
// configuration from before engines were chosen, which keeps its provider
// and billing path. It has to run before the section's settings move away.
func defaultAPIEngine(doc map[string]any) bool {
	model, ok := doc["model"].(map[string]any)
	if _, current := doc["assistants"]; !ok || current {
		return false
	}
	if _, explicit := model["engine"]; explicit {
		return false
	}
	model["engine"] = "openai-compatible"
	for _, key := range []string{"model", "effort"} {
		if _, ok := model[key]; !ok {
			model[key] = ""
		}
	}
	return true
}

// moveRenamedKeys carries each renamed key to where it went, and removes
// the dropped ones. Earlier versions saved every default, and a CLI's
// default stays one rather than being written down; the endpoint's settings
// move as they are, since an empty key there means none.
func moveRenamedKeys(doc map[string]any) bool {
	changed := false
	for from, to := range renamedKeys {
		if from == "limits.role_usage" || strings.HasPrefix(to, profileKey) {
			continue
		}
		value, ok := removePath(doc, from)
		if !ok {
			continue
		}
		changed = true
		if s, _ := value.(string); to == "" || isCLIDefault(to, s) {
			continue
		}
		parts := strings.Split(to, ".")
		parent := doc
		for _, part := range parts[:len(parts)-1] {
			parent = section(parent, part)
		}
		parent[parts[len(parts)-1]] = value
	}
	return changed
}

// isCLIDefault says whether value at path is what a blank CLI setting
// means anyway.
func isCLIDefault(path, value string) bool {
	parts := strings.Split(path, ".")
	if len(parts) != 3 {
		return false
	}
	if (&Engines{}).CLIRef(parts[1]) == nil {
		return false
	}
	bin, home := DefaultBinary(parts[1])
	switch parts[2] {
	case "bin":
		return value == "" || value == bin
	case "home":
		return value == "" || value == home
	}
	return false
}

// convertRoleUsage turns a used-percent limit into the floor it leaves: 0
// stays off, the old default of 90 becomes the default, and anything else
// leaves 100 minus it on both windows. A pause while usage couldn't be read
// applied to every engine, and still does.
func convertRoleUsage(doc map[string]any) bool {
	limits, ok := doc["limits"].(map[string]any)
	if !ok {
		return false
	}
	usage, ok := limits["role_usage"].(map[string]any)
	if !ok {
		return false
	}
	delete(limits, "role_usage")
	pause, _ := usage["on_unavailable"].(string)
	// The old layout knew only these two.
	for _, engine := range []string{"codex", "claude"} {
		used, limited := usage[engine+"_max_used_percent"].(float64)
		floor, set := floorFrom(used)
		if limited && set {
			floors := section(section(section(doc, "engines"), engine), "usage_floor")
			floors["5h_percent"], floors["1w_percent"] = floor, floor
		}
		if pause == OnUnknownUsagePause {
			section(section(doc, "engines"), engine)["on_unknown_usage"] = pause
		}
	}
	return true
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

// removePath takes the value at a dotted path out of doc, if it is there.
func removePath(doc map[string]any, path string) (any, bool) {
	parts := strings.Split(path, ".")
	parent := doc
	for _, part := range parts[:len(parts)-1] {
		child, ok := parent[part].(map[string]any)
		if !ok {
			return nil, false
		}
		parent = child
	}
	value, ok := parent[parts[len(parts)-1]]
	delete(parent, parts[len(parts)-1])
	return value, ok
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

// convertDocument parses a config document and converts it to the current
// layout, reporting whether it had to.
func convertDocument(data []byte) (map[string]any, bool, error) {
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, false, err
	}
	return doc, convertLegacy(doc), nil
}

// ConvertLegacyJSON is a config body in any layout, in the current one: what
// an open dashboard from before an upgrade still sends.
func ConvertLegacyJSON(data []byte) ([]byte, error) {
	doc, changed, err := convertDocument(data)
	if err != nil || !changed {
		return data, err
	}
	return json.Marshal(doc)
}
