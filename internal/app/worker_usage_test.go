package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/integrations/worker"
	"github.com/shhac/lib-agent-harness/session"
)

func quotaFixture(used float64) session.QuotaSnapshot {
	observation := session.Observation{Quality: session.Measured, ObservedAt: time.Now()}
	return session.QuotaSnapshot{Observation: observation, Complete: true, Windows: []session.QuotaWindow{{Observation: observation, ID: "codex/primary", Scope: "codex", UsedPercent: &used}}}
}

type quotaManaged struct{ client *worker.Client }

func (f quotaManaged) Prepare(context.Context, string, string, config.Model) (*worker.Client, error) {
	return f.client, nil
}
func (f quotaManaged) Client(context.Context, string, string, config.Model) (*worker.Client, error) {
	return f.client, nil
}
func (f quotaManaged) Close() error { return nil }

func usageRuntimeFixture(t *testing.T, handler http.HandlerFunc) (*App, core.Agent) {
	t.Helper()
	a, server := runtimeFixture(t, handler)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.Core.CreateProject(context.Background(), core.ProjectInput{Title: "Usage protected", AcceptanceCriteria: "Verified output", Directories: []string{dir}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := a.Config()
	cfg.Workers[0] = config.Worker{ID: "fake", Name: "Project worker", Managed: true, ProjectID: p.ID, Workspace: dir, Capabilities: []string{"implement"}}
	if err = a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	client, err := worker.New(worker.Config{Endpoint: server.URL, Capabilities: []string{"implement"}})
	if err != nil {
		t.Fatal(err)
	}
	a.managed = quotaManaged{client}
	ag, err := a.Core.Delegate(context.Background(), core.DelegateInput{ProjectID: p.ID, ProfileID: "fake", Role: "worker", Task: "Bounded task", AcceptanceCriteria: "Verified output"})
	if err != nil {
		t.Fatal(err)
	}
	a.workerUsage.Inspect = func(context.Context, session.Options) (session.Inspection, error) {
		return session.Inspection{Quota: quotaFixture(90)}, nil
	}
	return a, ag
}
func clearQuotaCache(a *App) {
	a.workerUsage.Forget()
}

func TestQuotaHoldLeavesDispatchQueuedAndAutomaticallyRecovers(t *testing.T) {
	ctx := context.Background()
	starts := 0
	a, ag := usageRuntimeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/runs" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		starts++
		var request worker.StartRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		writeRun(w, worker.Run{ID: "run", DispatchKey: request.DispatchKey, Status: "running", Summary: "Working", UpdatedAt: time.Now()})
	})
	if err := a.tick(ctx, false); err != nil {
		t.Fatal(err)
	}
	snap, _ := a.Snapshot(ctx)
	if starts != 0 || snap.Agents[0].Status != "queued" || snap.Agents[0].ExternalID != "" || snap.Agents[0].DispatchKey != ag.DispatchKey {
		t.Fatal("hold altered dispatch", snap.Agents)
	}
	visible := false
	for _, status := range snap.Integrations {
		if status.ID == "worker-usage:fake" && status.Status == "paused" && strings.Contains(status.Detail, "90.0%") {
			visible = true
		}
	}
	if !visible {
		t.Fatal("hold missing from dashboard/model state", snap.Integrations)
	}
	a.workerUsage.Inspect = func(context.Context, session.Options) (session.Inspection, error) {
		return session.Inspection{Quota: quotaFixture(10)}, nil
	}
	clearQuotaCache(a)
	if err := a.tick(ctx, false); err != nil {
		t.Fatal(err)
	}
	if starts != 1 {
		t.Fatal("queued work did not restart", starts)
	}
}
func TestUsageDoesNotBlockObservationButHoldsResumeAndInstructions(t *testing.T) {
	ctx := context.Background()
	gets, posts := 0, 0
	a, ag := usageRuntimeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			gets++
			writeRun(w, worker.Run{ID: "saved", Status: "interrupted", Summary: "Stopped", UpdatedAt: time.Now()})
			return
		}
		posts++
		t.Error("new worker work dispatched at quota limit")
	})
	if _, err := a.Core.BeginDispatch(ctx, ag.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.Core.MarkDispatched(ctx, ag.ID, "saved"); err != nil {
		t.Fatal(err)
	}
	if err := a.tick(ctx, false); err != nil {
		t.Fatal(err)
	}
	snap, _ := a.Core.Snapshot(ctx)
	current := snap.Agents[0]
	if gets != 1 || posts != 0 || current.ResumeKey != "" || current.ExternalID != "saved" {
		t.Fatal("resume hold lost session or skipped reconciliation", current)
	}
	if _, err := a.SendAgent(ctx, current, "Continue"); err == nil {
		t.Fatal("manual instruction bypassed gate")
	}
	key := "retryable-instruction"
	err := a.once(ctx, key, func() error { err := a.sendInstruction(ctx, current, key, "Continue"); return err })
	if err == nil {
		t.Fatal("automatic instruction bypassed gate")
	}
	claimed, err := a.Core.ClaimEvent(ctx, key)
	if err != nil || !claimed {
		t.Fatal("quota hold consumed idempotency key", claimed, err)
	}
}
func TestUnavailableAndDisabledWorkerUsagePolicies(t *testing.T) {
	a, _ := usageRuntimeFixture(t, func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected broker call") })
	a.workerUsage.Inspect = func(context.Context, session.Options) (session.Inspection, error) {
		return session.Inspection{}, errors.New("unavailable")
	}
	ctx := context.Background()
	if err := a.workerUsageAllowed(ctx, "fake"); err != nil {
		t.Fatal("default unavailable should allow", err)
	}
	cfg := a.Config()
	cfg.Limits.WorkerUsage.OnUnavailable = "pause"
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if err := a.workerUsageAllowed(ctx, "fake"); err == nil {
		t.Fatal("pause policy ignored")
	}
	cfg.Limits.WorkerUsage.CodexMaxUsedPercent = 0
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	a.workerUsage.Inspect = func(context.Context, session.Options) (session.Inspection, error) {
		t.Fatal("disabled limit inspected CLI")
		return session.Inspection{}, nil
	}
	clearQuotaCache(a)
	if err := a.workerUsageAllowed(ctx, "fake"); err != nil {
		t.Fatal("disabled limit blocked", err)
	}
	external, _ := runtimeFixture(t, func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected broker call") })
	external.workerUsage.Inspect = a.workerUsage.Inspect
	cfg = external.Config()
	cfg.Limits.WorkerUsage.OnUnavailable = "pause"
	if err := external.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if err := external.workerUsageAllowed(ctx, "fake"); err == nil {
		t.Fatal("external broker incorrectly attributed to local account")
	}
}

func TestWorkerUsageUsesProfileEngineAndThreshold(t *testing.T) {
	a, _ := usageRuntimeFixture(t, func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected broker call") })
	cfg := a.Config()
	override := cfg.WorkerModel
	override.Engine = "claude"
	override.Model = "claude-opus-5"
	override.ClaudeHome = filepath.Join(t.TempDir(), "different-login")
	cfg.Workers[0].ModelProfile = &override
	cfg.Limits.WorkerUsage.ClaudeMaxUsedPercent = 75
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	a.workerUsage.Inspect = func(_ context.Context, o session.Options) (session.Inspection, error) {
		if o.Engine != session.Claude || o.Home != override.ClaudeHome {
			t.Fatalf("wrong worker login: %+v", o)
		}
		q := quotaFixture(75)
		q.Windows[0].ID = "seven_day"
		q.Windows[0].Scope = "seven_day"
		return session.Inspection{Quota: q}, nil
	}
	if err := a.workerUsageAllowed(context.Background(), "fake"); err == nil {
		t.Fatal("Claude-specific limit ignored")
	}
}

func TestUsageHeldDelegationRetriesWithoutOrphaningOrDuplicatingChild(t *testing.T) {
	ctx := context.Background()
	messages := 0
	run := worker.Run{ID: "manager", Status: "waiting", Summary: "Waiting for child", UpdatedAt: time.Now(), Delegation: &worker.DelegationRequest{RequestID: "stable-child-request", WorkerProfile: "fake", Role: "worker", Task: "Bounded implementation", AcceptanceCriteria: "Tests pass", Capabilities: []string{"implement"}}}
	a, _ := runtimeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/messages") {
			messages++
		}
		writeRun(w, run)
	})
	parent := commission(t, a, "")
	if _, err := a.Core.BeginDispatch(ctx, parent.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.Core.MarkDispatched(ctx, parent.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	cfg := a.Config()
	cfg.Limits.WorkerUsage.OnUnavailable = "pause"
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	// An external manager has unavailable local telemetry. Pausing it must happen
	// before child creation, exactly like a measured exhausted subscription.
	if err := a.tick(ctx, false); err != nil {
		t.Fatal("quota hold reported as supervision failure", err)
	}
	snap, _ := a.Core.Snapshot(ctx)
	if len(snap.Agents) != 1 || messages != 0 {
		t.Fatal("delegation hold created child before admission")
	}
	cfg.Limits.WorkerUsage.OnUnavailable = "allow"
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		snap, _ = a.Core.Snapshot(ctx)
		current, _ := findAgent(snap, parent.ID)
		if err := a.superviseAgent(ctx, current, false); err != nil {
			t.Fatal(err)
		}
	}
	snap, _ = a.Core.Snapshot(ctx)
	if len(snap.Agents) != 2 || messages != 1 {
		t.Fatalf("delegation failed recovery or duplicated: agents=%d messages=%d", len(snap.Agents), messages)
	}
}

func failingInspection(context.Context, session.Options) (session.Inspection, error) {
	return session.Inspection{}, errors.New("CLI login could not be inspected")
}
func lowUsageInspection(context.Context, session.Options) (session.Inspection, error) {
	return session.Inspection{Quota: quotaFixture(5)}, nil
}
