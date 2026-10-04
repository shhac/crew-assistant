package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/autopilot"
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/spf13/cobra"
)

func completionRoot(t *testing.T) *cobra.Command {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	return NewRoot("test")
}

// Every setting in the file has a key, and every key names a setting in the
// file, so get, set and unset reach the same paths the file holds.
func TestConfigKeysCoverTheFile(t *testing.T) {
	root := completionRoot(t)
	set, _, err := root.Find([]string{"config", "set"})
	if err != nil || set.ValidArgsFunction == nil {
		t.Fatal("config set completion missing", err)
	}
	keys, directive := set.ValidArgsFunction(set, nil, "")
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Fatalf("directive %v", directive)
	}
	c := config.Default()
	// Mode rows are optional map entries, like the optional engine floors
	// populated below. Include every compiled row in the file/key coverage.
	c.Autopilot.Modes = map[string]autopilot.Mode{}
	for _, function := range autopilot.Catalog() {
		c.Autopilot.Modes[function.ID] = function.DefaultMode
	}
	floor, runs := 10, 2
	for _, engine := range []*config.CLIEngine{&c.Engines.Codex, &c.Engines.Claude} {
		*engine = config.CLIEngine{Bin: "x", Home: "/x", UsageFloor: config.UsageFloor{FiveHourPercent: &floor, WeekPercent: &floor}, OnUnknownUsage: "pause", RoleRuns: &runs}
	}
	c.Engines.Codex.BrowserBridgeHome = "/bridge"
	// Grok reports no usage windows, so it has no floors to set.
	c.Engines.Grok = config.CLIEngine{Bin: "x", Home: "/x"}
	c.Engines.OpenAICompatible.EffortParameter = "reasoning.effort"
	c.Engines.Providers = []config.Provider{{ID: "openrouter", Name: "OpenRouter"}}
	c.Models.Suggestions.Provider = "openrouter"
	raw, _ := json.Marshal(c)
	var object map[string]any
	_ = json.Unmarshal(raw, &object)
	var leaves []string
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		m, ok := v.(map[string]any)
		if !ok || slices.Contains(keys, prefix) {
			leaves = append(leaves, prefix)
			return
		}
		for k, child := range m {
			walk(strings.TrimPrefix(prefix+"."+k, "."), child)
		}
	}
	walk("", object)
	slices.Sort(leaves)
	if !reflect.DeepEqual(leaves, keys) {
		t.Fatalf("file paths and keys differ:\nfile %v\nkeys %v", leaves, keys)
	}
	for _, tc := range []struct {
		key, prefix string
		want        []string
	}{
		{"models.suggestions.effort", "m", []string{"max", "medium", "minimal"}},
		{"models.suggestions.engine", "", []string{"claude", "codex", "grok", "openai-compatible"}},
		{"assistant.seat", "", []string{"milo"}},
		{"dashboard.tailscale", "", []string{"off", "serve"}},
		{"upgrade.mode", "", []string{"ask", "automatic", "off"}},
		{"engines.claude.on_unknown_usage", "", []string{"allow", "pause"}},
		{"assistant.theme", "da", []string{"dark"}},
		{"engines.openai-compatible.api_key_env", "", nil},
		{"connections", "", nil},
	} {
		values, d := set.ValidArgsFunction(set, []string{tc.key}, tc.prefix)
		if !reflect.DeepEqual(values, tc.want) || d != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("%s = %v, %v; want %v", tc.key, values, d, tc.want)
		}
	}
	if _, d := set.ValidArgsFunction(set, []string{"engines.codex.home"}, ""); d != cobra.ShellCompDirectiveFilterDirs {
		t.Error("a home should complete directories")
	}
	values, _ := set.ValidArgsFunction(set, []string{"assistant.seat", "milo"}, "")
	if len(values) != 0 {
		t.Fatal("offered a third argument")
	}
}

func TestCompletionProtocolDoesNotCreateConfigOrState(t *testing.T) {
	root := completionRoot(t)
	paths, err := config.Paths()
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"__complete", "model", "login", "--engine", ""})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "claude\ncodex\n:4") {
		t.Fatalf("completion output: %q", output.String())
	}
	for _, path := range []string{filepath.Dir(paths.Config), filepath.Dir(paths.State)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("completion created %s: %v", path, err)
		}
	}
}

func TestGeneratedShellCompletionSyntax(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			root := completionRoot(t)
			var output bytes.Buffer
			root.SetOut(&output)
			root.SetErr(&bytes.Buffer{})
			root.SetArgs([]string{"completion", shell})
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), "crew-assistant") || output.Len() < 100 {
				t.Fatal("missing generated completion script")
			}
			binary, err := exec.LookPath(shell)
			if err != nil {
				t.Skipf("script generated; %s unavailable for syntax check", shell)
			}
			cmd := exec.Command(binary, "-n")
			cmd.Stdin = bytes.NewReader(output.Bytes())
			if result, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("syntax: %s: %v", result, err)
			}
		})
	}
}
