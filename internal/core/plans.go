package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/text"
)

// Prerequisite records an external condition and the owner's outcome.
type Prerequisite struct {
	What    string `json:"what"`
	Blocker string `json:"blocker"`
	Outcome string `json:"outcome,omitempty"`
}

// Plan is what a researcher worked out about a task before anything was
// written: what already exists, what will change, what the record must say
// when a step stops part-way, how it will be tested, what stays out, and
// what is unclear. What the task waits for is kept on the task itself.
// Prerequisites record external conditions and outcomes copied from blockers.
type Plan struct {
	OwnerChecks   []string `json:"owner_checks,omitempty"`
	NeedsDesigner string   `json:"needs_designer,omitempty"`
	Summary       string   `json:"summary"`
	Exists        []string `json:"exists,omitempty"`
	Changes       []string `json:"changes,omitempty"`
	FailurePaths  []string `json:"failure_paths,omitempty"`
	Tests         []string `json:"tests,omitempty"`
	OutOfScope    []string `json:"out_of_scope,omitempty"`
	Questions     []string `json:"questions,omitempty"`
	// SplitOff is the parts of the task the plan leaves for later, each
	// queued as a task of its own that waits for this one.
	SplitOff []SplitPart `json:"split_off,omitempty"`
	Role     string      `json:"role"`
	// Answered marks a plan whose questions the owner answered: the
	// researcher plans again with the answer rather than going on with it.
	Answered      bool           `json:"answered,omitempty"`
	At            time.Time      `json:"at"`
	Prerequisites []Prerequisite `json:"prerequisites,omitempty"`
}

// SplitPart is one part of a task its plan leaves for later: its title and
// requirements, and the task it was queued as.
type SplitPart struct {
	Objective string   `json:"objective"`
	Criteria  []string `json:"criteria,omitempty"`
	Task      string   `json:"task,omitempty"`
}

// MaxSplitOff is how many parts one plan may split off; the rest are left
// out, so a runaway plan can't flood the to-do list.
const MaxSplitOff = 5

// unfinished reports a task that has not landed, been delivered or stopped.
func unfinished(v *Snapshot, id string) *Task {
	t := task(v, id)
	if t == nil || t.Finished() || project(v, t.ProjectID) == nil {
		return nil
	}
	return t
}

// waitsFor is what a task still waits for: the objectives of the tasks it
// depends on that have not finished. One that no longer exists, or that was
// stopped, no longer holds it back.
func waitsFor(v *Snapshot, t Task) []string {
	var out []string
	for _, id := range t.DependsOn {
		if dep := unfinished(v, id); dep != nil {
			name := dep.Objective
			if dep.ProjectID != t.ProjectID {
				if p := project(v, dep.ProjectID); p != nil {
					name = fmt.Sprintf("“%s” (%s in %s)", dep.Objective, p.TaskRef(dep.Number), p.Title)
				}
			}
			out = append(out, name)
		}
	}
	return out
}

// dependencies checks what a task says it depends on: in its own project
// unless anyProject is set, not itself, and never a loop, leaving both waiting
// forever. Ids of tasks that have already finished are kept but hold nothing
// back; a task may finish between being read and being named.
func dependencies(v *Snapshot, t Task, ids []string, anyProject bool) ([]string, error) {
	var out []string
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		// A task named by its readable ID is kept by its canonical one.
		dep := task(v, id)
		switch {
		case id == t.ID || (dep != nil && dep.ID == t.ID):
			return nil, errors.New("a task cannot wait for itself")
		// Editing same-project links keeps cross-project dependencies that
		// research already recorded, without permitting new ones here.
		case dep == nil || (dep.ProjectID != t.ProjectID && !anyProject && !slices.Contains(t.DependsOn, dep.ID)):
			return nil, fmt.Errorf("there is no task %q in this project", id)
		case slices.Contains(out, dep.ID):
			continue
		case reaches(v, dep.ID, t.ID, map[string]bool{}):
			return nil, fmt.Errorf("“%s” already waits for this task, so this task cannot wait for it", dep.Objective)
		}
		out = append(out, dep.ID)
	}
	return out, nil
}

// possibleDependencies is what a role says a task waits for, less any task
// dependencies would refuse: a role naming one impossible task keeps the
// rest of its list, so the task still waits for what it can.
func possibleDependencies(v *Snapshot, t Task, ids []string, anyProject bool) []string {
	var out []string
	for _, id := range ids {
		if deps, err := dependencies(v, t, append(slices.Clone(out), id), anyProject); err == nil {
			out = deps
		}
	}
	return out
}

// PlanDependencies returns validated unfinished dependencies and previously
// recorded dependencies, including resolved ones, using RecordPlan's checks.
func PlanDependencies(v Snapshot, t Task, ids []string) []string {
	var out []string
	for _, id := range possibleDependencies(&v, t, append(slices.Clone(t.DependsOn), ids...), true) {
		if unfinished(&v, id) != nil || slices.Contains(t.DependsOn, id) {
			out = append(out, id)
		}
	}
	return out
}

// reaches reports whether from depends on to, directly or through others.
func reaches(v *Snapshot, from, to string, seen map[string]bool) bool {
	if from == to {
		return true
	}
	if seen[from] {
		return false
	}
	seen[from] = true
	t := task(v, from)
	if t == nil {
		return false
	}
	for _, next := range t.DependsOn {
		if reaches(v, next, to, seen) {
			return true
		}
	}
	return false
}

// RecordPlan keeps a researcher's plan on its task and moves the task on.
// What it waits for comes first: a task with unlanded work to wait for goes
// back to the queue, keeping neither this plan nor its branch point, so it is
// researched again on top of the landed work. Then the researcher's
// questions, which the loop brings to the owner. Then a checker's request
// for more research, which goes back to that checker. Otherwise the
// implementer starts. A task whose work has begun is never sent back to the
// queue, nor made to wait for more: its drafts and branch stay. A plan kept
// on the task queues the parts it splits off, in the same change.
// ends records optional reply metadata with the plan under the same fence.
func (s *Service) RecordPlan(ctx context.Context, taskID string, plan Plan, dependsOn, prerequisites []string, ends ...TurnEnd) (Task, error) {
	var out Task
	err := s.store.update(ctx, func(v *Snapshot) error {
		t := task(v, taskID)
		if t == nil {
			return ErrNotFound
		}
		// A task stopped while the researcher worked stays stopped.
		if t.Status != TaskResearching {
			return fmt.Errorf("the task is no longer being researched: %w", ErrConflict)
		}
		begun := len(t.Revisions) > 0
		if begun {
			dependsOn = nil
		}
		recordedDeps := possibleDependencies(v, *t, t.DependsOn, true)
		deps := possibleDependencies(v, *t, append(slices.Clone(t.DependsOn), dependsOn...), true)
		now := s.now().UTC()
		t.DependsOn, t.UpdatedAt = deps, now
		markAll(t, deps, researcherLinker(*t), now)
		plan.At = now
		if seat, ok := t.Role(plan.Role); ok {
			for _, end := range ends {
				recordTurnEndLogged(v, t, RoleResearcher, "", seat, end, now)
			}
		}
		if !begun {
			s.planPrerequisites(v, t, prerequisites, now)
		}
		plan.Prerequisites = taskPrerequisites(*t)
		waiting := append(waitsFor(v, *t), BlockerReasons(*t)...)
		// Validate against the dependencies that survived this write, not the
		// researcher's raw ids or a snapshot taken before it ran.
		// Previously recorded dependencies are resolved declarations; newly
		// proposed finished ids still do not establish a scheduling condition.
		current := *t
		current.DependsOn = recordedDeps
		current.WaitsFor = waitsFor(v, *t)
		if !begun && UndeclaredPlanWait(plan, nil, current) && !slices.Contains(plan.Questions, UndeclaredWaitQuestion) {
			plan.Questions = append(plan.Questions, UndeclaredWaitQuestion)
		}
		request := t.OpenResearch()
		switch {
		case heldBack(v, *t) && !begun:
			t.Status, t.Plan, t.Detail = TaskQueued, nil, ""
			t.Base, t.From, t.Branch = "", "", ""
			recordTask(v, now, t, "task.queued", fmt.Sprintf("%s waits for %s", t.Objective, strings.Join(waiting, ", ")))
		case len(plan.Questions) > 0:
			t.Plan = &plan
		case request != nil:
			t.Plan = &plan
			request.Researcher, request.AnsweredAt = plan.Role, now
			t.reopen(request.From, request.Revision)
			t.Status, t.Detail = TaskReviewing, "Back from "+plan.Role+" with more research"
			recordTask(v, now, t, "task.researched", fmt.Sprintf("%s researched %s again for %s", plan.Role, t.Objective, request.From))
		default:
			t.Plan = &plan
			t.Status, t.Detail = TaskWriting, ""
			recordTask(v, now, t, "task.planned", fmt.Sprintf("Planned %s", t.Objective))
		}
		if p := project(v, t.ProjectID); p != nil {
			p.listChanged()
		}
		if t.Plan != nil {
			after := t.text()
			moved := []string{}
			for _, c := range cleanList(t.Plan.OwnerChecks) {
				if i := slices.Index(after.Criteria, c); i >= 0 {
					after.Criteria = slices.Delete(after.Criteria, i, i+1)
					if !slices.Contains(after.OwnerChecks, c) {
						after.OwnerChecks = append(after.OwnerChecks, c)
					}
					moved = append(moved, c)
				}
			}
			t.Plan.OwnerChecks = moved
			if !t.text().same(after) {
				if err := s.applyEdit(v, t, TaskEdit{By: plan.Role, Kind: RoleResearcher, After: after}, &out); err != nil {
					return err
				}
			}
			t = splitOff(v, t, now)
		}
		derive(v, t)
		out = *t
		return nil
	})
	return out, err
}

// splitOff queues each part t's plan leaves for later as a task of its own
// in t's project, waiting for t, unless an earlier plan for t queued it
// already: a part matches a task split off t with the same title, as it is
// now or as it was first asked for, however it is cased or spaced. Each
// task that waits for t and has not begun waits for each part too. The
// plan and t's activity name the task each part is.
// It returns t as it now is, since queueing may move the snapshot's tasks.
func splitOff(v *Snapshot, t *Task, now time.Time) *Task {
	p := project(v, t.ProjectID)
	if p == nil || len(t.Plan.SplitOff) == 0 {
		return t
	}
	id, projectID, objective, by := t.ID, t.ProjectID, t.Objective, researcherLinker(*t)
	var parts []SplitPart
	for _, part := range t.Plan.SplitOff {
		if len(parts) == MaxSplitOff {
			break
		}
		part.Objective = text.Clip(strings.TrimSpace(part.Objective), maxObjective)
		if part.Objective == "" {
			continue
		}
		criteria := []string{}
		for _, c := range cleanList(part.Criteria) {
			criteria = append(criteria, text.Clip(c, maxCriterion))
		}
		part.Criteria = criteria
		if done := splitTask(v, id, part.Objective); done != nil {
			part.Task = done.ID
		} else {
			n := Task{ID: uid(), ProjectID: projectID, Objective: part.Objective, Criteria: criteria, Status: TaskQueued, Stage: StageTodo, DependsOn: []string{id}, SplitFrom: id, Revisions: []Revision{}, Verdicts: []Verdict{}, CreatedAt: now, UpdatedAt: now}
			mark(&n, RelationDependsOn, id, by, now)
			numberTask(p, &n)
			v.Tasks = append(v.Tasks, n)
			recordTask(v, now, &n, "task.queued", fmt.Sprintf("%s split off %s", n.Objective, objective))
			part.Task = n.ID
		}
		// The same part named twice is one task.
		if !slices.ContainsFunc(parts, func(kept SplitPart) bool { return kept.Task == part.Task }) {
			parts = append(parts, part)
		}
	}
	// Every part the plan names, new or queued by an earlier plan, holds
	// back what waits for t, since a task may have come to wait for t since.
	// A part that has finished holds nothing back. The parts themselves wait
	// for t, and never for each other.
	var holding []*Task
	for _, part := range parts {
		if n := task(v, part.Task); n != nil && !n.Finished() {
			holding = append(holding, n)
		}
	}
	for i := range v.Tasks {
		dep := &v.Tasks[i]
		if dep.ProjectID != projectID || dep.SplitFrom == id || !slices.Contains(dep.DependsOn, id) || mayWait(*dep, by) != nil {
			continue
		}
		for _, n := range holding {
			if slices.Contains(dep.DependsOn, n.ID) {
				continue
			}
			deps, err := dependencies(v, *dep, append(slices.Clone(dep.DependsOn), n.ID), false)
			if err != nil {
				continue
			}
			dep.DependsOn, dep.UpdatedAt = deps, now
			mark(dep, RelationDependsOn, n.ID, by, now)
			recordTask(v, now, dep, "task.linked", fmt.Sprintf("%s waits for %s", dep.Objective, n.Objective))
		}
	}
	t = task(v, id)
	t.Plan.SplitOff = parts
	// Every part the plan names, whether queued now or by an earlier plan,
	// under the title its task has now.
	if len(parts) > 0 {
		var names []string
		for _, part := range parts {
			if n := task(v, part.Task); n != nil {
				names = append(names, n.Objective)
			}
		}
		recordTask(v, now, t, "task.split", fmt.Sprintf("Split %s off %s", strings.Join(names, "; "), objective))
	}
	return t
}

// splitTask is the task split off from before with the title objective, as
// it is now or as it was first asked for, whether or not it has finished.
func splitTask(v *Snapshot, from, objective string) *Task {
	want := sameTitle(objective)
	for i := range v.Tasks {
		t := &v.Tasks[i]
		if t.SplitFrom != from {
			continue
		}
		if sameTitle(t.Objective) == want || (len(t.Edits) > 0 && sameTitle(t.Edits[0].Before.Objective) == want) {
			return t
		}
	}
	return nil
}

// sameTitle is a title as two are compared: ignoring case and spacing, so
// "Export CSV", "export  csv" and "ExportCSV" are one title.
func sameTitle(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), "")
}

// researcherLinker is how the links a task's researcher adds are marked.
func researcherLinker(t Task) string {
	researcher, _ := t.Researcher()
	return TeamLinker(researcher.Member, RoleResearcher)
}
