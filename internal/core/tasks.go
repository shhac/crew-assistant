package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Task is one outcome worked through the loop. Its roles and round limit are
// copied from the playbook when it starts, so a playbook change never reshapes
// work already under way.
type Task struct {
	ID        string   `json:"id"`
	ProjectID string   `json:"project_id"`
	Objective string   `json:"objective"`
	Criteria  []string `json:"criteria"`
	Status    string   `json:"status"`
	Detail    string   `json:"detail,omitempty"`
	Roles     []Role   `json:"roles,omitempty"`
	// Playbook is the team's setup as it was when the task started: its
	// medium and, for code, the repository, check and branch prefix.
	Playbook  *Playbook `json:"playbook,omitempty"`
	MaxRounds int       `json:"max_rounds,omitempty"`
	Round     int       `json:"round"`
	// Direction is what the owner asked for along the way, in their words.
	Direction []string   `json:"direction,omitempty"`
	Revisions []Revision `json:"revisions"`
	Verdicts  []Verdict  `json:"verdicts"`
	// WriterSession resumes the implementer across rounds. Reviewers always
	// start fresh, so no earlier judgement anchors the next.
	WriterSession json.RawMessage `json:"writer_session,omitempty"`
	Failures      int             `json:"failures,omitempty"`
	// ResumeStatus is the step to return to when an owner decision about a
	// failure is answered with a retry.
	ResumeStatus string    `json:"resume_status,omitempty"`
	RetryAt      time.Time `json:"retry_at,omitempty"`
	DecisionID   string    `json:"decision_id,omitempty"`
	// Base, From and Branch record the commit a code task started from, the
	// owner's branch it was on, and the branch its revisions are committed to.
	Base        string `json:"base,omitempty"`
	From        string `json:"from,omitempty"`
	Branch      string `json:"branch,omitempty"`
	DeliveredTo string `json:"delivered_to,omitempty"`
	// CatchUps counts catch-ups while landing, so a target that keeps moving
	// comes to the owner instead of looping.
	CatchUps int `json:"catch_ups,omitempty"`
	// WakeErrors are problems with the implementer's last wake block, shown to
	// it in its next round.
	WakeErrors []string `json:"wake_errors,omitempty"`
	// Approved is the revision the owner approved to land. A revision that
	// only merged it cleanly with landed work keeps that approval.
	Approved int `json:"approved,omitempty"`
	// Proposal is the pull request a task lands through, and the branch the
	// project owns for it.
	Proposal  *Proposal `json:"proposal,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Task statuses. Writing, reviewing and deciding are the loop's own; waiting
// means an owner decision is open; the rest are final.
const (
	TaskQueued    = "queued"
	TaskWriting   = "writing"
	TaskReviewing = "reviewing"
	TaskDeciding  = "deciding"
	TaskWaiting   = "waiting"
	TaskDelivered = "delivered"
	TaskStopped   = "stopped"
	// TaskLanding is landing an approved change; TaskAwaiting is waiting, idle,
	// for something outside the team (CI, a review) before it can go on.
	TaskLanding  = "landing"
	TaskAwaiting = "awaiting"
	TaskLanded   = "landed"
)

func (t Task) Active() bool {
	return t.Status == TaskWriting || t.Status == TaskReviewing || t.Status == TaskDeciding || t.Status == TaskLanding
}

// Finished reports a task that will do nothing more on its own.
func (t Task) Finished() bool {
	return t.Status == TaskDelivered || t.Status == TaskLanded || t.Status == TaskStopped
}

// Proposal is a task's pull request. Branch is owned by the project: it is
// only ever updated with a lease on Pushed, the last commit pushed there.
type Proposal struct {
	Branch string `json:"branch"`
	Pushed string `json:"pushed,omitempty"`
	Number int    `json:"number,omitempty"`
	URL    string `json:"url,omitempty"`
	// Seen is the newest review or comment already passed to the team, and
	// ChecksFor the commit whose failing checks were.
	Seen      time.Time `json:"seen,omitzero"`
	ChecksFor string    `json:"checks_for,omitempty"`
}

// Revision is one snapshot of the artifact, stamped with the brief it answers.
type Revision struct {
	N            int      `json:"n"`
	BriefVersion int      `json:"brief_version"`
	Files        []string `json:"files"`
	// CleanMergeOf is the revision this one merged, unchanged, with work that
	// landed since. The daemon made it; the task's own change is the same.
	CleanMergeOf int `json:"clean_merge_of,omitempty"`
	// Ref identifies the revision in its medium, such as a commit.
	Ref     string    `json:"ref,omitempty"`
	Summary string    `json:"summary,omitempty"`
	At      time.Time `json:"at"`
}

// Verdict is one reviewer's judgement of one revision.
type Verdict struct {
	Revision     int       `json:"revision"`
	Role         string    `json:"role"`
	BriefVersion int       `json:"brief_version"`
	Outcome      string    `json:"outcome"`
	Summary      string    `json:"summary"`
	Findings     []Finding `json:"findings,omitempty"`
	Question     string    `json:"question,omitempty"`
	At           time.Time `json:"at"`
}

const (
	VerdictPass     = "pass"
	VerdictRevise   = "revise"
	VerdictQuestion = "question"
)

type Finding struct {
	Criterion string `json:"criterion,omitempty"`
	Note      string `json:"note"`
}

type TaskInput struct {
	Objective string   `json:"objective"`
	Criteria  []string `json:"criteria"`
}

func task(v *Snapshot, id string) *Task {
	for i := range v.Tasks {
		if v.Tasks[i].ID == id {
			return &v.Tasks[i]
		}
	}
	return nil
}

// QueueTask asks for an outcome. It starts when the loop reaches it.
func (s *Service) QueueTask(ctx context.Context, projectID string, in TaskInput) (Task, error) {
	if !required(in.Objective) {
		return Task{}, errors.New("a task needs an objective")
	}
	now := s.now().UTC()
	out := Task{ID: uid(), ProjectID: projectID, Objective: strings.TrimSpace(in.Objective), Criteria: cleanList(in.Criteria), Status: TaskQueued, Revisions: []Revision{}, Verdicts: []Verdict{}, CreatedAt: now, UpdatedAt: now}
	err := s.store.update(ctx, func(v *Snapshot) error {
		p := project(v, projectID)
		if p == nil {
			return ErrNotFound
		}
		if p.Playbook == nil {
			return errors.New("choose how this project's work gets done before asking for it")
		}
		if !required(p.Brief.Goal) {
			return errors.New("give the project a brief before asking for work")
		}
		v.Tasks = append(v.Tasks, out)
		record(v, now, projectID, "task.queued", out.Objective)
		return nil
	})
	return out, err
}

// NextTask returns the task the loop should work on: the one already under
// way, or else the oldest queued task, which it starts. Phase one works on one
// task at a time.
func (s *Service) NextTask(ctx context.Context) (Task, bool, error) {
	var out Task
	found := false
	err := s.store.update(ctx, func(v *Snapshot) error {
		for _, t := range v.Tasks {
			if t.Active() {
				out, found = t, true
				return nil
			}
		}
		for i := range v.Tasks {
			t := &v.Tasks[i]
			if t.Status != TaskQueued {
				continue
			}
			p := project(v, t.ProjectID)
			if p == nil || p.Playbook == nil {
				continue
			}
			now := s.now().UTC()
			pinned := *p.Playbook
			pinned.Roles = append([]Role(nil), p.Playbook.Roles...)
			pinned.Prepare = append([]string(nil), p.Playbook.Prepare...)
			t.Playbook = &pinned
			t.Roles = append([]Role(nil), p.Playbook.Roles...)
			t.MaxRounds = p.Playbook.MaxRounds
			t.Round = 1
			t.Status = TaskWriting
			t.Detail = ""
			t.UpdatedAt = now
			out, found = *t, true
			record(v, now, t.ProjectID, "task.started", t.Objective)
			return nil
		}
		return nil
	})
	return out, found, err
}

// UpdateTask applies one loop transition atomically. The loop decides what
// happens next; the store makes sure it happens to the current record.
func (s *Service) UpdateTask(ctx context.Context, id string, fn func(*Task, *Project) (activity string, err error)) (Task, error) {
	var out Task
	err := s.store.update(ctx, func(v *Snapshot) error {
		t := task(v, id)
		if t == nil {
			return ErrNotFound
		}
		p := project(v, t.ProjectID)
		if p == nil {
			return ErrNotFound
		}
		activity, err := fn(t, p)
		if err != nil {
			return err
		}
		if t.Finished() {
			cancelTaskWakes(v, t.ID)
		}
		t.UpdatedAt = s.now().UTC()
		if activity != "" {
			record(v, t.UpdatedAt, t.ProjectID, "task."+t.Status, activity)
		}
		out = *t
		return nil
	})
	return out, err
}

// OpenTaskDecision puts a choice about a task in front of the owner and holds
// the task until it is answered.
func (s *Service) OpenTaskDecision(ctx context.Context, taskID, kind string, in DecisionInput) (Decision, error) {
	if !required(in.Title, in.Context, in.Recommendation) || len(in.Choices) < 2 {
		return Decision{}, errors.New("decision requires title, context, recommendation and at least two choices")
	}
	now := s.now().UTC()
	out := Decision{ID: uid(), Kind: kind, TaskID: taskID, Title: in.Title, Context: in.Context, Recommendation: in.Recommendation, Choices: in.Choices, Status: "open", CreatedAt: now}
	err := s.store.update(ctx, func(v *Snapshot) error {
		t := task(v, taskID)
		if t == nil {
			return ErrNotFound
		}
		out.ProjectID = t.ProjectID
		t.Status = TaskWaiting
		t.DecisionID = out.ID
		t.UpdatedAt = now
		v.Decisions = append(v.Decisions, out)
		record(v, now, t.ProjectID, "decision.opened", in.Title)
		return nil
	})
	return out, err
}
