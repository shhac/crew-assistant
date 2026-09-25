package core

// Stages place a task on its project's board. They are derived from the
// loop's own state whenever the state is read or written, never set directly,
// so a board can never disagree with what the loop is doing.
const (
	StageTodo         = "todo"
	StageImplementing = "implementing"
	StageReviewing    = "reviewing"
	StageQA           = "qa"
	StageReady        = "ready"
	StageDone         = "done"
	StageStopped      = "stopped"
)

func deriveStages(v *Snapshot) {
	for i := range v.Tasks {
		derive(v, &v.Tasks[i])
	}
}

func derive(v *Snapshot, t *Task) {
	t.Stage, t.Checking, t.Answered = stageOf(v, *t), "", false
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
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
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
// brief; a brief change asks every checker again.
func (t Task) Judged(role string, revision, briefVersion int) bool {
	for _, v := range t.Verdicts {
		if v.Role == role && v.Revision == revision && v.BriefVersion == briefVersion {
			return true
		}
	}
	return false
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
	case next.Kind == RoleQA:
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
// whoever asked it; a failure with the step that failed.
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
		if n := len(t.Revisions); n > 0 {
			for _, verdict := range t.Verdicts {
				if verdict.Revision != t.Revisions[n-1].N || verdict.Outcome != VerdictQuestion {
					continue
				}
				if r, ok := t.Role(verdict.Role); ok && r.Kind == RoleQA {
					return StageQA
				}
			}
		}
		return StageReviewing
	}
	return lastCheck(t)
}
