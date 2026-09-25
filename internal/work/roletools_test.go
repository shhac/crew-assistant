package work

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/shhac/lib-agent-harness/session"

	"github.com/shhac/crew-assistant/internal/core"
)

func callTool(t *testing.T, tools roleTools, name string, args map[string]string) session.ToolResult {
	t.Helper()
	raw, _ := json.Marshal(args)
	result, err := tools.Handler().CallTool(context.Background(), session.ToolCall{Name: name, Arguments: raw})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func toolNames(tools roleTools) []string {
	var out []string
	for _, d := range tools.Definitions() {
		out = append(out, d.Name)
	}
	return out
}

// A role sees its own project's tasks, filtered as it asks, and links only
// its own task, only as its role may, and only while its turn still counts.
func TestARoleSeesItsProjectsTasksAndLinksOnlyItsOwn(t *testing.T) {
	a, p, first := loopApp(t, &scriptedRunner{}, "")
	ctx := context.Background()
	second, _ := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Write the follow-up", Criteria: []string{"Warm"}})
	third, _ := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Translate it", Criteria: []string{"Accurate"}})
	other, _ := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Elsewhere", Template: "draft", Brief: core.BriefInput{Goal: "Other", Criteria: []string{"x"}}})
	hidden, _ := a.Core.QueueTask(ctx, other.ID, core.TaskInput{Objective: "Secret elsewhere", Criteria: []string{"x"}})

	researcher := a.toolsFor(first, core.RoleResearcher, "")
	if got := callTool(t, researcher, "list_tasks", map[string]string{"which": "", "related_to": "", "text": ""}); got.IsError || !strings.Contains(got.Content, second.ID) || strings.Contains(got.Content, hidden.ID) || !strings.Contains(got.Content, "(your task)") {
		t.Fatalf("list: %+v", got)
	}
	if got := callTool(t, researcher, "list_tasks", map[string]string{"which": "", "related_to": "", "text": "translate"}); !strings.Contains(got.Content, third.ID) || strings.Contains(got.Content, second.ID) {
		t.Fatalf("text filter: %s", got.Content)
	}
	if got := callTool(t, researcher, "read_task", map[string]string{"task_id": hidden.ID}); !got.IsError || strings.Contains(got.Content, "Secret") {
		t.Fatalf("read another project's task: %+v", got)
	}
	if got := callTool(t, researcher, "link_tasks", map[string]string{"relation": "depends_on", "other_task_id": second.ID}); got.IsError {
		t.Fatalf("link: %s", got.Content)
	}
	if got := callTool(t, researcher, "link_tasks", map[string]string{"relation": "relates_to", "other_task_id": hidden.ID}); !got.IsError || strings.Contains(got.Content, "Secret") {
		t.Fatalf("linked across projects: %+v", got)
	}
	if got := callTool(t, researcher, "list_tasks", map[string]string{"which": "", "related_to": second.ID, "text": ""}); !strings.Contains(got.Content, first.ID) || strings.Contains(got.Content, third.ID) {
		t.Fatalf("related filter: %s", got.Content)
	}
	if got := callTool(t, researcher, "read_task", map[string]string{"task_id": first.ID}); !strings.Contains(got.Content, "- depends on "+second.ID) {
		t.Fatalf("read shows links: %s", got.Content)
	}

	reviewer := a.toolsFor(second, core.RoleReviewer, "member-one")
	if got := callTool(t, reviewer, "link_tasks", map[string]string{"relation": "depends_on", "other_task_id": third.ID}); !got.IsError {
		t.Fatal("a reviewer decided what a task waits for")
	}
	if got := callTool(t, reviewer, "link_tasks", map[string]string{"relation": "relates_to", "other_task_id": third.ID}); got.IsError {
		t.Fatalf("relates: %s", got.Content)
	}
	snap, _ := a.Core.Snapshot(ctx)
	if linkedSecond, _ := findTask(snap, p.ID, second.ID); linkedSecond.LinkedBy["relates_to:"+third.ID].By != "member:member-one" {
		t.Fatalf("mark %v", linkedSecond.LinkedBy)
	}

	// A role limited to related work can't take away what a task waits
	// for, even a link the team set.
	if got := callTool(t, reviewer, "unlink_tasks", map[string]string{"other_task_id": first.ID}); !got.IsError {
		t.Fatal("a reviewer released a task the researcher made wait")
	}
	if got := callTool(t, researcher, "unlink_tasks", map[string]string{"other_task_id": second.ID}); got.IsError {
		t.Fatalf("the researcher couldn't take back its own link: %s", got.Content)
	}

	pm := a.projectTools(p.ID)
	if names := toolNames(pm); len(names) != 2 {
		t.Fatalf("the PM links through its answer, not tools: %v", names)
	}

	a.StopTask(ctx, p.ID, third.ID)
	stale := a.toolsFor(third, core.RoleResearcher, "")
	stale.status = core.TaskResearching
	if got := callTool(t, stale, "link_tasks", map[string]string{"relation": "relates_to", "other_task_id": first.ID}); !got.IsError {
		t.Fatal("a stopped task's turn changed its links")
	}
}

// Research alone looks outward, and decides what a task waits for; other
// roles only mark related work, and every turn can look tasks up.
func TestEachRoleGetsItsTools(t *testing.T) {
	a, _, task := loopApp(t, &scriptedRunner{}, "")
	for status, want := range map[string]struct {
		web   bool
		tools []string
	}{
		core.TaskResearching: {true, []string{"list_tasks", "read_task", "link_tasks", "unlink_tasks"}},
		core.TaskWriting:     {false, []string{"list_tasks", "read_task", "link_tasks", "unlink_tasks"}},
		core.TaskReviewing:   {false, []string{"list_tasks", "read_task", "link_tasks", "unlink_tasks"}},
	} {
		task.Status = status
		spec, cleanup, err := a.roleSpec(task, core.Role{Name: "Seat", Kinds: []string{core.RoleResearcher, core.RoleImplementer}, Engine: "claude"}, t.TempDir(), false, docsMedium{}, "prompt")
		if err != nil {
			t.Fatal(err)
		}
		if cleanup != nil {
			cleanup()
		}
		var names []string
		for _, d := range spec.Tools {
			names = append(names, d.Name)
		}
		if spec.Web != want.web || strings.Join(names, " ") != strings.Join(want.tools, " ") || !strings.Contains(spec.Instructions, "list_tasks") {
			t.Errorf("%s: web %v tools %v", status, spec.Web, names)
		}
		link := spec.Tools[2].Description
		if (status == core.TaskResearching) != strings.Contains(link, "depends_on") {
			t.Errorf("%s may link as: %s", status, link)
		}
	}
}

func TestATurnPlaysTheRoleItsStepCallsFor(t *testing.T) {
	both := core.Role{Kinds: []string{core.RoleResearcher, core.RoleImplementer}}
	qa := core.Role{Kinds: []string{core.RoleQA}}
	for _, tc := range []struct {
		status string
		role   core.Role
		want   string
	}{
		{core.TaskResearching, both, core.RoleResearcher},
		{core.TaskDesigning, both, core.RoleDesigner},
		{core.TaskWriting, both, core.RoleImplementer},
		{core.TaskReviewing, qa, core.RoleQA},
		{core.TaskDeciding, qa, core.RoleQA},
		{core.TaskReviewing, core.Role{Kinds: []string{core.RoleReviewer}}, core.RoleReviewer},
		{core.TaskLanding, both, core.RoleImplementer},
		{core.TaskLanding, qa, core.RoleQA},
	} {
		if got := turnKind(core.Task{Status: tc.status}, tc.role); got != tc.want {
			t.Errorf("%s with %v: %s, want %s", tc.status, tc.role.Kinds, got, tc.want)
		}
	}
}
