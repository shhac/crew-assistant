package core

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/shhac/crew-assistant/internal/config"
)

// Role is one seat on a project team: a name, the kinds of role it holds,
// and how it runs. An implementer produces the artifact; reviewers
// judge it against the brief and never change it; a researcher works out
// what a task needs before anything is written; a designer gives design
// input when the researcher or the implementer hands it the task. A seat can
// hold those roles alongside one other, as when the owner's Ada both
// researches and implements.
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
	// Browser is whether QA in this seat uses its engine's own browser.
	Browser Browser `json:"browser,omitzero"`
}

// roleKinds are the kinds of role a member or seat can hold, in the order a
// team works.
var roleKinds = []string{RolePM, RoleResearcher, RoleDesigner, RoleImplementer, RoleReviewer, RoleQA}

// IsRoleKind reports whether kind is a kind of role, which a member can't
// be named.
func IsRoleKind(kind string) bool { return slices.Contains(roleKinds, kind) }

// working reports whether a kind of role does the work once it has started,
// rather than researching it, advising on its design or keeping the list.
func working(kind string) bool {
	return kind != RoleResearcher && kind != RoleDesigner && kind != RolePM
}

// Holds reports whether the seat holds a kind of role.
func (r Role) Holds(kind string) bool { return slices.Contains(r.Kinds, kind) }

// Working is the seat's one kind other than researcher, designer and PM:
// what it does once the work has started, and what a message to it reaches.
// A seat that only researches, designs or keeps the list has none.
func (r Role) Working() string {
	i := slices.IndexFunc(r.Kinds, working)
	if i < 0 {
		return ""
	}
	return r.Kinds[i]
}

const (
	RoleImplementer = "implementer"
	RoleReviewer    = "reviewer"
	// RoleQA runs the playbook's check command against a revision and reports
	// what failed. It may write while it runs; the medium discards it after.
	RoleQA = "qa"
	// RoleResearcher works out, before anything is written, what the task
	// needs: what exists, what will change, what is unclear and what it waits
	// on. It only reads. Older state calls it the planner.
	RoleResearcher = "researcher"
	// RoleDesigner gives design input when the researcher or the implementer
	// hands it the task, then hands it back. It only reads. No template has
	// one: a team has a designer only when a member is seated as one.
	RoleDesigner = "designer"
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
	// CheckInCopy has QA run the check in a writable copy of the revision in
	// its scratch folder, for a check that writes into the tree it runs in.
	// Otherwise it runs in the read-only checkout itself. Either way the
	// revision checked stays exactly as it was recorded.
	CheckInCopy bool `json:"check_in_copy,omitempty"`
	// CheckLoopback lets QA's check bind and reach this machine's own
	// addresses, for tests that start a local server; never wider network.
	CheckLoopback bool `json:"check_loopback,omitempty"`
	// Run is how QA starts the project and reaches it on this machine, to
	// use the app as well as run the check; nil keeps QA to the check.
	Run *RunRecipe `json:"run,omitempty"`
	// Sign is whether the team's commits are signed: "" as the owner's git
	// config for the repository says, SignAlways or SignNever.
	Sign string `json:"sign,omitempty"`
	// Land says what landing an approved change means for this project. Only
	// the owner or the assistant sets it; nothing inside the project can.
	Land LandPolicy `json:"land,omitzero"`
	// MaxActive is how many of the project's tasks may be under way at once;
	// 0 is one for each implementer seat. See ActiveCap.
	MaxActive int `json:"max_active,omitempty"`
	// StageLimits is the most tasks each working stage of the board may
	// hold at once, keyed by stage; a stage missing, or 0, has no limit, and
	// To do and triage never have one. See StageLimit.
	StageLimits map[string]int `json:"stage_limits,omitempty"`
}

// limitStages are the stages of the board a project can limit, in board
// order: each working stage, up to landing.
var limitStages = []string{StageResearching, StageDesigning, StageImplementing, StageReviewing, StageQA, StageReady}

// StageLimit is the most tasks the stage may hold at once, or 0 for no limit.
func (p Playbook) StageLimit(stage string) int {
	return p.StageLimits[stage]
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
			{Name: "Researcher", Kinds: []string{RoleResearcher}, Engine: "claude", Instructions: "Work out what this task needs before anything is written: read the repository, find what already exists, and say what will change, what is out of scope, what is unclear and what it has to wait for."},
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
		if p.Run != nil {
			return errors.New("run recipes are for code teams")
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
		if p.Run != nil {
			if err := p.Run.validate(); err != nil {
				return err
			}
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
		if !config.Supports(r.Engine, config.UseRoles) {
			return fmt.Errorf("role %s: engine must be %s", r.Name, strings.Join(config.EnginesFor(config.UseRoles), " or "))
		}
		if err := seatKinds(r); err != nil {
			return err
		}
		if err := r.Browser.validate(r.Engine, r.Holds(RoleQA)); err != nil {
			return fmt.Errorf("role %s: %w", r.Name, err)
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
	if implementers < 1 || reviewers < 1 {
		return errors.New("a playbook needs at least one implementer and at least one reviewer")
	}
	if p.MaxActive < 0 || p.MaxActive > maxActiveLimit {
		return fmt.Errorf("max_active must be between 1 and %d, or 0 for one per implementer seat", maxActiveLimit)
	}
	for stage, limit := range p.StageLimits {
		if !slices.Contains(limitStages, stage) {
			return fmt.Errorf("stage_limits: %q is not a stage that can have a limit; use one of %s", stage, strings.Join(limitStages, ", "))
		}
		if limit < 0 || limit > maxActiveLimit {
			return fmt.Errorf("stage_limits: %s must be between 1 and %d, or 0 for no limit", stage, maxActiveLimit)
		}
	}
	return nil
}

// maxActiveLimit bounds how many tasks a project may have under way at once.
const maxActiveLimit = 10

// seatKinds checks what one seat holds: known kinds, each once, and at most
// one of implementer, reviewer and QA. Verdicts and messages name the seat,
// so a seat that both reviewed and ran QA would have its verdicts collide,
// and one that reviewed its own work would not be a review. The researcher,
// designer and PM roles sit alongside any one of them.
func seatKinds(r Role) error {
	if len(r.Kinds) == 0 {
		return fmt.Errorf("role %s holds no kind of role", r.Name)
	}
	doing := 0
	for i, kind := range r.Kinds {
		if !slices.Contains(roleKinds, kind) {
			return fmt.Errorf("role %s: kind must be one of %s", r.Name, strings.Join(roleKinds, ", "))
		}
		if slices.Contains(r.Kinds[:i], kind) {
			return fmt.Errorf("role %s holds %s twice", r.Name, kind)
		}
		if working(kind) {
			doing++
		}
	}
	if doing > 1 {
		return fmt.Errorf("role %s can hold only one of implementer, reviewer and QA, alongside research, design and PM", r.Name)
	}
	return nil
}

// SetPlaybook replaces how a project's work gets done. Tasks already started
// keep the roles they started with.
func (s *Service) SetPlaybook(ctx context.Context, projectID string, playbook Playbook) (Project, error) {
	return s.EditPlaybook(ctx, projectID, func(_ *Snapshot, _ *Project, pb *Playbook) error {
		*pb = playbook
		return nil
	})
}

// EditPlaybook changes a project's team in one step, so edits made at the
// same time never undo each other. change gets a copy of the team, empty when
// the project has none, and the state as it stands; it must not call back
// into the Service, which is busy with this change until it returns. Tasks
// already started keep the roles they started with.
func (s *Service) EditPlaybook(ctx context.Context, projectID string, change func(snap *Snapshot, p *Project, pb *Playbook) error) (Project, error) {
	var out Project
	err := s.store.update(ctx, func(v *Snapshot) error {
		p := project(v, projectID)
		if p == nil {
			return ErrNotFound
		}
		var playbook Playbook
		if p.Playbook != nil {
			playbook = *p.Playbook
			playbook.Roles = slices.Clone(playbook.Roles)
			playbook.StageLimits = maps.Clone(playbook.StageLimits)
		}
		if err := change(v, p, &playbook); err != nil {
			return err
		}
		if err := playbook.Validate(); err != nil {
			return err
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
		parts = append(parts, r.Name+" on "+config.EngineLabel(r.Engine))
	}
	return strings.Join(parts, ", ")
}
