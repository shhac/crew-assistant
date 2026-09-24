package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/spf13/cobra"
)

func completionRoot(t *testing.T) *cobra.Command {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	return NewRoot("test")
}

func TestConfigCompletionsMatchSetTraversal(t *testing.T) {
	root := completionRoot(t)
	set, _, err := root.Find([]string{"config", "set"})
	if err != nil || set.ValidArgsFunction == nil {
		t.Fatal("config set completion missing", err)
	}
	got, directive := set.ValidArgsFunction(set, nil, "assistant.avatar")
	want := []string{"assistant.avatar", "assistant.avatar.accent", "assistant.avatar.background", "assistant.avatar.shape"}
	if !reflect.DeepEqual(got, want) || directive != cobra.ShellCompDirectiveNoFileComp {
		t.Fatalf("avatar keys = %v, %v", got, directive)
	}
	// Every suggested path exists in the JSON object config set traverses.
	raw, _ := json.Marshal(config.Default())
	var object map[string]any
	_ = json.Unmarshal(raw, &object)
	keys, _ := set.ValidArgsFunction(set, nil, "")
	for _, key := range keys {
		var value any = object
		for _, part := range strings.Split(key, ".") {
			parent, ok := value.(map[string]any)
			if !ok {
				t.Fatalf("completion %s traverses a non-object", key)
			}
			value, ok = parent[part]
			if !ok {
				t.Fatalf("completion %s is not a configuration key", key)
			}
		}
	}
	for _, tc := range []struct {
		key, prefix string
		want        []string
	}{
		{"model.effort", "m", []string{"max", "medium", "minimal"}},
		{"dashboard.tailscale", "", []string{"off", "serve"}},
		{"assistant.theme", "da", []string{"dark"}},
		{"model.api_key_env", "", nil},
		{"connections", "", nil},
	} {
		values, d := set.ValidArgsFunction(set, []string{tc.key}, tc.prefix)
		if !reflect.DeepEqual(values, tc.want) || d != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("%s = %v, %v; want %v", tc.key, values, d, tc.want)
		}
	}
	values, _ := set.ValidArgsFunction(set, []string{"assistant.name", "Iris"}, "")
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
