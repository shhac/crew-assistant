package upgrade

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/shhac/crew-assistant/internal/releaseversion"
)

type StartResult struct {
	Recovery  bool
	Probation bool
	Failed    bool
	Message   string
}

// Start must run with exclusive state ownership before config migration or
// opening SQLite. executable is the resolved running path, not argv[0].
// Exec must replace the process (tests inject it); a successful pin exec is
// returned as an error to prevent a returning fake from opening newer state.
func (e *Engine) Start(running, executable string) (StartResult, error) {
	if releaseversion.Valid(running) {
		running = "v" + strings.TrimPrefix(running, "v")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	var result StartResult
	r, err := ReadRecord(e.Path)
	if err != nil || r == nil {
		return result, err
	}
	if r.Step == RollingBack {
		if e.Restore == nil {
			return result, errors.New("startup restore is not configured")
		}
		if err = e.Restore(*r); err != nil {
			if e.Interrupted != nil && e.Interrupted() {
				r.OwnerStopped = true
				r.RestartPending = false
				return result, errors.Join(err, context.Canceled, e.write(r, RollingBack))
			}
			return result, err
		}
		if e.Interrupted != nil && e.Interrupted() {
			r.OwnerStopped = true
		}
		r.Pinned = true
		r.RestartPending = !r.OwnerStopped
		r.RecoveryStarting = false
		if err = e.write(r, RolledBack); err != nil {
			return result, err
		}
	}
	pinBinary := r.SavedBinary
	if r.PinBinary != "" {
		pinBinary = r.PinBinary
	}
	protected := r.Pinned && !((r.Step == Installing || r.Step == InstallFailed) && running == r.To)
	if e.Interrupted != nil && e.Interrupted() {
		if restartOutcome(r.Step) {
			r.RestartPending = false
			r.RecoveryStarting = false
			return result, errors.Join(context.Canceled, e.write(r, r.Step))
		}
		if r.Step == Probation || r.Step == HandingOver || r.Step == StoppedInProbation {
			return result, errors.Join(context.Canceled, e.write(r, StoppedInProbation))
		}
		return result, context.Canceled
	}
	if protected && filepath.Clean(executable) != filepath.Clean(pinBinary) {
		if e.Exec == nil {
			return result, errors.New("rollback pin exec is not configured")
		}
		if err = e.Exec(pinBinary, r.Args); err != nil {
			return result, fmt.Errorf("saved rollback binary cannot start; keep the daemon stopped and run crew-assistant upgrade clear-rollback --offline after restoring a working Homebrew binary: %w", err)
		}
		return result, errors.New("rollback pin exec returned without replacing the process")
	}
	if protected && running != r.From {
		return result, errors.New("saved rollback binary has the wrong version")
	}
	if r.Pinned {
		version := r.To
		if r.PinTo != "" {
			version = r.PinTo
		}
		result.Message = fmt.Sprintf("Rollback is in force: restored %s after %s failed: %s. Clear with crew-assistant upgrade clear-rollback.", r.From, version, r.Failure)
		if r.DetachedLog != "" {
			result.Message += " Detached daemon log: " + r.DetachedLog
		}
	}
	if r.Step == RolledBack {
		result.Recovery = r.RestartPending
		result.Failed = !r.FailedDecisionOpened
		result.Recovery = r.RestartPending
		return result, nil
	}
	switch r.Step {
	case Healthy, Abandoned:
		e.cleanup(r, r.Step == Healthy)
		return result, nil
	case BackupFailed:
		if running != r.From {
			return result, errors.New("upgrade backups did not complete; start the previous binary and retry before starting the new version")
		}
		result.Failed = !r.FailedDecisionOpened
		result.Recovery = r.RestartPending
		return result, nil
	case Draining, BackingUp:
		return result, e.abandon(r)
	case Installing, InstallFailed, HandingOver, Probation, StoppedInProbation:
		if running == r.To {
			// A process that never entered probation may start it once. A
			// second probation start restores the previous version.
			if r.ProbationStarts >= 1 {
				return result, e.startRollback(r, "the new version repeatedly stopped before becoming healthy")
			}
			result.Probation = true
			return result, nil
		}
		if running == r.From && (r.Step == Installing || r.Step == InstallFailed) {
			if r.Step == Installing {
				prepareFailureRestart(r)
			}
			if r.Step == Installing || r.Failure == "" {
				r.Failure = "the Homebrew install was interrupted; rerun the upgrade by hand to inspect its output"
			}
			r.PinBinary = ""
			r.PinBackups = Backups{}
			r.PinTo = ""
			if err = e.write(r, InstallFailed); err != nil {
				return result, err
			}
			result.Recovery = r.RestartPending
			result.Failed = !r.FailedDecisionOpened
			return result, nil
		}
		// Never allow the previous version to open potentially migrated
		// state after a handover, even if the watchdog was interrupted.
		if running == r.From {
			return result, e.startRollback(r, "the new version stopped before becoming healthy")
		}
		return result, e.startRollback(r, "a different version was started before the upgrade became healthy")
	}
	return result, errors.New("unhandled upgrade recovery step")
}

func (e *Engine) startRollback(r *Record, failure string) error {
	if e.Interrupted != nil && e.Interrupted() {
		return errors.Join(context.Canceled, e.write(r, StoppedInProbation))
	}
	if err := e.rollback(r, failure); err != nil {
		return err
	}
	return errors.New("rollback exec returned without replacing the process")
}
