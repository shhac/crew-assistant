// Package upgradestate holds the self-upgrade record and check result as they
// are persisted and shown, with no dependency on the machinery that installs,
// watches or rolls back an upgrade (internal/upgrade).
package upgradestate

import (
	"errors"
	"time"
)

// ErrInProgress refuses a second upgrade while one is under way.
var ErrInProgress = errors.New("an upgrade is already under way")

// Result is the outcome of checking for an installable release.
type Result struct {
	Available, Notes, URL, Error string
	CheckedAt                    time.Time
}

type Step string

const (
	Draining           Step = "draining"
	BackingUp          Step = "backing-up"
	BackupFailed       Step = "backup-failed"
	Installing         Step = "installing"
	InstallFailed      Step = "install-failed"
	HandingOver        Step = "handing-over"
	Probation          Step = "probation"
	Healthy            Step = "healthy"
	RollingBack        Step = "rolling-back"
	RolledBack         Step = "rolled-back"
	Abandoned          Step = "abandoned"
	StoppedInProbation Step = "stopped-in-probation"
)

type Backups struct {
	State  string `json:"state"`
	Config string `json:"config"`
}

type WaitingOn struct {
	Kind      string    `json:"kind"`
	Ref       string    `json:"ref"`
	Label     string    `json:"label,omitempty"`
	StartedAt time.Time `json:"started_at"`
}

// Record lives outside SQLite because rollback replaces that database.
type Record struct {
	OwnerStopped         bool        `json:"owner_stopped,omitempty"`
	RecoveryStarting     bool        `json:"recovery_starting,omitempty"`
	CleanupPending       bool        `json:"cleanup_pending,omitempty"`
	RestartAt            time.Time   `json:"restart_at,omitzero"`
	ProcessIdentity      string      `json:"process_identity,omitempty"`
	RestartPending       bool        `json:"restart_pending,omitempty"`
	ConfigPath           string      `json:"config_path,omitempty"`
	RunningBinary        string      `json:"running_binary,omitempty"`
	FailedVersion        string      `json:"failed_version,omitempty"`
	Automatic            bool        `json:"automatic,omitempty"`
	PID                  int         `json:"pid,omitempty"`
	Deadline             time.Time   `json:"deadline,omitzero"`
	Address              string      `json:"address,omitempty"`
	Step                 Step        `json:"step"`
	From                 string      `json:"from"`
	To                   string      `json:"to"`
	Prefix               string      `json:"prefix"`
	SavedBinary          string      `json:"saved_binary"`
	Backups              Backups     `json:"backups"`
	StartedAt            time.Time   `json:"started_at"`
	StepAt               time.Time   `json:"step_at"`
	LaunchdLabel         string      `json:"launchd_label,omitempty"`
	Args                 []string    `json:"args"`
	Failure              string      `json:"failure,omitempty"`
	FailedDecisionOpened bool        `json:"failed_decision_opened,omitempty"`
	ProbationStarts      int         `json:"probation_starts,omitempty"`
	Pinned               bool        `json:"pinned,omitempty"`
	PinBinary            string      `json:"pin_binary,omitempty"` // Previous pin retained until retry backups are complete.
	PinBackups           Backups     `json:"pin_backups,omitzero"`
	PinTo                string      `json:"pin_to,omitempty"`
	DetachedLog          string      `json:"detached_log,omitempty"`
	WaitingOn            []WaitingOn `json:"waiting_on,omitempty"`
}
