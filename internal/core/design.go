package core

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/shhac/crew-assistant/internal/text"
)

// DesignRequest is one hand-off to the designer: who handed the task over,
// from which step, what it asked, and the input that came back. The task
// returns to that step with the input, which is kept here so everyone who
// works on the task afterwards reads the same words.
type DesignRequest struct {
	ID string `json:"id"`
	// From is the seat that asked; Step is where the task goes back to,
	// TaskResearching or TaskWriting, in Round.
	From     string `json:"from"`
	Step     string `json:"step"`
	Round    int    `json:"round"`
	Question string `json:"question"`
	// Designer is the seat that gave Input. A request past DesignLimit goes
	// to the owner instead, and has neither.
	Designer string `json:"designer,omitempty"`
	Input    string `json:"input,omitempty"`
	// Decision is the owner's decision the request was brought to: one past
	// the limit, or one the designer escalated because it needed more than
	// design input.
	Decision   string    `json:"decision,omitempty"`
	At         time.Time `json:"at"`
	AnsweredAt time.Time `json:"answered_at,omitzero"`
}

// DesignLimit is how many times one step, in one round, can hand the task to
// the designer. Past it, the question goes to the owner, so a task can never
// pass back and forth on its own.
const DesignLimit = 2

// Open reports a request still waiting for its answer.
func (r DesignRequest) Open() bool { return r.AnsweredAt.IsZero() }

// DesignAsk is a hand-off the loop asks for.
type DesignAsk struct {
	From     string
	Question string
	// Owner is the decision the question becomes past DesignLimit.
	Owner DecisionInput
	// Also changes the task in the same change, such as keeping the
	// implementer's session.
	Also func(*Task)
}

// Designer is the seat that gives the task design input, if its team has
// one.
func (t Task) Designer() (Role, bool) {
	designers := t.RolesOf(RoleDesigner)
	if len(designers) == 0 {
		return Role{}, false
	}
	return designers[0], true
}

// DesignsAt counts the hand-offs made from a step in the current round.
func (t Task) DesignsAt(step string) int {
	n := 0
	for _, r := range t.Design {
		if r.Step == step && r.Round == t.Round {
			n++
		}
	}
	return n
}

// OpenDesign is the request the task is with the designer for.
func (t *Task) OpenDesign() *DesignRequest {
	for i := len(t.Design) - 1; i >= 0; i-- {
		if r := &t.Design[i]; r.Open() && r.Decision == "" {
			return r
		}
	}
	return nil
}

// DesignDecision is the request a decision was opened for, if any.
func (t *Task) DesignDecision(decisionID string) *DesignRequest {
	for i := range t.Design {
		if r := &t.Design[i]; decisionID != "" && r.Decision == decisionID {
			return r
		}
	}
	return nil
}

// AskDesign hands a task to its designer from the step it is on, recording
// who asked and what, in one change. Past DesignLimit the question goes to
// the owner as a decision instead. A task no longer at a step that can ask,
// such as one stopped meanwhile, is left as it is.
func (s *Service) AskDesign(ctx context.Context, taskID string, ask DesignAsk) (Task, error) {
	if err := ask.Owner.validTaskDecision(); err != nil {
		return Task{}, err
	}
	var out Task
	err := s.store.update(ctx, func(v *Snapshot) error {
		t := task(v, taskID)
		if t == nil {
			return ErrNotFound
		}
		if t.Status != TaskResearching && t.Status != TaskWriting {
			return fmt.Errorf("the task is no longer researching or being written: %w", ErrConflict)
		}
		designer, ok := t.Designer()
		if !ok {
			return errors.New("this task's team has no designer")
		}
		now := s.now().UTC()
		r := DesignRequest{ID: uid(), From: ask.From, Step: t.Status, Round: t.Round, Question: text.Clip(ask.Question, 4000), At: now}
		if ask.Also != nil {
			ask.Also(t)
		}
		t.Failures, t.RetryAt = 0, time.Time{}
		if t.DesignsAt(r.Step) >= DesignLimit {
			r.Decision = openTaskDecision(v, t, DecisionQuestion, ask.Owner, now).ID
		} else {
			t.Status, t.Detail = TaskDesigning, "With "+designer.Name+" for design input"
			record(v, now, t.ProjectID, "task.designing", fmt.Sprintf("%s handed %s to %s for design input", ask.From, t.Objective, designer.Name))
		}
		t.Design = append(t.Design, r)
		t.UpdatedAt = now
		derive(v, t)
		out = *t
		return nil
	})
	return out, err
}

// RecordDesign keeps the designer's input on its request and hands the task
// back to the step that asked, in one change, so a restart never runs the
// designer twice or loses what it said. A designer that escalates brings the
// owner a decision instead, and the task goes back once it is answered. A
// task stopped while the designer worked stays stopped.
func (s *Service) RecordDesign(ctx context.Context, taskID, requestID, designer, input string, escalate *DecisionInput) (Task, error) {
	if escalate != nil {
		if err := escalate.validTaskDecision(); err != nil {
			return Task{}, err
		}
	}
	var out Task
	err := s.store.update(ctx, func(v *Snapshot) error {
		t := task(v, taskID)
		if t == nil {
			return ErrNotFound
		}
		r := t.OpenDesign()
		if t.Status != TaskDesigning || r == nil || r.ID != requestID {
			return fmt.Errorf("the task is no longer with the designer: %w", ErrConflict)
		}
		now := s.now().UTC()
		r.Designer, r.Input = designer, text.Clip(input, 6000)
		t.Failures, t.RetryAt = 0, time.Time{}
		if escalate != nil {
			r.Decision = openTaskDecision(v, t, DecisionQuestion, *escalate, now).ID
		} else {
			r.AnsweredAt = now
			t.Status, t.Detail = r.Step, "Back from "+designer+" with design input"
			record(v, now, t.ProjectID, "task.designed", fmt.Sprintf("%s gave design input on %s", designer, t.Objective))
		}
		t.UpdatedAt = now
		derive(v, t)
		out = *t
		return nil
	})
	return out, err
}
