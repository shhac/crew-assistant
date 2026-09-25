package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
)

func TestTheAssistantCanStaffATeamAndRecordALearning(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	p, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Notes", Brief: core.BriefInput{Goal: "Notes", Criteria: []string{"Short"}}})
	if err != nil {
		t.Fatal(err)
	}
	rn, _ := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Rune", Kinds: []string{core.RoleReviewer}, Engine: "codex"})
	call := func(name string, in map[string]any) (any, error) {
		raw, _ := json.Marshal(in)
		return a.Execute(ctx, name, raw)
	}
	team := map[string]any{"project_id": p.ID, "template": "draft", "writer_engine": "", "reviewer_engine": "", "max_rounds": "", "deliver_to": "", "repo": "", "branch_prefix": "", "check": "", "sign": "", "prepare": []string{}, "implementer_member": "", "reviewer_member": rn.ID, "qa_member": ""}
	out, err := call("set_team", team)
	if err != nil || out.(core.Project).Playbook.Roles[1].Name != "Rune" {
		t.Fatalf("set_team %+v %v", out, err)
	}
	out, err = call("record_learning", map[string]any{"member_id": rn.ID, "text": "Check the error paths first.", "project_id": p.ID})
	if err != nil || len(out.(core.Member).Learnings) != 1 {
		t.Fatalf("record_learning %+v %v", out, err)
	}
}

func TestTheAssistantQueuesWorkThatWaitsAndSeesItsPlan(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	p, _ := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Notes", Brief: core.BriefInput{Goal: "Notes", Criteria: []string{"Short"}}, Template: "draft"})
	queue := func(objective string, deps []string) (core.Task, error) {
		raw, _ := json.Marshal(map[string]any{"project_id": p.ID, "objective": objective, "criteria": []string{}, "depends_on": deps})
		out, err := a.Execute(ctx, "queue_task", raw)
		if err != nil {
			return core.Task{}, err
		}
		return out.(core.Task), nil
	}
	first, err := queue("First", []string{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := queue("Second", []string{first.ID})
	if err != nil || len(second.DependsOn) != 1 {
		t.Fatalf("queue_task with a dependency: %+v %v", second, err)
	}
	a.Core.UpdateTask(ctx, first.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.Plan = &core.Plan{Summary: strings.Repeat("x", 2000), Changes: []string{"a", "b", "c", "d", "e", "f", "g"}}
		return "", nil
	})
	snap, _ := a.Core.Snapshot(ctx)
	view := assistantView(snap)
	for _, task := range view.Tasks {
		if task.ID == first.ID && (len(task.Plan.Summary) > 620 || len(task.Plan.Changes) != 5) {
			t.Fatalf("the assistant should see the gist of a plan: %d %d", len(task.Plan.Summary), len(task.Plan.Changes))
		}
		if task.ID == second.ID && (len(task.WaitsFor) != 1 || task.WaitsFor[0] != "First") {
			t.Fatalf("the assistant should see what a task waits for: %v", task.WaitsFor)
		}
	}
}
