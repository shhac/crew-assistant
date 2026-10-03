package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"time"
)

// DecisionEvaluation is an answered decision preserved independently of state.
// Only the exporter reads this record; it is never part of a prompt.
type DecisionEvaluation struct {
	DecisionID     string    `json:"decision_id"`
	ProjectID      string    `json:"project_id"`
	TaskID         string    `json:"task_id"`
	Kind           string    `json:"kind"`
	Title          string    `json:"title"`
	Context        string    `json:"context"`
	Choices        []string  `json:"choices"`
	Recommendation string    `json:"recommendation"`
	Answer         string    `json:"answer"`
	Disposition    string    `json:"disposition"`
	AnsweredBy     string    `json:"answered_by"`
	CreatedAt      time.Time `json:"created_at"`
	ResolvedAt     time.Time `json:"resolved_at"`
}

func keepDecisionEvaluation(ctx context.Context, conn *sql.Conn, d Decision) error {
	if d.Status != DecisionResolved || d.ResolvedAt == nil || d.Answer == "" {
		return nil
	}
	kind, disposition := d.Kind, d.Disposition
	if kind == "" {
		kind = "choice"
	}
	if disposition == "" {
		disposition = DispositionCustom
	}
	payload, err := json.Marshal(DecisionEvaluation{d.ID, d.ProjectID, d.TaskID, kind, d.Title, d.Context, d.Choices, d.Recommendation, d.Answer, disposition, d.AnsweredBy, d.CreatedAt, *d.ResolvedAt})
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "INSERT OR IGNORE INTO decision_evaluations(decision_id,resolved_at,payload) VALUES(?,?,?)", d.ID, d.ResolvedAt.UTC().Format("2006-01-02T15:04:05.000000000Z"), string(payload))
	return err
}

func (s *Store) backfillDecisionEvaluations(ctx context.Context) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	state, err := readState(ctx, conn)
	if err != nil {
		return err
	}
	for _, d := range state.Decisions {
		if err := keepDecisionEvaluation(ctx, conn, d); err != nil {
			return err
		}
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}

// ExportDecisionEvaluations streams a consistent SQLite read snapshot as JSONL.
func (s *Service) ExportDecisionEvaluations(ctx context.Context, w io.Writer) error {
	rows, err := s.store.db.QueryContext(ctx, "SELECT payload FROM decision_evaluations ORDER BY resolved_at, rowid")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return err
		}
		if _, err := io.WriteString(w, payload+"\n"); err != nil {
			return err
		}
	}
	return rows.Err()
}
