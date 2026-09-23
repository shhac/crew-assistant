package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/engine"
	"github.com/shhac/crew-assistant/internal/integrations/worker"
)

func TestOwnerChatControlsPreservedWorkerAndRetriesOnce(t *testing.T) {
	ctx := context.Background()
	posts := 0
	status := "running"
	a, _ := runtimeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			posts++
			if strings.HasSuffix(r.URL.Path, "/pause") {
				status = "paused"
			} else if strings.HasSuffix(r.URL.Path, "/resume") {
				status = "running"
			} else if strings.HasSuffix(r.URL.Path, "/cancel") {
				status = "cancelled"
			}
		}
		writeRun(w, worker.Run{ID: "external", Status: status, Summary: "Saved work", UpdatedAt: time.Now(), ControlCapabilities: []string{"pause", "resume", "stop"}})
	})
	ag := commission(t, a, "")
	_, _ = a.Core.BeginDispatch(ctx, ag.ID)
	_ = a.Core.MarkDispatched(ctx, ag.ID, "external")
	_ = a.Core.SetAgentControlCapabilities(ctx, ag.ID, []string{"pause", "resume", "stop"})
	control := func(action string) json.RawMessage {
		raw, _ := json.Marshal(engine.ControlAgentArgs{AgentID: ag.ID, Action: action})
		return raw
	}
	if _, err := a.Execute(ctx, "control_agent", control("pause")); err == nil {
		t.Fatal("unscoped executor gained owner control")
	}
	owner := ownerChatExecutor{app: a, turnID: "owner-pause"}
	if _, err := owner.Execute(ctx, "control_agent", control("pause")); err != nil {
		t.Fatal(err)
	}
	for _, executor := range []engine.ToolExecutor{projectExecutor{app: a, projectID: ag.ProjectID, workItemID: ag.WorkItemID}, a} {
		if _, err := executor.Execute(ctx, "control_agent", control("resume")); err == nil {
			t.Fatal("autonomous executor overrode owner hold")
		}
	}
	inspection, err := a.InspectAgent(ctx, ag.ID)
	if err != nil || !inspection.Controls.Resume || inspection.Agent.Status != "paused" || len(inspection.Conversation.Messages) == 0 {
		t.Fatal(inspection, err)
	}
	owner.turnID = "owner-resume"
	for i := 0; i < 2; i++ {
		if _, err := owner.Execute(ctx, "control_agent", control("resume")); err != nil {
			t.Fatal(err)
		}
	}
	if posts != 2 {
		t.Fatal("resume duplicated", posts)
	}
	snap, _ := a.Core.Snapshot(ctx)
	if len(snap.Agents) != 1 || snap.Agents[0].ExternalID != "external" {
		t.Fatal("resume replaced assignment", snap.Agents)
	}
	owner.turnID = "owner-stop"
	if _, err := owner.Execute(ctx, "control_agent", control("stop")); err != nil {
		t.Fatal(err)
	}
	owner.turnID = "after-stop"
	if _, err := owner.Execute(ctx, "control_agent", control("resume")); err == nil {
		t.Fatal("stopped worker resurrected")
	}
}

func TestChatTurnCarriesOwnerControlAuthority(t *testing.T) {
	a, _ := runtimeFixture(t, func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected broker") })
	ctx := context.Background()
	ag := commission(t, a, "")
	a.chatInvoker = func(ctx context.Context, _ engine.Config, _ engine.Request, executor engine.ToolExecutor) (engine.Result, error) {
		raw, _ := json.Marshal(engine.ControlAgentArgs{AgentID: ag.ID, Action: "pause"})
		_, err := executor.Execute(ctx, "control_agent", raw)
		return engine.Result{}, err
	}
	if _, err := a.runChatTurn(ctx, core.ChatTurn{ID: "real-current-turn", Message: "Pause this worker"}); err != nil {
		t.Fatal(err)
	}
	snap, _ := a.Core.Snapshot(ctx)
	if snap.Agents[0].Status != "paused" || !snap.Events["owner-control:"+ag.ID+":pause:chat:real-current-turn"] {
		t.Fatal("turn authority missing", snap.Agents, snap.Events)
	}
}

func TestMessageRefusalIsFinishedButUnknownDeliveryStaysPending(t *testing.T) {
	for _, tt := range []struct {
		name     string
		status   int
		body     string
		rejected bool
	}{
		{"explicit refusal", 409, `{"error":"interrupted workers require explicit resume"}`, true},
		{"unknown conflict", 409, `{"error":"idempotency key reused with different contents"}`, false},
		{"server failure", 503, `{}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			a, _ := runtimeFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					w.WriteHeader(tt.status)
					_, _ = w.Write([]byte(tt.body))
					return
				}
				writeRun(w, worker.Run{ID: "external", Status: "blocked", ProviderFailureKind: "unknown", Summary: "Explicit resume needed", UpdatedAt: time.Now(), ControlCapabilities: []string{"resume"}})
			})
			ag := commission(t, a, "")
			_, _ = a.Core.BeginDispatch(ctx, ag.ID)
			_ = a.Core.MarkDispatched(ctx, ag.ID, "external")
			ag.ExternalID = "external"
			_, err := a.SendAgent(ctx, ag, "Please inspect progress")
			if err == nil {
				t.Fatal("refusal reported success")
			}
			var refused *worker.RejectionError
			if errors.As(err, &refused) != tt.rejected {
				t.Fatal("wrong error", err)
			}
			snap, _ := a.Core.Snapshot(ctx)
			found := false
			for key, done := range snap.Events {
				if strings.HasPrefix(key, "instruction:") {
					found = true
					if done != tt.rejected {
						t.Fatal("incorrect operation certainty", key, done)
					}
				}
			}
			if !found {
				t.Fatal("no durable instruction record")
			}
			if !tt.rejected {
				if snap.Agents[0].Status != "reconciling" {
					t.Fatal("unknown delivery not held", snap.Agents[0])
				}
				if err := a.Core.BeginInstruction(ctx, ag.ID); err == nil {
					t.Fatal("ambiguous message repeated before reconciliation")
				}
			}
			if tt.rejected && snap.Agents[0].Status != "blocked" {
				t.Fatal("stale running state persisted", snap.Agents[0])
			}
			page, _ := a.Core.AgentConversation(ctx, ag.ID, 0, 0, 20)
			kind := "delivery_uncertain"
			if tt.rejected {
				kind = "delivery_rejected"
			}
			recorded := false
			for _, entry := range page.Messages {
				if entry.Kind == kind {
					recorded = true
				}
			}
			if !recorded {
				t.Fatal("missing delivery result", page)
			}
		})
	}
}

func TestAutomaticInstructionRefusalReleasesOnlyItsPendingOperation(t *testing.T) {
	ctx := context.Background()
	a, _ := runtimeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"code":"operation_rejected"}`))
			return
		}
		writeRun(w, worker.Run{ID: "external", Status: "blocked", ProviderFailureKind: "unknown", Summary: "Resume needed", UpdatedAt: time.Now()})
	})
	ag := commission(t, a, "")
	_, _ = a.Core.BeginDispatch(ctx, ag.ID)
	_ = a.Core.MarkDispatched(ctx, ag.ID, "external")
	ag.ExternalID = "external"
	err := a.once(ctx, "instruction:test", func() error { err := a.sendInstruction(ctx, ag, "instruction:test", "Direction"); return err })
	if err == nil {
		t.Fatal("refusal lost")
	}
	snap, _ := a.Core.Snapshot(ctx)
	if _, exists := snap.Events["instruction:test"]; exists {
		t.Fatal("definite refusal left pending operation")
	}
	if snap.Agents[0].Status != "blocked" {
		t.Fatal("refused instruction left agent running", snap.Agents[0])
	}
}

func TestScopedInspectionCannotReadAnotherProject(t *testing.T) {
	a, _ := runtimeFixture(t, func(http.ResponseWriter, *http.Request) { t.Fatal("read-only inspection contacted broker") })
	ag := commission(t, a, "")
	raw, _ := json.Marshal(engine.InspectAgentArgs{AgentID: ag.ID})
	own := projectExecutor{app: a, projectID: ag.ProjectID, workItemID: ag.WorkItemID}
	if _, err := own.Execute(context.Background(), "inspect_agent", raw); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []projectExecutor{{app: a, projectID: "other"}, {app: a, projectID: ag.ProjectID, workItemID: "other"}} {
		if _, err := scope.Execute(context.Background(), "inspect_agent", raw); err == nil {
			t.Fatal("inspection crossed authority")
		}
	}
}
