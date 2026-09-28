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
	// N numbers the designer's input on the task, from 1, as everyone
	// names it: design 2. A request with no input has none.
	N int `json:"n,omitempty"`
	// Marked is input the designer made the current design at some point:
	// a design rather than advice. Once another is current it is
	// superseded, and stays on the record.
	Marked bool `json:"marked,omitempty"`
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

// CurrentDesignInput is the task's current design, the target everyone
// works and judges to, if the designer has marked one.
func (t Task) CurrentDesignInput() (DesignRequest, bool) {
	for _, r := range t.Design {
		if t.CurrentDesign != "" && r.ID == t.CurrentDesign {
			return r, true
		}
	}
	return DesignRequest{}, false
}

// DesignNumbered is the designer's input numbered n.
func (t *Task) DesignNumbered(n int) *DesignRequest {
	for i := range t.Design {
		if r := &t.Design[i]; n > 0 && r.N == n {
			return r
		}
	}
	return nil
}

// nextDesignNumber numbers the next input the designer gives.
func (t Task) nextDesignNumber() int {
	n := 0
	for _, r := range t.Design {
		n = max(n, r.N)
	}
	return n + 1
}

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
			recordTask(v, now, t, "task.designing", fmt.Sprintf("%s handed %s to %s for design input", ask.From, t.Objective, designer.Name))
		}
		t.Design = append(t.Design, r)
		t.UpdatedAt = now
		derive(v, t)
		out = *t
		return nil
	})
	return out, err
}

// DesignReply is what the designer answered a request with.
type DesignReply struct {
	Designer, Input string
	// Current is which design becomes the task's current one: CurrentThis
	// for this input, an earlier design's number to keep or restore it, or
	// 0 to leave it as it is, for input that is advice only. An escalation
	// changes nothing current: the owner decides first.
	Current int
	// Escalate brings the owner a decision instead of handing the task back.
	Escalate *DecisionInput
}

// CurrentThis marks the input being given as the current design.
const CurrentThis = -1

// RecordDesign keeps the designer's input on its request and hands the task
// back to the step that asked, in one change, so a restart never runs the
// designer twice or loses what it said. Input is numbered, and in the same
// change becomes the current design or restores an earlier one, as the
// designer said. A designer that escalates brings the owner a decision
// instead, and the task goes back once it is answered. A task stopped while
// the designer worked stays stopped.
func (s *Service) RecordDesign(ctx context.Context, taskID, requestID string, reply DesignReply) (Task, error) {
	designer, escalate := reply.Designer, reply.Escalate
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
		r.Designer, r.Input = designer, text.Clip(reply.Input, 6000)
		if r.Input != "" {
			r.N = t.nextDesignNumber()
		}
		t.Failures, t.RetryAt = 0, time.Time{}
		if escalate != nil {
			r.Decision = openTaskDecision(v, t, DecisionQuestion, *escalate, now).ID
		} else {
			if err := markCurrent(v, t, r, designer, reply.Current, now); err != nil {
				return err
			}
			r.AnsweredAt = now
			t.Status, t.Detail = r.Step, "Back from "+designer+" with design input"
			recordTask(v, now, t, "task.designed", fmt.Sprintf("%s gave design input on %s", designer, t.Objective))
		}
		t.UpdatedAt = now
		derive(v, t)
		out = *t
		return nil
	})
	return out, err
}

// markCurrent makes the design current names the task's current design:
// this request's input, or an earlier design by its number. Naming the one
// already current changes nothing.
func markCurrent(v *Snapshot, t *Task, this *DesignRequest, designer string, current int, now time.Time) error {
	if current == 0 {
		return nil
	}
	n := current
	if current == CurrentThis {
		if this.N == 0 {
			return errors.New("input with no words can't be the current design")
		}
		n = this.N
	}
	target := t.DesignNumbered(n)
	if target == nil {
		return fmt.Errorf("there is no design %d to make current", n)
	}
	target.Marked = true
	if t.CurrentDesign == target.ID {
		return nil
	}
	t.CurrentDesign = target.ID
	recordTask(v, now, t, "task.design_current", fmt.Sprintf("%s marked design %d current on %s", designer, target.N, t.Objective))
	return nil
}
