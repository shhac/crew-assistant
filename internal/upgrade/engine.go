package upgrade

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/shhac/crew-assistant/internal/releaseversion"
)

var ErrInProgress = errors.New("an upgrade is already under way")
var ErrStopping = errors.New("the daemon is stopping")
var ErrFailedVersion = errors.New("this version already failed; choose Try again to retry")
var ErrRolledBack = errors.New("upgrade did not become healthy; rollback was requested")

// Engine owns the durable transitions. The host retains its state lock and
// supplies effects; no workspace command or real installer runs in tests.
// Every effect sees its step already persisted, including rollback restore.
type Engine struct {
	Arm            func(Record) error // Starts recovery before fallible shutdown or exec.
	Report         func(string)       // Receives fixed, secret-safe diagnostics.
	Path           string
	Now            func() time.Time
	Drain          func() bool
	Interrupted    func() bool // Owner signal, distinct from the upgrade's own drain.
	Backup         func(context.Context, Record) error
	Install        func(context.Context, Record) (string, error)
	Handover       func(context.Context, Record) error
	Close          func() error             // Must be idempotent, including after Handover.
	Cleanup        func(Record, bool) error // false: discard abandoned; true: prune older attempts.
	Restore        func(Record) error
	Exec           func(string, []string) error
	journalHeld    bool
	mu             sync.Mutex
	probing        bool
	probationReady bool // This process journaled probation before opening state.
}

func (e *Engine) write(r *Record, step Step) error {
	r.Step = step
	r.StepAt = time.Now().UTC()
	if e.Now != nil {
		r.StepAt = e.Now().UTC()
	}
	if e.journalHeld {
		return writeRecord(e.Path, *r)
	}
	return WriteRecord(e.Path, *r)
}

func active(step Step) bool {
	switch step {
	case Draining, BackingUp, Installing, HandingOver, Probation, RollingBack, StoppedInProbation:
		return true
	}
	return false
}

func InProgress(step Step) bool { return active(step) }

// Request journals draining before closing admission. No backup or installer
// starts here: the host must first finish every turn and then call FinishDrain.
func (e *Engine) Request(r Record) error {
	if !e.mu.TryLock() {
		return ErrInProgress
	}
	defer e.mu.Unlock()
	previous, err := ReadRecord(e.Path)
	if err != nil {
		return err
	}
	if previous != nil && (active(previous.Step) || previous.RestartPending) {
		return ErrInProgress
	}
	if r.Automatic && previous != nil && releaseversion.Valid(failedVersion(*previous)) && releaseversion.Compare(r.To, failedVersion(*previous)) <= 0 {
		return ErrFailedVersion
	}
	if previous != nil {
		r.FailedVersion = failedVersion(*previous)
	}
	if previous != nil && previous.Pinned {
		r.Pinned = true
		r.PinBinary = previous.PinBinary
		if r.PinBinary == "" {
			r.PinBinary = previous.SavedBinary
		}
		r.PinBackups = previous.PinBackups
		if r.PinBackups.State == "" {
			r.PinBackups = previous.Backups
		}
		r.DetachedLog = previous.DetachedLog
		r.PinTo = previous.PinTo
		if r.PinTo == "" {
			r.PinTo = previous.To
		}
		r.Failure = previous.Failure
		r.FailedVersion = failedVersion(*previous)
	}
	if e.Drain == nil {
		return errors.New("upgrade drain is not configured")
	}
	r.StartedAt = time.Now().UTC()
	r.OwnerStopped = false
	r.ProbationStarts = 0
	e.probationReady = false
	r.FailedDecisionOpened = false
	r.Deadline = time.Time{}
	if e.Now != nil {
		r.StartedAt = e.Now().UTC()
	}
	if err = e.write(&r, Draining); err != nil {
		return err
	}
	if !e.Drain() {
		return errors.Join(ErrStopping, e.write(&r, Abandoned))
	}
	return nil
}

// FinishDrain is called only after the supervision loop has returned. The
// context must belong to installation, not Graceful (which is already ended).
// The host checks signal precedence again immediately before calling this.
func (e *Engine) FinishDrain(ctx context.Context, reason string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, err := ReadRecord(e.Path)
	if err != nil {
		return err
	}
	if r == nil || r.Step != Draining {
		return errors.New("no drained upgrade to install")
	}
	if reason != "upgrade" {
		return e.abandon(r)
	}
	if err = ctx.Err(); err != nil {
		return errors.Join(err, e.write(r, Abandoned))
	}
	if e.Backup == nil || e.Install == nil || e.Handover == nil || e.Exec == nil {
		return errors.New("upgrade effects are not configured")
	}
	if err = e.write(r, BackingUp); err != nil {
		return err
	}
	if err = e.Backup(ctx, *r); err != nil {
		if e.interrupted(ctx) {
			return errors.Join(context.Canceled, e.abandon(r))
		}
		r.Failure = "could not back up state, config or the previous binary"
		recordFailedVersion(r, r.To)
		prepareFailureRestart(r)
		if writeErr := e.write(r, BackupFailed); writeErr != nil {
			return errors.Join(err, writeErr)
		}
		return e.restartFailure(ctx, r, r.RunningBinary)
	}
	if e.interrupted(ctx) {
		return errors.Join(context.Canceled, e.abandon(r))
	}
	if err = e.write(r, Installing); err != nil {
		return err
	}
	installed, installErr := e.Install(ctx, *r)
	if e.interrupted(ctx) {
		return context.Canceled
	} // Installing drives recovery on the next start.
	if releaseversion.Valid(installed) {
		installed = "v" + strings.TrimPrefix(installed, "v")
		if releaseversion.Compare(installed, r.To) > 0 {
			r.To = installed
		}
	}
	if installed != r.To {
		if installErr == nil {
			installErr = errors.New("Homebrew did not install the requested version; retry when the formula is updated")
		}
		r.Failure = installErr.Error()
		recordFailedVersion(r, r.To)
		r.PinBinary = ""
		r.PinBackups = Backups{}
		r.PinTo = ""
		prepareFailureRestart(r)
		if err = e.write(r, InstallFailed); err != nil {
			return err
		}
		return e.restartFailure(ctx, r, r.SavedBinary)
	}
	r.Pinned = false
	r.PinBinary = ""
	r.PinBackups = Backups{}
	r.PinTo = ""
	r.Failure = ""
	if err = e.write(r, HandingOver); err != nil {
		return err
	}
	if err = e.Handover(ctx, *r); err != nil {
		if e.interrupted(ctx) {
			return errors.Join(context.Canceled, e.write(r, StoppedInProbation))
		}
		return e.rollback(r, "dashboard handover failed")
	}
	if e.interrupted(ctx) {
		return errors.Join(context.Canceled, e.write(r, StoppedInProbation))
	}
	if err = e.Exec(filepath.Join(r.Prefix, "opt", "crew-assistant", "bin", "crew-assistant"), r.Args); err != nil {
		if e.interrupted(ctx) {
			return errors.Join(context.Canceled, e.write(r, StoppedInProbation))
		}
		return e.rollback(r, "could not start the installed binary")
	}
	return nil
}

func (e *Engine) interrupted(ctx context.Context) bool {
	return ctx.Err() != nil || e.Interrupted != nil && e.Interrupted()
}

func (e *Engine) rollback(r *Record, failure string) error {
	if e.Interrupted != nil && e.Interrupted() {
		return errors.Join(context.Canceled, e.write(r, StoppedInProbation))
	}

	if e.Close == nil || e.Restore == nil || e.Exec == nil {
		return errors.New("rollback effects are not configured")
	}
	r.Failure = failure
	r.RestartPending = true
	r.RecoveryStarting = false
	r.RestartAt = time.Time{}
	recordFailedVersion(r, r.To)
	if err := e.write(r, RollingBack); err != nil {
		return err
	}
	if e.Arm != nil {
		if err := e.Arm(*r); err != nil {
			if e.Report != nil {
				e.Report("Upgrade watchdog could not start; attempting direct recovery.")
			}
		}
	}
	if err := e.Close(); err != nil {
		if e.Interrupted != nil && e.Interrupted() {
			r.OwnerStopped = true
			r.RestartPending = false
			return errors.Join(context.Canceled, e.write(r, RollingBack))
		}
		return fmt.Errorf("close state before rollback: %w", err)
	}
	if err := e.Restore(*r); err != nil {
		if e.Interrupted != nil && e.Interrupted() {
			r.OwnerStopped = true
			r.RestartPending = false
			return errors.Join(err, context.Canceled, e.write(r, RollingBack))
		}
		return err
	}
	r.Pinned = true
	r.PinBinary = ""
	r.PinBackups = Backups{}
	r.PinTo = ""
	stopped := e.Interrupted != nil && e.Interrupted()
	if stopped {
		r.RestartPending = false
	}
	if err := e.write(r, RolledBack); err != nil {
		return err
	}
	if stopped {
		return context.Canceled
	}
	err := e.Exec(r.SavedBinary, r.Args)
	if err != nil && e.Interrupted != nil && e.Interrupted() {
		return errors.Join(err, context.Canceled, e.suppressRecovery(r))
	}
	return err
}

// Probation holds dispatch in the host until this returns successfully. The
// probe should obey cancellation; even a stuck probe cannot hold this caller
// indefinitely. The watchdog independently covers a hung process.
func (e *Engine) CheckHealth(ctx context.Context, bound time.Duration, probe func(context.Context) error) error {
	e.mu.Lock()
	if e.probing {
		e.mu.Unlock()
		return ErrInProgress
	}
	releaseInitial, lockErr := LockRecord(e.Path)
	if lockErr != nil {
		e.mu.Unlock()
		return lockErr
	}
	e.journalHeld = true
	finishInitial := func() { e.journalHeld = false; releaseInitial() }
	r, err := ReadRecord(e.Path)
	if err != nil {
		finishInitial()
		e.mu.Unlock()
		return err
	}
	if r == nil || (r.Step != HandingOver && r.Step != Installing && r.Step != Probation && r.Step != StoppedInProbation) {
		finishInitial()
		e.mu.Unlock()
		return errors.New("no upgrade awaiting probation")
	}
	if ctx.Err() != nil || e.Interrupted != nil && e.Interrupted() {
		err = e.write(r, StoppedInProbation)
		finishInitial()
		e.mu.Unlock()
		return err
	}
	if r.ProbationStarts >= 1 && !(e.probationReady && r.ProbationStarts == 1) {
		err = errors.Join(ErrRolledBack, e.rollback(r, "the new version repeatedly stopped before becoming healthy"))
		finishInitial()
		e.mu.Unlock()
		return err
	}
	if bound <= 0 || probe == nil {
		finishInitial()
		e.mu.Unlock()
		return errors.New("a bounded upgrade health probe is required")
	}
	if r.ProbationStarts == 0 {
		r.ProbationStarts++
	}
	r.Pinned = false
	r.PinBinary = ""
	r.PinBackups = Backups{}
	r.PinTo = ""
	if err = e.write(r, Probation); err != nil {
		finishInitial()
		e.mu.Unlock()
		return err
	}
	e.probing = true
	e.probationReady = false
	finishInitial()
	e.mu.Unlock()
	probeCtx, cancel := context.WithTimeout(ctx, bound)
	defer cancel()
	probed := make(chan error, 1)
	go func() { probed <- probe(probeCtx) }()
	select {
	case err = <-probed:
	case <-probeCtx.Done():
		err = probeCtx.Err()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.probing = false
	release, lockErr := LockRecord(e.Path)
	if lockErr != nil {
		return lockErr
	}
	defer release()
	e.journalHeld = true
	defer func() { e.journalHeld = false }()
	current, readErr := ReadRecord(e.Path)
	if readErr != nil {
		return readErr
	}
	if current != nil && current.StartedAt.Equal(r.StartedAt) && current.Step == StoppedInProbation && ctx.Err() != nil {
		return nil
	}
	if current == nil || !current.StartedAt.Equal(r.StartedAt) || current.Step != Probation {
		return ErrInProgress
	}
	if ctx.Err() != nil || e.Interrupted != nil && e.Interrupted() {
		return e.write(r, StoppedInProbation)
	} else if err != nil || probeCtx.Err() != nil {
		return errors.Join(ErrRolledBack, e.rollback(r, "the new version did not answer its API health check within the bound"))
	}
	r.Pinned = false
	r.CleanupPending = e.Cleanup != nil
	if err = e.write(r, Healthy); err != nil {
		return err
	}
	e.cleanup(r, true)
	return nil
}

// BeginProbation journals admission and the attempt count before opening or
// migrating state. A crash during migration is therefore never mistaken for
// an interrupted install in which the old version may open state normally.
func (e *Engine) BeginProbation(pid int, identity string, args []string, bound time.Duration) error {
	if pid <= 0 || identity == "" {
		return errors.New("cannot start upgrade probation without a confirmed daemon process identity")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	release, err := LockRecord(e.Path)
	if err != nil {
		return err
	}
	defer release()
	e.journalHeld = true
	defer func() { e.journalHeld = false }()
	r, err := ReadRecord(e.Path)
	if err != nil {
		return err
	}
	if r == nil || r.ProbationStarts != 0 || (r.Step != Installing && r.Step != InstallFailed && r.Step != HandingOver && r.Step != Probation && r.Step != StoppedInProbation) {
		return errors.New("probation has already started")
	}
	if e.Interrupted != nil && e.Interrupted() {
		return errors.Join(context.Canceled, e.write(r, StoppedInProbation))
	}
	// An explicit start replaces the previous process's stop, under the same
	// lock that serializes current stop handling and watchdog recovery.
	r.OwnerStopped = false
	r.PID, r.ProcessIdentity, r.Args = pid, identity, args
	if r.Deadline.IsZero() {
		r.Deadline = time.Now().Add(bound)
	}
	r.ProbationStarts = 1
	r.RestartPending = false
	r.RecoveryStarting = false
	r.Pinned = false
	r.PinBinary = ""
	r.PinBackups = Backups{}
	r.PinTo = ""
	if err = e.write(r, Probation); err != nil {
		return err
	}
	e.probationReady = true
	return nil
}

func (e *Engine) abandon(r *Record) error {
	if err := e.write(r, Abandoned); err != nil {
		return err
	}
	e.cleanup(r, false)
	return nil
}

func (e *Engine) UpdateWaiting(waiting []WaitingOn) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, err := ReadRecord(e.Path)
	if err != nil || r == nil {
		return err
	}
	if r.Step != Draining {
		return nil
	}
	r.WaitingOn = waiting
	return WriteRecord(e.Path, *r)
}

func (e *Engine) MarkFailureOpened(attempt time.Time) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	release, err := LockRecord(e.Path)
	if err != nil {
		return err
	}
	defer release()
	r, err := ReadRecord(e.Path)
	if err != nil || r == nil {
		return err
	}
	if !r.StartedAt.Equal(attempt) {
		return ErrInProgress
	}
	r.FailedDecisionOpened = true
	return writeRecord(e.Path, *r)
}

// Fail is also used for config/state/API startup failures during probation.
func (e *Engine) Fail(failure string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	release, err := LockRecord(e.Path)
	if err != nil {
		return err
	}
	defer release()
	e.journalHeld = true
	defer func() { e.journalHeld = false }()
	r, err := ReadRecord(e.Path)
	if err != nil {
		return err
	}
	if r == nil {
		return errors.New("no upgrade to roll back")
	}
	if e.Interrupted != nil && e.Interrupted() {
		if r.Step == Probation || r.Step == HandingOver || r.Step == StoppedInProbation {
			return e.write(r, StoppedInProbation)
		}
		return nil
	}
	return errors.Join(ErrRolledBack, e.rollback(r, failure))
}

func failedVersion(r Record) string {
	if r.FailedVersion != "" {
		return r.FailedVersion
	}
	if r.Failure == "" {
		return ""
	}
	if r.Step != Abandoned && r.Step != Draining {
		return r.To
	}
	return r.PinTo
}
func recordFailedVersion(r *Record, target string) {
	if !releaseversion.Valid(r.FailedVersion) || releaseversion.Compare(target, r.FailedVersion) > 0 {
		r.FailedVersion = target
	}
}

// StopProbation honors a stop even before configuration or state has opened.
func (e *Engine) StopProbation() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	release, err := LockRecord(e.Path)
	if err != nil {
		return err
	}
	defer release()
	e.journalHeld = true
	defer func() { e.journalHeld = false }()
	r, err := ReadRecord(e.Path)
	if err != nil || r == nil {
		return err
	}
	if r.Step == RollingBack {
		r.OwnerStopped = true
		r.RestartPending = false
		r.RecoveryStarting = false
		return e.write(r, RollingBack)
	}
	if restartOutcome(r.Step) && r.RestartPending {
		r.RestartPending = false
		r.RecoveryStarting = false
		return e.write(r, r.Step)
	}
	if r.Step != Probation && r.Step != HandingOver {
		return nil
	}
	return e.write(r, StoppedInProbation)
}

// Cleanup is reconciled on the next start. It must never stop a healthy daemon.
func (e *Engine) cleanup(r *Record, healthy bool) {
	if e.Cleanup == nil {
		return
	}
	if err := e.Cleanup(*r, healthy); err != nil {
		if e.Report != nil {
			e.Report("Upgrade backup cleanup failed; it will be retried on the next start.")
		}
		return
	}
	if healthy && r.CleanupPending {
		r.CleanupPending = false
		if err := e.write(r, Healthy); err != nil && e.Report != nil {
			e.Report("Upgrade cleanup completion could not be recorded; it will be reconciled on the next start.")
		}
	}
}

// BeginRecovery keeps the watchdog responsible for a restored process until
// configuration, state and its API have all succeeded.
func (e *Engine) BeginRecovery(pid int, identity string, args []string, bound time.Duration) error {
	if pid <= 0 || identity == "" {
		return errors.New("cannot start upgrade recovery without a confirmed daemon process identity")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	release, err := LockRecord(e.Path)
	if err != nil {
		return err
	}
	defer release()
	e.journalHeld = true
	defer func() { e.journalHeld = false }()
	r, err := ReadRecord(e.Path)
	if err != nil {
		return err
	}
	if r == nil || !restartOutcome(r.Step) || !r.RestartPending {
		return errors.New("no rollback restart pending")
	}
	if e.Interrupted != nil && e.Interrupted() {
		return errors.Join(context.Canceled, e.suppressRecovery(r))
	}
	r.OwnerStopped = false
	r.PID, r.ProcessIdentity, r.Args = pid, identity, args
	r.RecoveryStarting = true
	r.Deadline = time.Now().Add(bound)
	return e.write(r, r.Step)
}

func (e *Engine) ConfirmRecovery(ctx context.Context, bound time.Duration, probe func(context.Context) error) error {
	probeCtx, cancel := context.WithTimeout(ctx, bound)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- probe(probeCtx) }()
	var err error
	select {
	case err = <-done:
	case <-probeCtx.Done():
		err = probeCtx.Err()
	}
	// A parent stop cancels this probe; it is not a failed health deadline.
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if e.Interrupted != nil && e.Interrupted() {
		return context.Canceled
	}
	if err != nil {
		return errors.New("the restored version did not answer its API health check within the bound")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	release, err := LockRecord(e.Path)
	if err != nil {
		return err
	}
	defer release()
	r, err := ReadRecord(e.Path)
	if err != nil {
		return err
	}
	if ctx.Err() != nil || e.Interrupted != nil && e.Interrupted() {
		return context.Canceled
	}
	if r == nil || !restartOutcome(r.Step) || !r.RestartPending {
		return ErrInProgress
	}
	r.RestartPending = false
	r.RecoveryStarting = false
	return writeRecord(e.Path, *r)
}

func restartOutcome(step Step) bool {
	return step == RolledBack || step == BackupFailed || step == InstallFailed
}

// RecoveryBinary never selects incomplete retry material.
func RecoveryBinary(r Record) string {
	if r.Step == BackupFailed {
		if r.PinBinary != "" {
			return r.PinBinary
		}
		return r.RunningBinary
	}
	return r.SavedBinary
}
func prepareFailureRestart(r *Record) {
	r.RestartPending = true
	r.RecoveryStarting = false
	r.Deadline = time.Time{}
	r.RestartAt = time.Time{}
}

func (e *Engine) restartFailure(ctx context.Context, r *Record, binary string) error {
	if e.Arm != nil {
		if err := e.Arm(*r); err != nil && e.Report != nil {
			e.Report("Upgrade watchdog could not start; attempting direct recovery.")
		}
	}
	if e.Close != nil {
		if err := e.Close(); err != nil && e.Report != nil {
			e.Report("Upgrade shutdown did not complete; restarting the running version.")
		}
	}
	if e.interrupted(ctx) {
		return errors.Join(context.Canceled, e.suppressRecovery(r))
	}
	err := e.Exec(binary, r.Args)
	if err != nil && e.interrupted(ctx) {
		return errors.Join(err, context.Canceled, e.suppressRecovery(r))
	}
	return err
}

// Read the latest transition under the journal lock, preserving restoration
// obligations and pins even if a receiver changed the record during exec.
func (e *Engine) suppressRecovery(r *Record) error {
	if !e.journalHeld {
		release, err := LockRecord(e.Path)
		if err != nil {
			return err
		}
		defer release()
	}
	current, err := ReadRecord(e.Path)
	if err != nil {
		return err
	}
	if current == nil || !current.StartedAt.Equal(r.StartedAt) {
		return ErrInProgress
	}
	current.OwnerStopped = true
	current.RestartPending = false
	current.RecoveryStarting = false
	return writeRecord(e.Path, *current)
}
