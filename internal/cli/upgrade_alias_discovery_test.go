package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/lifecycle"
	"github.com/shhac/crew-assistant/internal/upgrade"
)

type aliasTransport func(*http.Request) (*http.Response, error)

func (f aliasTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

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
					t.Skip(err)
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
				for name, body := range map[string]string{"daemon.json": `{"url":"http://dashboard.fixture","local_url":"http://daemon.fixture"}`, "admin-token": "synthetic-token"} {
					if err := os.WriteFile(filepath.Join(runtime, name), []byte(body), 0600); err != nil {
						t.Fatal(err)
					}
				}
				original := http.DefaultTransport
				defer func() { http.DefaultTransport = original }()
				requests := []string{}
				http.DefaultTransport = aliasTransport(func(r *http.Request) (*http.Response, error) {
					if r.URL.Host != "daemon.fixture" || r.Header.Get("Authorization") != "Bearer synthetic-token" {
						t.Fatal("wrong daemon or token", r.URL)
					}
					requests = append(requests, r.Method+" "+r.URL.Path)
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"status":"fixture"}`)), Header: make(http.Header)}, nil
				})
				for _, spelling := range []string{state, alias} {
					if got := aliasCommand(t, spelling, config, "status"); got["status"] != "fixture" {
						t.Fatal(got)
					}
					got := aliasCommand(t, spelling, config, "dashboard", "open", "--print")
					code, err := os.ReadFile(filepath.Join(runtime, "pairing-code"))
					if err != nil || got["code"] != string(code) || got["url"] != "http://dashboard.fixture" {
						t.Fatal(got, err)
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
