package work

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
)

// roundLimited is a draft team with a round limit of one, and a PM if pm
// is set: the first draft's checks decide whether the owner is asked.
func roundLimited(t *testing.T, runner *scriptedRunner, pm bool) (*Loop, core.Project, core.Task) {
	t.Helper()
	return draftTeam(t, runner, pm, "1")
}

// draftTeam is a draft team with this round limit, and a PM if pm is set.
func draftTeam(t *testing.T, runner *scriptedRunner, pm bool, rounds string) (*Loop, core.Project, core.Task) {
	t.Helper()
	ctx := context.Background()
	a, p, task := loopApp(t, runner, "")
	choice := TeamChoice{Template: "draft", MaxRounds: rounds}
	if pm {
		pim, err := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Pim", Kinds: []string{core.RolePM}, Engine: "claude"})
		if err != nil {
			t.Fatal(err)
		}
		choice.PM = pim.ID
	}
	p, err := a.SetTeam(ctx, p.ID, choice)
	if err != nil {
		t.Fatal(err)
	}
	return a, p, task
}

func waiting(task core.Task) bool { return task.Status == core.TaskWaiting }

// waitingAgain is a task waiting on a decision other than d.
func waitingAgain(d core.Decision) func(core.Task) bool {
	return func(task core.Task) bool { return waiting(task) && task.DecisionID != d.ID }
}

// judgement is the PM's reading of the one finding a revise verdict makes.
func judgement(narrow, regression, repeat, core, wrongWay bool) string {
	return fmt.Sprintf(`{"findings": [{"finding": 1, "narrow": %v, "regression": %v, "repeat": %v, "core": %v}], "wrong_way": %v, "waiting_needs_fix": false, "reason": "Pim's reason", "follow_up": {"objective": "Soften the opening of the thank-you note", "criteria": ["The note opens warmly rather than formally"]}}`, narrow, regression, repeat, core, wrongWay)
}

// waitingNeedsFix is a judgement where the task waiting on this one needs
// what remains fixed first.
func waitingNeedsFix(reply string) string {
	return strings.Replace(reply, `"waiting_needs_fix": false`, `"waiting_needs_fix": true`, 1)
}

// At the round limit the PM judges what remains, and the owner gets the
// one choice a fixed rule makes of that judgement, with the PM's reason.
func TestThePMJudgesWhatRemainsAtTheRoundLimit(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, reply, want, why string
	}{
		{"narrow and new", judgement(true, false, false, false, false), choiceAcceptFollowUp, "the checks pass and what remains is narrow and new; accepting it lets the task waiting on it go on"},
		{"a waiting task needs it fixed", waitingNeedsFix(judgement(true, false, false, false, false)), choiceAnotherRound, "“Send the note round” waits on this task and needs what remains fixed first"},
		{"a regression", judgement(true, true, false, false, false), choiceAnotherRound, "“Soften the opening” is a regression"},
		{"a repeat", judgement(true, false, true, false, false), choiceAnotherRound, "“Soften the opening” was raised in an earlier round too"},
		{"the core purpose", judgement(true, false, false, true, false), choiceAnotherRound, "“Soften the opening” leaves the task's core purpose unmet"},
		{"heading the wrong way", judgement(true, false, false, false, true), choiceStop, "the draft is heading the wrong way"},
	} {
		t.Run(c.name, func(t *testing.T) {
			runner := &scriptedRunner{reviews: []string{revise}, escalate: []string{c.reply}}
			a, p, task := roundLimited(t, runner, true)
			ctx := context.Background()
			other, err := a.Core.QueueTaskAs(ctx, p.ID, core.TaskInput{Objective: "Send the note round", DependsOn: []string{task.ID}}, core.LinkedByPM)
			if err != nil {
				t.Fatal(err)
			}
			task = stepUntil(t, a, task.ID, waiting)
			d := openDecision(t, a, task)
			if d.Kind != core.DecisionEscalation || !slices.Equal(d.Choices, []string{choiceAnotherRound, choiceAcceptFollowUp, choiceAcceptDraft, choiceStop}) {
				t.Fatalf("escalation %+v", d)
			}
			if want := c.want + ": " + c.why + ". Pim's reason"; d.Recommendation != want {
				t.Fatalf("recommendation %q, want %q", d.Recommendation, want)
			}
			if !strings.Contains(d.Context, "Soften the opening") || !strings.Contains(d.Context, "Send the note round") {
				t.Fatalf("the context should carry what remains and what waits on it: %s", d.Context)
			}
			asked := turns(runner, "Judge what remains at the round limit")
			if len(asked) != 1 || asked[0].Write {
				t.Fatalf("the PM should judge once, reading only: %d", len(asked))
			}
			prompt := asked[0].Prompt
			for _, says := range []string{"Every check QA ran passed", "1. Reviewer [Warm tone]: Soften the opening", "What earlier rounds raised", "Send the note round", `"waiting_needs_fix"`} {
				if !strings.Contains(prompt, says) {
					t.Fatalf("the PM's prompt should say %q: %s", says, prompt)
				}
			}
			// The PM judges; it queues nothing, and the follow-up is the
			// owner's to choose.
			if tools := strings.Join(specTools(asked[0]), " "); strings.Contains(tools, "queue") {
				t.Fatalf("the PM judging an escalation should not queue tasks: %s", tools)
			}
			if snap, _ := a.Core.Snapshot(ctx); len(snap.Tasks) != 2 || other.ID == "" {
				t.Fatalf("nothing should be queued before the owner chooses: %d tasks", len(snap.Tasks))
			}
		})
	}
}

// Each thing that calls for another round does, even with every finding
// narrow; a failing check among them.
func TestTheRecommendationRule(t *testing.T) {
	t.Parallel()
	yes, no := true, false
	findings := []finding{{Role: "Reviewer", Note: "Rename the flag"}, {Role: "QA", Note: "Add a test"}}
	narrow := findingJudgement{Narrow: true}
	judged := func(wrongWay *bool, second findingJudgement) escalationJudgement {
		first := narrow
		first.Finding, second.Finding = 1, 2
		return escalationJudgement{Findings: []findingJudgement{first, second}, WrongWay: wrongWay, Reason: "because"}
	}
	needsFix := judged(&no, narrow)
	needsFix.WaitingNeedsFix = true
	one := []core.Task{{Objective: "Ship the flag"}}
	two := append(slices.Clone(one), core.Task{Objective: "Document the flag"})
	for _, c := range []struct {
		name    string
		j       escalationJudgement
		passed  bool
		waiting []core.Task
		want    string
		why     string
	}{
		{"narrow, new, checks pass", judged(&no, narrow), true, nil, choiceAcceptFollowUp, "narrow and new"},
		{"a failing check", judged(&no, narrow), false, nil, choiceAnotherRound, "a check is failing"},
		{"a regression", judged(&no, findingJudgement{Narrow: true, Regression: true}), true, nil, choiceAnotherRound, "“Add a test” is a regression"},
		{"a repeat", judged(&no, findingJudgement{Narrow: true, Repeat: true}), true, nil, choiceAnotherRound, "“Add a test” was raised in an earlier round"},
		{"the core purpose", judged(&no, findingJudgement{Narrow: true, Core: true}), true, nil, choiceAnotherRound, "“Add a test” leaves the task's core purpose unmet"},
		{"not narrow", judged(&no, findingJudgement{}), true, nil, choiceAnotherRound, "“Add a test” is not narrow"},
		{"the wrong way, even with a failing check", judged(&yes, narrow), false, nil, choiceStop, "heading the wrong way"},
		{"a waiting task needs it fixed", needsFix, true, one, choiceAnotherRound, "“Ship the flag” waits on this task and needs what remains fixed first"},
		{"a waiting task can go on", judged(&no, narrow), true, one, choiceAcceptFollowUp, "accepting it lets the task waiting on it go on"},
		{"several waiting tasks can go on", judged(&no, narrow), true, two, choiceAcceptFollowUp, "accepting it lets the 2 tasks waiting on it go on"},
		{"nothing waits, whatever the PM says", needsFix, true, nil, choiceAcceptFollowUp, "narrow and new"},
	} {
		t.Run(c.name, func(t *testing.T) {
			choice, why := recommend(c.j, findings, c.passed, c.waiting)
			if choice != c.want || !strings.Contains(why, c.why) {
				t.Fatalf("got %s: %s; want %s: %s", choice, why, c.want, c.why)
			}
		})
	}
	// QA's check failing is what "the checks passed" reads; a reviewer
	// asking for changes is not a failing check.
	task := core.Task{Roles: []core.Role{{Name: "Reviewer", Kinds: []string{core.RoleReviewer}}, {Name: "QA", Kinds: []string{core.RoleQA}}}}
	reviewer := core.Verdict{Role: "Reviewer", Outcome: core.VerdictRevise}
	if !checksPassed(task, []core.Verdict{reviewer, {Role: "QA", Outcome: core.VerdictPass}}) {
		t.Fatal("QA passed, so the checks passed")
	}
	if checksPassed(task, []core.Verdict{reviewer, {Role: "QA", Outcome: core.VerdictRevise}}) {
		t.Fatal("QA's check failed")
	}
}

// Without a PM, or with one that can't say, the owner can still accept and
// follow up, with a follow-up made from the findings as they are and no
// recommendation made up.
func TestWithoutThePMsJudgementTheFollowUpIsMadeFromTheFindings(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		pm   bool
	}{{"no PM", false}, {"a PM that can't say", true}} {
		t.Run(c.name, func(t *testing.T) {
			runner := &scriptedRunner{reviews: []string{revise}}
			a, _, task := roundLimited(t, runner, c.pm)
			task = stepUntil(t, a, task.ID, waiting)
			d := openDecision(t, a, task)
			if !slices.Contains(d.Choices, choiceAcceptFollowUp) || strings.HasPrefix(d.Recommendation, choiceAcceptFollowUp) || strings.HasPrefix(d.Recommendation, choiceAnotherRound+":") {
				t.Fatalf("escalation %+v", d)
			}
			want := core.TaskInput{Objective: fmt.Sprintf("Follow-up to %s: Thank-you note", task.Ref), Criteria: []string{"Warm tone: Soften the opening"}}
			if d.FollowUp == nil || d.FollowUp.Objective != want.Objective || !slices.Equal(d.FollowUp.Criteria, want.Criteria) {
				t.Fatalf("follow-up %+v", d.FollowUp)
			}
			if !strings.Contains(d.Context, want.Objective) || !strings.Contains(d.Context, "- Warm tone: Soften the opening") {
				t.Fatalf("the owner should read the follow-up: %s", d.Context)
			}
			if asked := len(turns(runner, "Judge what remains at the round limit")); asked > 0 != c.pm {
				t.Fatalf("the PM was asked %d times", asked)
			}
		})
	}
}

// Choosing Accept and follow up accepts the draft and queues exactly the
// follow-up the owner read, waiting for the accepted task and next in line
// after it, as the owner's own task.
func TestAcceptAndFollowUpQueuesTheFollowUpRightAfterTheTask(t *testing.T) {
	t.Parallel()
	runner := &scriptedRunner{reviews: []string{revise}, escalate: []string{judgement(true, false, false, false, false)}}
	a, p, task := roundLimited(t, runner, true)
	ctx := context.Background()
	later, err := a.Core.QueueTaskAs(ctx, p.ID, core.TaskInput{Objective: "Send the note round", DependsOn: []string{task.ID}}, core.LinkedByPM)
	if err != nil {
		t.Fatal(err)
	}
	task = stepUntil(t, a, task.ID, waiting)
	d := openDecision(t, a, task)
	if _, err = a.Core.ChooseDecision(ctx, d.ID, choiceAcceptFollowUp, core.FromOwner); err != nil {
		t.Fatal(err)
	}
	task = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Status != core.TaskWaiting })
	if task.Approved != len(task.Revisions) || (task.Status != core.TaskLanding && task.Status != core.TaskDelivered) {
		t.Fatalf("the draft should be accepted: %s, approved %d", task.Status, task.Approved)
	}
	snap, _ := a.Core.Snapshot(ctx)
	var ids []string
	for _, other := range snap.Tasks {
		if other.ProjectID == p.ID {
			ids = append(ids, other.ID)
		}
	}
	if len(ids) != 3 || ids[0] != task.ID || ids[2] != later.ID {
		t.Fatalf("the follow-up should sit right after the accepted task, before the rest: %v", ids)
	}
	follow, _ := findTask(snap, p.ID, ids[1])
	if follow.Objective != "Soften the opening of the thank-you note" || !slices.Equal(follow.Criteria, []string{"The note opens warmly rather than formally"}) {
		t.Fatalf("follow-up %q %v", follow.Objective, follow.Criteria)
	}
	if follow.Status != core.TaskQueued || !slices.Equal(follow.DependsOn, []string{task.ID}) || !follow.HeldByOwner(core.RelationDependsOn, task.ID) {
		t.Fatalf("the follow-up should be queued, not in triage, waiting for the task by the owner's link: %s %v %v", follow.Status, follow.DependsOn, follow.LinkedBy)
	}
	// The team can't take the owner's link away; the owner can stop it like
	// any other task.
	_, err = a.Core.UnlinkTasks(ctx, core.Link{Project: p.ID, Task: follow.ID, Other: task.ID, By: core.LinkedByPM})
	if !errors.Is(err, core.ErrConflict) {
		t.Fatalf("the PM unlinked the owner's follow-up: %v", err)
	}
	if stopped, err := a.StopTask(ctx, p.ID, follow.ID); err != nil || stopped.Status != core.TaskStopped {
		t.Fatalf("stopping the follow-up: %v", err)
	}
}

// ownerStepApp is a draft team whose task has a requirement the implementer
// can't meet from its sandbox, and says so in its first reply.
func ownerStepApp(t *testing.T, runner *scriptedRunner, pm bool) (*Loop, core.Task) {
	t.Helper()
	ctx := context.Background()
	a, p, first := draftTeam(t, runner, pm, "3")
	if _, err := a.StopTask(ctx, p.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	task, err := a.Core.QueueTaskAs(ctx, p.ID, core.TaskInput{Objective: "Thank-you page", Criteria: []string{"Warm tone", "It opens in the owner's browser"}}, core.LinkedByPM)
	if err != nil {
		t.Fatal(err)
	}
	runner.writerReplies = []string{"Wrote the page.\n```owner-step\n[{\"requirement\": \"it opens in the owner's browser\", \"why\": \"There is no browser in my sandbox.\"}]\n```"}
	return a, task
}

// A requirement the implementer says it can't meet from its sandbox comes
// to the owner as a proposed owner step, whether the checks send the draft
// back or pass it: never another round, and never landed without it. Taken
// on, it leaves the team's requirements and the delivery lists it to check.
func TestARequirementTheSandboxCantMeetBecomesAnOwnerStep(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		first string
	}{{"the checks send it back", revise}, {"the checks pass it", pass}} {
		t.Run(c.name, func(t *testing.T) { sandboxRequirementBecomesAnOwnerStep(t, c.first) })
	}
}

// sandboxRequirementBecomesAnOwnerStep runs the owner step through to the
// delivery, with first the check of the draft that raises it.
func sandboxRequirementBecomesAnOwnerStep(t *testing.T, first string) {
	runner := &scriptedRunner{
		reviews:   []string{first, pass},
		ownerStep: []string{`{"owner_step": true, "step": "Open the page in your browser and check it shows", "reason": "only the owner has a browser"}`},
	}
	a, task := ownerStepApp(t, runner, true)
	ctx := context.Background()
	task = stepUntil(t, a, task.ID, waiting)
	if len(task.Revisions) != 1 || len(task.Unreachable) != 1 || task.Unreachable[0].Criterion != "It opens in the owner's browser" {
		t.Fatalf("the requirement should be recorded against the draft: %d drafts, %+v", len(task.Revisions), task.Unreachable)
	}
	if !strings.Contains(writerTurns(runner)[0].Prompt, "```owner-step") {
		t.Fatal("the implementer was never told how to say it can't meet a requirement")
	}
	d := openDecision(t, a, task)
	if d.OwnerStep == nil || d.OwnerStep.Step != "Open the page in your browser and check it shows" || !slices.Equal(d.Choices, []string{choiceOwnerStep, choiceSplit, choiceKeepForTeam, choiceStop}) {
		t.Fatalf("owner step decision %+v", d)
	}
	if d.Recommendation != choiceOwnerStep+": only the owner has a browser" || !strings.Contains(d.Context, "There is no browser in my sandbox.") || !strings.Contains(d.Context, "- [ ] Open the page in your browser") {
		t.Fatalf("owner step decision %q: %s", d.Recommendation, d.Context)
	}
	asked := turns(runner, "Judge whether a requirement the implementer can't meet")
	if len(asked) != 1 || !strings.Contains(asked[0].Prompt, "There is no browser in my sandbox.") {
		t.Fatalf("the PM should judge the requirement once: %d", len(asked))
	}
	if _, err := a.Core.ChooseDecision(ctx, d.ID, choiceOwnerStep, core.FromOwner); err != nil {
		t.Fatal(err)
	}
	task = stepUntil(t, a, task.ID, waitingAgain(d))
	if !slices.Equal(task.Criteria, []string{"Warm tone"}) || !slices.Equal(task.OwnerSteps, []string{"Open the page in your browser and check it shows"}) || len(task.Unreachable) != 0 {
		t.Fatalf("the requirement should move to the owner: %v / %v / %+v", task.Criteria, task.OwnerSteps, task.Unreachable)
	}
	if len(task.Revisions) != 1 || len(task.Edits) != 1 || task.Edits[0].By != core.FromOwner {
		t.Fatalf("the same draft should be checked again without it, by the owner's edit: %d drafts, %+v", len(task.Revisions), task.Edits)
	}
	checklist := "After it lands, check:\n- [ ] Open the page in your browser and check it shows"
	d = openDecision(t, a, task)
	if d.Kind != core.DecisionDelivery || !strings.Contains(d.Context, checklist) {
		t.Fatalf("the approval should carry the checklist: %+v", d)
	}
	if _, err := a.Core.ChooseDecision(ctx, d.ID, choiceApprove, core.FromOwner); err != nil {
		t.Fatal(err)
	}
	task = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Finished() })
	if task.Status != core.TaskDelivered || !activityHas(t, a, checklist) {
		t.Fatalf("the delivery should list the owner's checklist: %s", task.Status)
	}
}

// Without a PM the owner judges the implementer's own words, and can keep
// the requirement for the team; a PM that says the team can meet it keeps
// it there itself, with a note saying why.
func TestARequirementKeptForTheTeamGoesOnToAnotherRound(t *testing.T) {
	t.Parallel()
	t.Run("the owner keeps it", func(t *testing.T) {
		runner := &scriptedRunner{reviews: []string{revise, pass}}
		a, task := ownerStepApp(t, runner, false)
		task = stepUntil(t, a, task.ID, waiting)
		d := openDecision(t, a, task)
		if d.OwnerStep == nil || d.OwnerStep.Step != "It opens in the owner's browser" || !strings.Contains(d.Context, "There is no browser in my sandbox.") || strings.HasPrefix(d.Recommendation, choiceOwnerStep+":") {
			t.Fatalf("owner step decision %+v", d)
		}
		if _, err := a.Core.ChooseDecision(context.Background(), d.ID, choiceKeepForTeam, core.FromOwner); err != nil {
			t.Fatal(err)
		}
		task = stepUntil(t, a, task.ID, waitingAgain(d))
		if len(task.Revisions) != 2 || len(task.OwnerSteps) != 0 || len(task.Criteria) != 2 || openDecision(t, a, task).Kind != core.DecisionDelivery {
			t.Fatalf("the team should revise with the requirement kept: %d drafts, %v", len(task.Revisions), task.Criteria)
		}
	})
	t.Run("the PM says the team can meet it", func(t *testing.T) {
		runner := &scriptedRunner{reviews: []string{revise, pass}, ownerStep: []string{`{"owner_step": false, "step": "", "reason": "a test can open it headless"}`}}
		a, task := ownerStepApp(t, runner, true)
		task = stepUntil(t, a, task.ID, waiting)
		if len(task.Revisions) != 2 || openDecision(t, a, task).Kind != core.DecisionDelivery {
			t.Fatalf("the team should revise without asking the owner: %d drafts", len(task.Revisions))
		}
		if !slices.ContainsFunc(task.Notes, func(n core.Note) bool { return strings.Contains(n.Text, "a test can open it headless") }) {
			t.Fatalf("the PM's reason should be left for the implementer: %+v", task.Notes)
		}
		if !strings.Contains(writerTurns(runner)[1].Prompt, "a test can open it headless") {
			t.Fatal("the implementer never read why")
		}
	})
}

// A step already the owner's is never raised again: quoting one, as worded
// or in part, would bring the owner the same step reworded, round after
// round.
func TestAnOwnerStepIsNotRaisedAgain(t *testing.T) {
	t.Parallel()
	steps := []string{"After it lands, ask the designer on a real task to generate one image and check it shows up attached to the task."}
	block := `[{"requirement": "ask the designer on a real task to generate one image", "why": "no network"}, {"requirement": "It opens in the owner's browser", "why": "no browser"}]`
	got, _ := parseOwnerSteps(block, 2, []string{"It opens in the owner's browser"}, steps, nil)
	if len(got) != 1 || got[0].Criterion != "It opens in the owner's browser" {
		t.Fatalf("raised: %+v", got)
	}
}

// A requirement of the brief the owner took on for a task is theirs for it:
// the team isn't asked to meet it, and raising it again is ignored.
func TestABriefRequirementTheOwnerTookIsNotTheTeams(t *testing.T) {
	t.Parallel()
	p := core.Project{Brief: core.Brief{Goal: "Tools", Criteria: []string{"CI is green on every platform", "The README says how"}}}
	task := core.Task{Objective: "Write the design", OwnerSteps: []string{"Check the CI run"}, OwnerTook: []string{"CI is green on every platform"}}
	brief := briefText(p, task)
	if strings.Contains(brief, "1. CI is green") || !strings.Contains(brief, "The README says how") || !strings.Contains(brief, "Check the CI run") {
		t.Fatalf("brief:\n%s", brief)
	}
	if got, _ := parseOwnerSteps(`[{"requirement": "CI is green on every platform", "why": "no CI here"}]`, 2, task.Criteria, task.OwnersAlready(), task.TeamKept); len(got) != 0 {
		t.Fatalf("raised again: %+v", got)
	}
}
