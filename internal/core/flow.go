package core

import (
	"cmp"
	"slices"
)

// A flow is the stages a task works through between leaving the to-do list
// and asking to land: data with a small fixed schema whose meaning Go owns,
// as design-docs/2026-11-01-pm-designed-flows.md describes. Nothing reads a
// flow to decide anything yet. DefaultFlow describes what the loop already
// does, and Place reads a task's record against it, so the two can be
// compared before any of the loop's transitions move over to flows.

// Kinds of flow stage.
const (
	// FlowAdvise reads and reports, as research does.
	FlowAdvise = "advise"
	// FlowMake writes the task's next revision.
	FlowMake = "make"
	// FlowChecks runs its checks together on one revision.
	FlowChecks = "checks"
	// FlowOnCall is a stage no task passes through: a turn asks it, and the
	// task goes back to the stage that asked, as design does.
	FlowOnCall = "on_call"
)

// What a check does with the revision it judges.
const (
	CapabilityCheck          = "check"
	CapabilityCheckByRunning = "check_by_running"
)

// The default flows' stage ids. Verdicts, claims and moves will name stages
// by these, never by their names.
const (
	FlowStageResearch  = "research"
	FlowStageDesign    = "design"
	FlowStageImplement = "implement"
	FlowStageWrite     = "write"
	FlowStageChecks    = "checks"
)

// Where a task stands outside its flow's stages: before it, deciding after
// its checks, waiting for the owner, in delivery, or done.
const (
	FlowQueued   = "queued"
	FlowTriage   = "triage"
	FlowDeciding = "deciding"
	FlowWaiting  = "waiting"
	FlowLanding  = "landing"
	FlowFinished = "finished"
)

type Flow struct {
	Stages []FlowStage `json:"stages"`
}

type FlowStage struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Role is the kind of role whose seats take an advise, make or on-call
	// stage's turns; a checks stage's seats are its checks'.
	Role   string      `json:"role,omitempty"`
	Checks []FlowCheck `json:"checks,omitempty"`
	// AskedFrom are the stages a turn may send the task here from out of
	// order. Once this stage is done the task goes back to the one that
	// asked.
	AskedFrom []string `json:"asked_from,omitempty"`
	// Enter and Skip are conditions, each a list of facts that must all
	// hold. Today's flows have none.
	Enter []FlowFact `json:"enter,omitempty"`
	Skip  []FlowFact `json:"skip,omitempty"`
}

// FlowCheck is one check of a checks stage: a checker group, whose seats
// judge a revision as one.
type FlowCheck struct {
	// ID is the checker group's key, which a renamed member's seats keep.
	ID         string   `json:"id"`
	Role       string   `json:"role"`
	Seats      []string `json:"seats"`
	Capability string   `json:"capability"`
	// Gates is whether the check must be satisfied before landing, rather
	// than only advise.
	Gates bool `json:"gates"`
}

// FlowFact is a fact from the fixed vocabulary a condition is made of.
type FlowFact struct {
	Name string   `json:"name"`
	Args []string `json:"args,omitempty"`
}

// DefaultFlow is the flow the loop works a team through today: research if
// the team has a researcher, design on call if it has a designer, the make
// stage, and every reviewer and QA checking each revision together. A
// documents team has the same shape as a code team; only its make stage is
// named for writing.
func DefaultFlow(medium string, roles []Role) Flow {
	team := Task{Roles: roles}
	build := FlowStage{ID: FlowStageImplement, Name: "Implement", Kind: FlowMake, Role: RoleImplementer}
	if medium == MediumDocuments {
		build.ID, build.Name = FlowStageWrite, "Write"
	}
	checks := FlowStage{ID: FlowStageChecks, Name: "Checks", Kind: FlowChecks}
	for _, g := range team.CheckerGroups() {
		c := FlowCheck{ID: g.Key, Role: g.Seats[0].Working(), Capability: CapabilityCheck, Gates: true}
		if c.Role == RoleQA {
			c.Capability = CapabilityCheckByRunning
		}
		for _, seat := range g.Seats {
			c.Seats = append(c.Seats, seat.Name)
		}
		checks.Checks = append(checks.Checks, c)
	}
	var stages []FlowStage
	researches := len(team.RolesOf(RoleResearcher)) > 0
	if researches {
		stages = append(stages, FlowStage{ID: FlowStageResearch, Name: "Research", Kind: FlowAdvise, Role: RoleResearcher, AskedFrom: []string{FlowStageChecks}})
	}
	if len(team.RolesOf(RoleDesigner)) > 0 {
		design := FlowStage{ID: FlowStageDesign, Name: "Design", Kind: FlowOnCall, Role: RoleDesigner}
		if researches {
			design.AskedFrom = append(design.AskedFrom, FlowStageResearch)
		}
		design.AskedFrom = append(design.AskedFrom, build.ID)
		stages = append(stages, design)
	}
	return Flow{Stages: append(stages, build, checks)}
}

// TaskFlow is the flow t works through: its pinned team's, or, before it
// starts, its project's team's, as the board reads it.
func TaskFlow(v *Snapshot, t Task) Flow {
	p := project(v, t.ProjectID)
	playbook, roles := t.Playbook, t.Roles
	if playbook == nil && p != nil {
		playbook = p.Playbook
	}
	if len(roles) == 0 && (t.Status == TaskQueued || t.Status == TaskTriage) {
		roles = playbookRoles(p)
	}
	medium := ""
	if playbook != nil {
		medium = playbook.Medium
	}
	return DefaultFlow(medium, roles)
}

// Stage is the flow's stage with this id.
func (f Flow) Stage(id string) (FlowStage, bool) {
	i := slices.IndexFunc(f.Stages, func(s FlowStage) bool { return s.ID == id })
	if i < 0 {
		return FlowStage{}, false
	}
	return f.Stages[i], true
}

// first is the id of the flow's first stage of a kind, or "" with none.
func (f Flow) first(kind string) string {
	if i := slices.IndexFunc(f.Stages, func(s FlowStage) bool { return s.Kind == kind }); i >= 0 {
		return f.Stages[i].ID
	}
	return ""
}

// stageFor is the stage of the flow a task status works in, or "".
func (f Flow) stageFor(status string) string {
	switch status {
	case TaskResearching:
		return f.first(FlowAdvise)
	case TaskDesigning:
		return f.first(FlowOnCall)
	case TaskWriting:
		return f.first(FlowMake)
	case TaskReviewing:
		return f.first(FlowChecks)
	}
	return ""
}

// FlowPlace is where a task's flow puts it, as read from its record.
type FlowPlace struct {
	// Stage is a stage id of the flow, one of the positions outside it, such
	// as FlowWaiting, or "" for a record the flow has no place for.
	Stage string
	// Within is the stage a task outside the flow's stages belongs to, where
	// the record says: the one a question to the owner goes back to, say.
	Within string
	// Returns is the stage a task asked into a stage out of order goes back
	// to once it is done.
	Returns string
	// Column is the board column the flow puts the task in; "" where
	// delivery or an owner decision, not the flow, decides it.
	Column string
	// Due are the gating checks still to be satisfied on the latest
	// revision.
	Due []FlowCheck
}

// Place is where the flow puts t, from v's record alone. It changes
// nothing.
func (f Flow) Place(v *Snapshot, t Task) FlowPlace {
	var due []FlowCheck
	if len(t.Revisions) > 0 && !t.Finished() {
		due = f.Due(t, briefVersion(v, t))
	}
	at := f.at(v, t, t.Status, due)
	at.Due = due
	return at
}

// Due are the gating checks t's latest revision still needs: each whose
// seats have given no verdict on it that still counts. While the owner's
// acceptance of a draft stands, only checks by running are due again: the
// owner accepted the reading checks' findings, not what merging it with
// later work does.
func (f Flow) Due(t Task, brief int) []FlowCheck {
	if len(t.Revisions) == 0 {
		return nil
	}
	latest := t.Revisions[len(t.Revisions)-1].N
	accepted := t.AcceptanceStands(brief)
	var out []FlowCheck
	for _, s := range f.Stages {
		for _, c := range s.Checks {
			if !c.Gates || (accepted && c.Capability != CapabilityCheckByRunning) {
				continue
			}
			if !slices.ContainsFunc(t.Verdicts, func(v Verdict) bool {
				return v.Revision == latest && slices.Contains(c.Seats, v.Role) && t.Counts(v, brief)
			}) {
				out = append(out, c)
			}
		}
	}
	return out
}

// at is where the flow puts t were its status status.
func (f Flow) at(v *Snapshot, t Task, status string, due []FlowCheck) FlowPlace {
	switch status {
	case TaskQueued:
		return FlowPlace{Stage: FlowQueued, Column: StageTodo}
	case TaskTriage:
		return FlowPlace{Stage: FlowTriage, Column: StageTriage}
	case TaskResearching:
		at := FlowPlace{Stage: f.stageFor(status), Column: StageResearching}
		if t.OpenResearch() != nil {
			at.Returns = f.first(FlowChecks)
		}
		return at
	case TaskDesigning:
		at := FlowPlace{Stage: f.stageFor(status), Column: StageDesigning}
		if r := t.OpenDesign(); r != nil {
			at.Returns = f.stageFor(r.Step)
		}
		return at
	case TaskWriting:
		// Answering a pull request is delivery's, which sends the task back
		// to be made again.
		if t.Proposal != nil && t.Proposal.Answering {
			return FlowPlace{Stage: FlowLanding, Within: f.stageFor(status)}
		}
		return FlowPlace{Stage: f.stageFor(status), Column: StageImplementing}
	case TaskReviewing:
		// With nothing to check, the task goes back to be made.
		if len(t.Revisions) == 0 {
			return FlowPlace{Stage: f.stageFor(TaskWriting), Column: StageImplementing}
		}
		return FlowPlace{Stage: f.stageFor(status), Column: f.checksColumn(due)}
	case TaskDeciding:
		if deliveryBegun(t) {
			return FlowPlace{Stage: FlowDeciding}
		}
		if len(t.Revisions) == 0 {
			return FlowPlace{Stage: FlowDeciding, Within: f.stageFor(TaskWriting), Column: StageImplementing}
		}
		return FlowPlace{Stage: FlowDeciding, Within: f.stageFor(TaskReviewing), Column: f.checksColumn(due)}
	case TaskWaiting:
		return f.waiting(v, t, due)
	case TaskLanding, TaskAwaiting:
		return FlowPlace{Stage: FlowLanding}
	case TaskDelivered, TaskLanded:
		return FlowPlace{Stage: FlowFinished, Column: StageDone}
	case TaskStopped:
		return FlowPlace{Stage: FlowFinished, Column: StageStopped}
	}
	return FlowPlace{}
}

// deliveryBegun reports a pull request that already has the latest
// revision: the task is past its flow, being decided whether it merges.
func deliveryBegun(t Task) bool {
	return t.PROpen() && len(t.Revisions) > 0 && t.Proposal.Pushed == t.Revisions[len(t.Revisions)-1].Ref
}

// checksColumn is the board column of a checks stage: reviewing while a
// reading check is due, then QA while only checks by running are, and once
// none is, the column of the last kind of check the stage has.
func (f Flow) checksColumn(due []FlowCheck) string {
	byRunning := func(c FlowCheck) bool { return c.Capability == CapabilityCheckByRunning }
	switch {
	case slices.ContainsFunc(due, func(c FlowCheck) bool { return !byRunning(c) }):
		return StageReviewing
	case len(due) > 0:
		return StageQA
	}
	for _, s := range f.Stages {
		if slices.ContainsFunc(s.Checks, byRunning) {
			return StageQA
		}
	}
	return StageReviewing
}

// waiting is where the flow puts a task waiting for the owner. A question
// belongs to whoever asked it, which the answer goes back to, and a failure
// to the step it resumes; delivery's decisions, and any other, are not the
// flow's to place.
func (f Flow) waiting(v *Snapshot, t Task, due []FlowCheck) FlowPlace {
	at := FlowPlace{Stage: FlowWaiting}
	d := decision(v, t.DecisionID)
	if d == nil {
		return at
	}
	within := func(in FlowPlace) FlowPlace {
		at.Within, at.Column = cmp.Or(in.Within, in.Stage), in.Column
		return at
	}
	switch d.Kind {
	case DecisionFailure:
		if t.ResumeStatus == "" || t.ResumeStatus == TaskWaiting {
			return within(FlowPlace{Stage: f.stageFor(TaskWriting), Column: StageImplementing})
		}
		return within(f.at(v, t, t.ResumeStatus, due))
	case DecisionQuestion:
		if r := t.DesignDecision(t.DecisionID); r != nil {
			return within(f.at(v, Task{}, r.Step, nil))
		}
		a := t.Asker
		if a == nil || a.Decision != t.DecisionID {
			return at
		}
		if a.Step == TaskResearching {
			return within(FlowPlace{Stage: f.stageFor(TaskResearching), Column: StageResearching})
		}
		column := StageReviewing
		for _, s := range f.Stages {
			if slices.ContainsFunc(s.Checks, func(c FlowCheck) bool {
				return c.Capability == CapabilityCheckByRunning && slices.Contains(c.Seats, a.From)
			}) {
				column = StageQA
			}
		}
		return within(FlowPlace{Stage: f.stageFor(TaskReviewing), Column: column})
	}
	return at
}
