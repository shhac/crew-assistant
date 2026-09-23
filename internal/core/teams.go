package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Brief is what a project is for. Every revision and verdict records the
// version it was made against, so work judged against an older brief is never
// mistaken for work judged against the current one.
type Brief struct {
	Version     int       `json:"version"`
	Goal        string    `json:"goal"`
	Audience    string    `json:"audience,omitempty"`
	Constraints string    `json:"constraints,omitempty"`
	Criteria    []string  `json:"criteria"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type BriefInput struct {
	Goal        string   `json:"goal"`
	Audience    string   `json:"audience"`
	Constraints string   `json:"constraints"`
	Criteria    []string `json:"criteria"`
}

// Role is one member of a project team. Exactly one implementer produces the
// artifact; reviewers judge it against the brief and never change it.
type Role struct {
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	Engine       string `json:"engine"`
	Model        string `json:"model,omitempty"`
	Effort       string `json:"effort,omitempty"`
	Instructions string `json:"instructions,omitempty"`
}

const (
	RoleImplementer = "implementer"
	RoleReviewer    = "reviewer"
)

// Playbook is how a project's work gets done. It is data with a small fixed
// schema, not a workflow language; a field is added only when a real project
// needs it.
type Playbook struct {
	Template  string `json:"template"`
	Medium    string `json:"medium"`
	Roles     []Role `json:"roles"`
	MaxRounds int    `json:"max_rounds"`
	// Deliver names who approves an outward delivery. Only the owner does, for
	// now: trust is extended per playbook once there is evidence to extend it.
	Deliver string `json:"deliver"`
	// DeliverTo is an optional absolute folder a delivered draft is copied to.
	DeliverTo string `json:"deliver_to,omitempty"`
}

const MediumDocuments = "documents"

// Templates are the playbooks the assistant starts a project from.
var Templates = map[string]Playbook{
	"draft": {
		Template: "draft",
		Medium:   MediumDocuments,
		Roles: []Role{
			{Name: "Writer", Kind: RoleImplementer, Engine: "claude", Instructions: "Write the deliverable the brief asks for as files in the working directory. Prefer Markdown."},
			{Name: "Reviewer", Kind: RoleReviewer, Engine: "codex", Instructions: "Judge the draft strictly against the brief's goal, audience, constraints and criteria."},
		},
		MaxRounds: 3,
		Deliver:   "owner",
	},
}

func (p Playbook) Validate() error {
	if p.Medium != MediumDocuments {
		return fmt.Errorf("unsupported medium %q", p.Medium)
	}
	if p.MaxRounds < 1 || p.MaxRounds > 10 {
		return errors.New("max_rounds must be between 1 and 10")
	}
	if p.Deliver != "owner" {
		return errors.New("deliveries are approved by the owner")
	}
	if p.DeliverTo != "" && !filepath.IsAbs(p.DeliverTo) {
		return errors.New("deliver_to must be an absolute folder")
	}
	implementers, reviewers := 0, 0
	names := map[string]bool{}
	for _, r := range p.Roles {
		if !required(r.Name) || names[r.Name] {
			return errors.New("each role needs a distinct name")
		}
		names[r.Name] = true
		if r.Engine != "codex" && r.Engine != "claude" {
			return fmt.Errorf("role %s: engine must be codex or claude", r.Name)
		}
		switch r.Kind {
		case RoleImplementer:
			implementers++
		case RoleReviewer:
			reviewers++
		default:
			return fmt.Errorf("role %s: kind must be implementer or reviewer", r.Name)
		}
	}
	if implementers != 1 || reviewers < 1 {
		return errors.New("a playbook needs exactly one implementer and at least one reviewer")
	}
	return nil
}

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
	MaxRounds int      `json:"max_rounds,omitempty"`
	Round     int      `json:"round"`
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
	DeliveredTo  string    `json:"delivered_to,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
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
)

func (t Task) Active() bool {
	return t.Status == TaskWriting || t.Status == TaskReviewing || t.Status == TaskDeciding
}

// Revision is one snapshot of the artifact, stamped with the brief it answers.
type Revision struct {
	N            int       `json:"n"`
	BriefVersion int       `json:"brief_version"`
	Files        []string  `json:"files"`
	Summary      string    `json:"summary,omitempty"`
	At           time.Time `json:"at"`
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

func cleanList(items []string) []string {
	out := []string{}
	for _, item := range items {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// UpdateBrief replaces a project's brief with a new version.
func (s *Service) UpdateBrief(ctx context.Context, projectID string, in BriefInput) (Project, error) {
	if !required(in.Goal) {
		return Project{}, errors.New("a brief needs a goal")
	}
	var out Project
	err := s.store.update(ctx, func(v *Snapshot) error {
		p := project(v, projectID)
		if p == nil {
			return ErrNotFound
		}
		now := s.now().UTC()
		p.Brief = Brief{Version: p.Brief.Version + 1, Goal: strings.TrimSpace(in.Goal), Audience: strings.TrimSpace(in.Audience), Constraints: strings.TrimSpace(in.Constraints), Criteria: cleanList(in.Criteria), UpdatedAt: now}
		p.UpdatedAt = now
		out = *p
		record(v, now, p.ID, "brief.updated", fmt.Sprintf("Brief for %s is now version %d", p.Title, p.Brief.Version))
		return nil
	})
	return out, err
}

// SetPlaybook replaces how a project's work gets done. Tasks already started
// keep the roles they started with.
func (s *Service) SetPlaybook(ctx context.Context, projectID string, playbook Playbook) (Project, error) {
	if err := playbook.Validate(); err != nil {
		return Project{}, err
	}
	var out Project
	err := s.store.update(ctx, func(v *Snapshot) error {
		p := project(v, projectID)
		if p == nil {
			return ErrNotFound
		}
		p.Playbook = &playbook
		p.UpdatedAt = s.now().UTC()
		out = *p
		record(v, p.UpdatedAt, p.ID, "playbook.set", "Team for "+p.Title+": "+playbookSummary(playbook))
		return nil
	})
	return out, err
}

func playbookSummary(p Playbook) string {
	parts := make([]string, 0, len(p.Roles))
	for _, r := range p.Roles {
		parts = append(parts, r.Name+" ("+r.Engine+")")
	}
	return strings.Join(parts, ", ")
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
