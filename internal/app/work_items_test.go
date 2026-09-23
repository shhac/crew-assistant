package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/engine"
	"github.com/shhac/crew-assistant/internal/integrations/worker"
)

func workItemFixture(t *testing.T, a *App) (core.Project, core.WorkItem) {
	t.Helper()
	ctx := context.Background()
	p, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Ongoing project"})
	if err != nil {
		t.Fatal(err)
	}
	item, err := a.Core.CreateWorkItem(ctx, core.WorkItemInput{ProjectID: p.ID, Title: "First outcome", Objective: "Validate a fixture", AcceptanceCriteria: "Recorded fixture checks pass"})
	if err != nil {
		t.Fatal(err)
	}
	return p, item
}
func TestWorkItemToolsAndContextStayScoped(t *testing.T) {
	ctx := context.Background()
	a := testApp(t)
	p, item := workItemFixture(t, a)
	other, otherItem := workItemFixture(t, a)
	_, err := a.Core.AddSteering(ctx, item.ID, "allowed-message", "Keep the fixture small")
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Core.AddSteering(ctx, otherItem.ID, "secret-message", "Unrelated direction")
	if err != nil {
		t.Fatal(err)
	}
	scope := projectExecutor{app: a, projectID: p.ID, workItemID: item.ID}
	raw, err := scope.context(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), other.ID) || strings.Contains(string(raw), otherItem.ID) || strings.Contains(string(raw), "Unrelated direction") {
		t.Fatal("unrelated work leaked", string(raw))
	}
	if !strings.Contains(string(raw), "allowed-message") {
		t.Fatal("work direction missing", string(raw))
	}
	for _, name := range []string{"create_work_item", "steer_work_item", "complete_project"} {
		payload, _ := json.Marshal(map[string]string{"project_id": p.ID, "work_item_id": item.ID})
		if _, err = scope.Execute(ctx, name, payload); err == nil {
			t.Fatalf("worker-triggered scope gained %s", name)
		}
	}
	payload, _ := json.Marshal(engine.SteerWorkItemArgs{ProjectID: p.ID, WorkItemID: otherItem.ID, MessageID: "cross", Message: "no"})
	if _, err = a.Execute(ctx, "steer_work_item", payload); err == nil {
		t.Fatal("steered unrelated work item")
	}
}
func TestWorkItemAcceptanceToolUsesRevisionAndLeavesProjectOpen(t *testing.T) {
	ctx := context.Background()
	a := testApp(t)
	p, item := workItemFixture(t, a)
	cfg := a.Config()
	cfg.Workers = []config.Worker{{ID: "fake", Endpoint: "http://127.0.0.1:1", Capabilities: []string{"implement"}}}
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	ag, err := a.Core.Delegate(ctx, core.DelegateInput{ProjectID: p.ID, WorkItemID: item.ID, ProfileID: "fake", Role: "worker", Task: "Validate fixture", AcceptanceCriteria: "Checks pass"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Core.BeginDispatch(ctx, ag.ID); err != nil {
		t.Fatal(err)
	}
	if err = a.Core.MarkDispatched(ctx, ag.ID, "run"); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Core.UpdateAgent(ctx, ag.ID, core.AgentUpdate{Status: "completed", Summary: "Checked", Evidence: []string{"Recorded check passed"}, ExternalID: "run"}); err != nil {
		t.Fatal(err)
	}
	snap, _ := a.Core.Snapshot(ctx)
	item = snap.WorkItems[0]
	payload, _ := json.Marshal(engine.AcceptWorkItemArgs{ProjectID: p.ID, WorkItemID: item.ID, ReviewRevision: "stale", Evidence: []string{"Inspected results"}})
	if _, err = a.Execute(ctx, "accept_work_item", payload); err == nil {
		t.Fatal("accepted stale evidence")
	}
	payload, _ = json.Marshal(engine.AcceptWorkItemArgs{ProjectID: p.ID, WorkItemID: item.ID, ReviewRevision: item.ReviewRevision, Evidence: []string{"Inspected results"}})
	if _, err = a.Execute(ctx, "accept_work_item", payload); err != nil {
		t.Fatal(err)
	}
	snap, _ = a.Core.Snapshot(ctx)
	if snap.WorkItems[0].Status != "accepted" || snap.Projects[0].Status == "completed" {
		t.Fatal("acceptance closed the ongoing project", snap)
	}
	next, _ := json.Marshal(engine.CreateWorkItemArgs{ProjectID: p.ID, Title: "Next outcome", Objective: "Next fixture", AcceptanceCriteria: []string{"Next checks pass"}})
	if _, err = a.Execute(ctx, "create_work_item", next); err != nil {
		t.Fatal("cannot define next work", err)
	}
}
func TestSteeringFanoutIsDurableAndExplicitAcknowledgementIsSeparate(t *testing.T) {
	ctx := context.Background()
	a := testApp(t)
	p, item := workItemFixture(t, a)
	deliveries := 0
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || !strings.HasSuffix(r.URL.Path, "/messages") {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var in struct {
			Message string `json:"message"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
		}
		if !strings.Contains(in.Message, "owner-message") || !strings.Contains(in.Message, "acknowledge_steering") {
			t.Error("missing durable steering", in.Message)
		}
		deliveries++
		_ = json.NewEncoder(w).Encode(worker.Run{ID: "run", Status: "running", Summary: "Working", UpdatedAt: time.Now()})
	}))
	defer broker.Close()
	cfg := a.Config()
	cfg.Workers = []config.Worker{{ID: "fake", Endpoint: broker.URL, Capabilities: []string{"implement"}}}
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	ag, err := a.Core.Delegate(ctx, core.DelegateInput{ProjectID: p.ID, WorkItemID: item.ID, ProfileID: "fake", Role: "worker", Task: "Check", AcceptanceCriteria: "Evidence"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Core.BeginDispatch(ctx, ag.ID); err != nil {
		t.Fatal(err)
	}
	if err = a.Core.MarkDispatched(ctx, ag.ID, "run"); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Core.AddSteering(ctx, item.ID, "owner-message", "Retain accessibility"); err != nil {
		t.Fatal(err)
	}
	if err = a.deliverSteering(ctx); err != nil {
		t.Fatal(err)
	}
	if err = a.deliverSteering(ctx); err != nil {
		t.Fatal(err)
	}
	snap, _ := a.Core.Snapshot(ctx)
	if deliveries != 1 || len(snap.SteeringReceipts) != 0 {
		t.Fatal("delivery replayed or falsely acknowledged", deliveries, snap.SteeringReceipts)
	}
	ag = snap.Agents[0]
	run := worker.Run{ID: "run", Status: "completed", Summary: "Finished", Evidence: []string{"Recorded checks"}, UpdatedAt: time.Now(), SteeringAcknowledgements: []string{"owner-message"}}
	if err = a.observeRun(ctx, ag, run, nil, false); err != nil {
		t.Fatal(err)
	}
	snap, _ = a.Core.Snapshot(ctx)
	if len(snap.SteeringReceipts) != 1 {
		t.Fatal("explicit acknowledgement lost")
	}
}

func TestAutomaticReviewAcceptsOnlyWorkItemAndDoesNotCloseProject(t *testing.T) {
	ctx := context.Background()
	a := testApp(t)
	p, item := workItemFixture(t, a)
	cfg := a.Config()
	cfg.Workers = []config.Worker{{ID: "fake", Endpoint: "http://127.0.0.1:1", Capabilities: []string{"implement"}}}
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	ag, err := a.Core.Delegate(ctx, core.DelegateInput{ProjectID: p.ID, WorkItemID: item.ID, ProfileID: "fake", Role: "worker", Task: "Check fixture", AcceptanceCriteria: "Evidence"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Core.BeginDispatch(ctx, ag.ID); err != nil {
		t.Fatal(err)
	}
	if err = a.Core.MarkDispatched(ctx, ag.ID, "run"); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Core.UpdateAgent(ctx, ag.ID, core.AgentUpdate{Status: "completed", Summary: "Finished", Evidence: []string{"Check passed"}, ExternalID: "run"}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			snapshot, _ := a.Core.Snapshot(ctx)
			current := snapshot.WorkItems[0]
			arguments, _ := json.Marshal(engine.AcceptWorkItemArgs{ProjectID: p.ID, WorkItemID: item.ID, ReviewRevision: current.ReviewRevision, Evidence: []string{"Inspected recorded check output"}})
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "accept", "type": "function", "function": map[string]any{"name": "accept_work_item", "arguments": string(arguments)}}}}}}})
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"The outcome is accepted. The project is ready for its next outcome."}}]}`))
	}))
	defer model.Close()
	cfg = a.Config()
	cfg.Model.Model = "fixture"
	cfg.Model.BaseURL = model.URL
	cfg.Model.APIKeyEnv = ""
	if err = a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"global", "project", "item"} {
		decisionInput := core.DecisionInput{Title: "Resolve before acceptance", Context: "Unknown expected behaviour", Recommendation: "Retain default", Choices: []string{"Retain", "Change"}}
		if scope != "global" {
			decisionInput.ProjectID = p.ID
		}
		if scope == "item" {
			decisionInput.WorkItemID = item.ID
		}
		decision, err := a.Core.CreateDecision(ctx, decisionInput)
		if err != nil {
			t.Fatal(err)
		}
		for repeat := 0; repeat < 2; repeat++ {
			if err = a.reviewFinished(ctx); err != nil {
				t.Fatal(err)
			}
		}
		if calls != 0 {
			t.Fatalf("%s unresolved decision triggered %d model calls", scope, calls)
		}
		if _, err = a.Core.ResolveDecision(ctx, decision.ID, "Retain"); err != nil {
			t.Fatal(err)
		}
	}
	if err = a.reviewFinished(ctx); err != nil {
		t.Fatal(err)
	}
	if err = a.reviewFinished(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := a.Core.Snapshot(ctx)
	if calls != 2 || snapshot.WorkItems[0].Status != "accepted" || snapshot.Projects[0].Status == "completed" {
		t.Fatal("automatic review did not accept exactly the work item", calls, snapshot)
	}
}
