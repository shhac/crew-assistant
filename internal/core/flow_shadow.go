package core

import (
	"slices"
	"strings"
)

// The flow runs in the shadow of the loop: each task's record is read
// against its flow and compared with what the loop itself recorded, so any
// difference shows up before a transition moves over to flows.

// Kinds of FlowDisagreement.
const (
	// FlowNoStage is a status the task's flow has no stage for.
	FlowNoStage = "no_stage"
	// FlowAskedFrom is a task asked into a stage from one its flow does not
	// allow it to be asked from.
	FlowAskedFrom = "asked_from"
	// FlowColumn is a board column other than the flow's.
	FlowColumn = "column"
	// FlowDue is a set of checks still due other than the loop's.
	FlowDue = "due"
)

// FlowDisagreement is one way a task's flow reads its record differently
// from the loop. Flow and Engine are each side's answer, made only of
// statuses, stage and column ids, and checker group keys.
type FlowDisagreement struct {
	TaskID    string
	ProjectID string
	// Revision is the task's latest revision, 0 before it has one.
	Revision int
	Kind     string
	Flow     string
	Engine   string
}

// FlowDisagreements compares every unfinished task's place in its flow with
// the loop's own record of it. It changes nothing in v.
func FlowDisagreements(v *Snapshot) []FlowDisagreement {
	var out []FlowDisagreement
	for _, t := range v.Tasks {
		if !t.Finished() {
			out = append(out, compareFlow(v, t)...)
		}
	}
	return out
}

func compareFlow(v *Snapshot, t Task) []FlowDisagreement {
	f := TaskFlow(v, t)
	at := f.Place(v, t)
	var out []FlowDisagreement
	differ := func(kind, flow, engine string) {
		d := FlowDisagreement{TaskID: t.ID, ProjectID: t.ProjectID, Kind: kind, Flow: flow, Engine: engine}
		if n := len(t.Revisions); n > 0 {
			d.Revision = t.Revisions[n-1].N
		}
		out = append(out, d)
	}
	if at.Stage == "" {
		differ(FlowNoStage, "", t.Status)
	}
	if asked := askedFrom(t); asked != "" {
		stage, _ := f.Stage(at.Stage)
		if at.Returns == "" || !slices.Contains(stage.AskedFrom, at.Returns) {
			differ(FlowAskedFrom, at.Stage+" from "+at.Returns, asked)
		}
	}
	// Engine's column is the one the status puts the task in, before a full
	// next column keeps it where it was: that hold is the board's capacity,
	// not where the flow has the task.
	if engine := stageOf(v, t); at.Column != "" && at.Column != engine {
		differ(FlowColumn, at.Column, engine)
	}
	if len(t.Revisions) > 0 {
		flow, engine := dueKeys(at.Due), outstandingCheckers(v, t)
		if !slices.Equal(flow, engine) {
			differ(FlowDue, strings.Join(flow, ","), strings.Join(engine, ","))
		}
	}
	return out
}

// askedFrom is the step the loop recorded a task researching or designing
// out of order was sent from, or "".
func askedFrom(t Task) string {
	switch t.Status {
	case TaskResearching:
		if r := t.OpenResearch(); r != nil {
			return TaskReviewing
		}
	case TaskDesigning:
		if r := t.OpenDesign(); r != nil {
			return r.Step
		}
	}
	return ""
}

func dueKeys(due []FlowCheck) []string {
	out := []string{}
	for _, c := range due {
		out = append(out, c.ID)
	}
	return out
}

// outstandingCheckers are the checker groups the loop still waits on for
// t's latest revision, as its board and its review step count them.
func outstandingCheckers(v *Snapshot, t Task) []string {
	out := []string{}
	brief := briefVersion(v, t)
	latest := t.Revisions[len(t.Revisions)-1].N
	for _, r := range t.AcceptanceCheckers(brief) {
		if !t.Judged(r.Name, latest, brief) {
			out = append(out, t.CheckerGroup(r.Name))
		}
	}
	return out
}
