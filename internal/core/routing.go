package core

import (
	"context"
	"fmt"
	"time"

	"github.com/shhac/crew-assistant/internal/text"
)

// A task need not go through its steps in one line. A question goes back to
// whoever asked it once it is answered; a checker can send the task back to
// the researcher, and it comes back to that checker; and a checker can
// recommend where the task goes next, which the PM weighs where it differs
// from what the checks say.

// Asker is who asked the question an owner decision holds a task for, and
// where the answer goes: back to the researcher, which plans again with it,
// or to the checker, which judges Revision again with it.
type Asker struct {
	Decision string `json:"decision"`
	// From is the seat that asked; Step is TaskResearching or TaskReviewing.
	From     string `json:"from"`
	Step     string `json:"step"`
	Revision int    `json:"revision,omitempty"`
}

// AskQuestion brings the owner a role's question and records who asked, in
// one change, so the answer goes back to them.
func (s *Service) AskQuestion(ctx context.Context, taskID string, asker Asker, in DecisionInput) (Decision, error) {
	if err := in.validTaskDecision(); err != nil {
		return Decision{}, err
	}
	var out Decision
	err := s.store.update(ctx, func(v *Snapshot) error {
		t := task(v, taskID)
		if t == nil {
			return ErrNotFound
		}
		if t.Finished() {
			return fmt.Errorf("the task has finished: %w", ErrConflict)
		}
		out = openTaskDecision(v, t, DecisionQuestion, in, s.now().UTC())
		asker.Decision = out.ID
		t.Asker = &asker
		derive(v, t)
		return nil
	})
	return out, err
}

// BackToAsker sends the task back to whoever asked the question decisionID
// holds it for, in the same round, and gives who that was. The researcher
// plans again; a checker judges its revision again. It changes nothing and
// gives nil when no one is recorded as asking it.
func (t *Task) BackToAsker(decisionID string) *Asker {
	a := t.Asker
	if a == nil || decisionID == "" || a.Decision != decisionID {
		return nil
	}
	if a.Step == TaskResearching {
		if t.Plan != nil {
			t.Plan.Answered = true
		}
	} else {
		t.reopen(a.From, a.Revision)
	}
	t.Status, t.DecisionID, t.Asker = a.Step, "", nil
	return a
}

// reopen sets aside what a checker said of a revision, so it judges the
// revision again.
func (t *Task) reopen(role string, revision int) {
	for i := range t.Verdicts {
		if v := &t.Verdicts[i]; v.Role == role && v.Revision == revision {
			v.Answered = true
		}
	}
}

// ResearchRequest is one time a checker sent the task back to the
// researcher: who asked, about which revision, and what. The researcher
// updates the plan and the task goes back to the checker, which judges the
// revision again with it.
type ResearchRequest struct {
	ID       string `json:"id"`
	From     string `json:"from"`
	Round    int    `json:"round"`
	Revision int    `json:"revision"`
	Question string `json:"question"`
	// Researcher is who answered it. A request past ResearchLimit, or on a
	// team with no researcher, goes to the owner as Decision instead.
	Researcher string    `json:"researcher,omitempty"`
	Decision   string    `json:"decision,omitempty"`
	At         time.Time `json:"at"`
	AnsweredAt time.Time `json:"answered_at,omitzero"`
}

// ResearchLimit is how many times, in one round, the checkers can send the
// task back to the researcher. Past it, the question goes to the owner, so a
// task can never pass back and forth on its own.
const ResearchLimit = 2

// Open reports a request still waiting for the researcher.
func (r ResearchRequest) Open() bool { return r.AnsweredAt.IsZero() && r.Decision == "" }

// OpenResearch is the request the task is with the researcher for.
func (t *Task) OpenResearch() *ResearchRequest {
	for i := len(t.Research) - 1; i >= 0; i-- {
		if r := &t.Research[i]; r.Open() {
			return r
		}
	}
	return nil
}

// ResearchThisRound counts the requests made in the current round.
func (t Task) ResearchThisRound() int {
	n := 0
	for _, r := range t.Research {
		if r.Round == t.Round {
			n++
		}
	}
	return n
}

// ResearchAsk is a checker's request for more research.
type ResearchAsk struct {
	From     string
	Revision int
	Question string
	// Owner is the decision the request becomes past ResearchLimit or
	// without a researcher.
	Owner DecisionInput
}

// AskResearch sends a task being checked back to its researcher with a
// checker's question, recording who asked and what, in one change. Past
// ResearchLimit, or on a team with no researcher, the question goes to the
// owner instead, and the answer comes back to the checker.
func (s *Service) AskResearch(ctx context.Context, taskID string, ask ResearchAsk) (Task, error) {
	if err := ask.Owner.validTaskDecision(); err != nil {
		return Task{}, err
	}
	return s.editTaskRecord(ctx, "", taskID, func(t *Task, v *Snapshot) error {
		if t.Status != TaskReviewing && t.Status != TaskDeciding {
			return fmt.Errorf("the task is no longer being checked: %w", ErrConflict)
		}
		now := s.now().UTC()
		r := ResearchRequest{ID: uid(), From: ask.From, Round: t.Round, Revision: ask.Revision, Question: text.Clip(ask.Question, 4000), At: now}
		researcher, ok := t.Researcher()
		if !ok || t.ResearchThisRound() >= ResearchLimit {
			d := openTaskDecision(v, t, DecisionQuestion, ask.Owner, now)
			r.Decision = d.ID
			t.Asker = &Asker{Decision: d.ID, From: ask.From, Step: TaskReviewing, Revision: ask.Revision}
		} else {
			t.Status, t.Detail = TaskResearching, "With "+researcher.Name+" for more research"
			recordTask(v, now, t, "task.researching", fmt.Sprintf("%s sent %s back to %s for more research", ask.From, t.Objective, researcher.Name))
		}
		t.Research = append(t.Research, r)
		t.Failures, t.RetryAt = 0, time.Time{}
		t.UpdatedAt = now
		derive(v, t)
		return nil
	})
}

// Where a checker can recommend a task goes next: back to the implementer,
// back to the researcher, or on to approval or landing.
const (
	NextRevise   = "revise"
	NextResearch = "research"
	NextLand     = "land"
)

// Implied is where a verdict's outcome sends the task on its own; a
// question goes to the owner, which is no step of the team's.
func (v Verdict) Implied() string {
	switch v.Outcome {
	case VerdictPass:
		return NextLand
	case VerdictRevise:
		return NextRevise
	case VerdictResearch:
		return NextResearch
	}
	return ""
}

// Disagrees reports a recommendation other than where the verdict's own
// outcome sends the task.
func (v Verdict) Disagrees() bool { return v.Next != "" && v.Next != v.Implied() }

// RecordRoute records where the PM sent a task after its checks, and why.
func (s *Service) RecordRoute(ctx context.Context, taskID, pm, next, reason string) error {
	return s.store.update(ctx, func(v *Snapshot) error {
		t := task(v, taskID)
		if t == nil {
			return ErrNotFound
		}
		summary := fmt.Sprintf("%s sent %s on to %s", pm, t.Objective, next)
		if reason != "" {
			summary += ": " + text.Clip(reason, 300)
		}
		recordTask(v, s.now().UTC(), t, "task.routed", summary)
		return nil
	})
}
