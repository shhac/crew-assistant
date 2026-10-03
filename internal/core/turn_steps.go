package core

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/shhac/crew-assistant/internal/text"
)

// TurnStep is one thing a seat did on a task, as its session reported it: the
// prompt it was given, a reply it wrote, or a tool it ran with what went in
// and what came out. Steps are the owner's to read in the dashboard and go
// nowhere else: no prompt, log or other role is given them.
type TurnStep struct {
	ID     int64  `json:"id"`
	TaskID string `json:"task_id"`
	// Seat is the seat's name, Member the team member in it, if any, and Role
	// the kind of work it was doing.
	Seat   string `json:"seat"`
	Member string `json:"member,omitempty"`
	Role   string `json:"role"`
	// Turn names the run the step belongs to, and Item the step within it: a
	// reply that streams in, or a tool that later finishes, is one step
	// updated in place.
	Turn string    `json:"turn"`
	Item string    `json:"item"`
	At   time.Time `json:"at"`
	// Kind is StepPrompt, StepReply, StepTool or StepNote.
	Kind string `json:"kind"`
	Text string `json:"text,omitempty"`
	Tool string `json:"tool,omitempty"`
	// Input is the tool's arguments, as the JSON the session reported.
	Input    string `json:"input,omitempty"`
	Output   string `json:"output,omitempty"`
	Status   string `json:"status,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
	// Clipped says some of Text, Input or Output was cut, here or by the
	// session before it arrived.
	Clipped bool `json:"clipped,omitempty"`
}

const (
	StepNote   = "note"
	StepPrompt = "prompt"
	StepReply  = "reply"
	StepTool   = "tool"
)

// What one step and one task keep. A step's text is cut to its first part;
// a task keeps its newest steps, across all its seats, up to both bounds.
const (
	maxStepText   = 16 << 10
	maxStepInput  = 4 << 10
	maxStepOutput = 8 << 10
	maxTaskSteps  = 600
	maxTaskBytes  = 2 << 20
)

// RecordTurnStep keeps a step, replacing the one its turn already has for
// the same item, and lets the task's oldest steps go past its bounds.
func (s *Service) RecordTurnStep(ctx context.Context, step TurnStep) error {
	if step.TaskID == "" || step.Turn == "" || step.Item == "" {
		return fmt.Errorf("a turn step needs its task, turn and item")
	}
	clip := func(field *string, limit int) {
		if len(*field) > limit {
			*field, step.Clipped = text.Clip(*field, limit), true
		}
	}
	clip(&step.Text, maxStepText)
	clip(&step.Input, maxStepInput)
	clip(&step.Output, maxStepOutput)
	step.ID = 0
	return s.store.keepStep(ctx, step)
}

// TurnSteps are a seat's steps on a task, oldest first. taskID may be the
// task's readable ID.
func (s *Service) TurnSteps(ctx context.Context, projectID, taskID, seat string) ([]TurnStep, error) {
	v, err := s.store.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	t, ok := v.FindTask(taskID)
	if !ok || t.ProjectID != projectID {
		return nil, ErrNotFound
	}
	return s.store.steps(ctx, t.ID, seat)
}

func (s *Store) keepStep(ctx context.Context, step TurnStep) error {
	payload, err := json.Marshal(step)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	if _, err = conn.ExecContext(ctx, `INSERT INTO turn_steps(task_id, seat, turn, item, payload) VALUES(?,?,?,?,?)
		ON CONFLICT(turn, item) DO UPDATE SET payload=excluded.payload`, step.TaskID, step.Seat, step.Turn, step.Item, string(payload)); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, `DELETE FROM turn_steps WHERE id IN (
		SELECT id FROM (
			SELECT id, ROW_NUMBER() OVER newest AS n, SUM(length(payload)) OVER newest AS size
			FROM turn_steps WHERE task_id=? WINDOW newest AS (ORDER BY id DESC)
		) WHERE n > ? OR size > ?)`, step.TaskID, maxTaskSteps, maxTaskBytes); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}

func (s *Store) steps(ctx context.Context, taskID, seat string) ([]TurnStep, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, payload FROM turn_steps WHERE task_id=? AND seat=? ORDER BY id", taskID, seat)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TurnStep{}
	for rows.Next() {
		var id int64
		var payload string
		if err := rows.Scan(&id, &payload); err != nil {
			return nil, err
		}
		var step TurnStep
		if err := json.Unmarshal([]byte(payload), &step); err != nil {
			return nil, fmt.Errorf("decode turn step: %w", err)
		}
		step.ID = id
		out = append(out, step)
	}
	return out, rows.Err()
}
