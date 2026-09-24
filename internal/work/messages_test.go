package work

import (
	"context"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
)

func TestTheWriterRevisesWithAMessageInsteadOfAwaitingApproval(t *testing.T) {
	runner := &scriptedRunner{reviews: []string{pass, pass}}
	a, p, _ := loopApp(t, runner, "")
	task := settle(t, a)
	openDecision(t, a, task)
	m, err := a.MessageTeam(context.Background(), p.ID, task.ID, "Writer", core.FromOwner, "Mention the launch party")
	if err != nil {
		t.Fatal(err)
	}
	task = settle(t, a)
	if openDecision(t, a, task).Kind != core.DecisionDelivery || len(task.Revisions) != 2 {
		t.Fatalf("expected a second draft to approve: %+v", task)
	}
	if writer := runner.seen[2]; !writer.Write || !strings.Contains(writer.Prompt, "Mention the launch party") {
		t.Fatal("the message did not reach the writer")
	}
	if got := task.Messages[0]; got.ID != m.ID || got.Status != core.MessageAnswered || got.Revision != 2 || got.Reply != "Wrote the note." {
		t.Fatalf("the message was not answered by draft 2: %+v", got)
	}
}

func TestAMessageSentMidTurnIsNotDroppedWhenTheReviewPasses(t *testing.T) {
	runner := &scriptedRunner{reviews: []string{pass, pass}}
	var a *Loop
	var p core.Project
	var task core.Task
	sent := false
	runner.onWriter = func(string) {
		if sent {
			return
		}
		sent = true
		if _, err := a.MessageTeam(context.Background(), p.ID, task.ID, "implementer", core.FromAssistant, "Sign it from the whole team"); err != nil {
			panic(err)
		}
	}
	a, p, task = loopApp(t, runner, "")
	task = settle(t, a)
	if openDecision(t, a, task).Kind != core.DecisionDelivery || len(task.Revisions) != 2 {
		t.Fatalf("the passing draft went to approval without the message: %+v", task)
	}
	if got := task.Messages[0]; got.Status != core.MessageAnswered || got.Revision != 2 {
		t.Fatalf("draft 1 claimed a message it never saw: %+v", got)
	}
	if task.DirectionPending != 0 {
		t.Fatalf("direction still pending: %d", task.DirectionPending)
	}
}

func TestAskingTheReviewerChecksTheLatestDraftNow(t *testing.T) {
	runner := &scriptedRunner{reviews: []string{pass}}
	a, p, task := loopApp(t, runner, "")
	ctx := context.Background()
	// The first draft is written; the task now waits for its review.
	if _, err := a.loopStep(ctx, false); err != nil {
		t.Fatal(err)
	}
	m, err := a.MessageTeam(ctx, p.ID, task.ID, "reviewer", core.FromOwner, "Is it warm enough?")
	if err != nil {
		t.Fatal(err)
	}
	task = settle(t, a)
	if openDecision(t, a, task).Kind != core.DecisionDelivery {
		t.Fatalf("task %+v", task)
	}
	if len(runner.seen) != 2 || !strings.Contains(runner.seen[1].Prompt, "Is it warm enough?") {
		t.Fatalf("the review was not the one asked for: %d turns", len(runner.seen))
	}
	if got := task.Messages[0]; got.Status != core.MessageAnswered || got.Outcome != core.VerdictPass || got.Reply != "Meets the brief." {
		t.Fatalf("reply %+v", got)
	}
	if len(task.Verdicts) != 1 || task.Verdicts[0].Asked != m.ID {
		t.Fatalf("the asked-for review did not count: %+v", task.Verdicts)
	}

	// Asked again once approval is waiting, a check that fails is put in
	// front of the owner rather than counted.
	runner.reviews = []string{revise}
	if _, err = a.MessageTeam(ctx, p.ID, task.ID, "Reviewer", core.FromOwner, "Look again"); err != nil {
		t.Fatal(err)
	}
	task = settle(t, a)
	d := openDecision(t, a, task)
	if len(task.Verdicts) != 1 || !strings.Contains(d.Context, "Too formal.") {
		t.Fatalf("the failed check should be on the approval, not counted: %+v / %q", task.Verdicts, d.Context)
	}
}
