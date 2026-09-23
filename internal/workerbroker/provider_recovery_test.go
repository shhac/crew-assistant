package workerbroker

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/integrations/worker"
	"github.com/shhac/lib-agent-harness/session"
)

// The automatic provider-retry machinery is gone with the transport it belonged
// to. Repeating a refused HTTP request was a safe no-op; repeating a native turn
// is not, because by the time one fails the worker may already have edited files
// and run commands. So every failure now stops the assignment for a person, and
// what these tests are about is that stopping is done honestly: the harness's
// own classification survives, an owner's decision is never overwritten, the
// work is preserved, and only an explicit resume restarts anything.

func failingRun(t *testing.T, b *Broker) string {
	t.Helper()
	resp := request(t, b, "/runs", "dispatch-one", startRequest())
	if resp.Code != 201 {
		t.Fatal(resp.Body.String())
	}
	var run worker.Run
	if err := json.Unmarshal(resp.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if err := b.update(run.ID, func(r *storedRun) error {
		r.Run.Status = "running"
		r.ModelCalls = 1
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return run.ID
}

// A failure stops the assignment where it is, keeps everything it produced, and
// waits for a person. Nothing wakes it by itself, and an ordinary message is not
// a decision.
func TestAFailedSessionStopsAndStaysExplicitlyResumable(t *testing.T) {
	exit := 2
	for _, tc := range []struct {
		name     string
		failure  error
		code     string
		evidence string
	}{
		{"expired login", &session.TurnError{Engine: "claude", Code: "authentication_failed"}, "authentication_failed", evidenceNativeHarness},
		{"lost process", &session.ProcessError{Engine: "codex", Code: session.ProcessExited, ExitCode: &exit}, session.ProcessExited, evidenceLocalProcess},
		{"refused capability check", &session.CapabilityError{Engine: "codex", Code: session.CapabilityNativeToolsPresent, Phase: session.BeforeLaunch}, session.CapabilityNativeToolsPresent, evidenceCapabilityCheck},
		{"unclassified", errors.New("secret raw transport content"), "", evidenceUntyped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := newFixture(t, "https://model.test", &fakeDocker{})
			defer b.Close()
			id := failingRun(t, b)
			b.modelFailureAt(id, "native_turn", tc.failure)
			r, _ := b.snapshot(id)
			if r.PendingStatus != "blocked" || !r.Run.RetryAt.IsZero() || r.Run.ProviderFailures != 0 {
				t.Fatalf("a native failure was scheduled for automatic recovery: %+v", r.Run)
			}
			if r.Run.ModelFailureCode != tc.code || r.Run.ModelFailureEvidence != tc.evidence {
				t.Fatalf("the harness's classification was lost: code=%q evidence=%q", r.Run.ModelFailureCode, r.Run.ModelFailureEvidence)
			}
			if strings.Contains(r.PendingSummary, "secret") {
				t.Fatalf("an opaque error's text was published: %q", r.PendingSummary)
			}
			if tc.code == "" && !strings.Contains(r.PendingSummary, "could not classify") {
				t.Errorf("an unclassifiable failure was presented as a classified one: %q", r.PendingSummary)
			}
			b.finalize(id, r.PendingStatus, r.PendingSummary, nil)
			if response := request(t, b, "/runs/"+id+"/messages", "automatic-wake", map[string]string{"message": "continue"}); response.Code != 409 {
				t.Fatal("a message automatically recovered a failed worker")
			}
			if response := request(t, b, "/runs/"+id+"/resume", "owner-retry", map[string]string{"instruction": "Owner inspected saved work and corrected setup"}); response.Code != 200 {
				t.Fatalf("explicit resume denied: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

// An owner's decision outranks a failure discovered afterwards.
func TestAFailureNeverOverwritesAnOwnerHold(t *testing.T) {
	for _, hold := range []string{"paused", "cancelled"} {
		t.Run(hold, func(t *testing.T) {
			b, _ := newFixture(t, "https://model.test", &fakeDocker{})
			defer b.Close()
			id := failingRun(t, b)
			b.update(id, func(r *storedRun) error { r.PendingStatus = hold; r.PendingSummary = "Owner hold"; return nil })
			b.modelFailureAt(id, "native_turn", &session.TurnError{Engine: "claude", Code: "authentication_failed"})
			r, _ := b.snapshot(id)
			if r.PendingStatus != hold || r.PendingSummary != "Owner hold" || r.Run.ProviderFailureKind != "" {
				t.Fatal("a failure overwrote an owner hold", r.PendingStatus, r.PendingSummary)
			}
		})
	}
}

// A failure recorded before cleanup finishes must not become an automatic
// recovery when the process restarts.
func TestRestartPreservesPendingModelBlockerAfterCleanup(t *testing.T) {
	for _, kind := range []string{"authentication", "unknown", "budget"} {
		t.Run(kind, func(t *testing.T) {
			b, cfg := newFixture(t, "https://model.test", &fakeDocker{})
			id := failingRun(t, b)
			switch kind {
			case "authentication":
				b.modelFailureAt(id, "native_turn", &session.TurnError{Engine: "claude", Code: "authentication_failed"})
			case "unknown":
				b.modelFailureAt(id, "native_turn", errors.New("unclassified failure"))
			case "budget":
				b.terminal(id, "blocked", "Worker token budget reached")
			}
			before, _ := b.snapshot(id)
			if before.Run.Status != "running" || before.PendingStatus != "blocked" {
				t.Fatal("fixture did not stop before cleanup")
			}
			if err := b.Close(); err != nil {
				t.Fatal(err)
			}
			restarted, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer restarted.Close()
			after, err := restarted.snapshot(id)
			if err != nil {
				t.Fatal(err)
			}
			if after.Run.Status != "blocked" || after.PendingStatus != "" || after.Run.Summary != before.PendingSummary {
				t.Fatalf("restart converted a blocker into automatic recovery: %+v", after.Run)
			}
			if after.Run.ProviderFailureKind != before.Run.ProviderFailureKind || !after.Run.RetryAt.IsZero() {
				t.Fatalf("restart invented a retry: %+v", after.Run)
			}
		})
	}
}

// A diagnostic stays until the worker succeeds at something. Clearing it on the
// next attempt's start would hide the reason the owner was asked to intervene.
func TestModelFailureDiagnosticsPersistAndClearOnSuccess(t *testing.T) {
	b, _ := newFixture(t, "https://model.test", &fakeDocker{})
	defer b.Close()
	id := failingRun(t, b)
	exit := 1
	b.modelFailureAt(id, "session_open", &session.ProcessError{Engine: "claude", Code: session.ProcessExited, ExitCode: &exit})
	r, _ := b.snapshot(id)
	if r.Run.ModelFailureEngine != "claude" || r.Run.ModelFailureCode != session.ProcessExited {
		t.Fatalf("the failure lost its engine or code: %+v", r.Run)
	}
	if r.Run.ModelExitCode == nil || *r.Run.ModelExitCode != exit {
		t.Fatalf("the exit status was lost: %+v", r.Run.ModelExitCode)
	}
	if r.Run.ModelFailurePhase == "" {
		t.Error("a failure with no phase was left with nothing to place it by")
	}
	b.clearProviderFailure(id)
	cleared, _ := b.snapshot(id)
	if cleared.Run.ModelFailureCode != "" || cleared.Run.ModelFailureEngine != "" || cleared.Run.ModelExitCode != nil || cleared.Run.ModelFailureEvidence != "" {
		t.Fatalf("a resolved failure was still being reported: %+v", cleared.Run)
	}
}

// Evidence names what was available to classify with, so an owner can tell a
// failure the harness described from one that arrived as an opaque error.
func TestModelFailureRecordsWhichEvidenceWasAvailable(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"harness turn", &session.TurnError{Engine: "claude", Code: "refusal"}, evidenceNativeHarness},
		{"harness process", &session.ProcessError{Engine: "claude", Code: session.ProcessSignalled}, evidenceLocalProcess},
		{"capability check", &session.CapabilityError{Engine: "claude", Code: session.CapabilityHostedToolsMissing}, evidenceCapabilityCheck},
		{"nothing", errors.New("opaque"), evidenceUntyped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyModelFailure(tc.err).evidence; got != tc.want {
				t.Fatalf("evidence %q, wanted %q", got, tc.want)
			}
		})
	}
}
