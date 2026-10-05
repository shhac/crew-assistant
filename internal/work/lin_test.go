package work

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/integrations/connections"
	"github.com/shhac/lib-agent-harness/session"
)

// The PM takes on a ticket the owner names: it queues a task for it, or links
// one, through the project's own Linear account and only while the project
// still uses it, and the issue's description reaches the team.
func TestPMLinksLinearIssuesThroughItsProject(t *testing.T) {
	t.Parallel()
	lp, p, task := loopApp(t, &scriptedRunner{}, "")
	ctx := context.Background()
	if pm := lp.managerTools(p.ID, core.Role{}); slices.Contains(toolNames(pm), "link_task_linear") || strings.Contains(queueSchema(pm), "linear_issue") {
		t.Fatal("unlinked PM offered Linear links")
	}
	cfg := config.Default()
	cfg.Connections = []config.Connection{{ID: "linear", Name: "Linear", Tool: "lin", Profiles: []string{"home"}}}
	lp.Config = func() config.Config { return cfg }
	if err := lp.Core.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	link := &core.LinearLink{ConnectionID: "linear", Profile: "home", Kind: "team", ID: "11111111-1111-1111-1111-111111111111", Name: "Engineering", Rules: core.LinearRules{Assignee: "any"}}
	if _, err := lp.Core.SetProjectLinear(ctx, p.ID, link); err != nil {
		t.Fatal(err)
	}
	issues := map[string]string{"EX-1454": "22222222-2222-2222-2222-222222222222", "EX-7": "33333333-3333-3333-3333-333333333333"}
	reads := 0
	lp.linear = connections.Client{Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
		if args[0] == "auth" {
			return []byte(`{"alias":"home"}`), nil
		}
		reads++
		if args[len(args)-1] != "home" {
			t.Fatal(args)
		}
		var vars struct{ ID string }
		_ = json.Unmarshal([]byte(args[4]), &vars)
		for identifier, id := range issues {
			switch {
			case args[2] == connections.LinearIssueRefQuery && vars.ID == identifier:
				return []byte(`{"issue":{"id":"` + id + `","identifier":"` + identifier + `","title":"Membership renewal","url":"https://linear.app/x/issue/` + identifier + `"}}`), nil
			case args[2] == connections.LinearIssueContextQuery && vars.ID == id:
				return []byte(`{"issue":{"id":"` + id + `","description":"Renew memberships on the 1st."}}`), nil
			}
		}
		t.Fatal(args)
		return nil, nil
	}}
	call := func(r roleTools, name string, args map[string]string) session.ToolResult {
		raw, _ := json.Marshal(args)
		out, err := r.Handler().CallTool(ctx, session.ToolCall{Name: name, Arguments: raw})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	pm := lp.managerTools(p.ID, core.Role{Name: "Pim"})
	if !slices.Contains(toolNames(pm), "link_task_linear") || !strings.Contains(queueSchema(pm), "linear_issue") || !strings.Contains(pm.guide(), "issue get EX-123") {
		t.Fatal("linked PM missing Linear links")
	}
	answer := lp.answerTools(p.ID, core.Role{})
	if slices.Contains(toolNames(answer), "link_task_linear") || strings.Contains(answer.guide(), "issue get EX-123") {
		t.Fatal("answer turn offered Linear links")
	}
	got := call(pm, "queue_task", map[string]string{"title": "", "requirements": "", "depends_on": "", "linear_issue": "EX-1454"})
	if got.IsError || !strings.Contains(got.Content, "linked to Linear EX-1454") {
		t.Fatal(got)
	}
	snap, _ := lp.Core.Snapshot(ctx)
	queued := snap.Tasks[len(snap.Tasks)-1]
	if queued.Objective != "EX-1454: Membership renewal" || len(queued.LinearLinks) != 1 || queued.LinearLinks[0].By != core.LinkedByPM {
		t.Fatal(queued)
	}
	if brief := briefText(p, queued); !strings.Contains(brief, "Linear issue linked to this task: EX-1454") || !strings.Contains(brief, "Renew memberships on the 1st.") {
		t.Fatal(brief)
	}
	if got = call(pm, "queue_task", map[string]string{"title": "Again", "requirements": "", "depends_on": "", "linear_issue": "EX-1454"}); !got.IsError || !strings.Contains(got.Content, "already linked") {
		t.Fatal("issue queued twice", got)
	}
	if got = call(pm, "link_task_linear", map[string]string{"task_id": task.ID, "kind": "issue", "ref": "EX-7"}); got.IsError {
		t.Fatal(got)
	}
	snap, _ = lp.Core.Snapshot(ctx)
	if linked, _ := snap.FindTask(task.ID); len(linked.LinearLinks) != 1 || linked.LinearLinks[0].Identifier != "EX-7" || linked.LinearLinks[0].Description == "" {
		t.Fatal(linked.LinearLinks)
	}
	if got = call(pm, "link_task_linear", map[string]string{"task_id": task.ID, "kind": "team", "ref": "EX-7"}); !got.IsError {
		t.Fatal("unknown kind linked")
	}
	before := reads
	lp.Demo = true
	if got = call(pm, "link_task_linear", map[string]string{"task_id": task.ID, "kind": "issue", "ref": "EX-7"}); !got.IsError || reads != before {
		t.Fatal("demo read Linear")
	}
	lp.Demo = false
	if _, err := lp.Core.ClearProjectLinear(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if got = call(pm, "link_task_linear", map[string]string{"task_id": task.ID, "kind": "issue", "ref": "EX-7"}); !got.IsError || !strings.Contains(got.Content, "no longer linked") || reads != before {
		t.Fatal("unlinked project still linked issues", got)
	}
}

func queueSchema(r roleTools) string {
	for _, d := range r.Definitions() {
		if d.Name == "queue_task" {
			data, _ := json.Marshal(d.Schema)
			return string(data)
		}
	}
	return ""
}

func TestPMLinOfferedLinkedOnlyAndChecksCurrentAuthority(t *testing.T) {
	t.Parallel()
	lp, p, task := loopApp(t, &scriptedRunner{}, "")
	ctx := context.Background()
	if pm := lp.managerTools(p.ID, core.Role{}); slices.Contains(toolNames(pm), "lin") || strings.Contains(pm.guide(), "Use the lin tool") {
		t.Fatal("unlinked PM offered lin")
	}
	cfg := config.Default()
	cfg.Connections = []config.Connection{{ID: "linear", Name: "Linear", Tool: "lin", Profiles: []string{"home"}}}
	lp.Config = func() config.Config { return cfg }
	if err := lp.Core.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	link := &core.LinearLink{ConnectionID: "linear", Profile: "home", Kind: "team", ID: "11111111-1111-1111-1111-111111111111", Name: "Engineering", Rules: core.LinearRules{Assignee: "any"}}
	if _, err := lp.Core.SetProjectLinear(ctx, p.ID, link); err != nil {
		t.Fatal(err)
	}
	calls := 0
	lp.linear = connections.Client{Run: func(context.Context, string, []string) ([]byte, error) { return []byte(`{"alias":"home"}`), nil }, RunClipped: func(_ context.Context, _ string, args []string) connections.LinResult {
		calls++
		if !slices.Equal(args[:4], []string{"--color", "never", "--workspace", "home"}) {
			t.Fatal(args)
		}
		return connections.LinResult{Output: "data", Failed: true, Error: "permission denied"}
	}}
	pm := lp.managerTools(p.ID, core.Role{Name: "Pim"})
	if !slices.Contains(toolNames(pm), "lin") || !strings.Contains(pm.guide(), "Use the lin tool") {
		t.Fatal("linked PM missing lin")
	}
	if slices.Contains(toolNames(lp.toolsFor(task, core.RoleResearcher, core.Role{})), "lin") {
		t.Fatal("researcher offered lin")
	}
	invoke := func(r roleTools, args []string) session.ToolResult {
		raw, _ := json.Marshal(map[string]any{"args": args, "reference": ""})
		out, err := r.Handler().CallTool(ctx, session.ToolCall{Name: "lin", Arguments: raw})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := invoke(pm, []string{"issue", "get", "ENG-1"}); got.IsError || !strings.Contains(got.Content, `"failed":true`) || !strings.Contains(got.Content, "permission denied") {
		t.Fatalf("failure hidden: %+v", got)
	}
	write := []string{"issue", "comment", "new", "ENG-1", "private comment body"}
	if got := invoke(pm, write); !got.IsError || calls != 1 {
		t.Fatal("write granted without opt-in")
	}
	cfg.Connections[0].AllowWrites = true
	if got := invoke(lp.answerTools(p.ID, core.Role{}), write); !got.IsError {
		t.Fatal("answer turn changed Linear")
	}
	if got := invoke(pm, write); got.IsError || calls != 2 {
		t.Fatal(got)
	}
	snap, _ := lp.Core.Snapshot(ctx)
	var events []string
	for _, e := range snap.Activity {
		if e.Kind == "linear.write" {
			events = append(events, e.Summary)
		}
	}
	if len(events) != 2 || strings.Contains(strings.Join(events, " "), "private comment body") || !strings.Contains(strings.Join(events, " "), "failed") {
		t.Fatalf("activity: %v", events)
	}
	lp.Demo = true
	if got := invoke(pm, write); !got.IsError || calls != 2 {
		t.Fatal("demo called lin")
	}
	lp.Demo = false
	cfg.Connections[0].AllowWrites = false
	if got := invoke(pm, write); !got.IsError || calls != 2 {
		t.Fatal("mid-turn write opt-out ignored")
	}
	if _, err := lp.Core.ClearProjectLinear(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if got := invoke(pm, []string{"issue", "get", "ENG-1"}); !got.IsError || !strings.Contains(got.Content, "no longer linked") || calls != 2 {
		t.Fatal("unlink ignored")
	}
}
