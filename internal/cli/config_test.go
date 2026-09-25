package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
)

func runConfig(t *testing.T, dir string, args ...string) (map[string]any, error) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg-config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "xdg-state"))
	root := NewRoot("test")
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs(append([]string{"--config", filepath.Join(dir, "config.json"), "--state", filepath.Join(dir, "state.db"), "config"}, args...))
	if err := root.Execute(); err != nil {
		return nil, err
	}
	var record map[string]any
	if err := json.Unmarshal(out.Bytes(), &record); err != nil {
		t.Fatalf("%v: %q", err, out.String())
	}
	return record, nil
}

func TestConfigGetSetUnsetOnAFileInTheEarlierLayout(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.json")
	old := `{"model":{"engine":"claude","model":"claude-opus-5","effort":"high","max_tokens":4096,"claude_home":"/synthetic/claude"},"limits":{"max_model_calls_per_day":100,"max_model_turns":8,"role_usage":{"claude_max_used_percent":98}},"future":{"kept":true}}`
	if err := os.WriteFile(file, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := runConfig(t, dir, "get", "engines.claude.usage_floor.1w_percent")
	if err != nil || got["value"] != "2" {
		t.Fatalf("old limit not read as a floor: %v %v", got, err)
	}
	if _, err := runConfig(t, dir, "set", "engines.claude.usage_floor.5h_percent", "101"); err == nil {
		t.Fatal("a floor over 100 was accepted")
	}
	if _, err := runConfig(t, dir, "set", "engines.claude.usage_floor.5h_percent", "0"); err != nil {
		t.Fatal(err)
	}
	if got, _ := runConfig(t, dir, "get", "engines.claude.usage_floor.5h_percent"); got["value"] != "0" || got["set"] != true {
		t.Fatalf("an explicit 0 was lost: %v", got)
	}
	if got, _ := runConfig(t, dir, "get", "engines.claude.home"); got["value"] != "/synthetic/claude" || got["set"] != true {
		t.Fatalf("the old home did not move: %v", got)
	}
	if got, err := runConfig(t, dir, "unset", "engines.claude.usage_floor.1w_percent"); err != nil || got["value"] != "" || got["set"] != false {
		t.Fatalf("unset left the floor: %v %v", got, err)
	}
	if got, _ := runConfig(t, dir, "get", "future.kept"); got["value"] != "true" || got["known_key"] != false {
		t.Fatalf("an unknown key can't be read: %v", got)
	}
	if got, err := runConfig(t, dir, "unset", "future"); err != nil || got["unset"] != true {
		t.Fatalf("an unknown key can't be removed: %v %v", got, err)
	}
	if _, err := runConfig(t, dir, "set", "model.engine", "gemini"); err == nil {
		t.Fatal("an unknown engine was accepted")
	}
	data, _ := os.ReadFile(file)
	for _, gone := range []string{"role_usage", "claude_home", "1w_percent", "future"} {
		if strings.Contains(string(data), gone) {
			t.Fatalf("%s still in the file:\n%s", gone, data)
		}
	}
}

// Each engine's keys write to that engine and leave the other as it was.
func TestEngineKeysLandOnTheirEngine(t *testing.T) {
	for _, name := range []string{"codex", "claude"} {
		dir := t.TempDir()
		for key, value := range map[string]string{"bin": "/opt/" + name, "home": "/synthetic/" + name, "usage_floor.5h_percent": "3", "usage_floor.1w_percent": "4", "on_unknown_usage": "pause"} {
			if _, err := runConfig(t, dir, "set", "engines."+name+"."+key, value); err != nil {
				t.Fatal(key, err)
			}
		}
		cfg, err := config.Load(filepath.Join(dir, "config.json"))
		if err != nil {
			t.Fatal(err)
		}
		set, _ := cfg.Engines.CLI(name)
		if set.Bin != "/opt/"+name || set.Home != "/synthetic/"+name || *set.UsageFloor.FiveHourPercent != 3 || *set.UsageFloor.WeekPercent != 4 || set.OnUnknownUsage != "pause" {
			t.Fatalf("%s: %+v", name, set)
		}
		other := "claude"
		if name == "claude" {
			other = "codex"
		}
		if untouched, _ := cfg.Engines.CLI(other); !reflect.DeepEqual(untouched, config.CLIEngine{}) {
			t.Fatalf("setting %s changed %s: %+v", name, other, untouched)
		}
	}
}
