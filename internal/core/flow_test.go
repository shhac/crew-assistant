package core

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// shape is a flow reduced to what a team's shape decides: each stage's id
// and kind, with its checks' seats and capabilities, and where it may be
// asked from.
func shape(f Flow) []string {
	var out []string
	for _, s := range f.Stages {
		line := s.ID + ":" + s.Kind
		for _, c := range s.Checks {
			line += " " + c.Role + "[" + strings.Join(c.Seats, ",") + "]=" + c.Capability
			if !c.Gates {
				line += "(advises)"
			}
		}
		if len(s.AskedFrom) > 0 {
			line += " <-" + strings.Join(s.AskedFrom, ",")
		}
		out = append(out, line)
	}
	return out
}

func seat(name string, kinds ...string) Role { return Role{Name: name, Kinds: kinds} }

func TestEachTeamShapeHasTheFlowTheLoopWorksItThrough(t *testing.T) {
	code, draft := Templates["code"].Roles, Templates["draft"].Roles
	without := func(roles []Role, kind string) []Role {
		return slices.DeleteFunc(slices.Clone(roles), func(r Role) bool { return r.Holds(kind) })
	}
	designer := seat("Dee", RoleDesigner)
	ada := []Role{seat("Ada", RoleResearcher, RoleImplementer), seat("Rev", RoleReviewer)}
	pair := []Role{seat("Imp", RoleImplementer), {Name: "Claudius", Kinds: []string{RoleReviewer}, Member: "m1"}, {Name: "Claudius #2", Kinds: []string{RoleReviewer}, Member: "m1"}, seat("Rune", RoleReviewer)}
	for name, tc := range map[string]struct {
		medium string
		roles  []Role
		want   []string
	}{
		"code": {MediumGit, code, []string{
			"research:advise <-checks", "implement:make",
			"checks:checks reviewer[Reviewer]=check qa[QA]=check_by_running",
		}},
		"code without a researcher": {MediumGit, without(code, RoleResearcher), []string{
			"implement:make", "checks:checks reviewer[Reviewer]=check qa[QA]=check_by_running",
		}},
		"code without QA": {MediumGit, without(code, RoleQA), []string{
			"research:advise <-checks", "implement:make", "checks:checks reviewer[Reviewer]=check",
		}},
		"code with a designer": {MediumGit, append(slices.Clone(code), designer), []string{
			"research:advise <-checks", "design:on_call <-research,implement", "implement:make",
			"checks:checks reviewer[Reviewer]=check qa[QA]=check_by_running",
		}},
		"code with a designer and no researcher": {MediumGit, append(without(code, RoleResearcher), designer), []string{
			"design:on_call <-implement", "implement:make",
			"checks:checks reviewer[Reviewer]=check qa[QA]=check_by_running",
		}},
		"draft": {MediumDocuments, draft, []string{
			"write:make", "checks:checks reviewer[Reviewer]=check",
		}},
		"draft with QA": {MediumDocuments, append(slices.Clone(draft), seat("QA", RoleQA)), []string{
			"write:make", "checks:checks reviewer[Reviewer]=check qa[QA]=check_by_running",
		}},
		// Kinds decide, not medium: the loop researches for any team with a
		// researcher.
		"draft with a researcher": {MediumDocuments, append([]Role{seat("Scout", RoleResearcher)}, draft...), []string{
			"research:advise <-checks", "write:make", "checks:checks reviewer[Reviewer]=check",
		}},
		"one seat researches and implements": {MediumGit, ada, []string{
			"research:advise <-checks", "implement:make", "checks:checks reviewer[Rev]=check",
		}},
		// A member's seats judge as one check; another seat is a check of its own.
		"one member in two seats": {MediumGit, pair, []string{
			"implement:make", "checks:checks reviewer[Claudius,Claudius #2]=check reviewer[Rune]=check",
		}},
		"no checkers": {MediumGit, []Role{seat("Imp", RoleImplementer)}, []string{
			"implement:make", "checks:checks",
		}},
	} {
		if got := shape(DefaultFlow(tc.medium, tc.roles)); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: flow\n%q\nwant\n%q", name, got, tc.want)
		}
	}
}

// Each check is its checker group, so a verdict counts for a check exactly
// when it counts for the loop.
func TestAChecksIDIsItsCheckerGroup(t *testing.T) {
	roles := []Role{seat("Imp", RoleImplementer), {Name: "Claudius", Kinds: []string{RoleReviewer}, Member: "m1"}, seat("QA", RoleQA)}
	task := Task{Roles: roles}
	f := DefaultFlow(MediumGit, roles)
	checks, _ := f.Stage(FlowStageChecks)
	var ids, groups []string
	for _, c := range checks.Checks {
		ids = append(ids, c.ID)
	}
	for _, g := range task.CheckerGroups() {
		groups = append(groups, g.Key)
	}
	if !slices.Equal(ids, groups) {
		t.Fatalf("check ids %q, checker groups %q", ids, groups)
	}
}

func TestTheFlowPlacesATaskFromItsRecord(t *testing.T) {
	team := []Role{seat("Scout", RoleResearcher), seat("Dee", RoleDesigner), seat("Imp", RoleImplementer), seat("Rev", RoleReviewer), seat("QA", RoleQA)}
	drafted := []Revision{{N: 1}, {N: 2}}
	reviewed := []Verdict{{Revision: 2, Role: "Rev", Outcome: VerdictPass, BriefVersion: 1}}
	both := append(slices.Clone(reviewed), Verdict{Revision: 2, Role: "QA", Outcome: VerdictPass, BriefVersion: 1})
	decisions := []Decision{{ID: "failure", Kind: DecisionFailure}, {ID: "question", Kind: DecisionQuestion}, {ID: "delivery", Kind: DecisionDelivery}}
	v := &Snapshot{Projects: []Project{{ID: "p", Brief: Brief{Version: 1}}}, Decisions: decisions}
	reviewer, qa := "reviewer/seat/rev", "qa/seat/qa"
	for name, tc := range map[string]struct {
		task Task
		want FlowPlace
		due  []string
	}{
		"queued":   {Task{Status: TaskQueued}, FlowPlace{Stage: FlowQueued, Column: StageTodo}, nil},
		"research": {Task{Status: TaskResearching}, FlowPlace{Stage: FlowStageResearch, Column: StageResearching}, nil},
		"research a check asked for": {Task{Status: TaskResearching, Revisions: drafted, Research: []ResearchRequest{{From: "Rev", Revision: 2}}},
			FlowPlace{Stage: FlowStageResearch, Returns: FlowStageChecks, Column: StageResearching}, []string{reviewer, qa}},
		"design asked from research": {Task{Status: TaskDesigning, Design: []DesignRequest{{Step: TaskResearching}}},
			FlowPlace{Stage: FlowStageDesign, Returns: FlowStageResearch, Column: StageDesigning}, nil},
		"design asked from implementing": {Task{Status: TaskDesigning, Design: []DesignRequest{{Step: TaskWriting}}},
			FlowPlace{Stage: FlowStageDesign, Returns: FlowStageImplement, Column: StageDesigning}, nil},
		"implementing": {Task{Status: TaskWriting}, FlowPlace{Stage: FlowStageImplement, Column: StageImplementing}, nil},
		"checks, both due": {Task{Status: TaskReviewing, Revisions: drafted},
			FlowPlace{Stage: FlowStageChecks, Column: StageReviewing}, []string{reviewer, qa}},
		"checks, QA due": {Task{Status: TaskReviewing, Revisions: drafted, Verdicts: reviewed},
			FlowPlace{Stage: FlowStageChecks, Column: StageQA}, []string{qa}},
		"checks in": {Task{Status: TaskDeciding, Revisions: drafted, Verdicts: both},
			FlowPlace{Stage: FlowDeciding, Within: FlowStageChecks, Column: StageQA}, nil},
		"a verdict on an earlier revision": {Task{Status: TaskReviewing, Revisions: drafted, Verdicts: []Verdict{{Revision: 1, Role: "Rev", BriefVersion: 1}}},
			FlowPlace{Stage: FlowStageChecks, Column: StageReviewing}, []string{reviewer, qa}},
		"a verdict on an older brief": {Task{Status: TaskReviewing, Revisions: drafted, Verdicts: []Verdict{{Revision: 2, Role: "Rev"}}},
			FlowPlace{Stage: FlowStageChecks, Column: StageReviewing}, []string{reviewer, qa}},
		"a verdict on older task text": {Task{Status: TaskReviewing, TextVersion: 1, Revisions: drafted, Verdicts: reviewed},
			FlowPlace{Stage: FlowStageChecks, Column: StageReviewing}, []string{reviewer, qa}},
		"a question answered": {Task{Status: TaskReviewing, Revisions: drafted, Verdicts: []Verdict{{Revision: 2, Role: "Rev", BriefVersion: 1, Answered: true}}},
			FlowPlace{Stage: FlowStageChecks, Column: StageReviewing}, []string{reviewer, qa}},
		"an accepted draft merged": {Task{Status: TaskReviewing, Approved: 1, Acceptance: &DraftAcceptance{Revision: 1, BriefVersion: 1},
			Revisions: []Revision{{N: 1}, {N: 2, CleanMergeOf: 1}}},
			FlowPlace{Stage: FlowStageChecks, Column: StageQA}, []string{qa}},
		"QA asked the owner": {Task{Status: TaskWaiting, DecisionID: "question", Revisions: drafted, Verdicts: reviewed, Asker: &Asker{Decision: "question", From: "QA", Step: TaskReviewing, Revision: 2}},
			FlowPlace{Stage: FlowWaiting, Within: FlowStageChecks, Column: StageQA}, []string{qa}},
		"the researcher asked the owner": {Task{Status: TaskWaiting, DecisionID: "question", Asker: &Asker{Decision: "question", From: "Scout", Step: TaskResearching}},
			FlowPlace{Stage: FlowWaiting, Within: FlowStageResearch, Column: StageResearching}, nil},
		"failed mid-check": {Task{Status: TaskWaiting, DecisionID: "failure", ResumeStatus: TaskReviewing, Revisions: drafted, Verdicts: reviewed},
			FlowPlace{Stage: FlowWaiting, Within: FlowStageChecks, Column: StageQA}, []string{qa}},
		"waiting to deliver": {Task{Status: TaskWaiting, DecisionID: "delivery", Revisions: drafted, Verdicts: both},
			FlowPlace{Stage: FlowWaiting}, nil},
		"landing": {Task{Status: TaskLanding, Revisions: drafted, Verdicts: both}, FlowPlace{Stage: FlowLanding}, nil},
		"landed":  {Task{Status: TaskLanded, Revisions: drafted}, FlowPlace{Stage: FlowFinished, Column: StageDone}, nil},
	} {
		tc.task.ProjectID, tc.task.Roles = "p", team
		at := TaskFlow(v, tc.task).Place(v, tc.task)
		due := dueKeys(at.Due)
		at.Due = nil
		if !reflect.DeepEqual(at, tc.want) || !slices.Equal(due, append([]string{}, tc.due...)) {
			t.Errorf("%s: placed %+v due %q, want %+v due %q", name, at, due, tc.want, tc.due)
		}
		if ds := FlowDisagreements(&Snapshot{Projects: v.Projects, Decisions: decisions, Tasks: []Task{tc.task}}); len(ds) > 0 {
			t.Errorf("%s: the flow disagrees with the loop: %+v", name, ds)
		}
	}
}

// The shadow says when a record has no place in its flow, rather than
// guess one.
func TestTheShadowReportsARecordItsFlowHasNoPlaceFor(t *testing.T) {
	team := []Role{seat("Imp", RoleImplementer), seat("Rev", RoleReviewer)}
	v := &Snapshot{Tasks: []Task{
		{ID: "a", Status: TaskDesigning, Roles: team, Design: []DesignRequest{{Step: TaskWriting}}},
		{ID: "b", Status: TaskResearching, Roles: team},
	}}
	var got []string
	for _, d := range FlowDisagreements(v) {
		got = append(got, d.TaskID+" "+d.Kind+" "+d.Engine)
	}
	want := []string{"a no_stage designing", "a asked_from writing", "b no_stage researching"}
	if !slices.Equal(got, want) {
		t.Fatalf("disagreements %q, want %q", got, want)
	}
}
