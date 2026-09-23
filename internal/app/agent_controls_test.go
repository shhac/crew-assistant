package app

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/integrations/worker"
)

func TestOwnerPausedBrokerIsNotAutomaticallyResumed(t *testing.T) {
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
			}
		}
		writeRun(w, worker.Run{ID: "external", Status: status, Summary: "Preserved checkpoint", UpdatedAt: time.Now(), ControlCapabilities: []string{"pause", "resume", "stop"}})
	})
	ag := commission(t, a, "")
	if _, err := a.Core.BeginDispatch(ctx, ag.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.Core.MarkDispatched(ctx, ag.ID, "external"); err != nil {
		t.Fatal(err)
	}
	if err := a.Core.SetAgentControlCapabilities(ctx, ag.ID, []string{"pause", "resume", "stop"}); err != nil {
		t.Fatal(err)
	}
	paused, err := a.ControlAgent(ctx, ag.ID, "pause", "owner-pause")
	if err != nil || paused.Status != "paused" {
		t.Fatal(paused, err)
	}
	for i := 0; i < 2; i++ {
		if err = a.tick(ctx, false); err != nil {
			t.Fatal(err)
		}
	}
	if posts != 1 {
		t.Fatal("supervision resumed held worker", posts)
	}
	if _, err = a.SendAgent(ctx, paused, "Wake up"); err == nil {
		t.Fatal("ordinary instruction woke paused agent")
	}
	resumed, err := a.ControlAgent(ctx, ag.ID, "resume", "owner-resume")
	if err != nil || resumed.Status != "running" || resumed.OwnerControl != "" {
		t.Fatal(resumed, err)
	}
	if _, err = a.ControlAgent(ctx, ag.ID, "resume", "owner-resume"); err != nil {
		t.Fatal(err)
	}
	if posts != 2 {
		t.Fatal("control retry duplicated external operation", posts)
	}
	page, err := a.Core.AgentConversation(ctx, ag.ID, 0, 0, 50)
	if err != nil || len(page.Messages) < 3 {
		t.Fatal("missing conversation/control reports", page, err)
	}
}
func TestExternalBrokerControlsRequireAdvertisedSupport(t *testing.T) {
	a, _ := runtimeFixture(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("unsupported control contacted broker") })
	ag := commission(t, a, "")
	ctx := context.Background()
	_, _ = a.Core.BeginDispatch(ctx, ag.ID)
	_ = a.Core.MarkDispatched(ctx, ag.ID, "external")
	controls, err := a.AgentControls(ctx, ag.ID)
	if err != nil || controls.Pause || controls.Stop || controls.Resume {
		t.Fatal(controls, err)
	}
	if _, err = a.ControlAgent(ctx, ag.ID, "pause", "unsupported"); err == nil {
		t.Fatal("unadvertised pause allowed")
	}
}

func TestUnstartedOwnerPauseDoesNotContactOrReconcileBroker(t *testing.T) {
	a, _ := runtimeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("unstarted paused worker contacted broker")
		http.Error(w, "unexpected", 500)
	})
	ag := commission(t, a, "")
	ctx := context.Background()
	paused, err := a.ControlAgent(ctx, ag.ID, "pause", "local-pause")
	if err != nil || paused.Status != "paused" {
		t.Fatal(paused, err)
	}
	if err = a.tick(ctx, false); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := a.Core.Snapshot(ctx)
	if snapshot.Agents[0].Status != "paused" {
		t.Fatal("queued pause entered reconciliation", snapshot.Agents[0])
	}
}
