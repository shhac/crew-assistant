//go:build !windows

package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/lifecycle"
	"github.com/shhac/crew-assistant/internal/upgrade"
	libcli "github.com/shhac/lib-agent-cli/cli"
)

func TestServeReportsFailedStopPersistenceAfterRecoveryExec(t *testing.T) {
	for _, step := range []upgrade.Step{upgrade.BackupFailed, upgrade.InstallFailed, upgrade.RolledBack} {
		t.Run(string(step), func(t *testing.T) {
			listener := upgradeTestListener(t)
			listener.Close()
			dir := canonicalTempDir(t)
			o := &options{statePath: filepath.Join(dir, "state.db"), configPath: filepath.Join(dir, "config.json"), version: "v1.0.0", globals: &libcli.Globals{Format: "ndjson"}}
			cfg := config.Default()
			cfg.Dashboard.Addr, cfg.Dashboard.Tailscale, cfg.Upgrade.Mode = listener.Addr().String(), "off", "off"
			if err := config.Save(o.configPath, cfg); err != nil {
				t.Fatal(err)
			}
			signals, handled := make(chan os.Signal), make(chan struct{})
			stop, end := lifecycle.Watch(context.Background(), signals, func(string) { close(handled) })
			defer end()
			h, err := prepareUpgradeStart(o, stop, false)
			if err != nil {
				t.Fatal(err)
			}
			defer h.lock.Unlock()
			o.upgradeHost = h
			h.executable = filepath.Join(dir, "running")
			writeExecutableFixture(t, h.executable, "#!/bin/sh\necho 'crew-assistant v1.0.0'\n")
			h.startDetached = func(string, []string, string) error { return nil }
			h.engine.Arm = func(upgrade.Record) error { return nil }
			blocker := filepath.Join(dir, "not-a-directory")
			if err := os.WriteFile(blocker, []byte("blocker"), 0600); err != nil {
				t.Fatal(err)
			}
			path := h.engine.Path
			r := upgrade.Record{From: o.version, To: "v2.0.0", ConfigPath: o.configPath, RunningBinary: h.executable, SavedBinary: filepath.Join(dir, "attempt", "previous"), Backups: upgrade.Backups{State: filepath.Join(dir, "attempt", "old.db"), Config: filepath.Join(dir, "attempt", "old.json")}, Prefix: filepath.Join(dir, "synthetic-homebrew")}
			if step == upgrade.BackupFailed {
				r.Backups.State = filepath.Join(blocker, "old.db")
			}
			// Synthetic Homebrew succeeds only for the sender rollback case.
			if step == upgrade.RolledBack {
				writeExecutableFixture(t, filepath.Join(r.Prefix, "bin", "brew"), "#!/bin/sh\nexit 0\n")
				writeExecutableFixture(t, filepath.Join(r.Prefix, "opt", "crew-assistant", "bin", "crew-assistant"), "#!/bin/sh\necho 'crew-assistant v2.0.0'\n")
			}
			execErr := errors.New("saved exec failed")
			execs := 0
			h.engine.Exec = func(string, []string) error {
				execs++
				if step == upgrade.RolledBack && execs == 1 {
					return errors.New("target exec failed")
				}
				signals <- lifecycle.Signals[1]
				select {
				case <-handled:
				case <-time.After(time.Second):
					t.Fatal("SIGTERM not handled")
				}
				// Preserve the last valid journal while making stop persistence fail.
				h.engine.Path = filepath.Join(blocker, "record.json")
				return execErr
			}
			if err := h.engine.Request(r); err != nil {
				t.Fatal(err)
			}
			err = serve(stop, o, cfg, false, "", false, true)
			var journalErr *os.PathError
			if !errors.Is(err, context.Canceled) || !errors.Is(err, execErr) || !errors.As(err, &journalErr) {
				t.Fatal("serve hid recovery errors", err)
			}
			got, err := upgrade.ReadRecord(path)
			if err != nil || got.Step != step || !got.RestartPending || got.OwnerStopped {
				t.Fatal(got, err)
			}
			if step == upgrade.RolledBack && !got.Pinned {
				t.Fatal("rollback protection lost", got)
			}
		})
	}
}

func TestUpgradeSignalStopOnlyIgnoresCancellationAlone(t *testing.T) {
	journalErr := errors.New("journal failed")
	for _, err := range []error{nil, context.Canceled, errors.Join(context.Canceled), errors.Join(context.Canceled, errors.Join(context.Canceled))} {
		if got := upgradeStopResult(err); got != nil {
			t.Fatal(got)
		}
	}
	if err := upgradeStopResult(errors.Join(context.Canceled, journalErr)); !errors.Is(err, journalErr) || !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestServeSignalDuringRecoveryConfirmation(t *testing.T) {
	for _, step := range []upgrade.Step{upgrade.BackupFailed, upgrade.InstallFailed, upgrade.RolledBack} {
		for _, outcome := range []string{"cancelled-probe", "observer-first", "parent-cancel", "journal-failure", "close-failure", "health-failure"} {
			t.Run(string(step)+"/"+outcome, func(t *testing.T) {
				listener := upgradeTestListener(t)
				listener.Close()
				dir := canonicalTempDir(t)
				cfg := config.Default()
				cfg.Dashboard.Addr, cfg.Dashboard.Tailscale, cfg.Upgrade.Mode = listener.Addr().String(), "off", "off"
				binary := filepath.Join(dir, "previous")
				writeExecutableFixture(t, binary, "synthetic saved binary")
				o := &options{statePath: filepath.Join(dir, "state.db"), configPath: filepath.Join(dir, "config.json"), version: "v1.0.0", globals: &libcli.Globals{Format: "ndjson"}, upgradeIdentity: func(int) string { return "fixture" }, upgradeExecutable: func() (string, error) { return binary, nil }, upgradeStarter: func(string, []string, string) error { return nil }}
				if err := config.Save(o.configPath, cfg); err != nil {
					t.Fatal(err)
				}
				path := upgrade.RecordPath(o.statePath)
				r := upgrade.Record{Step: step, From: o.version, To: "v2.0.0", SavedBinary: binary, RestartPending: true, Pinned: step == upgrade.RolledBack, ConfigPath: o.configPath, StartedAt: time.Now()}
				if err := upgrade.WriteRecord(path, r); err != nil {
					t.Fatal(err)
				}
				signals, handled := make(chan os.Signal), make(chan struct{})
				parent, cancelParent := context.WithCancel(context.Background())
				defer cancelParent()
				stop, end := lifecycle.Watch(parent, signals, func(string) { close(handled) })
				defer end()
				h, err := prepareUpgradeStart(o, stop, false)
				if err != nil {
					t.Fatal(err)
				}
				defer h.lock.Unlock()
				o.upgradeHost = h
				entered, probeDone := make(chan struct{}), make(chan struct{})
				closeErr := errors.New("shutdown failed")
				blocker := filepath.Join(dir, "not-a-directory")
				if err := os.WriteFile(blocker, []byte("blocker"), 0600); err != nil {
					t.Fatal(err)
				}
				h.probe = func(ctx context.Context, _, _ string) error {
					defer close(probeDone)
					if outcome == "observer-first" {
						if err := h.engine.StopProbation(); err != nil {
							t.Error(err)
						}
					}
					if outcome == "journal-failure" {
						h.engine.Path = filepath.Join(blocker, "record.json")
					}
					if outcome == "close-failure" {
						closeState := h.engine.Close
						h.engine.Close = func() error { return errors.Join(closeState(), closeErr) }
					}
					close(entered)
					if outcome == "health-failure" {
						return errors.New("probe failed")
					}
					<-ctx.Done()
					if outcome == "observer-first" {
						return nil
					}
					return ctx.Err()
				}
				finished := make(chan error, 1)
				go func() { finished <- serve(stop, o, cfg, false, "", false, true) }()
				defer func() {
					end()
					select {
					case <-probeDone:
					case <-time.After(time.Second):
						t.Error("probe did not stop")
					}
				}()
				select {
				case <-entered:
				case err := <-finished:
					// An immediate health failure can finish serve after closing
					// entered, before this select runs. Both channels are ready;
					// preserve the result for the assertions below.
					select {
					case <-entered:
						finished <- err
					default:
						t.Fatal("serve ended before probe", err)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("probe did not start")
				}
				if outcome == "parent-cancel" {
					cancelParent()
				} else if outcome != "health-failure" {
					signals <- lifecycle.Signals[1]
					<-handled
				}
				select {
				case err = <-finished:
				case <-time.After(12 * time.Second):
					t.Fatal("serve did not stop")
				}
				var journalErr *os.PathError
				switch outcome {
				case "journal-failure":
					if !errors.As(err, &journalErr) {
						t.Fatal("stop persistence error hidden", err)
					}
				case "close-failure":
					if !errors.Is(err, closeErr) {
						t.Fatal("shutdown error hidden", err)
					}
				case "health-failure":
					if err == nil || !strings.Contains(err.Error(), "did not answer its API health check") {
						t.Fatal("health failure hidden", err)
					}
				default:
					if err != nil {
						t.Fatal("successful signal stop failed", err)
					}
				}
				got, err := upgrade.ReadRecord(path)
				pending := outcome == "journal-failure" || outcome == "health-failure"
				if err != nil || got.Step != step || got.RestartPending != pending || got.RecoveryStarting != pending || got.Pinned != r.Pinned {
					t.Fatal(got, err)
				}
				if !pending {
					w := upgrade.Watchdog{Path: path, Alive: func(int) bool { t.Fatal("inspected stopped receiver"); return true }, StartDetached: func(upgrade.Record) error { t.Fatal("restarted stopped receiver"); return nil }, Kickstart: func(upgrade.Record) error { t.Fatal("kickstarted stopped receiver"); return nil }}
					for range 3 {
						if done, err := w.Tick(); err != nil || !done {
							t.Fatal(done, err)
						}
					}
				}
			})
		}
	}
}

// canonicalTempDir resolves macOS's /var symlink, as the upgrade journal
// does, so a fixture's saved binary matches the running executable's path.
func canonicalTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
