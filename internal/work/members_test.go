package work

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
)

func TestAMemberFillsItsRoleAndKeepsTheTemplatesWays(t *testing.T) {
	a := testLoop(t)
	ctx := context.Background()
	p, _ := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Notes", Brief: core.BriefInput{Goal: "Notes", Criteria: []string{"Short"}}})
	ada, _ := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Ada", Kind: core.RoleImplementer, Engine: "codex", Model: "gpt-6", Instructions: "Keep sentences short."})
	rn, _ := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Rune", Kind: core.RoleReviewer, Engine: "claude"})
	project, err := a.SetTeam(ctx, p.ID, TeamChoice{Template: "draft", Implementer: ada.ID, Reviewer: rn.ID, WriterEngine: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	writer, reviewer := project.Playbook.Roles[0], project.Playbook.Roles[1]
	if writer.Name != "Ada" || writer.Member != ada.ID || writer.Engine != "codex" || writer.Model != "gpt-6" {
		t.Fatalf("the member should bring its own name and engine: %+v", writer)
	}
	template := core.Templates["draft"].Roles[0].Instructions
	if !strings.HasPrefix(writer.Instructions, template) || !strings.HasSuffix(writer.Instructions, "Keep sentences short.") {
		t.Fatalf("instructions should be the template's, then the member's: %q", writer.Instructions)
	}
	if reviewer.Name != "Rune" || reviewer.Member != rn.ID {
		t.Fatalf("reviewer %+v", reviewer)
	}
	for _, choice := range []TeamChoice{
		{Template: "draft", Implementer: rn.ID},
		{Template: "draft", QA: ada.ID},
	} {
		if _, err := a.SetTeam(ctx, p.ID, choice); err == nil {
			t.Errorf("accepted %+v", choice)
		}
	}
	if _, err := a.SetTeam(ctx, p.ID, TeamChoice{Template: "draft", Reviewer: "gone"}); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("a member that does not exist: %v", err)
	}
}

// Learnings are pinned when a task starts, because a role's instructions are
// part of its session: one learned mid-task must not start the writer afresh,
// and reaches the member's next task instead.
func TestAMemberBringsWhatItLearnedToTheTasksItStarts(t *testing.T) {
	runner := &scriptedRunner{reviews: []string{revise, pass}}
	a, p, _ := loopApp(t, runner, "")
	ctx := context.Background()
	ada, _ := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Ada", Kind: core.RoleImplementer, Engine: "claude"})
	if _, err := a.Core.AddLearning(ctx, ada.ID, "Thank people by name.", p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetTeam(ctx, p.ID, TeamChoice{Template: "draft", Implementer: ada.ID}); err != nil {
		t.Fatal(err)
	}
	learned := false
	runner.onWriter = func(string) {
		if !learned {
			learned = true
			a.Core.AddLearning(ctx, ada.ID, "Sign off warmly.", "")
		}
	}
	if task := settle(t, a); task.Status != core.TaskWaiting || task.Round != 2 {
		t.Fatalf("task %+v", task)
	}
	var writers []string
	for _, spec := range runner.seen {
		if spec.Write {
			writers = append(writers, spec.Instructions)
		}
	}
	if len(writers) != 2 || writers[0] != writers[1] {
		t.Fatalf("the writer's instructions changed mid-task, which restarts its session: %q", writers)
	}
	if !strings.Contains(writers[0], "Thank people by name.") || strings.Contains(writers[0], "Sign off warmly.") {
		t.Fatalf("instructions %q", writers[0])
	}
	if _, err := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Another note"}); err != nil {
		t.Fatal(err)
	}
	next, ok, err := a.Core.NextTask(ctx)
	if err != nil || !ok || next.Objective != "Another note" {
		t.Fatalf("next %+v %v %v", next, ok, err)
	}
	if got := next.Roles[0].Instructions; !strings.Contains(got, "Sign off warmly.") || strings.Index(got, "Sign off warmly.") > strings.Index(got, "Thank people by name.") {
		t.Fatalf("the next task should carry every learning, newest first: %q", got)
	}
}
