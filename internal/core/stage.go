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
		v.Tasks[i].Stage = stageOf(v, v.Tasks[i])
	}
}

func stageOf(v *Snapshot, t Task) string {
	switch t.Status {
	case TaskQueued:
		return StageTodo
	case TaskWriting:
		return StageImplementing
	case TaskReviewing, TaskDeciding:
		return checkStage(t)
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

// checkStage is reviewing until every reviewer has judged the latest
// revision, then QA if the team has it.
func checkStage(t Task) string {
	n := len(t.Revisions)
	if n == 0 {
		return StageImplementing
	}
	latest := t.Revisions[n-1].N
	for _, r := range t.Roles {
		if r.Kind == RoleReviewer && !hasVerdict(t, r.Name, latest) {
			return StageReviewing
		}
	}
	return lastCheck(t)
}

func lastCheck(t Task) string {
	for _, r := range t.Roles {
		if r.Kind == RoleQA {
			return StageQA
		}
	}
	return StageReviewing
}

func hasVerdict(t Task, role string, revision int) bool {
	for _, v := range t.Verdicts {
		if v.Role == role && v.Revision == revision {
			return true
		}
	}
	return false
}

// waitingStage keeps a task that needs the owner in the column it stopped
// in: approval is the last step before landing; a question stays with
// whoever asked it; a failure with the step that failed.
func waitingStage(v *Snapshot, t Task) string {
	var kind string
	for _, d := range v.Decisions {
		if d.ID == t.DecisionID {
			kind = d.Kind
		}
	}
	switch kind {
	case "delivery":
		return StageReady
	case "failure":
		if t.ResumeStatus == "" || t.ResumeStatus == TaskWaiting {
			return StageImplementing
		}
		resumed := t
		resumed.Status = t.ResumeStatus
		return stageOf(v, resumed)
	case "question":
		if n := len(t.Revisions); n > 0 {
			for _, verdict := range t.Verdicts {
				if verdict.Revision == t.Revisions[n-1].N && verdict.Outcome == VerdictQuestion && roleKind(t, verdict.Role) == RoleQA {
					return StageQA
				}
			}
		}
		return StageReviewing
	}
	return lastCheck(t)
}

func roleKind(t Task, name string) string {
	for _, r := range t.Roles {
		if r.Name == name {
			return r.Kind
		}
	}
	return ""
}
