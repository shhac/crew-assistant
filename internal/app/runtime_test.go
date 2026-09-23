package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/integrations/worker"
)

func runtimeFixture(t *testing.T, handler http.HandlerFunc) (*App, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	store, err := core.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	cfg := config.Default()
	cfg.Model.Engine = "openai-compatible"
	cfg.Model.Effort = ""
	cfg.Model.Model = ""
	cfg.Workers = []config.Worker{{ID: "fake", Name: "Fake broker", Endpoint: server.URL, Capabilities: []string{"coordinate", "implement"}}}
	return New(core.NewService(store, cfg), cfg, filepath.Join(t.TempDir(), "config.json"), false), server
}
func commission(t *testing.T, a *App, parent string) core.Agent {
	t.Helper()
	ctx := context.Background()
	snap, _ := a.Core.Snapshot(ctx)
	var p core.Project
	var err error
	if len(snap.Projects) == 0 {
		p, err = a.Core.CreateProject(ctx, core.ProjectInput{Title: "Fictional task", AcceptanceCriteria: "Verified output"})
	} else {
		p = snap.Projects[0]
	}
	if err != nil {
		t.Fatal(err)
	}
	ag, err := a.Core.Delegate(ctx, core.DelegateInput{ProjectID: p.ID, ParentID: parent, ProfileID: "fake", Role: "manager", Task: "Coordinate a bounded task", AcceptanceCriteria: "Verified output"})
	if err != nil {
		t.Fatal(err)
	}
	return ag
}
func writeRun(w http.ResponseWriter, run worker.Run) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(run)
}

func TestUncertainStartReconcilesWithoutDuplicate(t *testing.T) {
	ctx := context.Background()
	starts := 0
	run := worker.Run{}
	a, _ := runtimeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/runs" {
			starts++
			var req worker.StartRequest
			json.NewDecoder(r.Body).Decode(&req)
			run = worker.Run{ID: "external", DispatchKey: req.DispatchKey, Status: "running", Summary: "Working in an isolated workspace", UpdatedAt: time.Now().UTC()}
			http.Error(w, "response lost", 500)
			return
		}
		if r.Method == "GET" {
			writeRun(w, run)
			return
		}
		t.Errorf("unexpected request %s %s", r.Method, r.URL)
		w.WriteHeader(400)
	})
	ag := commission(t, a, "")
	if err := a.tick(ctx, false); err == nil {
		t.Fatal("expected uncertain start")
	}
	snap, _ := a.Core.Snapshot(ctx)
	if snap.Agents[0].Status != "reconciling" {
		t.Fatal(snap.Agents[0])
	}
	if err := a.tick(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err := a.tick(ctx, false); err != nil {
		t.Fatal(err)
	}
	snap, _ = a.Core.Snapshot(ctx)
	if starts != 1 || snap.Agents[0].ExternalID != "external" || snap.Agents[0].DispatchKey != ag.DispatchKey {
		t.Fatal(starts, snap.Agents[0])
	}
}
func TestNoDispatchCannotBeUndoneByUnpausing(t *testing.T) {
	calls := 0
	a, _ := runtimeFixture(t, func(w http.ResponseWriter, r *http.Request) { calls++; t.Error("unexpected broker request") })
	commission(t, a, "")
	ctx := context.Background()
	a.Core.SetPaused(ctx, true)
	a.Core.SetPaused(ctx, false)
	if err := a.tick(ctx, true); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal(calls)
	}
}
func TestMissingRunNeverStartsReplacement(t *testing.T) {
	ctx := context.Background()
	posts := 0
	a, _ := runtimeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			posts++
		}
		w.WriteHeader(404)
	})
	ag := commission(t, a, "")
	a.Core.BeginDispatch(ctx, ag.ID)
	for i := 0; i < 3; i++ {
		if err := a.tick(ctx, false); err == nil {
			t.Fatal("missing worker should be visible")
		}
	}
	if posts != 0 {
		t.Fatal("missing status started replacement")
	}
}
func TestUncertainResumePreservesSessionAndDoesNotRepeat(t *testing.T) {
	ctx := context.Background()
	resumes := 0
	run := worker.Run{ID: "saved-session", Status: "interrupted", Summary: "Process stopped", UpdatedAt: time.Now().UTC()}
	a, _ := runtimeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/resume") {
			resumes++
			if r.Header.Get("Idempotency-Key") == "" {
				t.Error("resume key missing")
			}
			w.WriteHeader(500)
			return
		}
		if r.Method == "GET" {
			writeRun(w, run)
			return
		}
		t.Errorf("unexpected %s", r.URL)
		w.WriteHeader(400)
	})
	ag := commission(t, a, "")
	run.DispatchKey = ag.DispatchKey
	a.Core.BeginDispatch(ctx, ag.ID)
	a.Core.MarkDispatched(ctx, ag.ID, run.ID)
	if err := a.tick(ctx, false); err == nil {
		t.Fatal("uncertain resume not reported")
	}
	for i := 0; i < 3; i++ {
		_ = a.tick(ctx, false)
	}
	snap, _ := a.Core.Snapshot(ctx)
	if resumes != 1 || snap.Agents[0].ExternalID != run.ID || snap.Agents[0].Status != "reconciling" {
		t.Fatal(resumes, snap.Agents[0])
	}
}
func TestStaleReportsDoNotRenewLiveness(t *testing.T) {
	ctx := context.Background()
	reported := time.Now().Add(-time.Hour).UTC()
	run := worker.Run{ID: "external", Status: "running", Summary: "Still working", UpdatedAt: reported}
	a, _ := runtimeFixture(t, func(w http.ResponseWriter, r *http.Request) { writeRun(w, run) })
	ag := commission(t, a, "")
	a.Core.BeginDispatch(ctx, ag.ID)
	a.Core.MarkDispatched(ctx, ag.ID, run.ID)
	for i := 0; i < 3; i++ {
		if err := a.tick(ctx, false); err != nil {
			t.Fatal(err)
		}
	}
	snap, _ := a.Core.Snapshot(ctx)
	if snap.Agents[0].Status != "reconciling" {
		t.Fatal("polling concealed silence")
	}
	n := 0
	for _, e := range snap.Activity {
		if e.Kind == "agent.reconciling" {
			n++
		}
	}
	if n != 1 {
		t.Fatal("stale report spam", n)
	}
}
func TestManagerDelegationIsDurablyDeduplicated(t *testing.T) {
	ctx := context.Background()
	messages := 0
	run := worker.Run{ID: "manager-session", Status: "waiting", Summary: "Waiting for an implementation worker", UpdatedAt: time.Now().UTC(), Delegation: &worker.DelegationRequest{RequestID: "child-request-1", WorkerProfile: "fake", Role: "worker", Task: "Implement in isolation", AcceptanceCriteria: "Tests pass", Capabilities: []string{"implement"}}}
	a, _ := runtimeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/messages") {
			messages++
		}
		writeRun(w, run)
	})
	ag := commission(t, a, "")
	a.Core.BeginDispatch(ctx, ag.ID)
	a.Core.MarkDispatched(ctx, ag.ID, run.ID)
	for i := 0; i < 2; i++ {
		state, _ := a.Core.Snapshot(ctx)
		current, _ := findAgent(state, ag.ID)
		if err := a.superviseAgent(ctx, current, false); err != nil {
			t.Fatal(err)
		}
	}
	snap, _ := a.Core.Snapshot(ctx)
	if len(snap.Agents) != 2 || snap.Agents[1].ParentID != ag.ID || messages != 1 {
		t.Fatal(len(snap.Agents), messages)
	}
	bad := worker.Instruction{RequestID: "bad", TargetAgentID: ag.ID, Message: "Do unrelated work"}
	if err := a.routeInstruction(ctx, snap.Agents[1], bad); err == nil {
		t.Fatal("child instructed its parent")
	}
}
func TestOwnerAnswerAndNotificationsDeliveredOnce(t *testing.T) {
	ctx := context.Background()
	sent := 0
	run := worker.Run{ID: "external", Status: "waiting", Summary: "Awaiting answer", UpdatedAt: time.Now().UTC()}
	a, _ := runtimeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/messages") {
			sent++
		}
		writeRun(w, run)
	})
	ag := commission(t, a, "")
	a.Core.BeginDispatch(ctx, ag.ID)
	a.Core.MarkDispatched(ctx, ag.ID, run.ID)
	d, err := a.Core.CreateDecision(ctx, core.DecisionInput{ProjectID: ag.ProjectID, AgentID: ag.ID, Title: "Choose audience", Context: "Needs focus", Recommendation: "Pilot", Choices: []string{"Pilot", "Everyone"}})
	if err != nil {
		t.Fatal(err)
	}
	notifications := 0
	for i := 0; i < 2; i++ {
		a.notify(ctx, func(context.Context, string) error { notifications++; return nil })
	}
	a.Core.ResolveDecision(ctx, d.ID, "Pilot")
	for i := 0; i < 2; i++ {
		if err := a.propagateDecisions(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if sent != 1 || notifications != 1 {
		t.Fatal(sent, notifications)
	}
}
func TestDemoNeverContactsAdapters(t *testing.T) {
	calls := 0
	a, _ := runtimeFixture(t, func(w http.ResponseWriter, r *http.Request) { calls++ })
	commission(t, a, "")
	a.Demo = true
	if err := a.tick(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if err := a.SyncLinear(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal(calls)
	}
}

func TestMonitoringDoesNotWakeWorkersOrConsumeQuestions(t *testing.T) {
	ctx := context.Background()
	posts := 0
	run := worker.Run{ID: "external", Status: "waiting", Summary: "Need an owner decision", UpdatedAt: time.Now().UTC(), Decision: &worker.Decision{RequestID: "question", Question: "Which audience?", Why: "Need focus", Recommendation: "Pilot", Options: []string{"Pilot", "All"}}}
	a, _ := runtimeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			posts++
		}
		writeRun(w, run)
	})
	ag := commission(t, a, "")
	a.Core.BeginDispatch(ctx, ag.ID)
	a.Core.MarkDispatched(ctx, ag.ID, run.ID)
	if err := a.tick(ctx, true); err != nil {
		t.Fatal(err)
	}
	snap, _ := a.Core.Snapshot(ctx)
	if posts != 0 || len(snap.Decisions) != 0 {
		t.Fatal("monitoring performed coordination", posts, len(snap.Decisions))
	}
	fresh, err := a.Core.ClaimEvent(ctx, "question:"+ag.ID+":question")
	if err != nil || !fresh {
		t.Fatal("monitoring consumed question receipt", err)
	}
}
func TestDeferredNoEffectReceiptCanRetry(t *testing.T) {
	a, _ := runtimeFixture(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("no broker call expected") })
	ctx := context.Background()
	n := 0
	for i := 0; i < 2; i++ {
		err := a.once(ctx, "deferred", func() error { n++; return ErrAssistantBusy })
		if err == nil {
			t.Fatal("missing deferred error")
		}
	}
	if n != 2 {
		t.Fatal("busy reasoning was permanently consumed")
	}
	pending, _ := a.Core.PendingEvents(ctx)
	if len(pending) != 0 {
		t.Fatal(pending)
	}
}
func TestChildEvidenceForwardedToParentOnce(t *testing.T) {
	ctx := context.Background()
	sends := 0
	run := worker.Run{ID: "manager", Status: "waiting", Summary: "Reviewing child evidence", UpdatedAt: time.Now().UTC()}
	a, _ := runtimeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/messages") {
			sends++
		}
		writeRun(w, run)
	})
	parent := commission(t, a, "")
	a.Core.BeginDispatch(ctx, parent.ID)
	a.Core.MarkDispatched(ctx, parent.ID, "manager")
	child := commission(t, a, parent.ID)
	a.Core.BeginDispatch(ctx, child.ID)
	a.Core.MarkDispatched(ctx, child.ID, "child")
	report := worker.Run{ID: "child", Status: "completed", Summary: "Tests passed", Evidence: []string{"Fictional acceptance report"}, UpdatedAt: time.Now().UTC()}
	for i := 0; i < 2; i++ {
		if err := a.forwardProgress(ctx, child, report); err != nil {
			t.Fatal(err)
		}
	}
	if sends != 1 {
		t.Fatal("duplicate parent progress", sends)
	}
}

func TestFreshHeartbeatsStillEscalateMissingProgressOnce(t *testing.T) {
	ctx := context.Background()
	a, _ := runtimeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("unconfigured model fallback must not call broker")
	})
	ag := commission(t, a, "")
	a.Core.BeginDispatch(ctx, ag.ID)
	a.Core.MarkDispatched(ctx, ag.ID, "external")
	old := time.Now().Add(-4 * time.Hour).UTC()
	a.Core.UpdateAgent(ctx, ag.ID, core.AgentUpdate{Status: "running", Summary: "Still working", ExternalID: "external", UpdatedAt: old})
	fresh := time.Now().UTC()
	a.Core.UpdateAgent(ctx, ag.ID, core.AgentUpdate{Status: "running", Summary: "Still working", ExternalID: "external", UpdatedAt: fresh})
	run := worker.Run{ID: "external", Status: "running", Summary: "Still working", UpdatedAt: fresh}
	for i := 0; i < 2; i++ {
		if err := a.checkProgress(ctx, ag, run); err != nil {
			t.Fatal(err)
		}
	}
	snap, _ := a.Core.Snapshot(ctx)
	if len(snap.Decisions) != 1 || snap.Decisions[0].AgentID != ag.ID {
		t.Fatal("fresh heartbeat hid stalled progress", snap.Decisions)
	}
}
