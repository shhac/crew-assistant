package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/shhac/lib-agent-harness/session"
)

// TeamTurn is prospective accounting for one runner invocation. It contains
// no prompt, reply, tool content or provider error text. Retries are separate
// attempts; Observed is evidence, never an addition to terminal Usage.
type TeamTurn struct {
	ID         string `json:"id"`
	ProjectID  string `json:"project_id"`
	TaskID     string `json:"task_id,omitempty"`
	ClaimToken string `json:"claim_token,omitempty"`
	LaunchDir  string `json:"launch_dir,omitempty"`
	// UntrackedLaunch has no durable harness identity; recovery must hold it.
	UntrackedLaunch bool   `json:"untracked_launch,omitempty"`
	Role            string `json:"role"`
	Seat            string `json:"seat"`
	MemberID        string `json:"member_id,omitempty"`
	MemberName      string `json:"member_name,omitempty"`
	Engine          string `json:"engine"`
	// Empty Model means the provider default, not a resolved model name.
	Model      string            `json:"model"`
	PreviousID string            `json:"previous_id,omitempty"`
	RetryCause string            `json:"retry_cause,omitempty"`
	AdmittedAt time.Time         `json:"admitted_at"`
	Opening    *TeamTurnOpening  `json:"opening,omitempty"`
	AcceptedAt *time.Time        `json:"accepted_at,omitempty"`
	Terminal   *TeamTurnTerminal `json:"terminal,omitempty"`
	// CleanupConfirmedAt records later reclamation independently of immutable
	// terminal accounting. A terminal result alone does not prove cleanup.
	CleanupConfirmedAt *time.Time `json:"cleanup_confirmed_at,omitempty"`
	// Held means recovery could not confirm the old launch was gone.
	Held bool `json:"held"`
}

const (
	FreshNoThread            = "no_saved_thread"
	FreshEngineChanged       = "engine_changed"
	FreshModelChanged        = "model_changed"
	FreshOwnerRequested      = "owner_requested"
	FreshHarnessIncompatible = "harness_incompatible"
	FreshHarnessUnavailable  = "harness_unavailable"
)

type TeamTurnOpening struct {
	At          time.Time `json:"at"`
	Resumed     bool      `json:"resumed"`
	FreshReason string    `json:"fresh_reason,omitempty"`
	SessionID   string    `json:"session_id,omitempty"`
}

// TeamTurnTerminal separates the runner outcome from the provider status.
// FailureStage is a structured stage name, never raw stderr or an error string.
// A completed provider turn can still have a release failure.
type TeamTurnTerminal struct {
	At                 time.Time     `json:"at"`
	Outcome            string        `json:"outcome"`
	FailureStage       string        `json:"failure_stage,omitempty"`
	ProviderStatus     string        `json:"provider_status,omitempty"`
	ProviderTurnID     string        `json:"provider_turn_id,omitempty"`
	NativeError        bool          `json:"native_error"`
	CleanupConfirmed   bool          `json:"cleanup_confirmed"`
	Usage              session.Usage `json:"usage"`
	Observed           session.Usage `json:"observed"`
	CompactionUsage    session.Usage `json:"compaction_usage"`
	CompactionObserved session.Usage `json:"compaction_observed"`
}

// AdmitTeamTurn must succeed before invoking the runner. Attribution is
// immutable. Accounting deliberately does not require a live work claim:
// a revoked claim's already-admitted attempt still needs its terminal record.
func (s *Service) AdmitTeamTurn(ctx context.Context, turn TeamTurn) error {
	if turn.ID == "" || turn.ProjectID == "" || turn.Role == "" || turn.Seat == "" || turn.Engine == "" || turn.AdmittedAt.IsZero() {
		return fmt.Errorf("a team turn needs an id, project, role, seat, engine and admission time")
	}
	if turn.Opening != nil || turn.AcceptedAt != nil || turn.Terminal != nil || turn.CleanupConfirmedAt != nil || turn.Held {
		return fmt.Errorf("admission cannot contain later lifecycle data: %w", ErrConflict)
	}
	return s.store.changeTeamTurn(ctx, turn.ID, func(old *TeamTurn) (*TeamTurn, error) {
		if old != nil {
			admission := *old
			admission.Opening, admission.AcceptedAt, admission.Terminal, admission.Held = nil, nil, nil, false
			admission.CleanupConfirmedAt = nil
			admission.AdmittedAt = admission.AdmittedAt.UTC()
			turn.AdmittedAt = turn.AdmittedAt.UTC().Round(0)
			if reflect.DeepEqual(admission, turn) {
				return old, nil
			}
			return nil, ErrConflict
		}
		return &turn, nil
	})
}

func (s *Service) OpenTeamTurn(ctx context.Context, id string, opening TeamTurnOpening) error {
	opening.At = opening.At.UTC().Round(0)
	if opening.At.IsZero() || (opening.Resumed && opening.FreshReason != "") {
		return ErrConflict
	}
	return s.store.changeTeamTurn(ctx, id, func(t *TeamTurn) (*TeamTurn, error) {
		if t == nil {
			return nil, ErrNotFound
		}
		if t.Opening != nil {
			t.Opening.At = t.Opening.At.UTC().Round(0)
			if reflect.DeepEqual(*t.Opening, opening) {
				return t, nil
			}
			return nil, ErrConflict
		}
		if t.Terminal != nil || opening.At.Before(t.AdmittedAt) {
			return nil, ErrConflict
		}
		t.Opening = &opening
		return t, nil
	})
}

func (s *Service) AcceptTeamTurn(ctx context.Context, id string, at time.Time) error {
	return s.store.changeTeamTurn(ctx, id, func(t *TeamTurn) (*TeamTurn, error) {
		if t == nil {
			return nil, ErrNotFound
		}
		if t.AcceptedAt != nil {
			if t.AcceptedAt.Equal(at) {
				return t, nil
			}
			return nil, ErrConflict
		}
		if t.Terminal != nil || t.Opening == nil || at.Before(t.Opening.At) {
			return nil, ErrConflict
		}
		t.AcceptedAt = &at
		return t, nil
	})
}

// FinishTeamTurn is idempotent for the same terminal accounting. Conflicting
// accounting is rejected, including callbacks arriving after recovery.
func (s *Service) FinishTeamTurn(ctx context.Context, id string, terminal TeamTurnTerminal) error {
	terminal.At = terminal.At.UTC().Round(0)
	if terminal.At.IsZero() || terminal.Outcome == "" {
		return ErrConflict
	}
	return s.store.changeTeamTurn(ctx, id, func(t *TeamTurn) (*TeamTurn, error) {
		if t == nil {
			return nil, ErrNotFound
		}
		if t.Terminal != nil {
			t.Terminal.At = t.Terminal.At.UTC().Round(0)
			if reflect.DeepEqual(*t.Terminal, terminal) {
				return t, nil
			}
			return nil, ErrConflict
		}
		if terminal.At.Before(t.AdmittedAt) || (t.Opening != nil && terminal.At.Before(t.Opening.At)) || (t.AcceptedAt != nil && terminal.At.Before(*t.AcceptedAt)) {
			return nil, ErrConflict
		}
		t.Terminal, t.Held = &terminal, false
		return t, nil
	})
}

// RecoverTeamTurn reconciles cleanup separately from accounting. Terminal
// accounting is immutable; unknown usage stays unknown.
func (s *Service) RecoverTeamTurn(ctx context.Context, id string, confirmed bool, at time.Time) error {
	return s.store.changeTeamTurn(ctx, id, func(t *TeamTurn) (*TeamTurn, error) {
		if t == nil {
			return nil, ErrNotFound
		}
		if t.Terminal != nil {
			if t.Terminal.CleanupConfirmed || t.CleanupConfirmedAt != nil {
				return t, nil
			}
			if !confirmed {
				t.Held = true
				return t, nil
			}
			if at.Before(t.Terminal.At) {
				return nil, ErrConflict
			}
			at = at.UTC().Round(0)
			t.CleanupConfirmedAt, t.Held = &at, false
			return t, nil
		}
		if !confirmed {
			t.Held = true
			return t, nil
		}
		if at.Before(t.AdmittedAt) || (t.Opening != nil && at.Before(t.Opening.At)) || (t.AcceptedAt != nil && at.Before(*t.AcceptedAt)) {
			return nil, ErrConflict
		}
		t.Terminal = &TeamTurnTerminal{At: at, Outcome: "interrupted", FailureStage: "recovery", CleanupConfirmed: true}
		t.Held = false
		return t, nil
	})
}

// TeamTurnFilter supports the task/member history follow-up and launch
// reconciliation. Empty fields mean no filter; project-only turns have no task.
type TeamTurnFilter struct {
	ProjectID, TaskID, MemberID, ClaimToken string
	Incomplete                              bool
	// NeedsRecovery includes finalized attempts whose cleanup remains uncertain.
	NeedsRecovery bool
}

func (s *Service) TeamTurns(ctx context.Context, filter TeamTurnFilter) ([]TeamTurn, error) {
	query := "SELECT payload FROM team_turns WHERE 1=1"
	args := []any{}
	for _, f := range []struct{ column, value string }{{"project_id", filter.ProjectID}, {"task_id", filter.TaskID}, {"member_id", filter.MemberID}, {"claim_token", filter.ClaimToken}} {
		if f.value != "" {
			query += " AND " + f.column + "=?"
			args = append(args, f.value)
		}
	}
	query += " ORDER BY admitted_at, id"
	rows, err := s.store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TeamTurn{}
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var turn TeamTurn
		if err := json.Unmarshal([]byte(payload), &turn); err != nil {
			return nil, fmt.Errorf("decode team turn: %w", err)
		}
		if filter.Incomplete && turn.Terminal != nil {
			continue
		}
		if !filter.NeedsRecovery || turn.Terminal == nil || (!turn.Terminal.CleanupConfirmed && turn.CleanupConfirmedAt == nil) {
			out = append(out, turn)
		}
	}
	return out, rows.Err()
}

func (s *Store) changeTeamTurn(ctx context.Context, id string, change func(*TeamTurn) (*TeamTurn, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer tx.Close()
	if _, err := tx.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer tx.ExecContext(context.Background(), "ROLLBACK")
	var payload string
	err = tx.QueryRowContext(ctx, "SELECT payload FROM team_turns WHERE id=?", id).Scan(&payload)
	var old *TeamTurn
	if err == nil {
		old = new(TeamTurn)
		if err := json.Unmarshal([]byte(payload), old); err != nil {
			return err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	next, err := change(old)
	if err != nil {
		return err
	}
	if old == nil && next.PreviousID != "" {
		var previousPayload string
		if err := tx.QueryRowContext(ctx, "SELECT payload FROM team_turns WHERE id=?", next.PreviousID).Scan(&previousPayload); err != nil {
			return fmt.Errorf("preceding attempt: %w", err)
		}
		var previous TeamTurn
		if err := json.Unmarshal([]byte(previousPayload), &previous); err != nil {
			return err
		}
		if previous.Terminal == nil || previous.ProjectID != next.ProjectID || previous.TaskID != next.TaskID || previous.MemberID != next.MemberID || previous.Seat != next.Seat || previous.Role != next.Role || next.RetryCause == "" || next.AdmittedAt.Before(previous.Terminal.At) {
			return ErrConflict
		}
	}
	data, err := json.Marshal(next)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO team_turns(id, project_id, task_id, member_id, claim_token, admitted_at, payload) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET payload=excluded.payload`, next.ID, next.ProjectID, next.TaskID, next.MemberID, next.ClaimToken, next.AdmittedAt.UTC().Format("2006-01-02T15:04:05.000000000Z"), string(data))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "COMMIT")
	return err
}
