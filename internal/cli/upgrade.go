package cli

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
	"github.com/shhac/crew-assistant/internal/upgrade"
	"github.com/spf13/cobra"
)

func registerUpgrade(root *cobra.Command, o *options) {
	group := &cobra.Command{Use: "upgrade", Short: "Inspect the daemon's upgrade record"}
	group.AddCommand(&cobra.Command{Use: "status", Short: "Read upgrade status even when the daemon is down", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		r, err := upgrade.ReadRecord(upgrade.RecordPath(o.statePath))
		if err != nil {
			return err
		}
		if r == nil {
			return o.emit(map[string]any{"status": "no upgrade recorded", "rollback_in_force": false})
		}
		status := map[string]any{"step": r.Step, "from": r.From, "to": r.To, "since": r.StepAt, "rollback_in_force": r.Pinned}
		if r.Failure != "" {
			status["failure"] = r.Failure
		}
		if r.DetachedLog != "" {
			status["log"] = r.DetachedLog
		}
		if r.Pinned {
			status["status"] = "Rollback is in force"
			status["clear"] = "crew-assistant upgrade clear-rollback"
			if r.PinTo != "" {
				status["rollback_to"] = r.PinTo
			}
		}
		return o.emit(status)
	}})
	var offline bool
	clear := &cobra.Command{Use: "clear-rollback", Short: "Retry the installed version with backups and a health check", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if offline {
			return clearMissingRollbackBinary(o)
		}
		v, err := o.request("POST", "/api/upgrade/clear-rollback", nil)
		if err != nil {
			return err
		}
		return o.emit(v)
	}}
	clear.Flags().BoolVar(&offline, "offline", false, "Recover a missing saved binary while stopped: restore pre-upgrade backups (discard later changes) and remove its pin")
	group.AddCommand(clear)
	var pid int
	var attempt string
	watch := &cobra.Command{Use: "watch", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if pid <= 0 {
			return errors.New("watch requires a daemon PID")
		}
		at, err := time.Parse(time.RFC3339Nano, attempt)
		if err != nil {
			return errors.New("watch requires an upgrade attempt")
		}
		if err := canonicalUpgradeOptions(o); err != nil {
			return err
		}
		return upgradeWatchdog(o, at).Run(cmd.Context())
	}}
	watch.Flags().IntVar(&pid, "pid", 0, "Daemon PID")
	watch.Flags().StringVar(&attempt, "attempt", "", "Upgrade attempt start")
	group.AddCommand(watch)
	root.AddCommand(group)
}

// The ordinary clear path requires a daemon. This narrow rescue path handles
// a lost saved executable without opening state or starting any process.
func clearMissingRollbackBinary(o *options) error {
	return clearMissingRollbackBinaryWith(o, upgrade.Restore)
}

func clearMissingRollbackBinaryWith(o *options, restore func(string, string, upgrade.Backups) error) error {
	if err := canonicalUpgradeOptions(o); err != nil {
		return err
	}
	lock := flock.New(o.statePath + ".lock")
	ok, err := lock.TryLock()
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("stop the daemon before offline rollback recovery")
	}
	defer lock.Unlock()
	path := upgrade.RecordPath(o.statePath)
	r, err := upgrade.ReadRecord(path)
	if err != nil {
		return err
	}
	if r == nil || !r.Pinned {
		return errors.New("no rollback is in force")
	}
	previous := r.SavedBinary
	if r.PinBinary != "" {
		previous = r.PinBinary
		if r.PinBackups.State != "" {
			r.Backups = r.PinBackups
		}
	}
	if _, err = os.Stat(previous); !errors.Is(err, os.ErrNotExist) {
		return errors.New("the saved binary exists; start the daemon and use upgrade clear-rollback")
	}
	if err := validateUpgradeConfig(*r, o.configPath); err != nil {
		return err
	}
	// Keep RollingBack durable until both backups have been restored.
	r.Step, r.StepAt = upgrade.RollingBack, time.Now().UTC()
	if err = upgrade.WriteRecord(path, *r); err != nil {
		return err
	}
	if err = restore(o.statePath, o.configPath, r.Backups); err != nil {
		return err
	}
	r.RestartPending = false
	r.RecoveryStarting = false
	r.Pinned = false
	r.PinBinary = ""
	r.PinBackups = upgrade.Backups{}
	r.PinTo = ""
	r.Step, r.StepAt = upgrade.Abandoned, time.Now().UTC()
	if err = upgrade.WriteRecord(path, *r); err != nil {
		return err
	}
	return o.emit(map[string]string{"status": "Backups restored; rollback pin removed. Install a working Homebrew binary, then start crew-assistant serve.", "backups": filepath.Dir(r.Backups.State)})
}
