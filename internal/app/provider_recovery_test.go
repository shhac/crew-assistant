package app

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/integrations/worker"
)

func TestProviderResumeLostAcknowledgementDoesNotRepeat(t *testing.T) {
	ctx := context.Background()
	resumes := 0
	run := worker.Run{ID: "provider-session", Status: "retry_wait", Summary: "Provider unavailable", ProviderFailureKind: "overloaded", ProviderFailures: 1, RetryAt: time.Now().Add(-time.Second), UpdatedAt: time.Now().UTC()}
	a, _ := runtimeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/resume") {
			resumes++
			w.WriteHeader(500)
			return
		}
		if r.Method == "GET" {
			writeRun(w, run)
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(400)
	})
	ag := commission(t, a, "")
	run.DispatchKey = ag.DispatchKey
	a.Core.BeginDispatch(ctx, ag.ID)
	a.Core.MarkDispatched(ctx, ag.ID, run.ID)
	if err := a.tick(ctx, false); err == nil {
		t.Fatal("lost acknowledgement not surfaced")
	}
	for i := 0; i < 4; i++ {
		_ = a.tick(ctx, false)
	}
	snapshot, _ := a.Core.Snapshot(ctx)
	if resumes != 1 || snapshot.Agents[0].Status != "reconciling" || snapshot.Agents[0].Recoveries != 0 {
		t.Fatalf("uncertain provider resume repeated: resumes=%d agent=%+v", resumes, snapshot.Agents[0])
	}
}
func TestProviderRecoveryWaitsForDueTimeAndUsageHeadroom(t *testing.T) {
	for _, gate := range []string{"future", "quota", "daemon"} {
		t.Run(gate, func(t *testing.T) {
			ctx := context.Background()
			posts := 0
			run := worker.Run{ID: "provider-session", Status: "retry_wait", Summary: "Provider unavailable", ProviderFailureKind: "overloaded", ProviderFailures: 1, RetryAt: time.Now().Add(-time.Second), UpdatedAt: time.Now().UTC()}
			handler := func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					posts++
					t.Error("held provider retry reached broker mutation")
				}
				writeRun(w, run)
			}
			var a *App
			var ag core.Agent
			if gate == "quota" {
				a, ag = usageRuntimeFixture(t, handler)
			} else {
				a, _ = runtimeFixture(t, handler)
				ag = commission(t, a, "")
			}
			a.Core.BeginDispatch(ctx, ag.ID)
			a.Core.MarkDispatched(ctx, ag.ID, run.ID)
			if gate == "future" {
				run.RetryAt = time.Now().Add(time.Hour)
			}
			if gate == "daemon" {
				a.Core.SetPaused(ctx, true)
			}
			for i := 0; i < 2; i++ {
				if err := a.tick(ctx, false); err != nil {
					t.Fatal(err)
				}
			}
			snapshot, _ := a.Core.Snapshot(ctx)
			if posts != 0 || snapshot.Agents[0].ResumeKey != "" || snapshot.Agents[0].Status != "retry_wait" {
				t.Fatalf("provider wait escaped %s: %+v", gate, snapshot.Agents[0])
			}
		})
	}
}
func TestNewProviderRejectionReconcilesPriorResumeWithoutDuplicate(t *testing.T) {
	ctx := context.Background()
	resumes := 0
	run := worker.Run{ID: "provider-session", Status: "retry_wait", Summary: "Provider unavailable", ProviderFailureKind: "overloaded", ProviderFailures: 1, RetryAt: time.Now().Add(-time.Second), UpdatedAt: time.Now().UTC()}
	a, _ := runtimeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/resume") {
			resumes++
			w.WriteHeader(500)
			return
		}
		writeRun(w, run)
	})
	ag := commission(t, a, "")
	a.Core.BeginDispatch(ctx, ag.ID)
	a.Core.MarkDispatched(ctx, ag.ID, run.ID)
	_ = a.tick(ctx, false)
	// The first resumed call reached the provider despite its lost HTTP receipt.
	run.ProviderFailures = 2
	run.RetryAt = time.Now().Add(time.Hour)
	run.UpdatedAt = time.Now().UTC()
	if err := a.tick(ctx, false); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := a.Core.Snapshot(ctx)
	if resumes != 1 || snapshot.Agents[0].Status != "retry_wait" || snapshot.Agents[0].ResumeKey != "" || snapshot.Agents[0].ProviderFailures != 2 {
		t.Fatalf("new report failed reconciliation: %+v", snapshot.Agents[0])
	}
}

func TestModelBlockedControlsSaveDirectionWithoutStartingExecution(t *testing.T) {
	for _, status := range []string{"blocked", "retry_wait"} {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			posts := 0
			run := worker.Run{ID: "model-stopped", Status: status, Summary: "Model recovery needed", ProviderFailureKind: "authentication", UpdatedAt: time.Now().UTC(), ControlCapabilities: []string{"pause", "resume", "stop"}}
			if status == "retry_wait" {
				run.ProviderFailureKind = "overloaded"
				run.ProviderFailures = 1
				run.RetryAt = time.Now().Add(time.Hour)
			}
			a, _ := runtimeFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					posts++
					t.Error("saved steering tried to start model-blocked work")
				}
				writeRun(w, run)
			})
			ag := commission(t, a, "")
			a.Core.BeginDispatch(ctx, ag.ID)
			a.Core.MarkDispatched(ctx, ag.ID, run.ID)
			if err := a.tick(ctx, false); err != nil {
				t.Fatal(err)
			}
			controls, err := a.AgentControls(ctx, ag.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !controls.Message || !strings.Contains(controls.Reason, "saved") || controls.Resume != (status == "blocked") {
				t.Fatalf("wrong recovery controls: %+v", controls)
			}
			steering, err := a.SteerAgent(ctx, ag.ID, "saved-during-outage", "Keep the existing interface")
			if err != nil {
				t.Fatal(err)
			}
			if err = a.tick(ctx, false); err != nil {
				t.Fatal(err)
			}
			snapshot, _ := a.Core.Snapshot(ctx)
			if posts != 0 || len(snapshot.Steering) != 1 || snapshot.Steering[0].ID != steering.ID || snapshot.Agents[0].Status != status {
				t.Fatal("saved direction woke blocked work")
			}
		})
	}
}
