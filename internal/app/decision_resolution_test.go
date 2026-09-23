package app

import (
	"context"
	"net/http"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
)

func TestDismissedWorkerDecisionIsNeverDeliveredAsAnswer(t *testing.T) {
	calls := 0
	a, _ := runtimeFixture(t, func(w http.ResponseWriter, r *http.Request) { calls++; t.Error("dismissal contacted worker") })
	ctx := context.Background()
	ag := commission(t, a, "")
	if _, err := a.Core.BeginDispatch(ctx, ag.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.Core.MarkDispatched(ctx, ag.ID, "external"); err != nil {
		t.Fatal(err)
	}
	d, err := a.Core.CreateDecision(ctx, core.DecisionInput{ProjectID: ag.ProjectID, AgentID: ag.ID, Title: "Old setup question", Context: "Runtime is now configured", Recommendation: "Set up", Choices: []string{"Set up", "Wait"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Core.DismissDecision(ctx, d.ID, "Setup already completed"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = a.propagateDecisions(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 0 {
		t.Fatal("dismissal resumed worker")
	}
}
