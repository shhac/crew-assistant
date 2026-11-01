//go:build !windows

package work

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shhac/lib-agent-harness/sandbox"
)

func TestRunCheckScopeAndLegacyResult(t *testing.T) {
	for _, tc := range []struct {
		name           string
		scoped, record bool
		want, scope    string
	}{
		{"legacy", false, false, "make check", ""},
		{"scoped", true, true, "make scoped", "scoped"},
		{"missing record", true, false, "make check", "full"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lp, m, source := checkFixture(t)
			m.playbook.Check = "make check"
			if tc.scoped {
				m.playbook.ScopedCheck = "make scoped"
			}
			if tc.record {
				os.WriteFile(filepath.Join(source, ".git", "crew-base"), []byte("base"), 0600)
				os.WriteFile(filepath.Join(source, ".git", "crew-changed-files"), []byte("main.go\n"), 0600)
			}
			result := sandbox.CommandResult{Stdout: "passed"}
			lp.commands = func(_ context.Context, o sandbox.Options) (commandSandbox, error) {
				return &fakeCommands{run: func(_ context.Context, req sandbox.CommandRequest) (sandbox.CommandResult, error) {
					if req.Command != tc.want {
						t.Errorf("command=%s want=%s", req.Command, tc.want)
					}
					return result, nil
				}}, nil
			}
			runs := newCheckRuns(lp, m, source, nil)
			defer runs.close()
			out, err := runs.call(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !tc.scoped {
				legacy := `{"exit_code":0,"stdout":"passed","stderr":"","timed_out":false,"truncated":false}`
				if out != legacy {
					t.Fatalf("legacy output changed: %s != %s", out, legacy)
				}
				return
			}
			var reply struct {
				Scope string `json:"scope"`
				Note  string `json:"scope_note"`
			}
			if err = json.Unmarshal([]byte(out), &reply); err != nil {
				t.Fatal(err)
			}
			if reply.Scope != tc.scope || reply.Note == "" {
				t.Fatal(out)
			}
			if tc.record && (!strings.Contains(reply.Note, "1 changed files") || !strings.Contains(reply.Note, "before approval")) {
				t.Fatal(out)
			}
		})
	}
}
