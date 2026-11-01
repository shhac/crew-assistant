package work

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/shhac/lib-agent-harness/sandbox"

	"github.com/shhac/crew-assistant/internal/core"
)

// A command that startup cannot reclaim holds only the task whose turn
// opened it: another task of the same project, another project and an owner
// answer all go on, and the command's directory is kept.
func TestAnUnreclaimableCommandHoldsOnlyItsOwnTask(t *testing.T) {
	lp, p, a := loopApp(t, &scriptedRunner{}, "")
	ctx := context.Background()
	if _, err := lp.SetParallel(ctx, p.ID, 3); err != nil {
		t.Fatal(err)
	}
	b := queue(t, lp, p, "Second note")
	other, err := lp.Core.CreateProject(ctx, core.ProjectInput{Title: "Independent work", Template: "draft", Brief: core.BriefInput{Goal: "Continue independently"}})
	if err != nil {
		t.Fatal(err)
	}
	c := queue(t, lp, other, "Write a note")
	d := queue(t, lp, other, "Answer me")
	// When the daemon stopped, A, B and C were each being checked.
	claims := map[string]core.Claim{}
	for _, task := range []core.Task{a, b, c} {
		claim, err := lp.Core.ClaimTask(ctx, task.ID, core.TaskReviewing)
		if err != nil {
			t.Fatal(err)
		}
		claims[task.ID] = claim
	}
	asked, err := lp.Core.OpenTaskDecision(ctx, d.ID, core.DecisionFailure, core.DecisionInput{Title: "Retry", Context: "It failed", Recommendation: choiceTryAgain, Choices: []string{choiceTryAgain, choiceStop}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lp.Core.ChooseDecision(ctx, asked.ID, choiceStop, core.FromOwner); err != nil {
		t.Fatal(err)
	}
	// A's check opened a command whose cleanup could not be confirmed.
	left := filepath.Join(lp.commandsHome(), "commands", "a-check")
	lp.commands = func(context.Context, sandbox.Options) (commandSandbox, error) {
		if err := os.MkdirAll(left, 0700); err != nil {
			t.Fatal(err)
		}
		return &fakeCommands{closeErr: errors.New("cleanup unavailable")}, nil
	}
	box, err := lp.openCommands(core.Fenced(ctx, a.ID, claims[a.ID].Token), sandbox.Options{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if box.Close() == nil {
		t.Fatal("cleanup was confirmed")
	}
	lp.commands = func(context.Context, sandbox.Options) (commandSandbox, error) {
		return &fakeCommands{closeErr: errors.New("cleanup unavailable")}, nil
	}
	if err := lp.resume(ctx); !errors.Is(err, errCommandRecovery) {
		t.Fatalf("recovery claimed the command was reclaimed: %v", err)
	}
	for i := 0; i < 5; i++ {
		step(t, lp)
	}
	if now := taskByID(t, lp, a.ID); !slices.ContainsFunc(now.Claims, func(c core.Claim) bool { return c.Token == claims[a.ID].Token && c.Held != "" }) {
		t.Fatalf("A was let go with its command unreclaimed: %+v", now.Claims)
	}
	for _, task := range []core.Task{b, c} {
		if now := taskByID(t, lp, task.ID); now.Status == core.TaskQueued || slices.ContainsFunc(now.Claims, func(c core.Claim) bool { return c.Held != "" }) {
			t.Fatalf("%s was held by A's command: %+v", task.Objective, now)
		}
	}
	if now := taskByID(t, lp, d.ID); now.Status != core.TaskStopped {
		t.Fatalf("the owner's answer was not settled: %+v", now)
	}
	if _, err := os.Stat(left); err != nil {
		t.Fatalf("the unreclaimed command was not kept: %v", err)
	}
}
