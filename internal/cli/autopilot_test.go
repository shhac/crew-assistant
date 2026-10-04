package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofrs/flock"
	"github.com/shhac/crew-assistant/internal/app"
	"github.com/shhac/crew-assistant/internal/autopilot"
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/server"
	libcli "github.com/shhac/lib-agent-cli/cli"
	"github.com/shhac/lib-agent-output"
)

func TestAutopilotCLIModeSavesThroughDaemonWithRuntimeOverrides(t *testing.T) {
	o := standIn(t, nil, "unused")
	o.configPath = filepath.Join(filepath.Dir(o.statePath), "config.json")
	o.globals = &libcli.Globals{Format: string(output.FormatNDJSON)}
	disk := config.Default()
	if err := config.Save(o.configPath, disk); err != nil {
		t.Fatal(err)
	}
	effective := disk
	effective.Dashboard.Addr = "127.0.0.1:8341"
	effective.Dashboard.Tailscale = "serve"
	effective.Dashboard.TailscalePort = 10000
	effective.Dashboard.AllowedUsers = []string{"synthetic@example.test"}
	store, err := core.Open(o.statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a := app.New(core.NewService(store, effective), effective, o.configPath, app.Options{})
	auth, err := server.NewAuth(o.runtimeDir(), "http://daemon.invalid", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := server.New(a, auth)
	o.transport = handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = "127.0.0.1:12345"
		handler.ServeHTTP(w, r)
	})}
	lock := flock.New(o.statePath + ".lock")
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer lock.Unlock()
	p, err := a.Core.CreateProject(t.Context(), core.ProjectInput{Title: "Synthetic", Brief: core.BriefInput{Goal: "Test", Criteria: []string{"Test"}}, Template: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := a.Autopilot.Register("ci-failures", "v1", []string{"rename-project"}, func(core.Snapshot, core.ConcreteAction) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"set", "ci-failures", "act"}, {"unset", "ci-failures"}} {
		cmd := autopilotCommand(o)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if _, err := a.ReloadConfig(); err != nil {
			t.Fatalf("watcher reconciliation after %v: %v", args, err)
		}
		v, err := a.Core.Snapshot(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		current, _ := v.Project(p.ID)
		out, err := f.Submit(t.Context(), args[0], "reason", core.ConcreteAction{Kind: "rename-project", ProjectID: p.ID, TargetVersion: current.TitleRevision, Args: json.RawMessage(`{"title":"Updated"}`)})
		want := "performed"
		if args[0] == "unset" {
			want = "proposed"
		}
		if err != nil || out.Status != want {
			t.Fatalf("admission after watcher reload: %+v %v", out, err)
		}
	}
	loaded, err := config.Load(o.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Autopilot.Revisions["ci-failures"] != 2 || a.Config().Autopilot.Revisions["ci-failures"] != 2 {
		t.Fatal("daemon did not apply CLI edits")
	}
	if loaded.Dashboard.Addr != disk.Dashboard.Addr || a.Config().Dashboard.Addr != effective.Dashboard.Addr || a.Config().Dashboard.Tailscale != "serve" || a.Config().Dashboard.TailscalePort != 10000 {
		t.Fatal("mode edit changed dashboard settings")
	}
}

func TestAutopilotOfflineCLI(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"catalog"}, {"get", "landing-release-operator"}, {"set", "ci-failures", "act"}, {"get", "ci-failures"}, {"unset", "ci-failures"}} {
		root := NewRoot("test")
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		root.SetArgs(append([]string{"--config", filepath.Join(dir, "config.json"), "--state", filepath.Join(dir, "state.db"), "autopilot"}, args...))
		if err := root.Execute(); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	c, err := config.Load(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	mode, _ := c.Autopilot.EffectiveMode("ci-failures")
	if mode != autopilot.Suggest || c.Autopilot.Revisions["ci-failures"] != 2 {
		t.Fatal("unset/persistence failed")
	}
}

func TestAutopilotCLIRequestsBindRevision(t *testing.T) {
	var path string
	var body map[string]any
	o := standIn(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-token" {
			t.Error("missing owner authentication")
		}
		path = r.URL.RequestURI()
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"performed"}`))
	}), "synthetic-token")
	o.globals = &libcli.Globals{Format: string(output.FormatNDJSON)}
	for _, args := range [][]string{{"approve", "action-id", "3"}, {"cancel", "action-id", "3"}, {"undo", "action-id", "3"}, {"permission", "project-id", "true", "4"}, {"history", "--project", "project-id", "--forward", "--limit", "20"}} {
		cmd := autopilotCommand(o)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if args[0] == "history" {
			if !strings.Contains(path, "forward=true") || !strings.Contains(path, "limit=20") {
				t.Fatal("history bounds missing")
			}
		} else {
			expected := float64(3)
			if args[0] == "permission" {
				expected = 4
			}
			if body["revision"] != expected {
				t.Fatalf("missing revision: %v", body)
			}
			if _, ok := body["actor"]; ok {
				t.Fatal("CLI supplied actor")
			}
		}
	}
}
