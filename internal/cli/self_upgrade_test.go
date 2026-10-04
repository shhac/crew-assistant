//go:build !windows

package cli

import (
	"context"
	"errors"
	"github.com/gofrs/flock"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/lifecycle"
	"github.com/shhac/crew-assistant/internal/server"
	"github.com/shhac/crew-assistant/internal/upgrade"
	libcli "github.com/shhac/lib-agent-cli/cli"
)

type localReleaseFixture struct{}

func (localReleaseFixture) Do(r *http.Request) (*http.Response, error) {
	body := `version "2.0.0"`
	if strings.Contains(r.URL.Path, "releases") {
		body = `{"tag_name":"v2.0.0","body":"Fixture release","html_url":"https://fixture.invalid/release"}`
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func waitUpgradeDaemon(t *testing.T, o *options, done <-chan error) runtimeInfo {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("daemon exited: %v", err)
		default:
		}
		if info, err := o.runtime(); err == nil {
			if _, err = o.request("GET", "/api/config", nil); err == nil {
				return info
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("daemon did not become ready")
	return runtimeInfo{}
}

func writeExecutableFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
}

func TestServeRunsRealBackupInstallerAndSessionHandover(t *testing.T) {
	for _, mode := range []string{"ask", "automatic"} {
		t.Run(mode, func(t *testing.T) {
			l := upgradeTestListener(t)
			address := l.Addr().String()
			l.Close()
			t.Setenv("HOMEBREW_NO_INSTALL_CLEANUP", "1")
			dir := t.TempDir()
			cfg := config.Default()
			cfg.Dashboard.Addr = address
			cfg.Upgrade.Mode = mode
			o := &options{upgradeIdentity: func(int) string { return "fixture" }, configPath: filepath.Join(dir, "config.json"), statePath: filepath.Join(dir, "state.db"), version: "v1.0.0", globals: &libcli.Globals{Format: "ndjson"}}
			if err := config.Save(o.configPath, cfg); err != nil {
				t.Fatal(err)
			}
			store, err := core.Open(o.statePath)
			if err != nil {
				t.Fatal(err)
			}
			s := core.NewService(store, cfg)
			s.OnUpgradeRequested(func(string) error { return nil })
			if err = s.RecordUpdateCheck(context.Background(), o.version, upgrade.Result{Available: "v2.0.0"}); err != nil {
				t.Fatal(err)
			}
			snapshot, _ := s.Snapshot(context.Background())
			store.Close()
			stop, cancel := lifecycle.Watch(context.Background(), make(chan os.Signal), func(string) {})
			defer cancel()
			h, err := prepareUpgradeStart(o, stop, false)
			if err != nil {
				t.Fatal(err)
			}
			defer h.lock.Unlock()
			o.upgradeHost = h
			h.prefix = filepath.Join(dir, "homebrew")
			h.executable = filepath.Join(h.prefix, "Cellar", "crew-assistant", "1.0.0", "bin", "crew-assistant")
			writeExecutableFixture(t, h.executable, "#!/bin/sh\necho 'crew-assistant v1.0.0'\n")
			writeExecutableFixture(t, filepath.Join(h.prefix, "bin", "brew"), "#!/bin/sh\n[ \"${HOMEBREW_NO_INSTALL_CLEANUP+x}\" != x ] || exit 7\n[ \"$1\" = upgrade ] || exit 8\n[ \"$2\" = "+cfg.Upgrade.Formula+" ] || exit 9\n")
			writeExecutableFixture(t, filepath.Join(h.prefix, "opt", "crew-assistant", "bin", "crew-assistant"), "#!/bin/sh\necho 'crew-assistant v2.0.0'\n")
			h.checker = upgrade.New(o.version, localReleaseFixture{}, nil)
			watchStarted := false
			h.startDetached = func(binary string, args []string, log string) error {
				r, err := upgrade.ReadRecord(h.engine.Path)
				if err != nil || r.Step != upgrade.HandingOver || r.Deadline.IsZero() {
					t.Error(r, err)
				}
				if binary != r.SavedBinary || !strings.Contains(strings.Join(args, " "), "upgrade watch --pid") || filepath.Base(log) != "watchdog.log" {
					t.Error(binary, args, log)
				}
				watchStarted = true
				return nil
			}
			execTarget := make(chan string, 1)
			h.engine.Exec = func(binary string, args []string) error {
				if !watchStarted || os.Getenv(upgradeListenerEnv) == "" {
					t.Error("exec before watchdog or listener handover")
				}
				execTarget <- binary
				return nil
			}
			done := make(chan error, 1)
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				done <- serve(stop, o, cfg, false, "", false, true)
			}()
			// A failed assertion must not leave a daemon changing the next test's listener environment.
			defer func() {
				cancel()
				select {
				case <-finished:
				case <-time.After(12 * time.Second):
					t.Error("daemon did not stop")
				}
			}()
			var info runtimeInfo
			if mode == "ask" {
				info = waitUpgradeDaemon(t, o, done)
			}
			var cookie *http.Cookie
			if mode == "ask" {
				code, err := server.Pair(o.runtimeDir())
				if err != nil {
					t.Fatal(err)
				}
				req, _ := http.NewRequest("POST", info.LocalURL+"/api/session", strings.NewReader(`{"token":"`+code+`"}`))
				req.Header.Set("X-Requested-With", "crew-assistant")
				req.Header.Set("Content-Type", "application/json")
				response, err := (&http.Client{Timeout: time.Second}).Do(req)
				if err != nil {
					t.Fatal(err)
				}
				response.Body.Close()
				if response.StatusCode != 200 || len(response.Cookies()) == 0 {
					t.Fatal(response.StatusCode)
				}
				cookie = response.Cookies()[0]
				d := snapshot.Decisions[0]
				if _, err = o.request("POST", "/api/decisions/"+d.ID+"/resolve", map[string]string{"choice": core.UpgradeChoice("v2.0.0")}); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case target := <-execTarget:
				if target != filepath.Join(h.prefix, "opt", "crew-assistant", "bin", "crew-assistant") {
					t.Fatal(target)
				}
			// HTTP shutdown allows ten seconds, including newly accepted connections.
			case <-time.After(12 * time.Second):
				r, err := upgrade.ReadRecord(h.engine.Path)
				t.Fatalf("serve did not run upgrade: record=%+v, error=%v", r, err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			r, err := upgrade.ReadRecord(h.engine.Path)
			if err != nil || r.Step != upgrade.HandingOver || r.Address == "" {
				t.Fatal(r, err)
			}
			for path, mode := range map[string]os.FileMode{r.SavedBinary: 0700, r.Backups.Config: 0600, r.Backups.State: 0600} {
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != mode {
					t.Fatal(path, info, err)
				}
			}
			backup, err := core.Open(r.Backups.State)
			if err != nil {
				t.Fatal(err)
			}
			backed, _ := backup.Snapshot(context.Background())
			backup.Close()
			if backed.Update.Available != "v2.0.0" {
				t.Fatal(backed.Update)
			}
			if mode == "ask" {
				auth, err := server.NewAuth(o.runtimeDir(), info.LocalURL, "", nil)
				if err != nil {
					t.Fatal(err)
				}
				req := httptest.NewRequest("GET", info.LocalURL+"/api/state", nil)
				req.AddCookie(cookie)
				w := httptest.NewRecorder()
				auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })).ServeHTTP(w, req)
				if w.Code != 200 {
					t.Fatal("paired browser lost during actual serve handover", w.Code)
				}
			}
		})
	}
}

func TestServeFailedProbationRestoresBeforeExec(t *testing.T) {
	l := upgradeTestListener(t)
	address := l.Addr().String()
	l.Close()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Dashboard.Addr = address
	o := &options{upgradeIdentity: func(int) string { return "fixture" }, configPath: filepath.Join(dir, "config.json"), statePath: filepath.Join(dir, "state.db"), version: "v2.0.0", globals: &libcli.Globals{Format: "ndjson"}}
	o.upgradeStarter = func(string, []string, string) error { return nil }
	if err := config.Save(o.configPath, cfg); err != nil {
		t.Fatal(err)
	}
	store, err := core.Open(o.statePath)
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(dir, "old.db")
	if err = store.Backup(context.Background(), backup); err != nil {
		t.Fatal(err)
	}
	store.Close()
	configBackup := filepath.Join(dir, "old.json")
	if err = upgrade.CopyFile(o.configPath, configBackup, 0600); err != nil {
		t.Fatal(err)
	}
	oldConfig, _ := os.ReadFile(configBackup)
	newConfig := cfg
	newConfig.Assistants[0].Name = "New version wrote this"
	config.Save(o.configPath, newConfig)
	r := upgrade.Record{Step: upgrade.HandingOver, From: "v1.0.0", To: o.version, SavedBinary: filepath.Join(dir, "previous"), Backups: upgrade.Backups{State: backup, Config: configBackup}, Deadline: time.Now().Add(time.Second), StartedAt: time.Now()}
	if err = upgrade.WriteRecord(upgrade.RecordPath(o.statePath), r); err != nil {
		t.Fatal(err)
	}
	stop, cancel := lifecycle.Watch(context.Background(), make(chan os.Signal), func(string) {})
	defer cancel()
	h, err := prepareUpgradeStart(o, stop, false)
	if err != nil {
		t.Fatal(err)
	}
	defer h.lock.Unlock()
	o.upgradeHost = h
	h.probe = func(context.Context, string, string) error { return errors.New("fake API failure") }
	executed := false
	h.engine.Exec = func(binary string, args []string) error {
		if binary != r.SavedBinary {
			t.Error(binary)
		}
		state, _ := os.ReadFile(o.statePath)
		old, _ := os.ReadFile(backup)
		if string(state) != string(old) {
			t.Error("previous binary would read newer state")
		}
		config, _ := os.ReadFile(o.configPath)
		if string(config) != string(oldConfig) {
			t.Error("config not restored")
		}
		for _, suffix := range []string{"-wal", "-shm"} {
			if _, err := os.Stat(o.statePath + suffix); !errors.Is(err, os.ErrNotExist) {
				t.Error(err)
			}
		}
		executed = true
		return nil
	}
	if err = serve(stop, o, newConfig, false, "", false, true); !errors.Is(err, upgrade.ErrRolledBack) {
		t.Fatal(err)
	}
	got, _ := upgrade.ReadRecord(h.engine.Path)
	if !executed || got.Step != upgrade.RolledBack || !got.Pinned {
		t.Fatal(executed, got)
	}
}

func TestRealHostHandoverExecFailureClosesTwiceSafely(t *testing.T) {
	l := upgradeTestListener(t)
	defer l.Close()
	dir := t.TempDir()
	o := &options{upgradeIdentity: func(int) string { return "fixture" }, statePath: filepath.Join(dir, "state.db"), configPath: filepath.Join(dir, "config.json"), version: "v1.0.0"}
	cfg := config.Default()
	if err := config.Save(o.configPath, cfg); err != nil {
		t.Fatal(err)
	}
	store, err := core.Open(o.statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	auth, err := server.NewAuth(o.runtimeDir(), "http://"+l.Addr().String(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "running")
	writeExecutableFixture(t, exe, "#!/bin/sh\nexit 0\n")
	h := &upgradeHost{executable: exe, engine: &upgrade.Engine{Path: upgrade.RecordPath(o.statePath), Drain: func() bool { return true }}, startDetached: func(string, []string, string) error { return nil }}
	h.engine.Restore = func(r upgrade.Record) error { return upgrade.Restore(o.statePath, o.configPath, r.Backups) }
	h.wire(o, func() config.Config { return cfg }, store, auth, l, &http.Server{})
	defer func() {
		if h.listenerFile != nil {
			h.listenerFile.Close()
		}
		os.Unsetenv(upgradeListenerEnv)
	}()
	h.engine.Install = func(_ context.Context, r upgrade.Record) (string, error) { return r.To, nil }
	attempts := filepath.Join(dir, "attempt")
	r := upgrade.Record{From: o.version, To: "v2.0.0", Prefix: "/fake", RunningBinary: exe, SavedBinary: filepath.Join(attempts, "previous"), Backups: upgrade.Backups{State: filepath.Join(attempts, "old.db"), Config: filepath.Join(attempts, "old.json")}}
	execs := 0
	h.engine.Exec = func(binary string, _ []string) error {
		execs++
		if execs == 1 {
			return errors.New("exec failure")
		}
		if binary != r.SavedBinary {
			t.Fatal(binary)
		}
		return nil
	}
	if err := h.engine.Request(r); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.FinishDrain(context.Background(), "upgrade"); err != nil {
		t.Fatal(err)
	}
	got, _ := upgrade.ReadRecord(h.engine.Path)
	if execs != 2 || got.Step != upgrade.RolledBack {
		t.Fatal(execs, got)
	}
	restored, err := core.Open(o.statePath)
	if err != nil {
		t.Fatal(err)
	}
	restored.Close()
}
func TestRealHostCloseFailureLeavesArmedRecovery(t *testing.T) {
	l := upgradeTestListener(t)
	defer l.Close()
	dir := t.TempDir()
	o := &options{upgradeIdentity: func(int) string { return "fixture" }, statePath: filepath.Join(dir, "state.db"), configPath: filepath.Join(dir, "config.json"), version: "v1.0.0"}
	cfg := config.Default()
	if err := config.Save(o.configPath, cfg); err != nil {
		t.Fatal(err)
	}
	store, err := core.Open(o.statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	auth, err := server.NewAuth(o.runtimeDir(), "http://"+l.Addr().String(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "running")
	writeExecutableFixture(t, exe, "#!/bin/sh\nexit 0\n")
	h := &upgradeHost{executable: exe, engine: &upgrade.Engine{Path: upgrade.RecordPath(o.statePath), Drain: func() bool { return true }}, startDetached: func(string, []string, string) error { return nil }}
	h.engine.Restore = func(r upgrade.Record) error { return upgrade.Restore(o.statePath, o.configPath, r.Backups) }
	h.wire(o, func() config.Config { return cfg }, store, auth, l, &http.Server{})
	defer func() {
		if h.listenerFile != nil {
			h.listenerFile.Close()
		}
		os.Unsetenv(upgradeListenerEnv)
	}()

	armed := false
	h.startDetached = func(string, []string, string) error { armed = true; return nil }
	h.engine.Arm = func(r upgrade.Record) error { return h.arm(o, r) }
	h.engine.Install = func(_ context.Context, r upgrade.Record) (string, error) {
		sessions := filepath.Join(o.runtimeDir(), "upgrade-sessions.json")
		if _, err := os.Stat(sessions); err != nil {
			t.Fatal("sessions not durable before install", err)
		}
		if err := os.Remove(sessions); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(sessions, 0700); err != nil {
			t.Fatal(err)
		}
		return r.To, nil
	}
	attempts := filepath.Join(dir, "attempt")
	r := upgrade.Record{From: o.version, To: "v2.0.0", Prefix: "/fake", RunningBinary: exe, SavedBinary: filepath.Join(attempts, "previous"), Backups: upgrade.Backups{State: filepath.Join(attempts, "old.db"), Config: filepath.Join(attempts, "old.json")}}

	h.engine.Exec = func(string, []string) error { t.Fatal("exec before safe close"); return nil }
	if err := h.engine.Request(r); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.FinishDrain(context.Background(), "upgrade"); err == nil {
		t.Fatal("Close failure hidden")
	}
	got, _ := upgrade.ReadRecord(h.engine.Path)
	if !armed || got.Step != upgrade.RollingBack || !got.RestartPending {
		t.Fatal(armed, got)
	}
	restarted := false
	w := upgrade.Watchdog{Path: h.engine.Path, Attempt: got.StartedAt, Alive: func(int) bool { return false }, Acquire: func() (func(), bool, error) { return func() {}, true, nil }, Restore: h.engine.Restore, StartDetached: func(upgrade.Record) error { restarted = true; return nil }}
	if _, err := w.Tick(); err != nil {
		t.Fatal(err)
	}
	got, _ = upgrade.ReadRecord(h.engine.Path)
	if !restarted || got.Step != upgrade.RolledBack || !got.RestartPending {
		t.Fatal(restarted, got)
	}
	restored, err := core.Open(o.statePath)
	if err != nil {
		t.Fatal(err)
	}
	restored.Close()
}
func TestSavedInstallFailureStartupRetainsRealRetryCapability(t *testing.T) {
	dir := t.TempDir()
	// Real attempts use the canonical journal directory, even when /var is an alias.
	saved := filepath.Join(filepath.Dir(upgrade.RecordPath(filepath.Join(dir, "state.db"))), "attempts", "fixture", "previous")
	writeExecutableFixture(t, saved, "#!/bin/sh\nexit 0\n")
	o := &options{upgradeIdentity: func(int) string { return "fixture" }, statePath: filepath.Join(dir, "state.db"), configPath: filepath.Join(dir, "config.json"), version: "v1.0.0", upgradeExecutable: func() (string, error) { return saved, nil }}
	r := upgrade.Record{Step: upgrade.InstallFailed, From: o.version, To: "v2.0.0", SavedBinary: saved, Prefix: filepath.Join(dir, "homebrew"), Failure: "brew failed", StartedAt: time.Now()}
	if err := upgrade.WriteRecord(upgrade.RecordPath(o.statePath), r); err != nil {
		t.Fatal(err)
	}
	stop, end := lifecycle.Watch(context.Background(), make(chan os.Signal), func(string) {})
	defer end()
	h, err := prepareUpgradeStart(o, stop, false)
	if err != nil {
		t.Fatal(err)
	}
	defer h.lock.Unlock()
	if h.prefix != r.Prefix || !h.start.Failed {
		t.Fatal(h.prefix, h.start)
	}
	store, err := core.Open(o.statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := core.NewService(store, config.Default())
	service.OnUpgradeRequested(func(target string) error { return h.request(o, "127.0.0.1:1234", target, false) })
	if err := service.RecordUpgradeFailure(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	snap, _ := service.Snapshot(context.Background())
	if _, err := service.ChooseDecision(context.Background(), snap.Decisions[0].ID, core.RetryUpgradeChoice(r.To), core.FromOwner); err != nil {
		t.Fatal(err)
	}
	got, _ := upgrade.ReadRecord(h.engine.Path)
	if got.Step != upgrade.Draining {
		t.Fatal(got)
	}
}
func TestPinnedHomebrewStartupExecsPreviousBeforeOpeningState(t *testing.T) {
	for _, label := range []string{"", "fixture.crew"} {
		t.Run(label, func(t *testing.T) {
			dir := t.TempDir()
			exe := filepath.Join(dir, "Cellar", "crew-assistant", "2.0.0", "bin", "crew-assistant")
			writeExecutableFixture(t, exe, "#!/bin/sh\nexit 0\n")
			saved := filepath.Join(dir, "saved")
			writeExecutableFixture(t, saved, "#!/bin/sh\nexit 0\n")
			o := &options{upgradeIdentity: func(int) string { return "fixture" }, statePath: filepath.Join(dir, "state.db"), configPath: filepath.Join(dir, "config.json"), version: "v2.0.0", upgradeExecutable: func() (string, error) { return exe, nil }}
			os.WriteFile(o.statePath, []byte("must not open"), 0600)
			r := upgrade.Record{Step: upgrade.RolledBack, From: "v1.0.0", To: o.version, Pinned: true, SavedBinary: saved, LaunchdLabel: label}
			if err := upgrade.WriteRecord(upgrade.RecordPath(o.statePath), r); err != nil {
				t.Fatal(err)
			}
			called := false
			o.upgradeExecer = func(binary string, args []string) error {
				called = true
				if binary != saved {
					t.Fatal(binary)
				}
				return nil
			}
			stop, end := lifecycle.Watch(context.Background(), make(chan os.Signal), func(string) {})
			defer end()
			if _, err := prepareUpgradeStart(o, stop, false); err == nil || !called {
				t.Fatal(called, err)
			}
			lock := flock.New(o.statePath + ".lock")
			if ok, err := lock.TryLock(); err != nil || !ok {
				t.Fatal("failed startup retained lock", ok, err)
			}
			lock.Unlock()
		})
	}
}
