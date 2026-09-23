package workerbroker

import (
	"encoding/json"
	"testing"

	"github.com/shhac/crew-assistant/internal/integrations/worker"
	"github.com/shhac/lib-agent-harness/session"
)

// Pausing a live worker is covered end-to-end in native_integration_test.go,
// where a real turn is interrupted at a resumable point. What is here is the
// durable side: an owner's hold outranks everything that happens afterwards, and
// nothing a pause touches is allowed to claim more than it knows.

// A pause is confirmed only once the container is confirmed gone. Until then the
// assignment is still running, however the owner's request is recorded.
func TestPauseIsNotConfirmedUntilCleanupIs(t *testing.T) {
	b, _ := newFixture(t, "https://model.test", &fakeDocker{})
	defer b.Close()
	id := runningRun(t, b)
	response := request(t, b, "/runs/"+id+"/pause", "pause-once", struct{}{})
	var run worker.Run
	_ = json.Unmarshal(response.Body.Bytes(), &run)
	if response.Code != 200 || run.Status != "running" || !run.PauseRequested {
		t.Fatal("pause falsely confirmed before the boundary", response.Code, run)
	}
	held, _ := b.snapshot(id)
	if held.PendingStatus != "paused" {
		t.Fatalf("the owner's hold was not recorded: %q", held.PendingStatus)
	}
	// Cleanup that could not be confirmed leaves execution where it was.
	b.cleanupUncertain(id, "Cleanup unconfirmed")
	uncertain, _ := b.snapshot(id)
	if uncertain.Run.Status != "running" || uncertain.PendingStatus != "paused" {
		t.Fatal("unconfirmed cleanup released execution", uncertain)
	}
	// And an ordinary message cannot wake a worker the owner has held.
	if code := request(t, b, "/runs/"+id+"/messages", "must-not-wake", map[string]string{"message": "wake"}).Code; code != 409 {
		t.Fatal("ordinary message resumed a held worker", code)
	}
	b.finalize(id, "completed", "Synthetic outcome", nil)
	paused, _ := b.snapshot(id)
	if paused.Run.Status != "paused" || paused.Run.PauseRequested {
		t.Fatalf("confirmed cleanup did not settle the pause: %q request=%v", paused.Run.Status, paused.Run.PauseRequested)
	}
}

func TestPauseSurvivesBrokerRestartAndResumeReusesTheSession(t *testing.T) {
	b, cfg := newFixture(t, "https://model.test", &fakeDocker{})
	response := request(t, b, "/runs", "dispatch-one", startRequest())
	var run worker.Run
	_ = json.Unmarshal(response.Body.Bytes(), &run)
	if err := b.update(run.ID, func(r *storedRun) error {
		r.Run.Status = "running"
		r.Run.PauseRequested = true
		r.PendingStatus = "paused"
		r.Session = &session.Ref{Engine: "claude", ID: "session-1"}
		r.SessionPhase = phaseOpen
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	b.Close()
	reopened, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	r, _ := reopened.snapshot(run.ID)
	if r.Run.Status != "paused" || r.Run.PauseRequested {
		t.Fatal("lost owner hold", r.Run)
	}
	response = request(t, reopened, "/runs/"+run.ID+"/resume", "owner-resume", map[string]string{"instruction": "Continue"})
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	resumed, _ := reopened.snapshot(run.ID)
	if resumed.Run.Status != "queued" || resumed.Run.ID != run.ID {
		t.Fatal("resume did not requeue the same assignment", resumed)
	}
	if resumed.Session == nil || resumed.Session.ID != "session-1" {
		t.Fatal("resume discarded the coding session", resumed.Session)
	}
}

func TestPausePreservesPeerOutboxBeforeResume(t *testing.T) {
	b, _ := newFixture(t, "https://model.test", &fakeDocker{})
	defer b.Close()
	response := request(t, b, "/runs", "dispatch-one", startRequest())
	var run worker.Run
	_ = json.Unmarshal(response.Body.Bytes(), &run)
	_ = b.update(run.ID, func(r *storedRun) error {
		r.Run.Status = "running"
		r.PendingMessage = &worker.PeerMessage{RequestID: "peer-msg", TargetAgentID: "peer", Message: "Evidence"}
		return nil
	})
	response = request(t, b, "/runs/"+run.ID+"/pause", "pause-outbox", struct{}{})
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	b.finalize(run.ID, "waiting", "Peer delivery pending", nil)
	current, _ := b.snapshot(run.ID)
	if current.Run.Status != "paused" || current.Run.Message == nil || current.PendingMessage != nil {
		t.Fatal("outbox lost on paused cleanup", current)
	}
	response = request(t, b, "/runs/"+run.ID+"/resume", "resume-outbox", map[string]string{"instruction": "Continue"})
	_ = json.Unmarshal(response.Body.Bytes(), &run)
	if response.Code != 200 || run.Status != "waiting" || run.Message == nil {
		t.Fatal("resumed before routing saved peer message", run)
	}
}

func TestPauseCleanupFailureNeverClaimsCheckpointIsStopped(t *testing.T) {
	b, _ := newFixture(t, "https://model.test", &fakeDocker{})
	defer b.Close()
	response := request(t, b, "/runs", "dispatch-one", startRequest())
	var run worker.Run
	_ = json.Unmarshal(response.Body.Bytes(), &run)
	_ = b.update(run.ID, func(r *storedRun) error {
		r.Run.Status = "running"
		r.PendingStatus = "paused"
		r.Run.PauseRequested = true
		return nil
	})
	b.cleanupUncertain(run.ID, "Cleanup unconfirmed")
	current, _ := b.snapshot(run.ID)
	if current.Run.Status != "running" || current.PendingStatus != "paused" {
		t.Fatal("unconfirmed cleanup released execution", current)
	}
	b.terminal(run.ID, "interrupted", "Shutdown")
	current, _ = b.snapshot(run.ID)
	if current.PendingStatus != "paused" {
		t.Fatal("shutdown lost owner pause")
	}
}
