package core

import "slices"

// Column capacities make a bottleneck push back up the pipeline. A task
// enters its next column when there is room, independently of whether a
// teammate can start its step. Until then it stays in the column it finished.
// Every task holding a column counts, including owner and outside waits.
// A full column pulls nothing from the preceding one; To do may also be
// limited, but triage never is. A move back never waits, and lowering a
// capacity never evicts tasks. Schedule moves working tasks forward; owner
// and outside waits retain their place until they are under way again.

// placeStage is the working stage t has reached, towards stage limits, or ""
// for a task on the to-do list, in triage or finished. It is the board's
// stage, but for a draft being checked. While a reviewer has yet to pass the
// latest draft the task is reviewing, with QA's check of it running beside
// the review however full QA is: limits count tasks, not checks. Once every
// check is in, a draft that passed them all is on its way to Ready, and one
// that did not stays where it is until it goes back.
func placeStage(v *Snapshot, t Task) string {
	if t.Finished() || t.Status == TaskQueued || t.Status == TaskTriage {
		return ""
	}
	if (t.Status == TaskReviewing || t.Status == TaskDeciding) && len(t.Revisions) > 0 {
		_, pending := t.nextChecker(briefVersion(v, t))
		switch {
		case !pending && !turnedDown(v, t, RoleReviewer, RoleQA):
			return passedStage(t)
		case !pending && t.Place != "":
			return t.Place
		case turnedDown(v, t, RoleReviewer) && !later(t.Place, StageReviewing):
			return StageReviewing
		}
	}
	// Opening a pull request needs room among the open ones.
	if t.Status == TaskLanding && t.UsesPRs() && !t.PROpen() {
		return StagePROpen
	}
	if stage := stageOf(v, t); slices.Contains(limitStages, stage) {
		return stage
	}
	return ""
}

// turnedDown reports whether a checker holding one of kinds gave the latest
// draft anything but a pass.
func turnedDown(v *Snapshot, t Task, kinds ...string) bool {
	latest := t.Revisions[len(t.Revisions)-1].N
	brief := briefVersion(v, t)
	for _, verdict := range t.Verdicts {
		if verdict.Revision != latest || !t.Counts(verdict, brief) || verdict.Outcome == VerdictPass {
			continue
		}
		if r, ok := t.Role(verdict.Role); ok && slices.ContainsFunc(kinds, r.Holds) {
			return true
		}
	}
	return false
}

// later reports whether stage a comes after stage b on the board; any
// working stage comes after none.
func later(a, b string) bool {
	return slices.Index(limitStages, a) > slices.Index(limitStages, b)
}

// held is how many tasks hold each working stage, per project.
type held map[string]map[string]int

// holdings brings each task's place up to date where that needs no room:
// a task's first look, a move back, or leaving the working stages. It
// counts tasks per project for the moves that need room. Owner and outside
// waits retain Place and count towards capacity.
func holdings(v *Snapshot) held {
	counts := held{}
	for _, p := range v.Projects {
		counts[p.ID] = map[string]int{}
	}
	for i := range v.Tasks {
		t := &v.Tasks[i]
		stage := placeStage(v, *t)
		// A pull request GitHub says is ready moved on outside the team, so
		// its task moves with it.
		if stage == "" || t.Place == "" || !later(stage, t.Place) || (t.Status == TaskAwaiting && t.UsesPRs()) {
			t.Place = stage
		}
		if counts[t.ProjectID] != nil {
			if t.Status == TaskQueued {
				counts[t.ProjectID][StageTodo]++
			} else if t.Place != "" {
				counts[t.ProjectID][t.Place]++
			}
		}
	}
	return counts
}

// stageLimit is the limit on a project's stage, as its team has it now.
func stageLimit(v *Snapshot, projectID, stage string) int {
	if p := project(v, projectID); p != nil && p.Playbook != nil {
		return p.Playbook.StageLimit(stage)
	}
	return 0
}

// room says what a task waits for to enter stage from the stage it holds,
// or nil when the stage has room.
func (h held) room(v *Snapshot, projectID, from, stage string) *Wait {
	limit := stageLimit(v, projectID, stage)
	if count := h[projectID][stage]; limit > 0 && count >= limit {
		wait := &Wait{Kind: WaitStage, Stage: stage, From: from, Count: count, Limit: limit}
		if count == 1 {
			for _, t := range v.Tasks {
				if t.ProjectID == projectID && (t.Place == stage || (stage == StageTodo && t.Status == TaskQueued)) {
					holder := onTask(v, t)
					wait.On, wait.Objective = holder.On, holder.Objective
					break
				}
			}
		}
		return wait
	}
	return nil
}

// move counts a task out of the stage it holds and into stage.
func (h held) move(t *Task, stage string) {
	if counts := h[t.ProjectID]; counts != nil {
		if t.Place != "" {
			counts[t.Place]--
		}
		counts[stage]++
	}
	t.Place = stage
}

// entry is the later stage t has reached, which it may enter when that
// stage has room, or what it waits for when the stage is full; "" and nil
// when it has no stage to enter.
func (h held) entry(v *Snapshot, t *Task) (string, *Wait) {
	stage := placeStage(v, *t)
	if stage == "" || !later(stage, t.Place) {
		return "", nil
	}
	if wait := h.room(v, t.ProjectID, t.Place, stage); wait != nil {
		return "", wait
	}
	return stage, nil
}
