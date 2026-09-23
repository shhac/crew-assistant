package workerbroker

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/shhac/crew-assistant/internal/integrations/worker"
)

func TestSteeringReceiptsSurviveRestartAndDeduplicate(t *testing.T) {
	b, cfg := newFixture(t, "https://provider.test/v1", &fakeDocker{})
	response := request(t, b, "/runs", "dispatch-one", startRequest())
	var run worker.Run
	if err := json.Unmarshal(response.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if err := b.update(run.ID, func(r *storedRun) error { r.Run.Status = "running"; return nil }); err != nil {
		t.Fatal(err)
	}
	for _, args := range []string{
		`{"message_ids":["first","first"]}`,
		`{"message_ids":["first","second"]}`,
	} {
		if result := rawInvoke(b, run.ID, "agent-one", "acknowledge_steering", args); result.IsError {
			t.Fatal(result.Content)
		}
	}
	// Completing the run must not discard receipts that the daemon has not
	// observed yet. They are transported with the final report after restart.
	b.finalize(run.ID, "completed", "Synthetic outcome", []string{"synthetic evidence"})
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	got, err := restarted.snapshot(run.ID)
	if err != nil || !reflect.DeepEqual(got.Run.SteeringAcknowledgements, []string{"first", "second"}) {
		t.Fatal(got.Run, err)
	}
	if got.Run.Status != "completed" {
		t.Fatal("receipt changed execution status", got.Run)
	}
}

func TestSteeringReceiptsRejectInvalidOrExcessClaims(t *testing.T) {
	b, _ := newFixture(t, "https://provider.test/v1", &fakeDocker{})
	defer b.Close()
	response := request(t, b, "/runs", "dispatch-one", startRequest())
	var run worker.Run
	json.Unmarshal(response.Body.Bytes(), &run)
	b.update(run.ID, func(r *storedRun) error { r.Run.Status = "running"; return nil })
	for _, args := range []string{
		`{}`, `{"message_ids":[]}`, `{"message_ids":[""]}`,
		`{"message_ids":[" wrong "]}`,
		`{"message_ids":["one"],"agent_id":"another-agent"}`,
	} {
		if result := rawInvoke(b, run.ID, "agent-one", "acknowledge_steering", args); !result.IsError {
			t.Fatalf("accepted invalid receipt: %s", args)
		}
	}
	ids := make([]string, 200)
	for i := range ids {
		ids[i] = fmt.Sprintf("steering-%d", i)
	}
	args, _ := json.Marshal(map[string]any{"message_ids": ids})
	if result := rawInvoke(b, run.ID, "agent-one", "acknowledge_steering", string(args)); result.IsError {
		t.Fatal(result.Content)
	}
	if result := rawInvoke(b, run.ID, "agent-one", "acknowledge_steering", `{"message_ids":["overflow"]}`); !result.IsError {
		t.Fatal("unbounded cumulative receipts")
	}
	got, _ := b.snapshot(run.ID)
	if !reflect.DeepEqual(got.Run.SteeringAcknowledgements, ids) {
		t.Fatal("rejected receipt changed persisted acknowledgements")
	}
	b.finalize(run.ID, "cancelled", "Stopped", nil)
	if result := rawInvoke(b, run.ID, "agent-one", "acknowledge_steering", `{"message_ids":["late"]}`); !result.IsError {
		t.Fatal("cancelled execution created a receipt")
	}
}
