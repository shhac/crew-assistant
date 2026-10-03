package work

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
)

func TestOwnerChecksNeverEnterTeamPromptsOrEscalations(t *testing.T) {
	const marker = "UNIQUE_OWNER_CHECK_AFTER_LANDING"
	for _, medium := range []string{core.MediumGit, core.MediumDocuments} {
		p := core.Project{Brief: core.Brief{Goal: "Build"}}
		playbook := core.Playbook{Medium: medium}
		task := core.Task{Objective: "Build", Criteria: []string{"Tests pass", marker}, OwnerChecks: []string{marker}, Playbook: &playbook, Plan: &core.Plan{Summary: "Build", OwnerChecks: []string{marker}}}
		for _, prompt := range []string{writerPrompt(p, task, "", true), reviewerPrompt(p, task, core.Revision{}), checkerPrompt(p, task, core.Revision{}, core.Role{Kinds: []string{core.RoleQA}}, &playbook), researcherPrompt(p, task, nil, nil)} {
			if strings.Contains(prompt, marker) {
				t.Fatal("owner check leaked into prompt")
			}
		}
		prompt := researcherPrompt(p, task, nil, nil)
		if !strings.Contains(prompt, "owner_checks") || !strings.Contains(prompt, "move any such task criterion") {
			t.Fatal("researcher lacks moving instructions")
		}
		block := `[{"requirement":"` + marker + `","why":"Only after landing"}]`
		if got := parseOwnerSteps(block, 1, append(task.Criteria, marker), task.OwnersAlready()); len(got) != 0 {
			t.Fatalf("escalated owner's check: %+v", got)
		}
		activity := landedOn(&task, &p, core.Revision{}, "main", "")
		if !strings.Contains(activity, task.OwnerChecklist()) {
			t.Fatal("landing loses checklist")
		}
	}
}

func TestEvaluationOnlyMarkerNeverEntersAnyTeamPrompt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	st, err := core.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const marker = "EVALUATION_ONLY_TEAM_MARKER"
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("INSERT INTO decision_evaluations(decision_id,resolved_at,payload) VALUES(?,?,?)", "evaluation-only", "2026-10-03", `{"context":"`+marker+`"}`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	s := core.NewService(st, config.Default())
	p, err := s.CreateProject(context.Background(), core.ProjectInput{Title: "Build", Template: "draft", Brief: core.BriefInput{Goal: "Build"}})
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.QueueTask(context.Background(), p.ID, core.TaskInput{Objective: "Build"})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, prompt := range []string{writerPrompt(p, task, "", true), reviewerPrompt(p, task, core.Revision{}), checkerPrompt(p, task, core.Revision{}, core.Role{Kinds: []string{core.RoleQA}}, p.Playbook), researcherPrompt(p, task, otherWork(snap, task), snap.Projects), ownerStepPrompt(p, task, core.Unreachable{}), designerPrompt(p, task, core.DesignRequest{}, false), pmPrompt(snap, p)} {
		if strings.Contains(prompt, marker) {
			t.Fatal("evaluation entered role prompt")
		}
	}
}

func TestResearcherMovesPostLandingCriterionAndDeliveryShowsChecks(t *testing.T) {
	const moved = "CI green on every platform"
	const created = "Owner live check"
	a, runner, p := plannedCode(t, 4, `{"summary":"Build Feature", "owner_checks":["`+moved+`","Demonstrated test evidence","unknown"], "changes":["Add Feature"]}`)
	ctx := context.Background()
	task, err := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Add A", Criteria: []string{moved}, OwnerChecks: []string{created}})
	if err != nil {
		t.Fatal(err)
	}
	task = taskNow(t, a, task.ID)
	if task.Plan == nil || len(task.Plan.OwnerChecks) != 1 || task.Plan.OwnerChecks[0] != moved || len(task.Criteria) != 0 || len(task.OwnerChecks) != 2 || len(task.Edits) != 1 || task.Edits[0].Kind != core.RoleResearcher {
		t.Fatalf("%+v", task)
	}
	researcher := false
	for _, spec := range runner.seen {
		if strings.Contains(spec.Prompt, created) {
			t.Fatal("creation check leaked")
		}
		if strings.Contains(spec.Prompt, "Reply with only this JSON object") || strings.Contains(spec.Prompt, "the plan in a few sentences") {
			researcher = true
			continue
		}
		if strings.Contains(spec.Prompt, moved) {
			t.Fatal("moved criterion leaked into later prompt")
		}
	}
	if !researcher {
		t.Fatal("no research turn")
	}
	d := openDecision(t, a, task)
	if !strings.Contains(d.Context, task.OwnerChecklist()) {
		t.Fatalf("delivery: %s", d.Context)
	}
}

func TestResolveDecisionAttributesAssistantEvaluation(t *testing.T) {
	a, _, _ := loopApp(t, &scriptedRunner{}, "")
	ctx := context.Background()
	d, err := a.Core.CreateDecision(ctx, core.DecisionInput{Title: "Which", Context: "Context", Recommendation: "Yes", Choices: []string{"Yes", "No"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.ResolveDecision(ctx, d.ID, "", "Custom", core.FromAssistant); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	if err = a.Core.ExportDecisionEvaluations(ctx, &b); err != nil || !strings.Contains(b.String(), `"answered_by":"assistant"`) {
		t.Fatalf("%s %v", b.String(), err)
	}
}

func TestUndoOlderEditNeverReturnsMovedCheckToWriterPrompt(t *testing.T) {
	a, _, p := plannedCode(t, 4, `{"summary":"Build"}`)
	ctx := context.Background()
	task, err := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Build", Criteria: []string{"CI green"}})
	if err != nil {
		t.Fatal(err)
	}
	edited, err := a.Core.EditTask(ctx, core.EditInput{Project: p.ID, Task: task.ID, Kind: core.RoleResearcher, Add: []string{"Tests"}})
	if err != nil {
		t.Fatal(err)
	}
	a.Core.NextTask(ctx)
	_, err = a.Core.RecordPlan(ctx, task.ID, core.Plan{Summary: "Build", Role: "Researcher", OwnerChecks: []string{"CI green"}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	undone, err := a.Core.UndoTaskEdit(ctx, p.ID, task.ID, edited.Edits[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(writerPrompt(p, undone, "", true), "CI green") || !strings.Contains(undone.OwnerChecklist(), "CI green") {
		t.Fatal("undo exposed an owner check to the writer or removed it from delivery")
	}
}
