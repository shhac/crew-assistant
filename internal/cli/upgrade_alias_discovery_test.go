package cli

import (
	"context"
	"encoding/json"
	"github.com/shhac/crew-assistant/internal/testutil"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/lifecycle"
	"github.com/shhac/crew-assistant/internal/upgrade"
)

func aliasCommand(t *testing.T, state, config string, args ...string) map[string]any {
	t.Helper()
	out, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	original := os.Stdout
	os.Stdout = out
	defer func() { os.Stdout = original }()
	root := NewRoot("v1.0.0")
	root.SetArgs(append([]string{"--state", state, "--config", config}, args...))
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := out.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.NewDecoder(out).Decode(&got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestCommandsDiscoverDaemonAndUpgradeThroughAliases(t *testing.T) {
	for _, fileAlias := range []bool{false, true} {
		for _, aliasStart := range []bool{false, true} {
			t.Run(map[bool]string{true: "file", false: "directory"}[fileAlias]+map[bool]string{true: "/alias-start", false: "/real-start"}[aliasStart], func(t *testing.T) {
				realDir, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				state, config := filepath.Join(realDir, "state.db"), filepath.Join(realDir, "config.json")
				if err := os.WriteFile(state, []byte("synthetic state"), 0600); err != nil {
					t.Fatal(err)
				}
				alias := filepath.Join(t.TempDir(), "elsewhere")
				target := realDir
				if fileAlias {
					target = state
				}
				if err := os.Symlink(target, alias); err != nil {
					testutil.RequireSymlinkResult(t, err)
				}
				if !fileAlias {
					alias = filepath.Join(alias, "state.db")
				}
				startPath := state
				if aliasStart {
					startPath = alias
				}
				o := &options{statePath: startPath, configPath: config, version: "v1.0.0"}
				stop, end := lifecycle.Watch(context.Background(), make(chan os.Signal), func(string) {})
				defer end()
				h, err := prepareUpgradeStart(o, stop, false)
				if err != nil {
					t.Fatal(err)
				}
				if o.statePath != state {
					t.Fatalf("startup selected %s, want %s", o.statePath, state)
				}
				if err := h.lock.Unlock(); err != nil {
					t.Fatal(err)
				}
				// Fixtures are written at the independently resolved real destination,
				// never through runtimeDir or an alias-based journal lookup.
				runtime := state + ".runtime"
				if err := os.MkdirAll(runtime, 0700); err != nil {
					t.Fatal(err)
				}
				sockets, err := os.MkdirTemp("", "crew-alias-")
				if err != nil {
					t.Fatal(err)
				}
				defer os.RemoveAll(sockets)
				socket := filepath.Join(sockets, "owner.sock")
				listener, err := net.Listen("unix", socket)
				if err != nil {
					t.Skip("this environment refuses a Unix socket:", err)
				}
				requests := []string{}
				daemon := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/api/pairing" {
						w.Write([]byte(`{"code":"fixture-code","expires":"5 minutes"}`))
						return
					}
					requests = append(requests, r.Method+" "+r.URL.Path)
					w.Write([]byte(`{"status":"fixture"}`))
				})}
				go daemon.Serve(listener)
				defer daemon.Close()
				record := `{"url":"http://dashboard.fixture","local_url":"http://daemon.fixture","socket":"` + socket + `"}`
				if err := os.WriteFile(filepath.Join(runtime, "daemon.json"), []byte(record), 0600); err != nil {
					t.Fatal(err)
				}
				for _, spelling := range []string{state, alias} {
					if got := aliasCommand(t, spelling, config, "status"); got["status"] != "fixture" {
						t.Fatal(got)
					}
					got := aliasCommand(t, spelling, config, "dashboard", "open", "--print")
					if got["code"] != "fixture-code" || got["url"] != "http://dashboard.fixture" {
						t.Fatal(got)
					}
					aliasCommand(t, spelling, config, "upgrade", "clear-rollback")
				}
				if strings.Join(requests, ",") != "GET /api/state,POST /api/upgrade/clear-rollback,GET /api/state,POST /api/upgrade/clear-rollback" {
					t.Fatal(requests)
				}
				if err := os.RemoveAll(runtime); err != nil {
					t.Fatal(err)
				}
				r := upgrade.Record{Step: upgrade.RolledBack, From: "v1.0.0", To: "v2.0.0", Pinned: true, Failure: "fixture failure"}
				if err := upgrade.WriteRecord(upgrade.RecordPath(state), r); err != nil {
					t.Fatal(err)
				}
				// Atomic replacement must preserve file aliases and journal identity.
				replacement := filepath.Join(realDir, "replacement")
				if err := os.WriteFile(replacement, []byte("restored state"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(replacement, state); err != nil {
					t.Fatal(err)
				}
				for _, spelling := range []string{state, alias} {
					got := aliasCommand(t, spelling, config, "upgrade", "status")
					if got["rollback_in_force"] != true || got["failure"] != r.Failure {
						t.Fatal(got)
					}
				}
			})
		}
	}
}
