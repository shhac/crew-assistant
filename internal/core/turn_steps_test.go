package core

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// A seat's steps on a task come back in the order they began, with a step
// its turn reports again, such as a tool finishing, changed in place. Another
// seat's steps and another task's stay apart.
func TestTurnStepsAreKeptPerTaskAndSeatAndChangeInPlace(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	task, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Cache the lookups"})
	if err != nil {
		t.Fatal(err)
	}
	other, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Something else"})
	keep := func(step TurnStep) {
		t.Helper()
		if err := s.RecordTurnStep(testContext, step); err != nil {
			t.Fatal(err)
		}
	}
	ada := TurnStep{TaskID: task.ID, Seat: "Ada", Member: "m1", Role: RoleImplementer, Turn: "run-1"}
	prompt, tool := ada, ada
	prompt.Item, prompt.Kind, prompt.Text = "prompt", StepPrompt, "Cache the lookups"
	tool.Item, tool.Kind, tool.Tool, tool.Input, tool.Status = "call-1", StepTool, "Bash", `{"command":"go test ./..."}`, "running"
	keep(prompt)
	keep(tool)
	tool.Status, tool.Output = "completed", "ok"
	keep(tool)
	rune := TurnStep{TaskID: task.ID, Seat: "Rune", Role: RoleReviewer, Turn: "run-2", Item: "prompt", Kind: StepPrompt, Text: "Check it"}
	keep(rune)
	elsewhere := ada
	elsewhere.TaskID, elsewhere.Turn, elsewhere.Item, elsewhere.Kind = other.ID, "run-3", "prompt", StepPrompt
	keep(elsewhere)

	got, err := s.TurnSteps(testContext, p.ID, task.ID, "Ada")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Kind != StepPrompt || got[1].Tool != "Bash" || got[1].Status != "completed" || got[1].Output != "ok" || got[1].Input != tool.Input || got[0].ID >= got[1].ID {
		t.Fatalf("steps %+v", got)
	}
	if got, _ := s.TurnSteps(testContext, p.ID, task.Ref, "Rune"); len(got) != 1 || got[0].Text != "Check it" {
		t.Fatalf("by readable ID, Rune's steps %+v", got)
	}
	if _, err := s.TurnSteps(testContext, "another-project", task.ID, "Ada"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a task asked for under another project: %v", err)
	}
	if err := s.RecordTurnStep(testContext, TurnStep{TaskID: task.ID, Seat: "Ada"}); err == nil {
		t.Fatal("a step with no turn or item was kept")
	}
}

// Each step is cut to its first part, saying so, and a task keeps only its
// newest steps.
func TestTurnStepsAreBounded(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	task, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Cache the lookups"})
	long := TurnStep{TaskID: task.ID, Seat: "Ada", Turn: "run", Item: "call", Kind: StepTool, Tool: "Bash", Input: strings.Repeat("i", maxStepInput*2), Output: strings.Repeat("o", maxStepOutput*2)}
	if err := s.RecordTurnStep(testContext, long); err != nil {
		t.Fatal(err)
	}
	got, _ := s.TurnSteps(testContext, p.ID, task.ID, "Ada")
	if len(got) != 1 || !got[0].Clipped || len(got[0].Input) > maxStepInput+4 || len(got[0].Output) > maxStepOutput+4 {
		t.Fatalf("a long step kept %d and %d bytes, clipped %v", len(got[0].Input), len(got[0].Output), got[0].Clipped)
	}
	for i := range maxTaskSteps + 5 {
		step := TurnStep{TaskID: task.ID, Seat: "Ada", Turn: "run", Item: fmt.Sprint(i), Kind: StepReply, Text: fmt.Sprint("reply ", i)}
		if err := s.RecordTurnStep(testContext, step); err != nil {
			t.Fatal(err)
		}
	}
	got, _ = s.TurnSteps(testContext, p.ID, task.ID, "Ada")
	if len(got) != maxTaskSteps || got[0].Text != "reply 5" || got[len(got)-1].Text != fmt.Sprint("reply ", maxTaskSteps+4) {
		t.Fatalf("kept %d steps, from %q", len(got), got[0].Text)
	}
	for i := range 3 * maxTaskBytes / maxStepText {
		step := TurnStep{TaskID: task.ID, Seat: "Ada", Turn: "big", Item: fmt.Sprint(i), Kind: StepReply, Text: strings.Repeat("r", maxStepText)}
		if err := s.RecordTurnStep(testContext, step); err != nil {
			t.Fatal(err)
		}
	}
	got, _ = s.TurnSteps(testContext, p.ID, task.ID, "Ada")
	size := 0
	for _, step := range got {
		size += len(step.Text)
	}
	if size > maxTaskBytes || size < maxTaskBytes/2 {
		t.Fatalf("a task kept %d bytes of steps", size)
	}
}
