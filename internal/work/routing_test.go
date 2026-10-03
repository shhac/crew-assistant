package work

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/roles"
	"github.com/shhac/lib-agent-harness/session"
)

// The researcher's questions come back to the researcher with the owner's
// answers, and it plans again before anything is written.
func TestTheResearcherPlansAgainWithTheOwnersAnswer(t *testing.T) {
	t.Parallel()
	a, runner, p := plannedCode(t, 6, `{"summary": "Unclear which colour.", "questions": ["Which colour?"]}`)
	ctx := context.Background()
	task, _ := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Paint it"})
	task = taskNow(t, a, task.ID)
	d := openDecision(t, a, task)
	if task.Asker == nil || task.Asker.Step != core.TaskResearching || task.Asker.From != "Researcher" {
		t.Fatalf("who asked: %+v", task.Asker)
	}
	a.Core.AnswerDecision(ctx, d.ID, "Blue", core.FromOwner)
	task = taskNow(t, a, task.ID)
	research := turns(&runner.scriptedRunner, "Plan this task before anything is written")
	if len(research) != 2 || !strings.Contains(research[1].Prompt, "Blue") || !strings.Contains(research[1].Prompt, "Unclear which colour.") || !strings.Contains(research[1].Prompt, "Plan again with their answers") {
		t.Fatalf("the researcher should plan again with the answer: %d turns", len(research))
	}
	if task.Plan == nil || task.Plan.Summary != "Do the task as asked." || task.Plan.Answered || task.Round != 1 || len(task.Revisions) == 0 {
		t.Fatalf("the new plan, then the work: %+v round %d, %d drafts", task.Plan, task.Round, len(task.Revisions))
	}
}

// A checker's question comes back to that checker with the owner's answer,
// and it decides what happens next: here, that the draft passes as it is.
func TestTheCheckerThatAskedJudgesAgainWithTheAnswer(t *testing.T) {
	t.Parallel()
	runner := &scriptedRunner{reviews: []string{ask, pass}}
	a, _, _ := loopApp(t, runner, "")
	ctx := context.Background()
	task := settle(t, a)
	d := openDecision(t, a, task)
	if task.Stage != core.StageReviewing || task.Asker == nil || task.Asker.From != "Reviewer" || task.Asker.Revision != 1 {
		t.Fatalf("waiting on the owner for the Reviewer: %s %+v", task.Stage, task.Asker)
	}
	a.Core.AnswerDecision(ctx, d.ID, "The whole team", core.FromOwner)
	task = settle(t, a)
	d = openDecision(t, a, task)
	if d.Kind != core.DecisionDelivery || len(task.Revisions) != 1 || task.Round != 1 || runner.writes != 1 {
		t.Fatalf("the Reviewer should pass draft 1 with the answer: %s, %d drafts, round %d", d.Kind, len(task.Revisions), task.Round)
	}
	checks := turns(runner, "Judge the draft strictly")
	if len(checks) != 2 || !strings.Contains(checks[1].Prompt, "The whole team") || !task.Verdicts[0].Answered || task.Verdicts[1].Outcome != core.VerdictPass {
		t.Fatalf("the second check should read the answer: %d checks %+v", len(checks), task.Verdicts)
	}
}

// A reviewer can send the task back to the researcher; the researcher
// updates the plan and the task comes back to that reviewer, on the same
// draft and branch.
func TestAReviewerSendsTheTaskBackForResearchAndGetsItBack(t *testing.T) {
	t.Parallel()
	research := `{"outcome":"research","summary":"The approach is unchecked.","findings":[],"question":"Does the v2 API cover this?"}`
	a, runner, p := plannedCode(t, 0, plainPlan, `{"summary": "v2 covers it; use it.", "changes": ["use v2"]}`)
	runner.reviews = []string{research, pass, pass}
	ctx := context.Background()
	task, _ := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Add A"})
	task = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Status == core.TaskResearching && len(t.Revisions) > 0 })
	branch, base := task.Branch, task.Base
	if task.Stage != core.StageResearching || len(task.Research) != 1 || task.Research[0].From != "Reviewer" || task.Research[0].Question != "Does the v2 API cover this?" {
		t.Fatalf("with the researcher: %s %+v", task.Stage, task.Research)
	}
	task = taskNow(t, a, task.ID)
	d := openDecision(t, a, task)
	if d.Kind != core.DecisionDelivery || len(task.Revisions) != 1 || task.Branch != branch || task.Base != base || task.Plan.Summary != "v2 covers it; use it." || runner.edits != 1 {
		t.Fatalf("back to the reviewer, then on: %s, %d drafts, %d edits, branch %q, plan %+v", d.Kind, len(task.Revisions), runner.edits, task.Branch, task.Plan)
	}
	plans := turns(&runner.scriptedRunner, "Plan this task before anything is written")
	if len(plans) != 2 || !strings.Contains(plans[1].Prompt, "Does the v2 API cover this?") || !strings.Contains(plans[1].Prompt, "Reviewer, checking draft 1") {
		t.Fatalf("the researcher should get the question: %d turns", len(plans))
	}
	reviews := turns(&runner.scriptedRunner, "Review it as a careful senior engineer")
	if len(reviews) != 2 || !strings.Contains(reviews[1].Prompt, "v2 covers it; use it.") {
		t.Fatalf("the reviewer should judge draft 1 again with the new plan: %d reviews", len(reviews))
	}
}

// Where a checker recommends another step than its outcome leads to, the
// PM chooses; without a PM, or when the PM can't say, the checks decide.
func TestThePMChoosesWhereATaskGoesWhenACheckerRecommendsOtherwise(t *testing.T) {
	t.Parallel()
	warmer := `{"outcome":"pass","summary":"Fine.","findings":[],"question":"","next":"revise","note":"I want the closing warmer"}`
	for _, c := range []struct {
		name   string
		pm     bool
		route  []string
		drafts int
	}{
		{"the PM sends it back", true, []string{`{"next": "revise", "reason": "the closing matters to the owner"}`}, 2},
		{"the PM can't say", true, nil, 1},
		{"no PM", false, nil, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			runner := &scriptedRunner{reviews: []string{warmer, pass, pass, pass}, route: c.route}
			var a *Loop
			var task core.Task
			if c.pm {
				a, _, task, _ = pmTeam(t, runner)
			} else {
				a, _, task = loopApp(t, runner, "")
			}
			task = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Status == core.TaskWaiting })
			if len(task.Revisions) != c.drafts {
				t.Fatalf("%d drafts, want %d", len(task.Revisions), c.drafts)
			}
			asked := turns(runner, "Decide where this task goes next")
			if c.pm != (len(asked) > 0) {
				t.Fatalf("the PM was asked %d times", len(asked))
			}
			if c.pm && (!strings.Contains(asked[0].Prompt, "I want the closing warmer") || asked[0].Write) {
				t.Fatalf("the PM's read-only turn should carry the recommendation: %s", asked[0].Prompt)
			}
			if c.pm && strings.Join(specTools(asked[0]), " ") != pmToolNames {
				t.Fatalf("the PM choosing a route should have its task tools: %v", specTools(asked[0]))
			}
			if c.drafts == 2 {
				writers := writerTurns(runner)
				if !strings.Contains(writers[1].Prompt, "I want the closing warmer") {
					t.Fatal("the writer never read the recommendation")
				}
				snap, _ := a.Core.Snapshot(context.Background())
				if !slices.ContainsFunc(snap.Activity, func(e core.Activity) bool {
					return e.Kind == "task.routed" && strings.Contains(e.Summary, "the closing matters to the owner")
				}) {
					t.Fatal("the PM's choice was not recorded")
				}
			}
		})
	}
}

// Roles edit their task's title and requirements as their role allows,
// and leave notes every other role reads.
func TestRolesEditTheirTaskAndLeaveNotes(t *testing.T) {
	t.Parallel()
	a, p, task := loopApp(t, &scriptedRunner{}, "")
	ctx := context.Background()
	reviewer := a.toolsFor(task, core.RoleReviewer, core.Role{Name: "Rune"})
	if got := callTool(t, reviewer, "edit_task", map[string]string{"title": "Another title", "add_requirement": ""}); !got.IsError {
		t.Fatal("a reviewer reworded the title")
	}
	if got := callTool(t, reviewer, "edit_task", map[string]string{"add_requirement": "Names every team member"}); got.IsError {
		t.Fatalf("add: %s", got.Content)
	}
	researcher := a.toolsFor(task, core.RoleResearcher, core.Role{Name: "Rhea"})
	if got := callTool(t, researcher, "edit_task", map[string]string{"title": "Thank-you note for the launch", "requirements": "- Names every team member\n- Under 200 words", "add_requirement": ""}); got.IsError {
		t.Fatalf("rewrite: %s", got.Content)
	}
	if got := callTool(t, reviewer, "add_note", map[string]string{"text": "The launch was on the 3rd."}); got.IsError {
		t.Fatalf("note: %s", got.Content)
	}
	snap, _ := a.Core.Snapshot(ctx)
	edited, _ := findTask(snap, p.ID, task.ID)
	if edited.Objective != "Thank-you note for the launch" || !slices.Equal(edited.Criteria, []string{"Names every team member", "Under 200 words"}) || len(edited.Edits) != 2 || edited.Edits[1].By != "Rhea" {
		t.Fatalf("edited %q %v %+v", edited.Objective, edited.Criteria, edited.Edits)
	}
	read := callTool(t, researcher, "read_task", map[string]string{"task_id": task.ID})
	if !strings.Contains(read.Content, "Rune: The launch was on the 3rd.") || !strings.Contains(read.Content, "changed 2 times, last by Rhea") {
		t.Fatalf("read_task: %s", read.Content)
	}
	if prompt := writerPrompt(p, edited, "", false); !strings.Contains(prompt, "Under 200 words") || !strings.Contains(prompt, "1. Rune: The launch was on the 3rd.") {
		t.Fatalf("the writer should read the edited requirements and the note: %s", prompt)
	}
	stale := reviewer
	stale.status = core.TaskReviewing
	if got := callTool(t, stale, "add_note", map[string]string{"text": "late"}); !got.IsError {
		t.Fatal("a turn whose task moved on left a note")
	}
}

// editingRunner changes the task's requirements while the first check
// runs, as a checker using edit_task would.
type editingRunner struct {
	*scriptedRunner
	onCheck func()
}

func (r *editingRunner) Run(ctx context.Context, spec roles.Spec) (roles.Result, error) {
	if !spec.Write && strings.Contains(spec.Prompt, "Judge the draft strictly") && r.onCheck != nil {
		r.onCheck()
		r.onCheck = nil
	}
	return r.scriptedRunner.Run(ctx, spec)
}

// A verdict counts only against the requirements it judged: a requirement
// added while the draft was checked, even by the checker itself, has the
// draft checked again before it goes on.
func TestARequirementAddedDuringChecksIsCheckedBeforeTheDraftGoesOn(t *testing.T) {
	t.Parallel()
	scripted := &scriptedRunner{reviews: []string{pass, pass}}
	a, p, task := loopApp(t, scripted, "")
	a.runner = &editingRunner{scriptedRunner: scripted, onCheck: func() {
		if _, err := a.Core.EditTask(context.Background(), core.EditInput{Project: p.ID, Task: task.ID, By: "Reviewer", Kind: core.RoleReviewer, Add: []string{"Names every team member"}}); err != nil {
			t.Error(err)
		}
	}}
	task = settle(t, a)
	d := openDecision(t, a, task)
	checks := turns(scripted, "Judge the draft strictly")
	if d.Kind != core.DecisionDelivery || len(checks) != 2 || strings.Contains(checks[0].Prompt, "Names every team member") || !strings.Contains(checks[1].Prompt, "Names every team member") {
		t.Fatalf("the draft should be checked again with the new requirement: %s after %d checks", d.Kind, len(checks))
	}
	if task.TextVersion != 1 || task.Verdicts[0].TextVersion != 0 || task.Verdicts[1].TextVersion != 1 || len(task.Revisions) != 1 {
		t.Fatalf("text version %d, verdicts %+v", task.TextVersion, task.Verdicts)
	}
}

// routeEditingRunner has the PM edit the task it is routing, through its
// own tool, before it chooses.
type routeEditingRunner struct {
	*scriptedRunner
	edit func(spec roles.Spec)
}

func (r *routeEditingRunner) Run(ctx context.Context, spec roles.Spec) (roles.Result, error) {
	if strings.Contains(spec.Prompt, "Decide where this task goes next") && r.edit != nil {
		r.edit(spec)
		r.edit = nil
	}
	return r.scriptedRunner.Run(ctx, spec)
}

// A PM that changes the task's requirements while it chooses where the task
// goes can't send it on unchecked: the draft is checked again first.
func TestAPMThatEditsTheTaskItRoutesHasItCheckedAgain(t *testing.T) {
	t.Parallel()
	warmer := `{"outcome":"pass","summary":"Fine.","findings":[],"question":"","next":"revise","note":"I want the closing warmer"}`
	scripted := &scriptedRunner{reviews: []string{warmer, pass}, route: []string{`{"next": "land", "reason": "it reads well"}`}}
	a, _, task, _ := pmTeam(t, scripted)
	a.runner = &routeEditingRunner{scriptedRunner: scripted, edit: func(spec roles.Spec) {
		result, err := spec.Handler.CallTool(context.Background(), session.ToolCall{Name: "edit_task", Arguments: []byte(`{"task_id": "` + task.ID + `", "title": "", "requirements": "Names every team member"}`)})
		if err != nil || result.IsError {
			t.Errorf("the PM's edit: %+v %v", result, err)
		}
	}}
	task = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Status == core.TaskWaiting })
	checks := turns(scripted, "Judge the draft strictly")
	if len(checks) != 2 || !strings.Contains(checks[1].Prompt, "Names every team member") || len(task.Revisions) != 1 {
		t.Fatalf("the draft should be checked again against the PM's edit before approval: %d checks, %d drafts", len(checks), len(task.Revisions))
	}
	if d := openDecision(t, a, task); d.Kind != core.DecisionDelivery || task.Edits[0].Kind != core.RolePM || task.Verdicts[1].TextVersion != task.TextVersion {
		t.Fatalf("decision %s, edits %+v, verdicts %+v", d.Kind, task.Edits, task.Verdicts)
	}
}

// Every note stays readable to the team: the latest are in view, and
// read_notes pages through the rest.
func TestRolesReadEveryNoteAPageAtATime(t *testing.T) {
	t.Parallel()
	a, p, task := loopApp(t, &scriptedRunner{}, "")
	ctx := context.Background()
	for i := 1; i <= 25; i++ {
		if _, err := a.Core.AddNote(ctx, core.NoteInput{Project: p.ID, Task: task.ID, By: "Rune", Kind: core.RoleReviewer, Text: "Note " + strconv.Itoa(i)}); err != nil {
			t.Fatal(err)
		}
	}
	reviewer := a.toolsFor(task, core.RoleReviewer, core.Role{Name: "Rune"})
	read := callTool(t, reviewer, "read_task", map[string]string{"task_id": task.ID})
	if !strings.Contains(read.Content, "(13 earlier notes; read_notes from 1 reads them)") || !strings.Contains(read.Content, "14. Rune: Note 14") || strings.Contains(read.Content, "Rune: Note 13\n") {
		t.Fatalf("read_task: %s", read.Content)
	}
	first := callTool(t, reviewer, "read_notes", map[string]string{"task_id": task.Ref, "from": "1"})
	if first.IsError || !strings.HasPrefix(first.Content, "1. Rune: Note 1\n") || !strings.Contains(first.Content, "20. Rune: Note 20\n") || !strings.Contains(first.Content, "(5 later notes; read_notes from 21 reads them)") {
		t.Fatalf("from 1: %s", first.Content)
	}
	latest := callTool(t, reviewer, "read_notes", map[string]string{"task_id": task.ID, "from": ""})
	if !strings.HasPrefix(latest.Content, "(5 earlier notes; read_notes from 1 reads them)\n6. Rune: Note 6\n") || !strings.HasSuffix(latest.Content, "25. Rune: Note 25\n") {
		t.Fatalf("latest: %s", latest.Content)
	}
	if got := callTool(t, reviewer, "read_notes", map[string]string{"task_id": task.ID, "from": "zero"}); !got.IsError {
		t.Fatal("read notes from a number that isn't one")
	}
	if prompt := writerPrompt(p, taskNow(t, a, task.ID), "", false); !strings.Contains(prompt, "(13 earlier notes; read_notes from 1 reads them)") {
		t.Fatalf("the prompt should say how to read earlier notes: %s", prompt)
	}
}

// pmToolNames are the tools of every PM turn that decides something.
const pmToolNames = "list_tasks read_task read_notes set_blocker clear_blocker edit_task link_tasks unlink_tasks queue_task add_note"

// specTools names the tools a turn was given.
func specTools(spec roles.Spec) []string {
	var out []string
	for _, d := range spec.Tools {
		out = append(out, d.Name)
	}
	return out
}

// The researcher and the PM can remove every requirement, the last one
// included, while an empty value leaves them as they are.
func TestTheResearcherAndPMCanRemoveEveryRequirement(t *testing.T) {
	t.Parallel()
	a, p, task := loopApp(t, &scriptedRunner{}, "")
	ctx := context.Background()
	second, _ := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Translate it", Criteria: []string{"The owner's only requirement"}})
	pm := a.managerTools(p.ID, core.Role{Name: "Pim"})
	if got := callTool(t, pm, "edit_task", map[string]string{"task_id": second.ID, "title": "", "requirements": ""}); !got.IsError {
		t.Fatal("an edit that says nothing changed something")
	}
	if got := callTool(t, pm, "edit_task", map[string]string{"task_id": second.ID, "title": "", "requirements": " None "}); got.IsError {
		t.Fatalf("the PM clears the requirements: %s", got.Content)
	}
	a.Core.EditTask(ctx, core.EditInput{Project: p.ID, Task: task.ID, By: "Rev", Kind: core.RoleReviewer, Add: []string{"Warm"}})
	researcher := a.toolsFor(task, core.RoleResearcher, core.Role{Name: "Rhea"})
	if got := callTool(t, researcher, "edit_task", map[string]string{"title": "", "requirements": "none", "add_requirement": ""}); got.IsError {
		t.Fatalf("the researcher clears the requirements: %s", got.Content)
	}
	snap, _ := a.Core.Snapshot(ctx)
	for _, id := range []string{second.ID, task.ID} {
		cleared, _ := findTask(snap, p.ID, id)
		last := cleared.Edits[len(cleared.Edits)-1]
		if len(cleared.Criteria) != 0 || len(last.Before.Criteria) != 1 || len(last.After.Criteria) != 0 {
			t.Fatalf("%s: criteria %v, edit %+v", cleared.Objective, cleared.Criteria, last)
		}
	}
}

// QA decides what a task waits for, as the researcher does.
func TestQALinksWhatATaskWaitsFor(t *testing.T) {
	t.Parallel()
	a, p, first := loopApp(t, &scriptedRunner{}, "")
	ctx := context.Background()
	second, _ := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Translate it"})
	qa := a.toolsFor(first, core.RoleQA, core.Role{Name: "QA"})
	if !strings.Contains(qa.guide(), "depends on another, blocks another") {
		t.Fatalf("guide: %s", qa.guide())
	}
	if got := callTool(t, qa, "link_tasks", map[string]string{"relation": "blocks", "other_task_id": second.ID}); got.IsError {
		t.Fatalf("QA blocks: %s", got.Content)
	}
	snap, _ := a.Core.Snapshot(ctx)
	if waits, _ := findTask(snap, p.ID, second.ID); !slices.Equal(waits.DependsOn, []string{first.ID}) {
		t.Fatalf("depends on %v", waits.DependsOn)
	}
}

// The PM tidies any task, links any two and queues splits or siblings, a
// few each look; the owner's links stay theirs.
func TestThePMTidiesLinksAndQueuesTasks(t *testing.T) {
	t.Parallel()
	a, p, first := loopApp(t, &scriptedRunner{}, "")
	ctx := context.Background()
	second, _ := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Translate it"})
	pm := a.managerTools(p.ID, core.Role{Name: "Pim"})
	if names := toolNames(pm); strings.Join(names, " ") != pmToolNames {
		t.Fatalf("tools %v", names)
	}
	if got := callTool(t, pm, "edit_task", map[string]string{"task_id": second.Ref, "title": "Translate the note into French", "requirements": ""}); got.IsError {
		t.Fatalf("edit: %s", got.Content)
	}
	if got := callTool(t, pm, "link_tasks", map[string]string{"task_id": second.ID, "relation": "depends_on", "other_task_id": first.ID}); got.IsError {
		t.Fatalf("link: %s", got.Content)
	}
	for i := range maxPMQueued {
		if got := callTool(t, pm, "queue_task", map[string]string{"title": "Split part " + string(rune('A'+i)), "requirements": "Short", "depends_on": second.Ref}); got.IsError {
			t.Fatalf("queue: %s", got.Content)
		}
	}
	if got := callTool(t, pm, "queue_task", map[string]string{"title": "One too many", "requirements": "", "depends_on": ""}); !got.IsError {
		t.Fatal("the PM queued past its limit")
	}
	if got := callTool(t, pm, "add_note", map[string]string{"task_id": first.ID, "text": "Split the translation off."}); got.IsError {
		t.Fatalf("note: %s", got.Content)
	}
	snap, _ := a.Core.Snapshot(ctx)
	tidied, _ := findTask(snap, p.ID, second.ID)
	if tidied.Objective != "Translate the note into French" || tidied.Edits[0].Kind != core.RolePM || !slices.Equal(tidied.DependsOn, []string{first.ID}) || tidied.LinkedBy["depends_on:"+first.ID].By != core.LinkedByPM {
		t.Fatalf("tidied %+v", tidied)
	}
	var split []core.Task
	for _, t := range snap.Tasks {
		if strings.HasPrefix(t.Objective, "Split part") {
			split = append(split, t)
		}
	}
	if len(split) != maxPMQueued || !slices.Equal(split[0].DependsOn, []string{second.ID}) || split[0].LinkedBy["depends_on:"+second.ID].By != core.LinkedByPM {
		t.Fatalf("split %+v", split)
	}
	a.Core.LinkTasks(ctx, core.Link{Project: p.ID, Task: split[1].ID, Other: first.ID, Relation: core.RelationRelatesTo, By: core.LinkedByOwner})
	if got := callTool(t, pm, "unlink_tasks", map[string]string{"task_id": split[1].ID, "other_task_id": first.ID}); !got.IsError {
		t.Fatal("the PM took away the owner's link")
	}
}

// Answering the assistant's question changes nothing, so the PM may leave
// a note then, but not edit, link or queue.
func TestThePMAnsweringAQuestionOnlyLeavesNotes(t *testing.T) {
	t.Parallel()
	a, p, task := loopApp(t, &scriptedRunner{}, "")
	answering := a.answerTools(p.ID, core.Role{Name: "Pim"})
	if got := callTool(t, answering, "add_note", map[string]string{"task_id": task.ID, "text": "Asked about by the assistant."}); got.IsError {
		t.Fatalf("note: %s", got.Content)
	}
	for name, args := range map[string]map[string]string{
		"edit_task":  {"task_id": task.ID, "title": "Changed", "requirements": ""},
		"queue_task": {"title": "New", "requirements": "", "depends_on": ""},
	} {
		if got := callTool(t, answering, name, args); !got.IsError {
			t.Fatalf("%s worked while answering a question", name)
		}
	}
	snap, _ := a.Core.Snapshot(context.Background())
	if found, _ := findTask(snap, p.ID, task.ID); found.Objective != task.Objective || len(found.Notes) != 1 || found.Notes[0].By != "Pim" || len(snap.Tasks) != 1 {
		t.Fatalf("task %+v", found)
	}
}
