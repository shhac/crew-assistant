package app

import (
	"context"
	"encoding/json"
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
	rn, _ := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Rune", Kind: core.RoleReviewer, Engine: "codex"})
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
