package core

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Role is one seat on a project team: a name, the kinds of role it holds,
// and how it runs. Exactly one implementer produces the artifact; reviewers
// judge it against the brief and never change it; a planner works out what
// a task needs before anything is written. A seat can hold the planner role
// alongside one other, as when the owner's Ada both plans and implements.
type Role struct {
	Name  string   `json:"name"`
	Kinds []string `json:"kinds"`
	// LegacyKind is the one kind a seat held before seats could hold several;
	// it is read into Kinds and never written again.
	LegacyKind   string `json:"kind,omitempty"`
	Engine       string `json:"engine"`
	Model        string `json:"model,omitempty"`
	Effort       string `json:"effort,omitempty"`
	Instructions string `json:"instructions,omitempty"`
	// Member is the team member this role was copied from, if any.
	Member string `json:"member,omitempty"`
	// Learnings are the member's, as they were when the task started.
	Learnings []Learning `json:"learnings,omitempty"`
}

// Holds reports whether the seat holds a kind of role.
func (r Role) Holds(kind string) bool { return slices.Contains(r.Kinds, kind) }

// Working is the seat's one kind other than planner and PM: what it does
// once the work has started, and what a message to it reaches. A seat that
// only plans or keeps the list has none.
func (r Role) Working() string {
	for _, kind := range r.Kinds {
		if kind != RolePlanner && kind != RolePM {
			return kind
		}
	}
	return ""
}

const (
	RoleImplementer = "implementer"
	RoleReviewer    = "reviewer"
	// RoleQA runs the playbook's check command against a revision and reports
	// what failed. It may write while it runs; the medium discards it after.
	RoleQA = "qa"
	// RolePlanner works out, before anything is written, what the task needs:
	// what exists, what will change, what is unclear and what it waits on.
	// It only reads.
	RolePlanner = "planner"
	// RolePM keeps the project's to-do list: the order work starts in and
	// what waits for what. It directs no one; the owner and the assistant
	// can overrule it.
	RolePM = "pm"
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
	// Sign is whether the team's commits are signed: "" as the owner's git
	// config for the repository says, SignAlways or SignNever.
	Sign string `json:"sign,omitempty"`
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
	SignAlways      = "always"
	SignNever       = "never"
)

// Templates are the playbooks the assistant starts a project from.
var Templates = map[string]Playbook{
	"draft": {
		Template: "draft",
		Medium:   MediumDocuments,
		Roles: []Role{
			{Name: "Writer", Kinds: []string{RoleImplementer}, Engine: "claude", Instructions: "Write the deliverable the brief asks for as files in the working directory. Prefer Markdown."},
			{Name: "Reviewer", Kinds: []string{RoleReviewer}, Engine: "codex", Instructions: "Judge the draft strictly against the brief's goal, audience, constraints and criteria."},
		},
		MaxRounds: 3,
		Deliver:   "owner",
	},
	"code": {
		Template: "code",
		Medium:   MediumGit,
		Roles: []Role{
			{Name: "Planner", Kinds: []string{RolePlanner}, Engine: "claude", Instructions: "Work out what this task needs before anything is written: read the repository, find what already exists, and say what will change, what is out of scope, what is unclear and what it has to wait for."},
			{Name: "Implementer", Kinds: []string{RoleImplementer}, Engine: "claude", Model: "opus", Instructions: "Implement the task in this repository with tests, following the repository's own conventions and instructions."},
			{Name: "Reviewer", Kinds: []string{RoleReviewer}, Engine: "codex", Instructions: "Review the change against the brief and the task's criteria, as a careful senior engineer: correctness first, then design and tests."},
			{Name: "QA", Kinds: []string{RoleQA}, Engine: "codex", Instructions: "Run the project's check exactly as given and report what failed."},
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
		if p.Sign != "" && p.Sign != SignAlways && p.Sign != SignNever {
			return errors.New(`sign must be empty (follow your git config), "always" or "never"`)
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
		// Messages find a role by name ignoring case, so names must differ
		// by more than case.
		key := strings.ToLower(strings.TrimSpace(r.Name))
		if !required(r.Name) || names[key] {
			return errors.New("each role needs a distinct name")
		}
		names[key] = true
		if r.Engine != "codex" && r.Engine != "claude" {
			return fmt.Errorf("role %s: engine must be codex or claude", r.Name)
		}
		if err := seatKinds(r); err != nil {
			return err
		}
		switch {
		case r.Holds(RoleImplementer):
			implementers++
		case r.Holds(RoleReviewer):
			reviewers++
		case r.Holds(RoleQA) && strings.TrimSpace(p.Check) == "":
			return fmt.Errorf("role %s runs the check, but the team has no check command", r.Name)
		}
	}
	if implementers != 1 || reviewers < 1 {
		return errors.New("a playbook needs exactly one implementer and at least one reviewer")
	}
	return nil
}

// seatKinds checks what one seat holds: known kinds, each once, and at most
// one of implementer, reviewer and QA. Verdicts and messages name the seat,
// so a seat that both reviewed and ran QA would have its verdicts collide,
// and one that reviewed its own work would not be a review. The planner role
// sits alongside any one of them.
func seatKinds(r Role) error {
	if len(r.Kinds) == 0 {
		return fmt.Errorf("role %s holds no kind of role", r.Name)
	}
	working := 0
	for i, kind := range r.Kinds {
		switch kind {
		case RoleImplementer, RoleReviewer, RoleQA:
			working++
		case RolePlanner, RolePM:
		default:
			return fmt.Errorf("role %s: kind must be planner, pm, implementer, reviewer or qa", r.Name)
		}
		if slices.Contains(r.Kinds[:i], kind) {
			return fmt.Errorf("role %s holds %s twice", r.Name, kind)
		}
	}
	if working > 1 {
		return fmt.Errorf("role %s can hold only one of implementer, reviewer and QA, alongside planning and PM", r.Name)
	}
	return nil
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

func engineLabel(engine string) string {
	switch engine {
	case "codex":
		return "Codex"
	case "claude":
		return "Claude"
	}
	return engine
}

func playbookSummary(p Playbook) string {
	parts := make([]string, 0, len(p.Roles))
	for _, r := range p.Roles {
		parts = append(parts, r.Name+" on "+engineLabel(r.Engine))
	}
	return strings.Join(parts, ", ")
}
