package core

import "slices"

// Stages place a task on its project's board. They are derived from the
// loop's own state whenever the state is read or written, never set directly,
// so a board can never disagree with what the loop is doing.
const (
	StageTodo         = "todo"
	StageTriage       = "triage"
	StageResearching  = "researching"
	StageDesigning    = "designing"
	StageImplementing = "implementing"
	StageReviewing    = "reviewing"
	StageQA           = "qa"
	// StagePROpening and StagePROpen are a task landing through a pull
	// request: its checks passed and the pull request is still to open, and
	// open but not yet ready to land.
	StagePROpening = "pr_opening"
	StagePROpen    = "pr_open"
	StageReady     = "ready"
	StageDone      = "done"
	StageStopped   = "stopped"
)

func deriveStages(v *Snapshot) {
	index := blocking(v)
	for i := range v.Tasks {
		deriveWith(v, &v.Tasks[i], index)
	}
}

func derive(v *Snapshot, t *Task) { deriveWith(v, t, blocking(v)) }

// deriveWith derives t's place, with index saying which tasks depend on
// which, worked out once when deriving the whole snapshot.
func deriveWith(v *Snapshot, t *Task, index map[string][]string) {
	view := *t
	if len(view.Roles) == 0 && (t.Status == TaskQueued || t.Status == TaskTriage) {
		if p := project(v, t.ProjectID); p != nil {
			view.Roles = playbookRoles(p)
		}
	}
	t.Takes = view.nextTakers(briefVersion(v, *t))
	defer func() {
		for _, take := range t.Takes {
			kind := stepKinds[t.Status]
			group := ""
			if t.Status == TaskReviewing {
				if seat, ok := t.Role(t.Checking); ok {
					kind = seat.Working()
					group = t.CheckerGroup(seat.Name)
				}
			}
			if take.Kind == kind && take.Group == group && take.Preferred == "" && len(take.Others) > 1 && slices.Contains(take.Others, t.Checking) {
				t.Checking = ""
			}
		}
		claimed, count := "", 0
		for _, c := range t.Claims {
			if c.Step != t.Status {
				continue
			}
			if c.Step == TaskReviewing {
				r, ok := t.Role(c.Seat)
				if !ok || (t.Stage == StageQA) != (r.Working() == RoleQA) {
					continue
				}
			} else if c.Step != TaskResearching && c.Step != TaskDesigning {
				continue
			}
			claimed = c.Seat
			count++
		}
		if count == 1 {
			t.Checking = claimed
		} else if count > 1 {
			t.Checking = ""
		}
	}()
	t.Stage, t.Checking, t.WithDesigner, t.Answered, t.WaitsFor = stageOf(v, *t), "", false, false, nil
	// A task shows in the stage it holds: one waiting for room in the next stays in the one it finished;
	// one waiting for a teammate holds the column it entered. See stage_limits.go.
	if p := project(v, t.ProjectID); p != nil && p.Playbook != nil &&
		t.Place != "" && !t.Finished() && t.Status != TaskQueued && t.Status != TaskTriage {
		t.Stage = t.Place
		// Backward handoffs never need room, even before Schedule updates Place.
		if stage := placeStage(v, *t); t.Active() && stage != "" && !later(stage, t.Place) {
			t.Stage = stage
		}
	}
	t.PMDeciding = pmDeciding(v, *t)
	for i := range t.Blockers {
		b := &t.Blockers[i]
		b.AnswerPending = false
		b.CurrentSettlement = ""
		if b.Kind == BlockerPrerequisite {
			b.CurrentSettlement = PrerequisiteSettlementID(*b)
		}
		if b.Kind == BlockerPrerequisite && b.ClearedAt == nil {
			for _, d := range v.Decisions {
				if d.TaskID == t.ID && d.BlockerID == b.ID && d.Kind == DecisionPrerequisite && d.Status == DecisionOpen {
					b.AnswerPending = true
					break
				}
			}
		}
	}
	t.Blocks = index[t.ID]
	t.WaitingOn = nil
	t.Ref = ""
	if p := project(v, t.ProjectID); p != nil {
		t.Ref = p.TaskRef(t.Number)
	}
	// A task under way can be made to wait too: then it can't land yet.
	if !t.Finished() {
		t.WaitsFor = waitsFor(v, *t)
		t.WaitingOn = waitingOn(v, *t)
	}
	if t.Status == TaskTriage {
		if p := project(v, t.ProjectID); p != nil {
			if pm, ok := p.PMSeat(); ok {
				t.Checking = pm.Name
			}
		}
	}
	if t.Status == TaskResearching {
		if researcher, ok := t.Researcher(); ok {
			t.Checking = researcher.Name
		}
	}
	if t.Status == TaskDesigning {
		t.WithDesigner = true
		if designer, ok := t.Designer(); ok {
			t.Checking = designer.Name
		}
	}
	// A task in triage may wait on the owner's answer to the PM too.
	if t.Status == TaskWaiting || (t.Status == TaskTriage && t.DecisionID != "") {
		d := decision(v, t.DecisionID)
		t.Answered = d != nil && d.Status != DecisionOpen
	}
	if t.Status != TaskReviewing && t.Status != TaskDeciding {
		return
	}
	if next, ok := t.nextChecker(briefVersion(v, *t)); ok {
		t.Checking = next.Name
	}
}

func stageOf(v *Snapshot, t Task) string {
	switch t.Status {
	case TaskQueued:
		return StageTodo
	case TaskTriage:
		return StageTriage
	case TaskResearching:
		return StageResearching
	case TaskDesigning:
		return StageDesigning
	case TaskWriting:
		if t.Proposal != nil && t.Proposal.Answering {
			return StagePROpen
		}
		return StageImplementing
	case TaskReviewing, TaskDeciding:
		// A pull request whose latest draft is pushed is past its checks:
		// it is being decided whether it merges.
		if t.Status == TaskDeciding && t.PROpen() && len(t.Revisions) > 0 && t.Proposal.Pushed == t.Revisions[len(t.Revisions)-1].Ref {
			return landingStage(t)
		}
		return checkStage(v, t)
	case TaskWaiting:
		return waitingStage(v, t)
	case TaskLanding, TaskAwaiting:
		return landingStage(t)
	case TaskDelivered, TaskLanded:
		return StageDone
	case TaskStopped:
		return StageStopped
	}
	return StageTodo
}

// RolesOf is the task's roles of one kind, in team order.
func (t Task) RolesOf(kind string) []Role {
	var out []Role
	for _, r := range t.Roles {
		if r.Holds(kind) {
			out = append(out, r)
		}
	}
	return out
}

// Researcher is the seat that researches the task before anything is
// written, if its team has one.
func (t Task) Researcher() (Role, bool) {
	researchers, _ := t.preferredSeats(RoleResearcher, "")
	if len(researchers) == 0 {
		return Role{}, false
	}
	return researchers[0], true
}

// Role is the task's team member with this name.
func (t Task) Role(name string) (Role, bool) {
	for _, r := range t.Roles {
		if r.Name == name {
			return r, true
		}
	}
	return Role{}, false
}

// Checkers judge each revision in this order: every reviewer, then QA. Seats
// filled from one team member judge as one, so each checker group appears
// once, as its first seat.
func (t Task) Checkers() []Role {
	var out []Role
	for _, g := range t.CheckerGroups() {
		seats, _ := t.preferredSeats(g.Seats[0].Working(), g.Key)
		out = append(out, seats[0])
	}
	return out
}

// Judged is whether a role, or any seat in its checker group, has judged a
// revision against this version of the brief and of the task's objective
// and criteria; a change to either asks every checker again.
func (t Task) Judged(role string, revision, briefVersion int) bool {
	group := t.CheckerGroup(role)
	return slices.ContainsFunc(t.Verdicts, func(v Verdict) bool {
		return v.Revision == revision && t.CheckerGroup(v.Role) == group && t.Counts(v, briefVersion)
	})
}

// Counts reports whether a verdict still counts: made against this version
// of the brief and the task's current text, and not set aside by an answer.
func (t Task) Counts(v Verdict, briefVersion int) bool {
	return v.BriefVersion == briefVersion && v.TextVersion == t.TextVersion && !v.Answered
}

// nextChecker is the first checker still to judge the latest revision.
func (t Task) nextChecker(briefVersion int) (Role, bool) {
	n := len(t.Revisions)
	if n == 0 {
		return Role{}, false
	}
	for _, r := range t.AcceptanceCheckers(briefVersion) {
		if !t.Judged(r.Name, t.Revisions[n-1].N, briefVersion) {
			return r, true
		}
	}
	return Role{}, false
}

// checkStage is the column of whoever checks next: reviewing, then QA if the
// team has it, and the last check's column once every checker has judged.
func checkStage(v *Snapshot, t Task) string {
	if len(t.Revisions) == 0 {
		return StageImplementing
	}
	next, ok := t.nextChecker(briefVersion(v, t))
	switch {
	case !ok:
		return lastCheck(t)
	case next.Holds(RoleQA):
		return StageQA
	}
	return StageReviewing
}

func briefVersion(v *Snapshot, t Task) int {
	if p := project(v, t.ProjectID); p != nil {
		return p.Brief.Version
	}
	return 0
}

func lastCheck(t Task) string {
	if len(t.RolesOf(RoleQA)) > 0 {
		return StageQA
	}
	return StageReviewing
}

// landingStage is where a task on its way out stands: Ready, or for one
// landing through a pull request, how far the pull request has got.
func landingStage(t Task) string {
	switch {
	case !t.UsesPRs():
		return StageReady
	case !t.PROpen():
		return StagePROpening
	case t.Proposal.Observed != nil && t.Proposal.Observed.Ready:
		return StageReady
	}
	return StagePROpen
}

// passedStage is the stage a task enters once every check passed it.
func passedStage(t Task) string {
	if t.UsesPRs() && !t.PROpen() {
		return StagePROpening
	}
	return landingStage(t)
}

// waitingStage keeps a task that needs the owner in the column it stopped
// in: approval is the last step before landing; a question stays with
// whoever asked it, a design question with the step that asked for design
// input, since the answer goes back there; a failure with the step that
// failed.
func waitingStage(v *Snapshot, t Task) string {
	var kind string
	if d := decision(v, t.DecisionID); d != nil {
		kind = d.Kind
	}
	switch kind {
	case DecisionDelivery, DecisionReadyForReview, DecisionOutsideThreads:
		return landingStage(t)
	case DecisionUpdate:
		if t.UsesPRs() {
			return StagePROpen
		}
		return StageReady
	case DecisionFailure:
		if t.ResumeStatus == "" || t.ResumeStatus == TaskWaiting {
			return StageImplementing
		}
		resumed := t
		resumed.Status = t.ResumeStatus
		return stageOf(v, resumed)
	case DecisionQuestion:
		// A design question stays with the step that asked for design input.
		if r := t.DesignDecision(t.DecisionID); r != nil {
			return stageOf(v, Task{Status: r.Step})
		}
		// A question stays with whoever asked it, where that is recorded.
		if a := t.Asker; a != nil && a.Decision == t.DecisionID {
			if a.Step == TaskResearching {
				return StageResearching
			}
			if r, ok := t.Role(a.From); ok && r.Holds(RoleQA) {
				return StageQA
			}
			return StageReviewing
		}
		// Otherwise, before anything is written, only the researcher asks.
		if len(t.Revisions) == 0 {
			return StageResearching
		}
		latest := t.Revisions[len(t.Revisions)-1].N
		for _, verdict := range t.Verdicts {
			if verdict.Revision != latest || verdict.Outcome != VerdictQuestion {
				continue
			}
			if r, ok := t.Role(verdict.Role); ok && r.Holds(RoleQA) {
				return StageQA
			}
		}
		return StageReviewing
	}
	return lastCheck(t)
}
