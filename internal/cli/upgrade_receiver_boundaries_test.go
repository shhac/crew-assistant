//go:build !windows

package cli

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/lifecycle"
	"github.com/shhac/crew-assistant/internal/upgrade"
	libcli "github.com/shhac/lib-agent-cli/cli"
)

func TestFailureReceiverKeepsAddressDespiteUnavailableWatchdog(t *testing.T) {
	for _, step := range []upgrade.Step{upgrade.BackupFailed, upgrade.InstallFailed, upgrade.RolledBack} {
		t.Run(string(step), func(t *testing.T) {
			l := upgradeTestListener(t)
			address := l.Addr().String()
			l.Close()
			dir := t.TempDir()
			cfg := config.Default()
			cfg.Dashboard.Addr = "127.0.0.1:0"
			cfg.Upgrade.Mode = "off"
			o := &options{upgradeIdentity: func(int) string { return "fixture" }, configPath: filepath.Join(dir, "config.json"), statePath: filepath.Join(dir, "state.db"), version: "v1.0.0", globals: &libcli.Globals{Format: "ndjson"}}
			if err := config.Save(o.configPath, cfg); err != nil {
				t.Fatal(err)
			}
			store, err := core.Open(o.statePath)
			if err != nil {
				t.Fatal(err)
			}
			store.Close()
			binary := filepath.Join(dir, "previous")
			writeExecutableFixture(t, binary, "fixture")
			o.upgradeExecutable = func() (string, error) { return binary, nil }
			attempts := 0
			o.upgradeStarter = func(string, []string, string) error { attempts++; return errors.New("unwritable watchdog log") }
			r := upgrade.Record{Step: step, From: o.version, To: "v2.0.0", RunningBinary: binary, SavedBinary: binary, RestartPending: true, Pinned: step == upgrade.RolledBack, Address: address, StartedAt: time.Now(), Failure: "fixture failure"}
			if err := upgrade.WriteRecord(upgrade.RecordPath(o.statePath), r); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stop, end := lifecycle.Watch(ctx, make(chan os.Signal), func(string) {})
			defer end()
			h, err := prepareUpgradeStart(o, stop, false)
			if err != nil {
				t.Fatal(err)
			}
			defer h.lock.Unlock()
			o.upgradeHost = h
			done := make(chan error, 1)
			go func() { done <- serve(stop, o, cfg, false, "", false, true) }()
			info := waitUpgradeDaemon(t, o, done)
			if info.LocalURL != "http://"+address || attempts == 0 {
				t.Fatal(info, attempts)
			}
			response, err := (&http.Client{Timeout: time.Second}).Get(info.LocalURL)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			got, err := upgrade.ReadRecord(h.engine.Path)
			if err != nil || got.RestartPending {
				t.Fatal(got, err)
			}
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("receiver did not stop")
			}
		})
	}
}

func TestAliasStartsEnforcePinRestoreAndStateLock(t *testing.T) {
	for _, step := range []upgrade.Step{upgrade.RolledBack, upgrade.RollingBack} {
		for _, fileAlias := range []bool{false, true} {
			t.Run(string(step)+map[bool]string{true: "/file", false: "/directory"}[fileAlias], func(t *testing.T) {
				dir := t.TempDir()
				state, configPath := filepath.Join(dir, "state.db"), filepath.Join(dir, "config.json")
				for _, p := range []string{state, configPath} {
					if err := os.WriteFile(p, []byte("new"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				alias := filepath.Join(dir, "alias")
				target := dir
				if fileAlias {
					target = state
				}
				if err := os.Symlink(target, alias); err != nil {
					t.Skip(err)
				}
				if !fileAlias {
					alias = filepath.Join(alias, "state.db")
				}
				backups := upgrade.Backups{State: filepath.Join(dir, "old.db"), Config: filepath.Join(dir, "old.json")}
				for _, p := range []string{backups.State, backups.Config} {
					if err := os.WriteFile(p, []byte("old"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				binary := filepath.Join(dir, "previous")
				writeExecutableFixture(t, binary, "fixture")
				r := upgrade.Record{Step: step, Pinned: true, From: "v1.0.0", To: "v2.0.0", SavedBinary: binary, Backups: backups, ConfigPath: configPath}
				if err := upgrade.WriteRecord(upgrade.RecordPath(state), r); err != nil {
					t.Fatal(err)
				}
				o := &options{upgradeIdentity: func(int) string { return "fixture" }, statePath: alias, configPath: configPath, version: "v2.0.0", upgradeExecutable: func() (string, error) { return os.Executable() }}
				execed := false
				o.upgradeExecer = func(path string, args []string) error { execed = path == binary; return errors.New("fake exec") }
				stop, end := lifecycle.Watch(context.Background(), make(chan os.Signal), func(string) {})
				defer end()
				if _, err := prepareUpgradeStart(o, stop, false); err == nil || !execed {
					t.Fatal(execed, err)
				}
				if step == upgrade.RollingBack {
					b, _ := os.ReadFile(state)
					if string(b) != "old" {
						t.Fatal(string(b))
					}
				}
				lock := flock.New(o.statePath + ".lock")
				if err := lock.Lock(); err != nil {
					t.Fatal(err)
				}
				defer lock.Unlock()
				o.statePath = alias
				if _, err := prepareUpgradeStart(o, stop, false); err == nil || !strings.Contains(err.Error(), "another daemon owns") {
					t.Fatal(err)
				}
			})
		}
	}
}
