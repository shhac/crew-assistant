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

func TestPMLinOfferedLinkedOnlyAndChecksCurrentAuthority(t *testing.T) {
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
