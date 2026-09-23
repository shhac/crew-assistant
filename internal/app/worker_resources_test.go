package app

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/integrations/worker"
	"github.com/shhac/lib-agent-harness/session"
)

func usageWaitRun(kind string, ownerAction bool) worker.Run {
	hold := &worker.ResourceHold{Kind: kind, Reason: "Waiting for worker resources", OwnerAction: ownerAction}
	if !ownerAction {
		hold.NextCheckAt = time.Now().Add(-time.Minute).UTC()
		hold.ResetsAt = time.Now().Add(time.Hour).UTC()
	}
	return worker.Run{
		ID: "held-session", Status: "usage_wait", Summary: "Waiting for worker resources",
		UpdatedAt:           time.Now().UTC(),
		ControlCapabilities: []string{"pause", "resume", "stop"},
		ResourceHold:        hold,
		Usage:               worker.Usage{InputTokens: 900, OutputTokens: 100, TokenBudget: 1000},
	}
}

// A quota wait clears by itself, so the daemon continues the existing session
// once its stated next check is due. Continuing is not a recovery and must not
// spend the recovery allowance a genuine interruption is entitled to.
func TestQuotaWaitContinuesAutomaticallyWithoutSpendingRecoveries(t *testing.T) {
	ctx := context.Background()
	resumes := 0
	run := usageWaitRun(worker.HoldSubscriptionQuota, false)
	a, _ := runtimeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/resume") {
			resumes++
			run = worker.Run{ID: run.ID, DispatchKey: run.DispatchKey, Status: "running", Summary: "Continuing the existing assignment", UpdatedAt: time.Now().UTC(), ControlCapabilities: run.ControlCapabilities, Usage: run.Usage}
		}
		writeRun(w, run)
	})
	ag := commission(t, a, "")
	run.DispatchKey = ag.DispatchKey
	a.Core.BeginDispatch(ctx, ag.ID)
	a.Core.MarkDispatched(ctx, ag.ID, run.ID)
	if err := a.tick(ctx, false); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := a.Core.Snapshot(ctx)
	current := snapshot.Agents[0]
	if resumes != 1 {
		t.Fatalf("quota wait did not continue automatically: %d resumes", resumes)
	}
	if current.Recoveries != 0 {
		t.Fatalf("resource wait spent a recovery allowance: %d", current.Recoveries)
	}
	if current.ProviderFailures != 0 || current.ProviderFailureKind != "" {
		t.Fatal("resource wait was recorded as a provider failure", current.ProviderFailureKind)
	}
}

// Automatic continuation needs a hold that says what it is and when to look
// again. Anything else waits for the owner rather than being restarted on a
// schedule the daemon invented for it.
func TestOnlyWellFormedRecheckableHoldsContinueOnTheirOwn(t *testing.T) {
	for _, tc := range []struct {
		name      string
		hold      *worker.ResourceHold
		continue_ bool
	}{
		{"no hold reported", nil, false},
		{"unknown kind", &worker.ResourceHold{Kind: "something_else", NextCheckAt: time.Now().Add(-time.Minute)}, false},
		{"no next check", &worker.ResourceHold{Kind: worker.HoldSubscriptionQuota}, false},
		{"next check not due", &worker.ResourceHold{Kind: worker.HoldSubscriptionQuota, NextCheckAt: time.Now().Add(time.Hour)}, false},
		{"owner decision", &worker.ResourceHold{Kind: worker.HoldTokenBudget, OwnerAction: true, NextCheckAt: time.Now().Add(-time.Minute)}, false},
		{"quota, due", &worker.ResourceHold{Kind: worker.HoldSubscriptionQuota, NextCheckAt: time.Now().Add(-time.Minute)}, true},
		{"telemetry outage, due", &worker.ResourceHold{Kind: worker.HoldTelemetryUnavailable, NextCheckAt: time.Now().Add(-time.Minute)}, true},
		// A published reset is when the provider expects its window to roll over,
		// not the earliest moment work can continue: the owner may have relaxed
		// the threshold, and admission re-reads policy before anything restarts.
		{"due despite a distant reset", &worker.ResourceHold{Kind: worker.HoldSubscriptionQuota, NextCheckAt: time.Now().Add(-time.Minute), ResetsAt: time.Now().Add(72 * time.Hour)}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := worker.Run{Status: "usage_wait", ResourceHold: tc.hold}
			if continuable(run) != tc.continue_ {
				t.Fatalf("continuable=%v want %v", continuable(run), tc.continue_)
			}
		})
	}
}

// Relaxing the threshold has to be enough: the next scheduled check re-reads
// policy and account, and does not wait for a published weekly reset.
func TestRelaxedThresholdContinuesAtTheNextScheduledCheck(t *testing.T) {
	ctx := context.Background()
	resumes := 0
	run := usageWaitRun(worker.HoldSubscriptionQuota, false)
	run.ResourceHold.ResetsAt = time.Now().Add(72 * time.Hour).UTC()
	a, ag := usageRuntimeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/resume") {
			resumes++
		}
		writeRun(w, run)
	})
	run.DispatchKey = ag.DispatchKey
	a.Core.BeginDispatch(ctx, ag.ID)
	a.Core.MarkDispatched(ctx, ag.ID, run.ID)
	// The fixture's account sits at 90%, so the default policy holds it.
	if err := a.tick(ctx, false); err != nil {
		t.Fatal(err)
	}
	if resumes != 0 {
		t.Fatal("continued while the account was still over the limit")
	}
	cfg := a.Config()
	cfg.Limits.WorkerUsage.CodexMaxUsedPercent = 95
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	a.workerUsage.Forget()
	if err := a.tick(ctx, false); err != nil {
		t.Fatal(err)
	}
	if resumes != 1 {
		t.Fatalf("a relaxed threshold did not release the wait: %d resumes", resumes)
	}
}

// An owner-decision hold is the owner's move, and their resume has to actually
// work — not merely be offered.
func TestBudgetWaitResumesThroughTheOwnerControlAPI(t *testing.T) {
	ctx := context.Background()
	posts, resumes := 0, 0
	run := usageWaitRun(worker.HoldTokenBudget, true)
	a, _ := runtimeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			posts++
			if strings.HasSuffix(r.URL.Path, "/resume") {
				resumes++
				run = worker.Run{ID: run.ID, DispatchKey: run.DispatchKey, Status: "running", Summary: "Continuing with a raised budget", UpdatedAt: time.Now().UTC(), ControlCapabilities: run.ControlCapabilities, Usage: run.Usage}
			}
		}
		writeRun(w, run)
	})
	ag := commission(t, a, "")
	run.DispatchKey = ag.DispatchKey
	a.Core.BeginDispatch(ctx, ag.ID)
	a.Core.MarkDispatched(ctx, ag.ID, run.ID)
	for i := 0; i < 3; i++ {
		if err := a.tick(ctx, false); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, _ := a.Core.Snapshot(ctx)
	current := snapshot.Agents[0]
	if posts != 0 || current.Status != "usage_wait" || current.ResumeKey != "" {
		t.Fatalf("budget hold did not wait for the owner: posts=%d %+v", posts, current)
	}
	if current.UsageInputTokens != 900 || current.UsageOutputTokens != 100 || current.TokenBudget != 1000 {
		t.Fatalf("ledger did not reach the daemon: %+v", current)
	}
	inspection, err := a.InspectAgent(ctx, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Resources.WaitingOn != "token_budget" || inspection.Resources.UsedTokens != 1000 || !inspection.Resources.BudgetConfigured {
		t.Fatalf("assistant inspection hides the budget state: %+v", inspection.Resources)
	}
	// The owner raises the budget and resumes through the control they are shown.
	cfg := a.Config()
	cfg.Limits.WorkerTokenBudget = 100000
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ControlAgent(ctx, current.ID, "resume", "owner-turn-1"); err != nil {
		t.Fatalf("the offered resume does not work: %v", err)
	}
	if resumes != 1 {
		t.Fatalf("owner resume never reached the broker: %d", resumes)
	}
	snapshot, _ = a.Core.Snapshot(ctx)
	if snapshot.Agents[0].Recoveries != 0 {
		t.Fatal("owner resume from a resource hold spent a recovery allowance")
	}
}

// Owner pause and stop, a global pause and a no-dispatch boot each outrank an
// automatic continuation.
func TestResourceContinuationYieldsToOwnerAndDaemonControls(t *testing.T) {
	for _, gate := range []string{"owner_pause", "owner_stop", "global_pause", "no_dispatch"} {
		t.Run(gate, func(t *testing.T) {
			ctx := context.Background()
			posts := 0
			run := usageWaitRun(worker.HoldSubscriptionQuota, false)
			a, _ := runtimeFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					posts++
					t.Errorf("%s did not prevent continuation", gate)
				}
				writeRun(w, run)
			})
			ag := commission(t, a, "")
			run.DispatchKey = ag.DispatchKey
			a.Core.BeginDispatch(ctx, ag.ID)
			a.Core.MarkDispatched(ctx, ag.ID, run.ID)
			noDispatch := false
			switch gate {
			case "owner_pause":
				if _, _, err := a.Core.PrepareOwnerControl(ctx, ag.ID, "pause", "owner-pause"); err != nil {
					t.Fatal(err)
				}
			case "owner_stop":
				if _, _, err := a.Core.PrepareOwnerControl(ctx, ag.ID, "stop", "owner-stop"); err != nil {
					t.Fatal(err)
				}
			case "global_pause":
				if err := a.Core.SetPaused(ctx, true); err != nil {
					t.Fatal(err)
				}
			case "no_dispatch":
				noDispatch = true
			}
			for i := 0; i < 2; i++ {
				if err := a.tick(ctx, noDispatch); err != nil {
					t.Fatal(err)
				}
			}
			snapshot, _ := a.Core.Snapshot(ctx)
			if posts != 0 || snapshot.Agents[0].ResumeKey != "" {
				t.Fatalf("continuation escaped %s: %+v", gate, snapshot.Agents[0])
			}
		})
	}
}

// A resource wait is visible as waiting. Only a hold the owner has to act on
// puts the project in their queue, and a sibling that is merely waiting must
// not take the row from one that needs them.
func TestAttentionSeparatesOrdinaryWaitFromOwnerDecision(t *testing.T) {
	automatic := core.Agent{ID: "a1", ProjectID: "p1", Name: "Waiting worker", Status: "usage_wait", Summary: "Waiting for the account allowance", ResourceHoldKind: worker.HoldSubscriptionQuota, ResourceHoldResetsAt: time.Now().Add(time.Hour)}
	decision := core.Agent{ID: "a2", ProjectID: "p1", Name: "Budget worker", Status: "usage_wait", Summary: "Worker token budget reached", ResourceHoldKind: worker.HoldTokenBudget, ResourceHoldOwnerAction: true}

	base := core.Snapshot{Projects: []core.Project{{ID: "p1", Title: "Project", Status: "active"}}, Agents: []core.Agent{automatic}}
	rows := core.DeriveAttention(base)
	if len(rows) != 1 || rows[0].Execution != "usage_wait" {
		t.Fatalf("resource wait not reported: %+v", rows)
	}
	if rows[0].NextAction != "worker" || rows[0].Recovery != "scheduled" || rows[0].RecoveryAt == nil {
		t.Fatalf("a self-clearing wait asked the owner to act: %+v", rows[0])
	}

	// Either ordering has to reach the same answer: the decision wins the row.
	for _, agents := range [][]core.Agent{{automatic, decision}, {decision, automatic}} {
		base.Agents = agents
		rows = core.DeriveAttention(base)
		if len(rows) != 1 || rows[0].AgentID != decision.ID {
			t.Fatalf("an automatic wait masked an owner decision: %+v", rows)
		}
		if rows[0].NextAction != "owner" || rows[0].Recovery != "held" || rows[0].RecoveryAt != nil {
			t.Fatalf("an owner decision was reported as an ordinary wait: %+v", rows[0])
		}
	}
}

// Telemetry that cannot be read is a measurement problem, not a decision: the
// daemon keeps looking and recovers by itself when readings return.
func TestTelemetryOutageHoldRecoversWithoutAnOwnerDecision(t *testing.T) {
	ctx := context.Background()
	a, _ := usageRuntimeFixture(t, func(http.ResponseWriter, *http.Request) {})
	cfg := a.Config()
	cfg.Limits.WorkerUsage.OnUnavailable = "pause"
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	a.workerUsage.Inspect = failingInspection
	a.workerUsage.Forget()
	profile, err := a.Core.GetProfile("fake")
	if err != nil {
		t.Fatal(err)
	}
	subject := headroomSubject{StatusID: profile.ID, Name: profile.Name, Model: workerModel(a.Config(), profile), Inspectable: true}
	hold, err := a.workerHeadroom(ctx, subject, false)
	if err != nil {
		t.Fatal(err)
	}
	if hold == nil || hold.Kind != worker.HoldTelemetryUnavailable {
		t.Fatalf("an unreadable account did not produce a telemetry hold: %+v", hold)
	}
	if hold.OwnerAction || !hold.Recheckable() || hold.NextCheckAt.IsZero() {
		t.Fatalf("a telemetry outage was recorded as an owner decision: %+v", hold)
	}
	// Readings return; the same policy now allows work with no owner involvement.
	a.workerUsage.Inspect = lowUsageInspection
	a.workerUsage.Forget()
	hold, err = a.workerHeadroom(ctx, subject, false)
	if err != nil || hold != nil {
		t.Fatalf("recovered telemetry still held work: %+v %v", hold, err)
	}
}

// A running broker keeps the engine, binary and login it was opened with. Its
// headroom has to be measured against that account, not against a profile the
// owner has edited since — otherwise the guard inspects an account this worker
// is not spending from. Thresholds, by contrast, are read live.
func TestInferenceAdmissionInspectsTheBrokersOwnLoginNotCurrentConfig(t *testing.T) {
	ctx := context.Background()
	a, _ := usageRuntimeFixture(t, func(http.ResponseWriter, *http.Request) {})
	opened := a.Config().WorkerModel
	opened.CodexHome = "/synthetic/original-login"
	opened.CodexBin = "original-codex"

	var inspected []session.Options
	a.workerUsage.Inspect = func(_ context.Context, o session.Options) (session.Inspection, error) {
		inspected = append(inspected, o)
		return session.Inspection{Quota: quotaFixture(92)}, nil
	}
	projectID := a.Config().Workers[0].ProjectID

	// The owner edits the worker profile to a different engine and login. The
	// broker that is already running has not changed.
	cfg := a.Config()
	changed := cfg.WorkerModel
	changed.Engine = "claude"
	changed.Model = "claude-opus-5"
	changed.ClaudeHome = "/synthetic/different-login"
	cfg.Workers[0].ModelProfile = &changed
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}

	if err := a.workerInferenceAdmission(ctx, projectID, opened); err == nil {
		t.Fatal("an account over its limit was admitted")
	}
	if len(inspected) != 1 || inspected[0].Engine != session.Codex || inspected[0].Home != opened.CodexHome || inspected[0].Binary != opened.CodexBin {
		t.Fatalf("admission inspected the wrong account: %+v", inspected)
	}

	// Only the threshold is live: raising it releases the same broker's work
	// without the broker's identity being re-read from configuration.
	cfg = a.Config()
	cfg.Limits.WorkerUsage.CodexMaxUsedPercent = 95
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	a.workerUsage.Forget()
	if err := a.workerInferenceAdmission(ctx, projectID, opened); err != nil {
		t.Fatalf("a raised threshold did not apply: %v", err)
	}
	if len(inspected) != 2 || inspected[1].Home != opened.CodexHome {
		t.Fatalf("the second check changed account: %+v", inspected)
	}
}
