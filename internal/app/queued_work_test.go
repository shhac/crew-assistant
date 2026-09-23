package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/engine"
)

func TestQueuedOutcomeStartsAfterAcceptanceWithoutRecommissioning(t *testing.T) {
	ctx := context.Background()
	a := testApp(t)
	p, first := workItemFixture(t, a)
	cfg := a.Config()
	cfg.Workers = []config.Worker{{ID: "fake", ProjectID: p.ID, Endpoint: "http://127.0.0.1:1", Capabilities: []string{"implement"}}}
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	original, err := a.Core.Delegate(ctx, core.DelegateInput{ProjectID: p.ID, WorkItemID: first.ID, ProfileID: "fake", Role: "worker", Task: "First", AcceptanceCriteria: "Checked"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Core.BeginDispatch(ctx, original.ID); err != nil {
		t.Fatal(err)
	}
	if err = a.Core.MarkDispatched(ctx, original.ID, "run-first"); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Core.UpdateAgent(ctx, original.ID, core.AgentUpdate{Status: "completed", Summary: "Checked", Evidence: []string{"Fixture passes"}, ExternalID: "run-first"}); err != nil {
		t.Fatal(err)
	}
	next, err := a.Core.QueueWorkItem(ctx, core.WorkItemInput{ProjectID: p.ID, AfterWorkItemID: first.ID, Title: "Next", Objective: "Next outcome", AcceptanceCriteria: "Next check passes"})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			args, _ := json.Marshal(engine.DelegateArgs{ProjectID: p.ID, WorkItemID: next.ID, WorkerProfile: "fake", Role: "worker", Objective: next.Objective, AcceptanceCriteria: []string{next.AcceptanceCriteria}})
			json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "commission", "type": "function", "function": map[string]any{"name": "delegate", "arguments": string(args)}}}}}}})
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"Next outcome commissioned."}}]}`))
	}))
	defer provider.Close()
	cfg = a.Config()
	cfg.Model.Model = "fixture"
	cfg.Model.BaseURL = provider.URL
	cfg.Model.APIKeyEnv = ""
	if err = a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if err = a.commissionQueuedWork(ctx); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("commissioned before acceptance")
	}
	snap, _ := a.Core.Snapshot(ctx)
	for _, item := range snap.WorkItems {
		if item.ID == first.ID {
			_, err = a.Core.AcceptWorkItem(ctx, first.ID, item.ReviewRevision, []string{"Reviewed fixture"}, "owner")
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = a.commissionQueuedWork(ctx); err != nil {
			t.Fatal(err)
		}
	}
	snap, _ = a.Core.Snapshot(ctx)
	if calls != 2 || len(snap.Agents) != 2 || snap.Agents[1].WorkItemID != next.ID {
		t.Fatal("queued work lost or duplicated", calls, snap.Agents)
	}
}

func TestQueuedExecutorCannotCrossScopeOrOutrunWithdrawal(t *testing.T) {
	ctx := context.Background()
	a := testApp(t)
	p, first := workItemFixture(t, a)
	next, err := a.Core.QueueWorkItem(ctx, core.WorkItemInput{ProjectID: p.ID, AfterWorkItemID: first.ID, Title: "Next", Objective: "Next", AcceptanceCriteria: "Check"})
	if err != nil {
		t.Fatal(err)
	}
	scope := queuedWorkExecutor{projectExecutor{app: a, projectID: p.ID, workItemID: next.ID}}
	for _, name := range []string{"create_work_item", "queue_work_item", "unqueue_work_item", "accept_work_item", "configure_worker", "message_agent"} {
		if _, err = scope.Execute(ctx, name, json.RawMessage(`{}`)); err == nil {
			t.Fatalf("queued turn gained %s", name)
		}
	}
	if _, err = scope.Execute(ctx, "prepare_worker", json.RawMessage(`{"project_id":"elsewhere","workspace":""}`)); err == nil {
		t.Fatal("cross-project setup allowed")
	}
	if _, err = a.Core.CancelQueuedWorkItem(ctx, next.ID); err != nil {
		t.Fatal(err)
	}
	cfg := a.Config()
	cfg.Workers = []config.Worker{{ID: "fake", Endpoint: "http://127.0.0.1:1", Capabilities: []string{"implement"}}}
	if err = a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(engine.DelegateArgs{ProjectID: p.ID, WorkItemID: next.ID, WorkerProfile: "fake", Role: "worker", Objective: "Next", AcceptanceCriteria: []string{"Check"}})
	if _, err = scope.Execute(ctx, "delegate", args); err == nil {
		t.Fatal("withdrawn queue turn commissioned work")
	}
	snap, _ := a.Core.Snapshot(ctx)
	if len(snap.Agents) != 0 {
		t.Fatal("unexpected assignment")
	}
}

func readyQueuedOutcome(t *testing.T, a *App) (core.Project, core.WorkItem) {
	t.Helper()
	ctx := context.Background()
	p, first := workItemFixture(t, a)
	cfg := a.Config()
	cfg.Workers = []config.Worker{{ID: "fake", ProjectID: p.ID, Endpoint: "http://127.0.0.1:1", Capabilities: []string{"implement"}}}
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	original, err := a.Core.Delegate(ctx, core.DelegateInput{ProjectID: p.ID, WorkItemID: first.ID, ProfileID: "fake", Role: "worker", Task: "First", AcceptanceCriteria: "Checked"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Core.BeginDispatch(ctx, original.ID); err != nil {
		t.Fatal(err)
	}
	if err = a.Core.MarkDispatched(ctx, original.ID, "run-first"); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Core.UpdateAgent(ctx, original.ID, core.AgentUpdate{Status: "completed", Summary: "Checked", Evidence: []string{"Fixture passes"}, ExternalID: "run-first"}); err != nil {
		t.Fatal(err)
	}
	snap, err := a.Core.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range snap.WorkItems {
		if item.ID == first.ID {
			if _, err = a.Core.AcceptWorkItem(ctx, first.ID, item.ReviewRevision, []string{"Reviewed fixture"}, "owner"); err != nil {
				t.Fatal(err)
			}
		}
	}
	next, err := a.Core.QueueWorkItem(ctx, core.WorkItemInput{ProjectID: p.ID, AfterWorkItemID: first.ID, Title: "Next", Objective: "Next outcome", AcceptanceCriteria: "Next check passes"})
	if err != nil {
		t.Fatal(err)
	}
	return p, next
}

func TestQueuedNoEffectBecomesVisibleDecisionAndRetriesOnlyAfterResolution(t *testing.T) {
	ctx := context.Background()
	a := testApp(t)
	p, next := readyQueuedOutcome(t, a)
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"I will get to this later."}}]}`))
		case 2:
			raw, _ := json.Marshal(engine.DelegateArgs{ProjectID: p.ID, WorkItemID: next.ID, WorkerProfile: "fake", Role: "worker", Objective: next.Objective, AcceptanceCriteria: []string{next.AcceptanceCriteria}})
			json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "retry-commission", "type": "function", "function": map[string]any{"name": "delegate", "arguments": string(raw)}}}}}}})
		default:
			w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"Commissioned."}}]}`))
		}
	}))
	defer provider.Close()
	cfg := a.Config()
	cfg.Model.Model = "fixture"
	cfg.Model.BaseURL = provider.URL
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := a.commissionQueuedWork(ctx); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := a.Core.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || len(snap.Decisions) != 1 || snap.Decisions[0].WorkItemID != next.ID || snap.Decisions[0].Status != "open" {
		t.Fatalf("no-op wasn't bounded by a visible decision: calls=%d decisions=%+v", calls.Load(), snap.Decisions)
	}
	for _, item := range snap.WorkItems {
		if item.ID == next.ID && item.Status != "blocked" {
			t.Fatalf("no-op outcome status %s", item.Status)
		}
	}
	if _, err = a.Core.ResolveDecision(ctx, snap.Decisions[0].ID, "Retry using the current setup"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = a.commissionQueuedWork(ctx); err != nil {
			t.Fatal(err)
		}
	}
	snap, err = a.Core.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 || len(snap.Agents) != 2 || snap.Agents[1].WorkItemID != next.ID || len(snap.Decisions) != 1 {
		t.Fatalf("resolved queue did not progress exactly once: calls=%d agents=%+v decisions=%+v", calls.Load(), snap.Agents, snap.Decisions)
	}
}

func TestQueuedMissingModelBecomesActionableWithoutInferenceLoop(t *testing.T) {
	ctx := context.Background()
	a := testApp(t)
	_, next := readyQueuedOutcome(t, a)
	for i := 0; i < 3; i++ {
		if err := a.commissionQueuedWork(ctx); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := a.Core.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Decisions) != 1 || !strings.Contains(snap.Decisions[0].Context, "Settings") || snap.Decisions[0].WorkItemID != next.ID {
		t.Fatalf("missing setup isn't actionable: %+v", snap.Decisions)
	}
	if len(snap.ModelCalls) != 0 {
		t.Fatal("unavailable model consumed model allowance")
	}
	for _, item := range snap.WorkItems {
		if item.ID == next.ID && item.Status != "blocked" {
			t.Fatalf("unavailable model left status %s", item.Status)
		}
	}
	// Completing the once marker is safe because the prepared decision is the
	// durable recovery route, not a lost silent ready item.
	pending, err := a.Core.PendingEvents(ctx)
	if err != nil || len(pending) != 0 {
		t.Fatalf("missing model left ambiguous operation: %v %v", pending, err)
	}
}

func TestQueueFallbackDoesNotDuplicateModelDecision(t *testing.T) {
	ctx := context.Background()
	a := testApp(t)
	p, next := readyQueuedOutcome(t, a)
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			raw, _ := json.Marshal(engine.DecisionArgs{ProjectID: p.ID, WorkItemID: next.ID, Question: "Choose fixture scope", Why: "Two valid implementations", Recommendation: "A", Options: []string{"A", "B"}})
			json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "prepared-choice", "type": "function", "function": map[string]any{"name": "ask_decision", "arguments": string(raw)}}}}}}})
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"A decision is ready."}}]}`))
	}))
	defer provider.Close()
	cfg := a.Config()
	cfg.Model.Model = "fixture"
	cfg.Model.BaseURL = provider.URL
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := a.commissionQueuedWork(ctx); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := a.Core.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || len(snap.Decisions) != 1 || snap.Decisions[0].Title != "Choose fixture scope" {
		t.Fatalf("fallback duplicated an actual prepared decision: %+v", snap.Decisions)
	}
}

func TestQueueFallbackDoesNotBlockWithdrawnOutcome(t *testing.T) {
	ctx := context.Background()
	a := testApp(t)
	_, next := readyQueuedOutcome(t, a)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := a.Core.CancelQueuedWorkItem(ctx, next.ID); err != nil {
			t.Error(err)
		}
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"No worker started."}}]}`))
	}))
	defer provider.Close()
	cfg := a.Config()
	cfg.Model.Model = "fixture"
	cfg.Model.BaseURL = provider.URL
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if err := a.commissionQueuedWork(ctx); err != nil {
		t.Fatal(err)
	}
	snap, err := a.Core.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Decisions) != 0 || len(snap.Agents) != 1 {
		t.Fatalf("withdrawal gained a fallback decision or assignment: %+v %+v", snap.Decisions, snap.Agents)
	}
}

func TestQueuedExecutorRechecksWithdrawalAfterPrerequisiteAccepted(t *testing.T) {
	ctx := context.Background()
	a := testApp(t)
	p, next := readyQueuedOutcome(t, a)
	scope := queuedWorkExecutor{projectExecutor{app: a, projectID: p.ID, workItemID: next.ID}}
	if _, err := a.Core.CancelQueuedWorkItem(ctx, next.ID); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(engine.DelegateArgs{ProjectID: p.ID, WorkItemID: next.ID, WorkerProfile: "fake", Role: "worker", Objective: next.Objective, AcceptanceCriteria: []string{next.AcceptanceCriteria}})
	if _, err := scope.Execute(ctx, "delegate", raw); err == nil || !strings.Contains(err.Error(), "withdrawn") {
		t.Fatalf("stale queue executor missed withdrawn authorization: %v", err)
	}
	snap, err := a.Core.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Agents) != 1 {
		t.Fatalf("stale queued turn started work: %+v", snap.Agents)
	}
}
