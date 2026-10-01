package work

import (
	"context"
	"strings"
	"testing"

	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/session"

	"github.com/shhac/crew-assistant/internal/core"
)

// browserTeam is a writing team whose writer is a member allowing the
// browser, and a template reviewer that isn't.
func browserTeam(t *testing.T, runner *scriptedRunner) (*Loop, core.Member) {
	t.Helper()
	a, p, _ := loopApp(t, runner, "")
	ctx := context.Background()
	ada, err := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Ada", Kinds: []string{core.RoleImplementer}, Engine: "claude", Browser: core.Browser{On: true, Name: "Work"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetTeam(ctx, p.ID, TeamChoice{Template: "draft", Implementer: ada.ID}); err != nil {
		t.Fatal(err)
	}
	return a, ada
}

func writerTurnsOf(runner *scriptedRunner) []int {
	var out []int
	for i, spec := range runner.seen {
		if spec.Write {
			out = append(out, i)
		}
	}
	return out
}

// A member who allows the browser has it in every turn they take, whatever
// the role, told what it is for; a seat no member fills has none.
func TestAMembersBrowserPolicyReachesTheirTurns(t *testing.T) {
	runner := &scriptedRunner{reviews: []string{pass}}
	a, _ := browserTeam(t, runner)
	settle(t, a)
	writers := writerTurnsOf(runner)
	if len(writers) == 0 {
		t.Fatal("no writer turn")
	}
	writer := runner.seen[writers[0]]
	if !writer.Browser || !strings.Contains(writer.Instructions, "only for controlling a browser") || !strings.Contains(writer.Instructions, `named "Work"`) {
		t.Fatalf("the writer's turn: browser %v, instructions %q", writer.Browser, writer.Instructions)
	}
	for _, spec := range runner.seen {
		if !spec.Write && spec.Browser {
			t.Fatalf("the template reviewer was given the browser: %+v", spec)
		}
	}
}

// The policy is read as each turn starts, so withdrawing it takes the
// browser from the member's next turn without re-seating anyone.
func TestWithdrawingTheBrowserReachesTheNextTurn(t *testing.T) {
	runner := &scriptedRunner{reviews: []string{pass}}
	a, ada := browserTeam(t, runner)
	seat := core.Role{Name: "Ada", Kinds: []string{core.RoleImplementer}, Engine: "claude", Member: ada.ID}
	if !a.baseSpec(seat, t.TempDir(), "go").Browser {
		t.Fatal("allowed, the turn had no browser")
	}
	if _, err := a.Core.SaveMember(context.Background(), ada.ID, core.MemberInput{Name: "Ada", Kinds: []string{core.RoleImplementer}, Engine: "claude"}); err != nil {
		t.Fatal(err)
	}
	if spec := a.baseSpec(seat, t.TempDir(), "go"); spec.Browser || strings.Contains(spec.Instructions, "browser") {
		t.Fatalf("withdrawn, the turn still had the browser: %+v", spec)
	}
}

// When Chrome isn't connected, a turn given the browser goes on without
// it, told so, rather than failing the task.
func TestATurnWhoseBrowserIsUnreachableGoesOnWithoutIt(t *testing.T) {
	runner := &scriptedRunner{reviews: []string{pass}, fail: []error{&session.CapabilityError{Engine: harness.Claude, Code: session.CapabilityBrowserToolsMissing, Phase: session.BeforeFirstPrompt}}}
	a, _ := browserTeam(t, runner)
	task := settle(t, a)
	writers := writerTurnsOf(runner)
	if len(writers) < 2 {
		t.Fatalf("the writer wasn't run again: %d turns", len(writers))
	}
	first, retried := runner.seen[writers[0]], runner.seen[writers[1]]
	if !first.Browser || retried.Browser || !strings.Contains(retried.Instructions, noBrowserNote) {
		t.Fatalf("first %v, retried %v %q", first.Browser, retried.Browser, retried.Instructions)
	}
	if len(task.Revisions) == 0 || task.Failures != 0 {
		t.Fatalf("the task should have its draft without a failure: %+v", task)
	}
}
