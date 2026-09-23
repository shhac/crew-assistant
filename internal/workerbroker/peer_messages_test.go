package workerbroker

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/integrations/worker"
)

func peerArgs() map[string]string {
	return map[string]string{"target_agent_id": "peer-two", "message": "The fixture contract is ready"}
}

func TestPeerOutboxSurvivesRestartBeforePublication(t *testing.T) {
	b, cfg := newFixture(t, "https://provider.test/v1", &fakeDocker{})
	response := request(t, b, "/runs", "dispatch-one", startRequest())
	var run worker.Run
	json.Unmarshal(response.Body.Bytes(), &run)
	b.update(run.ID, func(r *storedRun) error { r.Run.Status = "running"; return nil })
	if result := invoke(t, b, run.ID, "agent-one", "send_message", peerArgs()); result.IsError {
		t.Fatal(result.Content)
	}
	pending, _ := b.snapshot(run.ID)
	if pending.PendingMessage == nil || pending.Run.Message != nil {
		t.Fatal("outbox exposed before cleanup", pending)
	}
	requestID := pending.PendingMessage.RequestID
	b.Close()
	restarted, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	recovered, _ := restarted.snapshot(run.ID)
	if recovered.Run.Status != "waiting" || recovered.Run.Message == nil || recovered.Run.Message.RequestID != requestID || recovered.PendingMessage != nil {
		t.Fatal("outbox lost during recovery", recovered)
	}
	found := false
	for _, definition := range workerToolDefinitions() {
		if definition.Name == "send_message" {
			found = true
		}
	}
	if !found || !strings.Contains(workerPrompt(recovered.Request), "send_message") {
		t.Fatal("peer tool unavailable to recovered worker")
	}
}

func TestPeerOutboxWaitsForMatchingAcknowledgement(t *testing.T) {
	b, _ := newFixture(t, "https://provider.test/v1", &fakeDocker{})
	defer b.Close()
	response := request(t, b, "/runs", "dispatch-one", startRequest())
	var run worker.Run
	json.Unmarshal(response.Body.Bytes(), &run)
	b.update(run.ID, func(r *storedRun) error { r.Run.Status = "running"; return nil })
	if result := invoke(t, b, run.ID, "agent-one", "send_message", peerArgs()); result.IsError {
		t.Fatal(result.Content)
	}
	// Incoming information during cleanup must not wake the sender or discard
	// its unpublished outbound request.
	if got := request(t, b, "/runs/"+run.ID+"/messages", "unrelated", map[string]string{"message": "Other peer data"}); got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	b.finalize(run.ID, "waiting", "Pending peer delivery", nil)
	waiting, _ := b.snapshot(run.ID)
	if waiting.Run.Status != "waiting" || waiting.Run.Message == nil {
		t.Fatal(waiting)
	}
	requestID := waiting.Run.Message.RequestID
	if got := request(t, b, "/runs/"+run.ID+"/messages", "peer-message-ack:agent-one:wrong", map[string]string{"message": "Wrong acknowledgement"}); got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	waiting, _ = b.snapshot(run.ID)
	if waiting.Run.Status != "waiting" || waiting.Run.Message == nil {
		t.Fatal("unrelated input displaced outbox", waiting)
	}
	key := "peer-message-ack:agent-one:" + requestID
	for i := 0; i < 2; i++ {
		if got := request(t, b, "/runs/"+run.ID+"/messages", key, map[string]string{"message": "Daemon delivered message"}); got.Code != 200 {
			t.Fatal(got.Body.String())
		}
	}
	resumed, _ := b.snapshot(run.ID)
	if resumed.Run.Status != "queued" || resumed.Run.Message != nil || len(resumed.Messages) != 3 {
		t.Fatal("acknowledgement did not resume exactly once", resumed)
	}
}

// A worker cannot claim to be someone else, or address itself. The sender is the
// run's own agent, and the tool schema has no field for an alternative.
func TestPeerToolDoesNotAcceptForgedSenderOrSelfDelivery(t *testing.T) {
	b, _ := newFixture(t, "https://provider.test/v1", &fakeDocker{})
	defer b.Close()
	forged := rawInvoke(b, "unused", "agent-one", "send_message",
		`{"target_agent_id":"peer-two","message":"fixture","sender_id":"owner"}`)
	if !forged.IsError {
		t.Fatal("forged sender accepted", forged.Content)
	}
	self := invoke(t, b, "unused", "agent-one", "send_message",
		map[string]string{"target_agent_id": "agent-one", "message": "fixture"})
	if !self.IsError {
		t.Fatal("a worker addressed itself", self.Content)
	}
}

func TestArtifactFailurePreservesPeerOutboxThroughResume(t *testing.T) {
	b, _ := newFixture(t, "https://provider.test/v1", &fakeDocker{})
	defer b.Close()
	response := request(t, b, "/runs", "dispatch-one", startRequest())
	var run worker.Run
	json.Unmarshal(response.Body.Bytes(), &run)
	b.update(run.ID, func(r *storedRun) error { r.Run.Status = "running"; return nil })
	if result := invoke(t, b, run.ID, "agent-one", "send_message", peerArgs()); result.IsError {
		t.Fatal(result.Content)
	}
	// Artifact failure happens after confirmed container cleanup.
	b.finalize(run.ID, "interrupted", "Artifact collection failed", nil)
	interrupted, _ := b.snapshot(run.ID)
	if interrupted.Run.Message == nil || interrupted.PendingMessage != nil {
		t.Fatal("artifact failure hid outbox", interrupted)
	}
	if got := request(t, b, "/runs/"+run.ID+"/resume", "resume-one", map[string]string{"instruction": "Reconcile and continue"}); got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	resumed, _ := b.snapshot(run.ID)
	if resumed.Run.Status != "waiting" || resumed.Run.Message == nil || resumed.Run.Message.RequestID != interrupted.Run.Message.RequestID {
		t.Fatal("resume displaced outbound message", resumed)
	}
}
