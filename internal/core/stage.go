package core

import "slices"

// Stages place a task on its project's board. They are derived from the
// loop's own state whenever the state is read or written, never set directly,
// so a board can never disagree with what the loop is doing.
const (
	StageTodo         = "todo"
	StageResearching  = "researching"
	StageDesigning    = "designing"
	StageImplementing = "implementing"
	StageReviewing    = "reviewing"
	StageQA           = "qa"
	StageReady        = "ready"
	StageDone         = "done"
	StageStopped      = "stopped"
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
	t.Stage, t.Checking, t.WithDesigner, t.Answered, t.WaitsFor = stageOf(v, *t), "", false, false, nil
	t.PMDeciding = pmDeciding(v, *t)
	t.Blocks = index[t.ID]
	t.Ref = ""
	if p := project(v, t.ProjectID); p != nil {
		t.Ref = p.TaskRef(t.Number)
	}
	// A task under way can be made to wait too: then it can't land yet.
	if !t.Finished() {
		t.WaitsFor = waitsFor(v, *t)
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
	if t.Status == TaskWaiting {
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
	case TaskResearching:
		return StageResearching
	case TaskDesigning:
		return StageDesigning
	case TaskWriting:
		return StageImplementing
	case TaskReviewing, TaskDeciding:
		return checkStage(v, t)
	case TaskWaiting:
		return waitingStage(v, t)
	case TaskLanding, TaskAwaiting:
		return StageReady
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
	researchers := t.RolesOf(RoleResearcher)
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

// Checkers judge each revision in this order: every reviewer, then QA.
func (t Task) Checkers() []Role {
	return append(t.RolesOf(RoleReviewer), t.RolesOf(RoleQA)...)
}

// Judged is whether a role has judged a revision against this version of the
// brief and of the task's objective and criteria; a change to either asks
// every checker again.
func (t Task) Judged(role string, revision, briefVersion int) bool {
	return slices.ContainsFunc(t.Verdicts, func(v Verdict) bool {
		return v.Role == role && v.Revision == revision && t.Counts(v, briefVersion)
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
	for _, r := range t.Checkers() {
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
	case DecisionDelivery, DecisionUpdate:
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
