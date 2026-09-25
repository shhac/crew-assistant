package app

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
)

// The assistant passes the owner's words and a picked choice separately, so
// words that spell a choice never act as one.
func TestResolveDecisionKeepsChoicesAndWordsApart(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	open := func() core.Decision {
		d, err := a.Core.CreateDecision(ctx, core.DecisionInput{Title: "Ship?", Context: "Ready", Recommendation: "Approve", Choices: []string{"Approve", "Stop"}})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	resolve := func(d core.Decision, choice, answer string) (core.Decision, error) {
		raw, _ := json.Marshal(map[string]string{"decision_id": d.ID, "choice": choice, "answer": answer})
		out, err := a.Execute(ctx, "resolve_decision", raw)
		if err != nil {
			return core.Decision{}, err
		}
		return out.(core.Decision), nil
	}
	if got, err := resolve(open(), "", "Approve"); err != nil || got.Disposition != core.DispositionCustom {
		t.Fatalf("words recorded as %+v, %v", got, err)
	}
	if got, err := resolve(open(), "Approve", ""); err != nil || got.Disposition != core.DispositionChoice {
		t.Fatalf("choice recorded as %+v, %v", got, err)
	}
	if _, err := resolve(open(), "Approve", "and make it quick"); err == nil {
		t.Fatal("both a choice and an answer were accepted")
	}
}

func TestTheAssistantReadsOneTaskInFullWithinItsProject(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	p, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Notes", Template: "draft", Brief: core.BriefInput{Goal: "A note", Criteria: []string{"Short"}}})
	if err != nil {
		t.Fatal(err)
	}
	task, _ := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Write it"})
	read := func(projectID string) (any, error) {
		raw, _ := json.Marshal(map[string]string{"project_id": projectID, "task_id": task.ID})
		return a.Execute(ctx, "read_task", raw)
	}
	if out, err := read(p.ID); err != nil || out.(core.Task).Objective != "Write it" {
		t.Fatalf("read %+v, %v", out, err)
	}
	if _, err := read("another-project"); err == nil {
		t.Fatal("a task was read through the wrong project")
	}
}

// An open decision is sent to the owner once: claimed before sending, never
// sent again, and a send that failed stays pending for inspection rather than
// being tried again blind.
func TestEachOpenDecisionIsSentToTheOwnerOnce(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	open := func(title string) core.Decision {
		d, err := a.Core.CreateDecision(ctx, core.DecisionInput{Title: title, Context: "Ready", Recommendation: "Approve", Choices: []string{"Approve", "Stop"}})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	first, failing := open("First"), open("Failing")
	var sent []string
	send := func(_ context.Context, text string) error {
		sent = append(sent, text)
		if strings.HasPrefix(text, "Failing") {
			return errors.New("slack is down")
		}
		return nil
	}
	a.notify(ctx, send)
	a.notify(ctx, send)
	if len(sent) != 2 || !slices.ContainsFunc(sent, func(s string) bool { return strings.HasPrefix(s, first.Title) }) {
		t.Fatalf("each decision should be sent once: %q", sent)
	}
	pending, _ := a.Core.PendingEvents(ctx)
	if len(pending) != 1 || pending[0] != "notify:decision:"+failing.ID {
		t.Fatalf("the failed send should stay pending: %v", pending)
	}
	snap, _ := a.Core.Snapshot(ctx)
	if !slices.ContainsFunc(snap.Activity, func(e core.Activity) bool { return e.Kind == "operation.interrupted" }) {
		t.Fatal("the failed send should be noted for inspection")
	}
}
