package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	libcli "github.com/shhac/lib-agent-cli/cli"
	"github.com/spf13/cobra"
)

func TestEnginePauseCommands(t *testing.T) {
	for _, args := range [][]string{
		{"pause", "claude", "--for", "1h"}, {"pause", "claude", "--until", "18:00"}, {"pause", "claude"},
		{"resume", "claude"}, {"status"},
	} {
		t.Run(args[0]+"/"+args[len(args)-1], func(t *testing.T) {
			called := false
			o := standIn(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				if args[0] == "status" {
					if r.Method != "GET" || r.URL.Path != "/api/engines" {
						t.Fatal(r.Method, r.URL)
					}
					_, _ = w.Write([]byte(`[{"engine":"claude","state":"running"},{"engine":"codex","state":"paused"}]`))
					return
				}
				if r.Method != "PUT" || r.URL.Path != "/api/engines/claude/paused" {
					t.Fatal(r.Method, r.URL)
				}
				var in struct {
					Paused bool
					Until  *time.Time
				}
				if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
					t.Fatal(err)
				}
				if in.Paused != (args[0] == "pause") {
					t.Fatal(in)
				}
				if len(args) == 4 {
					if in.Until == nil || !in.Until.After(time.Now()) {
						t.Fatal(in)
					}
					if args[2] == "--for" && time.Until(*in.Until) < 59*time.Minute {
						t.Fatal(in)
					}
					if args[2] == "--until" && in.Until.Local().Format("15:04") != "18:00" {
						t.Fatal(in)
					}
				}
				_, _ = w.Write([]byte(`{"paused":true}`))
			}), "token")
			o.globals = &libcli.Globals{Format: "ndjson"}
			root := &cobra.Command{Use: "test"}
			registerEngine(root, o)
			root.SetArgs(append([]string{"engine"}, args...))
			output, err := os.CreateTemp(t.TempDir(), "engine-output")
			if err != nil {
				t.Fatal(err)
			}
			original := os.Stdout
			os.Stdout = output
			err = root.Execute()
			os.Stdout = original
			if err != nil {
				t.Fatal(err)
			}
			if err := output.Close(); err != nil {
				t.Fatal(err)
			}
			emitted, err := os.ReadFile(output.Name())
			if err != nil {
				t.Fatal(err)
			}
			if args[0] == "status" {
				lines := strings.Split(strings.TrimSpace(string(emitted)), "\n")
				if len(lines) != 2 {
					t.Fatal("expected one item per engine", string(emitted))
				}
				for _, line := range lines {
					var item map[string]any
					if err := json.Unmarshal([]byte(line), &item); err != nil {
						t.Fatal(err)
					}
					if item["engine"] == nil || item["state"] == nil {
						t.Fatal(item)
					}
				}
			}
			if !called {
				t.Fatal("no request")
			}
		})
	}
}

func TestEnginePauseRejectsConflictingTimesBeforeRequest(t *testing.T) {
	o := standIn(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("request before validation") }), "token")
	root := &cobra.Command{Use: "test"}
	registerEngine(root, o)
	root.SetArgs([]string{"engine", "pause", "claude", "--for", "1h", "--until", "18:00"})
	if err := root.Execute(); err == nil {
		t.Fatal("conflicting flags accepted")
	}
}

func TestEnginePauseUntilParsing(t *testing.T) {
	now := time.Date(2026, 10, 3, 19, 0, 0, 0, time.FixedZone("local", 3600))
	end, err := pauseUntil("", "18:00", now)
	if err != nil || end.Day() != 4 || end.Hour() != 18 {
		t.Fatal(end, err)
	}
	for _, value := range []string{"bad", "2000-01-01T00:00:00Z"} {
		if _, err := pauseUntil("", value, now); err == nil {
			t.Fatal(value)
		}
	}
	if _, err := pauseUntil("-1h", "", now); err == nil {
		t.Fatal("negative accepted")
	}
}
