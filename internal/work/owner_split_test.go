package work

import (
	"context"
	"encoding/json"
	"github.com/shhac/crew-assistant/internal/text"
	"slices"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
)

func ownerBlock(criterion string) string {
	b, _ := json.Marshal([]ownerStepEntry{{Requirement: criterion, Why: "No browser in sandbox"}})
	return "Draft ready.\n```owner-step\n" + string(b) + "\n```"
}

func TestKeptRequirementsReportInNextDraft(t *testing.T) {
	for _, choice := range []string{choiceSplit, choiceKeepForTeam} {
		t.Run(choice, func(t *testing.T) {
			ctx := context.Background()
			reviews := []string{revise, pass}
			if choice == choiceSplit {
				reviews = []string{revise, revise, pass}
			}
			runner := &scriptedRunner{reviews: reviews}
			a, task := ownerStepApp(t, runner, false)
			team := "It opens in the owner's browser"
			var split *core.OwnerSplit
			if choice == choiceSplit {
				team = "It opens"
				split = &core.OwnerSplit{Team: team, Owner: "Check in the owner's browser"}
			}
			runner.writerReplies = append(runner.writerReplies, strings.Repeat("Detailed hand-over. ", 200)+ownerBlock(team))
			task = stepUntil(t, a, task.ID, waiting)
			d := openDecision(t, a, task)
			if !slices.Equal(d.Choices, []string{choiceOwnerStep, choiceSplit, choiceKeepForTeam, choiceStop}) {
				t.Fatalf("choices: %v", d.Choices)
			}
			if _, err := a.ResolveDecision(ctx, d.ID, choice, "", core.FromOwner, split); err != nil {
				t.Fatal(err)
			}
			// A fresh loop finds the resolved decision after restart and applies it.
			fresh := &Loop{Core: a.Core}
			snap, err := a.Core.Snapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if applied, err := fresh.settleAnswers(ctx, snap); err != nil || !applied {
				t.Fatalf("restart application: %v %v", applied, err)
			}
			snap, err = a.Core.Snapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if applied, err := fresh.settleAnswers(ctx, snap); err != nil || applied {
				t.Fatalf("applied twice: %v %v", applied, err)
			}
			task = stepUntil(t, a, task.ID, waitingAgain(d))
			if len(task.Revisions) != 2 || len(task.Unreachable) != 0 || openDecision(t, a, task).Kind != core.DecisionDelivery {
				t.Fatalf("repeat escalation: %+v", task)
			}
			if !slices.Contains(task.TeamKept, team) || !strings.Contains(task.Revisions[1].Summary, "Kept for the team, still not met from the sandbox:\n- "+team+": No browser in sandbox") {
				t.Fatalf("hand-over: %+v", task.Revisions[1])
			}
			if len(task.Revisions[1].Summary) > 2000 {
				t.Fatal("hand-over exceeds summary limit")
			}
			for _, limit := range []int{600, 800} {
				if !strings.Contains(text.Clip(task.Revisions[1].Summary, limit), "Kept for the team, still not met from the sandbox:\n- "+team+": No browser in sandbox") {
					t.Fatalf("kept report lost at %d bytes", limit)
				}
			}
			if !strings.Contains(openDecision(t, a, task).Context, "Kept for the team, still not met from the sandbox:") {
				t.Fatal("delivery does not report unmet kept requirement")
			}
			if choice == choiceSplit && (!slices.Equal(task.Criteria, []string{"Warm tone", team}) || !slices.Equal(task.OwnerChecks, []string{split.Owner})) {
				t.Fatalf("split lists: %+v", task)
			}
		})
	}
}

func TestChangedKeptRequirementEscalatesAgain(t *testing.T) {
	ctx := context.Background()
	runner := &scriptedRunner{reviews: []string{revise, revise, pass}}
	a, task := ownerStepApp(t, runner, false)
	changed := "It opens in two browsers"
	runner.writerReplies = append(runner.writerReplies, ownerBlock(changed))
	task = stepUntil(t, a, task.ID, waiting)
	d := openDecision(t, a, task)
	if _, err := a.ResolveDecision(ctx, d.ID, choiceKeepForTeam, "", core.FromOwner); err != nil {
		t.Fatal(err)
	}
	snap, _ := a.Core.Snapshot(ctx)
	if _, err := a.settleAnswers(ctx, snap); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Core.EditTask(ctx, core.EditInput{Project: task.ProjectID, Task: task.ID, By: "Owner", Kind: core.RolePM, Criteria: []string{"Warm tone", changed}}); err != nil {
		t.Fatal(err)
	}
	task = stepUntil(t, a, task.ID, waitingAgain(d))
	next := openDecision(t, a, task)
	if next.OwnerStep == nil || next.OwnerStep.Criterion != changed || len(task.Revisions) != 2 {
		t.Fatalf("changed requirement not escalated: %+v", next)
	}
}

func TestKeptPartPrecedesFuzzyOwnerFilter(t *testing.T) {
	block := `[{"requirement":"README updated","why":"no sandbox check"}]`
	pending, kept := parseOwnerSteps(block, 2, []string{"README updated"}, []string{"README updated; CI green; release"}, []string{"README updated"})
	if len(pending) != 0 || len(kept) != 1 || kept[0].Criterion != "README updated" || kept[0].Why != "no sandbox check" {
		t.Fatalf("pending %v kept %v", pending, kept)
	}
	pending, kept = parseOwnerSteps(`[{"requirement":"README updated with examples","why":"no check"}]`, 3, []string{"README updated with examples"}, nil, []string{"README updated"})
	if len(pending) != 1 || len(kept) != 0 {
		t.Fatalf("changed text suppressed: %v %v", pending, kept)
	}
	pending, kept = parseOwnerSteps(`[{"requirement":"README updated on Linux","why":"no check"}]`, 3,
		[]string{"README updated on Linux"}, []string{"README updated on Linux and Windows; CI green; release"}, []string{"README updated"})
	if len(pending) != 1 || len(kept) != 0 {
		t.Fatalf("changed team part swallowed by original brief requirement: %v %v", pending, kept)
	}
}

func TestKeptWriterPromptPreservesStructuredReporting(t *testing.T) {
	p := core.Project{Brief: core.Brief{Goal: "Docs", Criteria: []string{"Brief check"}}}
	task := core.Task{Objective: "Docs", Criteria: []string{"Team check"}, TeamKept: []string{"Brief check", "Team check", "Outdated check"}}
	prompt := writerPrompt(p, task, "", false)
	if !strings.Contains(prompt, "The owner kept these requirements for the team:") || !strings.Contains(prompt, "- Brief check") || !strings.Contains(prompt, "- Team check") || strings.Contains(prompt, "Outdated check") || strings.Contains(prompt, "not in the owner-step block") || !strings.Contains(prompt, "Keep quoting them in the owner-step block") {
		t.Fatalf("misleading kept instructions: %s", prompt)
	}
	task.OwnerTook = []string{"Team check"}
	if !strings.Contains(briefText(p, task), "Team check") {
		t.Fatal("current team criterion hidden by settled original")
	}
}

func TestKeptWriterPromptAlsoUsesProjectCheck(t *testing.T) {
	p := core.Project{Brief: core.Brief{Goal: "Docs"}, Playbook: &core.Playbook{Medium: core.MediumGit, Check: "make check"}}
	task := core.Task{Objective: "Docs", Criteria: []string{"Team check"}, TeamKept: []string{"Team check"}}
	prompt := writerPrompt(p, task, "", false)
	for _, instruction := range []string{"- Team check", "Keep quoting them in the owner-step block", "Use run_check for the project's check", "is never an owner step"} {
		if !strings.Contains(prompt, instruction) {
			t.Fatalf("missing %q in merged prompt: %s", instruction, prompt)
		}
	}
}

func TestWholeSplitOriginalIsNotEscalatedAgain(t *testing.T) {
	ctx := context.Background()
	runner := &scriptedRunner{reviews: []string{revise, revise, pass}}
	a, task := ownerStepApp(t, runner, false)
	original := "Warm tone; It opens in the owner's browser"
	if _, err := a.Core.EditTask(ctx, core.EditInput{Project: task.ProjectID, Task: task.ID, Kind: core.RolePM, By: "Owner", Criteria: []string{"Warm tone", original}}); err != nil {
		t.Fatal(err)
	}
	runner.writerReplies = []string{ownerBlock(original), ownerBlock(original)}
	task = stepUntil(t, a, task.ID, waiting)
	d := openDecision(t, a, task)
	if _, err := a.ResolveDecision(ctx, d.ID, choiceSplit, "", core.FromOwner, &core.OwnerSplit{Team: "It opens", Owner: "Check browser"}); err != nil {
		t.Fatal(err)
	}
	task = stepUntil(t, a, task.ID, waitingAgain(d))
	if len(task.Revisions) != 2 || len(task.Unreachable) != 0 || openDecision(t, a, task).Kind != core.DecisionDelivery {
		t.Fatalf("whole original escalated again: %+v", task)
	}
}

func TestKeptBriefUsesCanonicalTextAcrossDrafts(t *testing.T) {
	ctx := context.Background()
	runner := &scriptedRunner{reviews: []string{revise, pass, pass}}
	a, task := ownerStepApp(t, runner, false)
	original := "It opens in the owner's browser with a live account"
	if _, err := a.Core.UpdateBrief(ctx, task.ProjectID, core.BriefInput{Goal: "Docs", Criteria: []string{original}}); err != nil {
		t.Fatal(err)
	}
	runner.writerReplies = []string{ownerBlock("browser with a live account"), ownerBlock("with a live account"), ownerBlock("browser with two live accounts")}
	task = stepUntil(t, a, task.ID, waiting)
	d := openDecision(t, a, task)
	if d.OwnerStep == nil || d.OwnerStep.Criterion != original {
		t.Fatalf("noncanonical brief criterion: %+v", d)
	}
	if _, err := a.ResolveDecision(ctx, d.ID, choiceKeepForTeam, "", core.FromOwner); err != nil {
		t.Fatal(err)
	}
	task = stepUntil(t, a, task.ID, waitingAgain(d))
	if openDecision(t, a, task).Kind != core.DecisionDelivery || len(task.Unreachable) != 0 || !strings.Contains(task.Revisions[1].Summary, original) {
		t.Fatalf("brief keep repeated: %+v", task)
	}
	changed := "It opens in the owner's browser with two live accounts"
	if _, err := a.Core.UpdateBrief(ctx, task.ProjectID, core.BriefInput{Goal: "Docs", Criteria: []string{changed}}); err != nil {
		t.Fatal(err)
	}
	// Send this draft back so the next writer quotes the changed brief.
	delivery := openDecision(t, a, task)
	if _, err := a.ResolveDecision(ctx, delivery.ID, "", "Another draft", core.FromOwner); err != nil {
		t.Fatal(err)
	}
	task = stepUntil(t, a, task.ID, waitingAgain(delivery))
	if got := openDecision(t, a, task); got.OwnerStep == nil || got.OwnerStep.Criterion != changed {
		t.Fatalf("changed brief not escalated: %+v", got)
	}
}

func TestSplitBriefOriginalAppearsOnceInPrompts(t *testing.T) {
	criterion := "The documentation is current"
	p := core.Project{Brief: core.Brief{Goal: "Docs", Criteria: []string{criterion}}}
	task := core.Task{Objective: "Docs", Criteria: []string{criterion}, OwnerTook: []string{criterion}}
	if got := strings.Count(briefText(p, task), criterion); got != 1 {
		t.Fatalf("brief original appears %d times", got)
	}
}

func TestSplitCollisionDoesNotBlockOtherAnswers(t *testing.T) {
	ctx := context.Background()
	runner := &scriptedRunner{reviews: []string{revise, pass}}
	a, task := ownerStepApp(t, runner, false)
	task = stepUntil(t, a, task.ID, waiting)
	d := openDecision(t, a, task)
	if _, err := a.ResolveDecision(ctx, d.ID, choiceSplit, "", core.FromOwner, &core.OwnerSplit{Team: "It opens", Owner: "Check browser"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.OwnerChecks = []string{"It opens"}
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
	other, err := a.Core.QueueTask(ctx, task.ProjectID, core.TaskInput{Objective: "Another task"})
	if err != nil {
		t.Fatal(err)
	}
	next, err := a.Core.OpenTaskDecision(ctx, other.ID, core.DecisionEscalation, core.DecisionInput{Title: "Stop?", Context: "Owner request", Recommendation: choiceStop, Choices: []string{choiceStop, choiceAnotherRound}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ResolveDecision(ctx, next.ID, choiceStop, "", core.FromOwner); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		snap, err := a.Core.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if applied, err := a.settleAnswers(ctx, snap); err != nil || !applied {
			t.Fatalf("answers pass %d stalled: %v %v", i, applied, err)
		}
	}
	snap, err := a.Core.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range snap.Tasks {
		if got.ID == other.ID && got.Status != core.TaskStopped {
			t.Fatal("other answer never applied")
		}
		if got.ID == task.ID && (got.Status == core.TaskWaiting || got.DecisionID != "" || slices.Contains(got.TeamKept, "It opens")) {
			t.Fatalf("split remains waiting: %+v", got)
		}
	}
	if applied, err := a.settleAnswers(ctx, snap); err != nil || applied {
		t.Fatalf("settled split retried: %v %v", applied, err)
	}
	advanced := stepUntil(t, a, task.ID, waitingAgain(d))
	if openDecision(t, a, advanced).Kind != core.DecisionDelivery {
		t.Fatal("later loop pass did not advance the split task")
	}
}

func TestKeptPromptInstructionOnlyWithActiveKeptList(t *testing.T) {
	p := core.Project{Brief: core.Brief{Goal: "Docs"}}
	for _, kept := range [][]string{nil, {"Outdated criterion"}} {
		prompt := writerPrompt(p, core.Task{Objective: "Docs", TeamKept: kept}, "", false)
		if strings.Contains(prompt, "If kept requirements") || strings.Contains(prompt, "Keep quoting them") {
			t.Fatal("prompt refers to an absent kept list")
		}
		if !strings.Contains(prompt, "```owner-step") {
			t.Fatal("general owner-step instructions missing")
		}
	}
}

func TestLongKeptEntryFitsDeliverySummary(t *testing.T) {
	criterion := strings.Repeat("Requirement ", 50)
	why := strings.Repeat("No sandbox access. ", 50)
	summary := draftSummary(strings.Repeat("Draft detail. ", 300), []core.Unreachable{{Criterion: criterion, Why: why}})
	report := "Kept for the team, still not met from the sandbox:\n- " + text.Clip(criterion, 300) + ": " + text.Clip(why, 200)
	if !strings.HasPrefix(text.Clip(summary, 600), report) || len(summary) > 2000 {
		t.Fatalf("delivery report truncated: %s", text.Clip(summary, 600))
	}
}

func TestOrdinaryDraftSummaryKeepsExistingLimit(t *testing.T) {
	reply := strings.Repeat("Draft detail. ", 300)
	if got := draftSummary(reply, nil); got != text.Clip(reply, 2000) {
		t.Fatal("changed ordinary summary clipping")
	}
}
