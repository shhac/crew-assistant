package work

import (
	"context"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
)

// subagentsTeam is a writing team whose writer is a member allowing
// subagents, and a template reviewer that isn't.
func subagentsTeam(t *testing.T, runner *scriptedRunner) (*Loop, core.Member) {
	t.Helper()
	if !config.Supports("claude", config.UseSubagents) {
		t.Skip("no sandboxed subagents on this platform")
	}
	a, p, _ := loopApp(t, runner, "")
	ctx := context.Background()
	ada, err := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Ada", Kinds: []string{core.RoleImplementer}, Engine: "claude", Subagents: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetTeam(ctx, p.ID, TeamChoice{Template: "draft", Implementer: ada.ID}); err != nil {
		t.Fatal(err)
	}
	return a, ada
}

// A member who allows subagents has them in their task turns; a seat no
// member fills has none.
func TestAMembersSubagentsReachTheirTaskTurns(t *testing.T) {
	t.Parallel()
	runner := &scriptedRunner{reviews: []string{pass}}
	a, _ := subagentsTeam(t, runner)
	settle(t, a)
	writers := writerTurnsOf(runner)
	if len(writers) == 0 {
		t.Fatal("no writer turn")
	}
	if !runner.seen[writers[0]].Subagents {
		t.Fatal("the writer's turn had no subagents")
	}
	for _, spec := range runner.seen {
		if !spec.Write && spec.Subagents {
			t.Fatalf("a turn the member didn't take was given subagents: %+v", spec)
		}
	}
}

// The setting is read as each turn starts, so withdrawing it reaches the
// member's next turn; an engine whose sandbox can't start subagents never
// gets them, whatever the member allows; and the PM's decisions, which don't
// go through a task turn, never do.
func TestSubagentsFollowTheMemberAndTheEngine(t *testing.T) {
	t.Parallel()
	a, ada := subagentsTeam(t, &scriptedRunner{})
	task := core.Task{ID: "task", ProjectID: "project", Status: core.TaskWriting}
	turn := func(seat core.Role) bool {
		t.Helper()
		spec, cleanup, err := a.roleSpec(task, seat, t.TempDir(), true, docsMedium{}, "go", nil, core.RoleImplementer)
		if err != nil {
			t.Fatal(err)
		}
		cleanup()
		return spec.Subagents
	}
	seat := core.Role{Name: "Ada", Kinds: []string{core.RoleImplementer}, Engine: "claude", Member: ada.ID}
	if !turn(seat) {
		t.Fatal("allowed, the turn had no subagents")
	}
	if a.baseSpec(seat, t.TempDir(), "decide").Subagents {
		t.Fatal("a PM-style turn was given subagents")
	}
	if turn(core.Role{Name: "Writer", Kinds: []string{core.RoleImplementer}, Engine: "claude"}) {
		t.Fatal("a template seat was given subagents")
	}
	for _, engine := range []string{"grok", "openai-compatible"} {
		other := seat
		other.Engine = engine
		if config.Supports(engine, config.UseSubagents) {
			t.Fatalf("%s offers subagents", engine)
		}
		if a.memberSubagents(other) {
			t.Fatalf("%s was given subagents", engine)
		}
	}
	if _, err := a.Core.SaveMember(context.Background(), ada.ID, core.MemberInput{Name: "Ada", Kinds: []string{core.RoleImplementer}, Engine: "claude"}); err != nil {
		t.Fatal(err)
	}
	if turn(seat) {
		t.Fatal("withdrawn, the turn still had subagents")
	}
}
