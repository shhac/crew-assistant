package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnEarlierLayoutLoadsAndIsRewrittenOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	old := `{
  "//": "the owner's note",
  "model": {"engine": "claude", "model": "opus", "effort": "", "max_tokens": 4096,
            "codex_bin": "codex", "codex_home": "` + DefaultCodexHome() + `",
            "claude_bin": "/opt/claude", "claude_home": "/Users/me/.claude-work",
            "base_url": "https://api.openai.com/v1", "api_key_env": "OPENAI_API_KEY"},
  "limits": {"max_model_turns": 7, "role_usage": {"codex_max_used_percent": 90, "claude_max_used_percent": 98, "on_unavailable": "pause"}},
  "chat": {"loading_phrases": {"enabled": true, "model": "x", "effort": "low"}},
  "future": {"setting": 1}
}`
	if err := os.WriteFile(path, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if bin, home := c.Engines.Binary("claude"); bin != "/opt/claude" || home != "/Users/me/.claude-work" || c.Engines.Codex.Home != "" || c.Engines.Codex.Bin != "" {
		t.Fatalf("engines %+v", c.Engines)
	}
	if five, week := c.Engines.Floors("claude"); five != 2 || week != 2 {
		t.Fatalf("a 98%% limit should leave 2%%: %d %d", five, week)
	}
	if c.Engines.Codex.UsageFloor.FiveHourPercent != nil || !c.Engines.PauseOnUnknownUsage("codex") || c.Limits.MaxModelTurns != 7 {
		t.Fatalf("the old default should stay a default, and pause carry over: %+v %+v", c.Engines.Codex, c.Limits)
	}
	if changed, err := Upgrade(path); err != nil || !changed {
		t.Fatalf("upgrade %v %v", changed, err)
	}
	data, _ := os.ReadFile(path)
	for _, gone := range []string{"codex_bin", "role_usage", `"model": "x"`, "max_used_percent"} {
		if strings.Contains(string(data), gone) {
			t.Fatalf("%s is still in the rewritten file:\n%s", gone, data)
		}
	}
	for _, kept := range []string{"the owner's note", `"future"`} {
		if !strings.Contains(string(data), kept) {
			t.Fatalf("%s was lost:\n%s", kept, data)
		}
	}
	if changed, _ := Upgrade(path); changed {
		t.Fatal("a file in the current layout was rewritten")
	}
	again, err := Load(path)
	if err != nil || again.Engines.Claude.Bin != "/opt/claude" {
		t.Fatalf("the rewritten file reads differently: %+v %v", again.Engines, err)
	}
	unknown := UnknownKeys(path)
	if len(unknown) != 1 || unknown[0].Path != "future" || unknown[0].Renamed {
		t.Fatalf("unknown %+v", unknown)
	}
	if err := Save(path, again); err != nil {
		t.Fatal(err)
	}
	if data, _ = os.ReadFile(path); !strings.Contains(string(data), `"future"`) {
		t.Fatal("saving dropped a key from a newer version")
	}
}

func TestAnOldDisabledLimitStaysOff(t *testing.T) {
	doc := map[string]any{"limits": map[string]any{"role_usage": map[string]any{"codex_max_used_percent": 0.0}}}
	convertLegacy(doc)
	raw, _ := json.Marshal(doc)
	var c Config
	json.Unmarshal(raw, &c)
	if five, week := c.Engines.Floors("codex"); five != 0 || week != 0 {
		t.Fatalf("a disabled limit became floors %d %d", five, week)
	}
}

func TestAKeyFromAnEarlierLayoutSaysWhereItWent(t *testing.T) {
	if to, ok := RenamedKey("model.codex_home"); !ok || to != "engines.codex.home" {
		t.Fatal(to, ok)
	}
	if _, ok := RenamedKey("model.engine"); ok {
		t.Fatal("a current key reported as renamed")
	}
	for key, want := range map[UnknownKey]string{
		{Path: "model.codex_home", Renamed: true, To: "engines.codex.home"}: "model.codex_home is now engines.codex.home",
		{Path: "chat.loading_phrases.model", Renamed: true}:                 "chat.loading_phrases.model is no longer a setting; remove it with crew-assistant config unset chat.loading_phrases.model",
		{Path: "futur"}: "futur is not a setting this version knows, so it has no effect; remove it with crew-assistant config unset futur",
	} {
		if key.String() != want {
			t.Errorf("%q", key.String())
		}
	}
}

func TestAnOldLimitLeavesItsFloor(t *testing.T) {
	for used, want := range map[float64]*int{0: intp(0), 1: intp(99), 50: intp(50), 89: intp(11), 90: nil, 91: intp(9), 97.6: intp(2), 100: intp(0)} {
		floor, set := floorFrom(used)
		if (want == nil) == set || (want != nil && floor != *want) {
			t.Errorf("%v%% used became %d (set %v)", used, floor, set)
		}
	}
}

func TestDefaultsAndAllowAreNotWrittenDown(t *testing.T) {
	_, claudeHome := DefaultBinary("claude")
	doc := map[string]any{
		"model":  map[string]any{"engine": "claude", "claude_bin": "claude", "claude_home": claudeHome, "codex_bin": ""},
		"limits": map[string]any{"role_usage": map[string]any{"codex_max_used_percent": 90.0, "on_unavailable": "allow"}},
	}
	if !convertLegacy(doc) {
		t.Fatal("nothing converted")
	}
	if _, ok := doc["engines"]; ok {
		t.Fatalf("defaults were written down: %v", doc["engines"])
	}
	if model := doc["model"].(map[string]any); len(model) != 1 {
		t.Fatalf("old keys left: %v", model)
	}
	untouched := map[string]any{"model": map[string]any{"engine": "codex"}}
	if convertLegacy(untouched) || len(untouched) != 1 {
		t.Fatalf("a current file changed: %v", untouched)
	}
}

// A file not yet rewritten says where its old sections went, not that they
// do nothing: they are still read.
func TestAnUnconvertedFileReportsItsOldKeysAsMoved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	old := `{"model":{"engine":"claude","claude_home":"/synthetic/claude"},"limits":{"role_usage":{"claude_max_used_percent":98}},"chat":{"loading_phrases":{"model":"x"}}}`
	if err := os.WriteFile(path, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	unknown := UnknownKeys(path)
	if len(unknown) != 3 {
		t.Fatalf("unknown %+v", unknown)
	}
	for _, k := range unknown {
		if !k.Renamed {
			t.Errorf("%s reported as having no effect: %s", k.Path, k)
		}
	}
}
