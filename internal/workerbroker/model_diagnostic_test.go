package workerbroker

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/integrations/worker"
	"github.com/shhac/lib-agent-harness/completion"
	"github.com/shhac/lib-agent-harness/session"
)

// A resource hold is not a provider failure. It must stop before any work is
// authorized, leave accounting untouched, and never be classified as something
// worth retrying.
func TestResourceHoldStopsBeforeWorkWithoutFailureClassification(t *testing.T) {
	b, _ := newFixture(t, "https://model.test", &fakeDocker{})
	defer b.Close()
	b.cfg.TokenBudget = func() int64 { return 5000 }
	id := runningRun(t, b)
	if err := b.update(id, func(r *storedRun) error { r.UsageInputTokens = 5000; return nil }); err != nil {
		t.Fatal(err)
	}
	before, _ := b.snapshot(id)
	request := ""
	err := b.reserveWorkerModelCall(context.Background(), id, stageNativeTurn, &request)
	if !errors.Is(err, worker.ErrResourceHold) {
		t.Fatalf("lost resource-hold identity: %v", err)
	}
	var failure *completion.RequestError
	if errors.As(err, &failure) {
		t.Fatalf("hold carried a provider classification: %+v", failure)
	}
	after, _ := b.snapshot(id)
	if before.ModelCalls != after.ModelCalls || after.PendingUsage != nil || request != "" {
		t.Fatal("hold changed accounting")
	}
	if after.Run.ResourceHold == nil || after.Run.ResourceHold.Kind != worker.HoldTokenBudget || !after.Run.ResourceHold.OwnerAction {
		t.Fatalf("budget hold not recorded as an owner decision: %+v", after.Run.ResourceHold)
	}
	if after.Run.ProviderFailures != 0 || !after.Run.RetryAt.IsZero() {
		t.Fatal("hold spent provider recovery allowance")
	}
}

// The boundary the daemon depends on is now checked before a session is used at
// all. A refusal is the application's finding about the installed harness, not a
// provider fault: it stops the assignment for a person, schedules no retry, and
// says which tools were the problem without carrying anything private.
func TestCapabilityRefusalBlocksWithActionableEvidence(t *testing.T) {
	b, _ := newFixture(t, "https://model.test", &fakeDocker{})
	defer b.Close()
	id := runningRun(t, b)
	b.blockOnCapability(id, &session.CapabilityError{
		Engine: "codex", Code: session.CapabilityNativeToolsPresent, Phase: session.BeforeLaunch,
		Tools: []string{"shell", "apply_patch"},
	})
	after, _ := b.snapshot(id)
	if after.PendingStatus != "blocked" {
		t.Fatalf("a harness that cannot be restricted kept running: %q", after.PendingStatus)
	}
	if !after.Run.RetryAt.IsZero() || after.Run.ProviderFailures != 0 {
		t.Fatal("a capability refusal was treated as a retryable provider fault")
	}
	if !strings.Contains(after.PendingSummary, "shell") || !strings.Contains(after.PendingSummary, "apply_patch") {
		t.Errorf("the refusal did not say which tools were present: %q", after.PendingSummary)
	}
	if !strings.Contains(after.PendingSummary, "codex") {
		t.Errorf("the refusal did not say which engine refused: %q", after.PendingSummary)
	}
	var sawError bool
	for _, entry := range after.Run.Activity {
		if entry.Kind == "error" && entry.Status == session.CapabilityNativeToolsPresent {
			sawError = true
		}
	}
	if !sawError {
		t.Errorf("the refusal was not visible in the activity record: %+v", after.Run.Activity)
	}
}

// Nothing a worker does is retried automatically, whatever the failure looks
// like. A provider rejection used to be safe to repeat because repeating it
// re-sent one request; a native turn may already have written files and run
// commands, so repeating it would do all of that again on top of itself.
func TestNoWorkerFailureSchedulesAnAutomaticRetry(t *testing.T) {
	for _, failure := range []error{
		&session.TurnError{Engine: "claude", Code: "authentication_failed"},
		&session.ProcessError{Engine: "claude", Code: session.ProcessSignalled},
		&session.CapabilityError{Engine: "codex", Code: session.CapabilityHostedToolsMissing},
		// Even a shape the previous transport would have retried without asking.
		&completion.RequestError{Engine: "claude", Kind: completion.ErrorOverloaded, Phase: completion.PhaseResponse, Code: "overloaded"},
		errors.New("opaque"),
	} {
		b, _ := newFixture(t, "https://model.test", &fakeDocker{})
		id := failingRun(t, b)
		b.modelFailureAt(id, "native_turn", failure)
		r, _ := b.snapshot(id)
		if r.PendingStatus != "blocked" || !r.Run.RetryAt.IsZero() || r.Run.ProviderFailures != 0 {
			t.Fatalf("%T was scheduled for automatic recovery: %+v", failure, r.Run)
		}
		if !strings.Contains(r.PendingSummary, "no automatic retry is scheduled") {
			t.Errorf("%T did not say it was waiting for a person: %q", failure, r.PendingSummary)
		}
		b.Close()
	}
}
