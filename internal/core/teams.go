package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
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
	// RoleQA runs the playbook's check command against a revision and reports
	// what failed. It may write while it runs; the medium discards it after.
	RoleQA = "qa"
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
	// Repo, BranchPrefix, Check and Prepare belong to the git medium: the
	// repository to clone (one of the project's folders), the prefix for
	// delivered branches, the command QA runs, and ignored dependency paths to
	// copy into the clone.
	Repo         string   `json:"repo,omitempty"`
	BranchPrefix string   `json:"branch_prefix,omitempty"`
	Check        string   `json:"check,omitempty"`
	Prepare      []string `json:"prepare,omitempty"`
	// Land says what landing an approved change means for this project. Only
	// the owner or the assistant sets it; nothing inside the project can.
	Land LandPolicy `json:"land,omitzero"`
}

// LandPolicy is what "landing" means for a code project: prose for people and
// agents, plus the few fields the loop needs to do it.
type LandPolicy struct {
	Means string `json:"means,omitempty"`
	// Via is LandBranch (a new local branch), LandPush (a fast-forward push
	// onto Target) or LandPullRequest (a GitHub pull request into Target).
	Via    string `json:"via,omitempty"`
	Target string `json:"target,omitempty"`
	// Method is fast-forward for a push, or squash, merge or rebase for a
	// pull request.
	Method string `json:"method,omitempty"`
	// GitHub is the owner/name repository a pull request is opened on.
	GitHub string `json:"github,omitempty"`
	// Approve is ApproveBefore (the owner approves before landing, or before a
	// pull request opens) or ApproveNone.
	Approve string `json:"approve,omitempty"`
}

const (
	LandBranch      = "branch"
	LandPush        = "push"
	LandPullRequest = "pull-request"
	ApproveBefore   = "before"
	ApproveNone     = "none"
)

// Way is how a change lands, defaulting to a new branch.
func (l LandPolicy) Way() string {
	if l.Via == "" {
		return LandBranch
	}
	return l.Via
}

// MergeMethod is how a pull request merges, squash unless set.
func (l LandPolicy) MergeMethod() string {
	if l.Method == "" {
		return "squash"
	}
	return l.Method
}

// AsksFirst reports whether the owner approves before a change lands.
func (l LandPolicy) AsksFirst() bool { return l.Approve != ApproveNone }

var githubRepo = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)

func (l LandPolicy) validate() error {
	if len(l.Means) > 2000 {
		return errors.New("what landing means must fit in 2000 characters")
	}
	if l.Approve != "" && l.Approve != ApproveBefore && l.Approve != ApproveNone {
		return errors.New("approve must be before or none")
	}
	switch l.Way() {
	case LandBranch:
		if l.Target != "" || l.Method != "" || l.GitHub != "" {
			return errors.New("landing on a new branch takes no target, method or github repository")
		}
	case LandPush:
		if !branchName(l.Target) {
			return errors.New("landing by push needs the target branch, such as main")
		}
		if l.Method != "" && l.Method != "fast-forward" {
			return errors.New("a push only lands by fast-forward")
		}
	case LandPullRequest:
		if !branchName(l.Target) {
			return errors.New("a pull request needs the branch it merges into, such as main")
		}
		if !githubRepo.MatchString(l.GitHub) {
			return errors.New("a pull request needs the GitHub repository as owner/name")
		}
		if l.Method != "" && l.Method != "squash" && l.Method != "merge" && l.Method != "rebase" {
			return errors.New("a pull request merges by squash, merge or rebase")
		}
	default:
		return errors.New("landing is via branch, push or pull-request")
	}
	return nil
}

func branchName(name string) bool {
	return name != "" && len(name) <= 200 && !strings.HasPrefix(name, "-") && !strings.HasSuffix(name, "/") && !strings.HasSuffix(name, ".lock") &&
		!strings.ContainsAny(name, " ~^:?*[\\") && !strings.Contains(name, "..") && !strings.Contains(name, "@{")
}

const (
	MediumDocuments = "documents"
	MediumGit       = "git"
)

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
	"code": {
		Template: "code",
		Medium:   MediumGit,
		Roles: []Role{
			{Name: "Implementer", Kind: RoleImplementer, Engine: "claude", Model: "opus", Instructions: "Implement the task in this repository with tests, following the repository's own conventions and instructions."},
			{Name: "Reviewer", Kind: RoleReviewer, Engine: "codex", Instructions: "Review the change against the brief and the task's criteria, as a careful senior engineer: correctness first, then design and tests."},
			{Name: "QA", Kind: RoleQA, Engine: "codex", Instructions: "Run the project's check exactly as given and report what failed."},
		},
		MaxRounds:    3,
		Deliver:      "owner",
		BranchPrefix: "crew/",
	},
}

func (p Playbook) Validate() error {
	switch p.Medium {
	case MediumDocuments:
		if p.Land != (LandPolicy{}) {
			return errors.New("landing policies are for code teams")
		}
	case MediumGit:
		if !filepath.IsAbs(p.Repo) {
			return errors.New("a code team needs the repository it works on")
		}
		if p.BranchPrefix == "" || strings.ContainsAny(p.BranchPrefix, " ~^:?*[\\") || strings.Contains(p.BranchPrefix, "..") {
			return errors.New("branch_prefix must be a simple branch-name prefix, such as crew/")
		}
		for _, rel := range p.Prepare {
			if filepath.IsAbs(rel) || strings.HasPrefix(filepath.Clean(rel), "..") {
				return fmt.Errorf("prepare path %q must be inside the repository", rel)
			}
		}
		if err := p.Land.validate(); err != nil {
			return err
		}
	default:
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
		case RoleQA:
			if strings.TrimSpace(p.Check) == "" {
				return fmt.Errorf("role %s runs the check, but the team has no check command", r.Name)
			}
		default:
			return fmt.Errorf("role %s: kind must be implementer, reviewer or qa", r.Name)
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
