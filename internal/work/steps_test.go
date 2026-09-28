package work

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shhac/lib-agent-harness/session"

	"github.com/shhac/crew-assistant/internal/core"
)

// A turn on a task is kept as the steps the owner reads in its member's
// panel: the prompt it was given, its replies as they stand, and each tool
// with what went in and what came out. A reply streaming in shows as it
// grows; a tool the turn never saw finish is marked stopped.
func TestATurnKeepsItsStepsForTheOwner(t *testing.T) {
	ctx := context.Background()
	a := testLoop(t)
	p, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Service", Brief: core.BriefInput{Goal: "Faster"}, Template: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	task, _ := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Cache the lookups"})
	steps := func() []core.TurnStep {
		t.Helper()
		got, err := a.Core.TurnSteps(ctx, p.ID, task.ID, "Ada")
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	watch := a.watchTurn(task, core.RoleImplementer, core.Role{Name: "Ada", Member: "m1"}, t.TempDir(), false)
	watch.Started()
	watch.Asked("Cache the lookups, keyed by tenant")
	watch.Saw(session.Event{Kind: "text_delta", ItemID: "msg-1", Text: "Looking at "})
	if got := steps(); len(got) != 2 || got[1].Kind != core.StepReply || got[1].Text != "Looking at " {
		t.Fatalf("a reply that has begun isn't showing: %+v", got)
	}
	watch.Saw(session.Event{Kind: "text_delta", ItemID: "msg-1", Text: "the store."})
	exit := 1
	watch.Saw(session.Event{Kind: "tool_started", ItemID: "call-1", Tool: "Bash", Input: json.RawMessage(`{"command":"go test ./..."}`)})
	watch.Saw(session.Event{Kind: "tool_completed", ItemID: "call-1", Status: "failed", Output: "FAIL store", ExitCode: &exit, OutputTruncated: true})
	watch.Saw(session.Event{Kind: "tool_started", ItemID: "call-2", Tool: "Read", Input: json.RawMessage(`{"file_path":"store.go"}`)})
	watch.Saw(session.Event{Kind: "text", ItemID: "msg-2", Text: "Done."})
	watch.Ended()

	got := steps()
	if len(got) != 5 {
		t.Fatalf("steps %+v", got)
	}
	prompt, reply, bash, read, done := got[0], got[1], got[2], got[3], got[4]
	if prompt.Kind != core.StepPrompt || prompt.Text != "Cache the lookups, keyed by tenant" || prompt.Member != "m1" || prompt.Role != core.RoleImplementer {
		t.Fatalf("prompt %+v", prompt)
	}
	if reply.Text != "Looking at the store." || reply.Turn != prompt.Turn {
		t.Fatalf("reply %+v", reply)
	}
	if bash.Tool != "Bash" || bash.Input != `{"command":"go test ./..."}` || bash.Status != "failed" || bash.Output != "FAIL store" || bash.ExitCode == nil || *bash.ExitCode != 1 || !bash.Clipped {
		t.Fatalf("bash %+v", bash)
	}
	if read.Tool != "Read" || read.Status != "interrupted" {
		t.Fatalf("a tool never seen finishing: %+v", read)
	}
	if done.Kind != core.StepReply || done.Text != "Done." {
		t.Fatalf("last reply %+v", done)
	}

	// A turn that has to ask again runs afresh, and its steps follow on.
	watch.Started()
	watch.Asked("Try again")
	watch.Ended()
	if got := steps(); len(got) != 6 || got[5].Text != "Try again" || got[5].Turn == prompt.Turn {
		t.Fatalf("the second run's steps %+v", got[5:])
	}
}

// A turn on no task, such as the PM ordering the list, keeps nothing.
func TestATurnOnNoTaskKeepsNoSteps(t *testing.T) {
	a := testLoop(t)
	kept := 0
	watch := a.watchTurn(core.Task{ProjectID: "p"}, core.RolePM, core.Role{Name: "Pia"}, t.TempDir(), false).(*liveTurn)
	watch.steps.keep = func(core.TurnStep) { kept++ }
	watch.Started()
	watch.Asked("Order the list")
	watch.Saw(session.Event{Kind: "text", ItemID: "1", Text: "Ordered."})
	watch.Ended()
	if kept != 0 {
		t.Fatalf("kept %d steps", kept)
	}
}
