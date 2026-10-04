package upgrade

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"time"

	"github.com/gofrs/flock"
)

// Watchdog runs from the saved binary. Acquire must take the same state lock
// as serve; the daemon is killed and reaped before Restore touches SQLite.
type Watchdog struct {
	Path          string
	Attempt       time.Time
	Report        func(string)
	Clock         Clock
	Identity      func(int) string
	Alive         func(int) bool
	Kill          func(int) error
	Acquire       func() (func(), bool, error)
	Restore       func(Record) error
	StartDetached func(Record) error
	Kickstart     func(Record) error
}

func watchdogDone(r *Record) bool {
	return r == nil || r.Step == Healthy || r.Step == RolledBack && !r.RestartPending || r.Step == StoppedInProbation || (r.Step == BackupFailed || r.Step == InstallFailed) && !r.RestartPending || r.Step == Abandoned
}

var errProcessInspection = errors.New("upgrade recovery cannot confirm the daemon process identity; retrying inspection without killing it")

type processState int

const (
	processGone processState = iota
	processMatching
	processUnknown
)

// A mismatch means the recorded process is gone, not that the current PID
// may be killed. Unknown identity never grants permission to kill either.
func (w *Watchdog) inspect(r Record) processState {
	if !w.Alive(r.PID) {
		return processGone
	}
	if w.Identity == nil {
		return processUnknown
	}
	identity := w.Identity(r.PID)
	if identity == "" || r.ProcessIdentity == "" {
		return processUnknown
	}
	if identity != r.ProcessIdentity {
		return processGone
	}
	return processMatching
}

// Tick reports completion. It can be retried after lock contention or a
// partial restore; RollingBack always redoes the restore before any restart.
func (w *Watchdog) Tick() (bool, error) {
	releaseJournal, err := LockRecord(w.Path)
	if err != nil {
		return false, err
	}
	defer releaseJournal()
	r, err := ReadRecord(w.Path)
	if err != nil {
		return false, err
	}
	if watchdogDone(r) || !w.Attempt.IsZero() && !r.StartedAt.Equal(w.Attempt) {
		return true, nil
	}
	state := processGone
	if !r.OwnerStopped && (!restartOutcome(r.Step) || r.RecoveryStarting) {
		state = w.inspect(*r)
		if state == processUnknown {
			return false, errProcessInspection
		}
	}
	clock := w.Clock
	if clock == nil {
		clock = SystemClock()
	}
	if state == processMatching && (r.Deadline.IsZero() || clock.Now().Before(r.Deadline)) {
		return false, nil
	}
	killed := false
	if r.Step != RollingBack && !restartOutcome(r.Step) {
		r.Failure = "the new version exited before becoming healthy"
		if state == processMatching {
			r.Failure = "the new version exceeded its health-check deadline"
		}
	}
	if !restartOutcome(r.Step) {
		r.Step, r.StepAt = RollingBack, clock.Now().UTC()
		if err = writeRecord(w.Path, *r); err != nil {
			return false, err
		}
	}
	if state == processMatching {
		// Revalidate immediately before killing, and never turn a previous
		// mismatch into permission to kill after a later failed lookup.
		state = w.inspect(*r)
		if state == processUnknown {
			return false, errProcessInspection
		}
		if state == processMatching {
			if err = w.Kill(r.PID); err != nil {
				return false, err
			}
			killed = true
			state = w.inspect(*r)
			if state == processUnknown {
				return false, errProcessInspection
			}
			if state == processMatching {
				return false, nil
			}
		}
	}
	release, ok, err := w.Acquire()
	if err != nil || !ok {
		return false, err
	}
	defer release()
	current, err := ReadRecord(w.Path)
	if err != nil {
		return false, err
	}
	if current == nil || !killed && watchdogDone(current) || !current.StartedAt.Equal(r.StartedAt) {
		return true, nil
	}
	// launchd may already have restarted into another probation process.
	if current.PID != r.PID {
		return false, nil
	}
	if restartOutcome(current.Step) && !current.RestartAt.IsZero() && clock.Now().Before(current.RestartAt.Add(10*time.Second)) {
		return false, nil
	}
	current.Failure = r.Failure
	if current.LaunchdLabel == "" {
		current.DetachedLog = filepath.Join(filepath.Dir(w.Path), "rollback-serve.log")
	}
	if !restartOutcome(current.Step) {
		current.Step, current.StepAt = RollingBack, clock.Now().UTC()
		if err = writeRecord(w.Path, *current); err != nil {
			return false, err
		}
		if err = w.Restore(*current); err != nil {
			return false, err
		}
	}
	current.RestartPending = !current.OwnerStopped
	current.RecoveryStarting = false
	current.RestartAt = clock.Now().UTC()
	recordFailedVersion(current, current.To)
	if !restartOutcome(current.Step) {
		current.Pinned = true
		current.PinBinary = ""
		current.PinBackups = Backups{}
		current.PinTo = ""
		current.Step = RolledBack
	}
	current.StepAt = clock.Now().UTC()
	if err = writeRecord(w.Path, *current); err != nil {
		return false, err
	}
	if current.OwnerStopped {
		return true, nil
	}
	// A detached daemon must acquire this lock itself. Release it before
	// starting; the pin makes a racing launchd start safe too.
	release()
	if current.LaunchdLabel != "" {
		if w.Kickstart == nil {
			return false, errors.New("launchd recovery is not configured")
		}
		err = w.Kickstart(*current)
	} else {
		err = w.StartDetached(*current)
	}
	if err != nil {
		current.RestartAt = time.Time{}
		return false, errors.Join(err, writeRecord(w.Path, *current))
	}
	return false, nil
}

// Run observes cancellation between ticks, before reports and retry scheduling.
// An active synchronous Tick finishes its protected operation before Run stops.
func (w *Watchdog) Run(ctx context.Context) error {
	lock := flock.New(WatchdogLockPath(w.Path, w.Attempt))
	ok, err := lock.TryLock()
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	defer lock.Unlock()
	clock := w.Clock
	if clock == nil {
		clock = SystemClock()
	}
	var reportedAt time.Time
	for {
		// A ready retry timer must not win over cancellation and start another
		// recovery attempt after the caller has stopped the watchdog.
		if err := ctx.Err(); err != nil {
			return err
		}
		done, err := w.Tick()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			if w.Report != nil && (reportedAt.IsZero() || clock.Now().Sub(reportedAt) >= time.Minute) {
				reportedAt = clock.Now()
				if errors.Is(err, errProcessInspection) {
					w.Report("Upgrade recovery cannot confirm the daemon process identity; retrying inspection without killing it. Check local process inspection permissions.")
				} else {
					w.Report("Upgrade recovery could not complete; retrying. Check access to the saved binary and state/config backups, or launchd job configuration.")
				}
			}
			done = false
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if done {
			return nil
		}
		timer := clock.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C():
		}
	}
}

// Each attempt gets its own lease. A retiring helper cannot hide a new one.
func WatchdogLockPath(path string, attempt time.Time) string {
	return path + "." + strconv.FormatInt(attempt.UnixNano(), 10) + ".watch.lock"
}
