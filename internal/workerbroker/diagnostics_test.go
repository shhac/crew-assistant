package workerbroker

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/diagnostics"
	"github.com/shhac/lib-agent-harness/completion"
)

func TestModelFailureReportsRunAndStageWithoutLeakingPayload(t *testing.T) {
	b, _ := newFixture(t, "http://127.0.0.1:1", &fakeDocker{})
	defer b.Close()
	id := failingRun(t, b)
	var buf bytes.Buffer
	b.cfg.Diagnostics = diagnostics.New(&buf)
	b.modelFailureAt(id, "context_preparation", errors.New("private-model-response"))
	var event diagnostics.Event
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &event); err != nil {
		t.Fatal(err)
	}
	if event.RunID != id || event.ProjectID != "project-one" || event.Stage != "context_preparation" || event.Code != "untyped_error" {
		t.Fatalf("unexpected event: %+v", event)
	}
	if strings.Contains(buf.String(), "private-model-response") {
		t.Fatal("raw error leaked")
	}
	run, _ := b.snapshot(id)
	if run.PendingStatus != "blocked" || !run.Run.RetryAt.IsZero() {
		t.Fatalf("unknown failure retried: %+v", run.Run)
	}
}

// A cooldown saved by an earlier version of this daemon still travels with the
// diagnostic, so an operator reading their log is told the same thing the run
// state says rather than being left to compare the two.
func TestADurableCooldownTravelsWithTheDiagnostic(t *testing.T) {
	b, _ := newFixture(t, "http://127.0.0.1:1", &fakeDocker{})
	defer b.Close()
	id := failingRun(t, b)
	if err := b.update(id, func(r *storedRun) error {
		r.Run.Status = "retry_wait"
		r.Run.RetryAt = now().Add(time.Minute)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	b.cfg.Diagnostics = diagnostics.New(&buf)
	b.reportFailure(id, "native_turn", &completion.RequestError{Kind: completion.ErrorOverloaded, Engine: "claude", Phase: completion.PhaseResponse, Code: "overloaded"})
	var event diagnostics.Event
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &event); err != nil {
		t.Fatal(err)
	}
	run, _ := b.snapshot(id)
	if event.RetryAt == nil || !event.RetryAt.Equal(run.Run.RetryAt) || event.FixableBy != "retry" || event.Code != "overloaded" {
		t.Fatalf("unexpected event: %+v", event)
	}
}

func TestRetryDiagnosticRequiresEffectiveWaitAndFutureCooldown(t *testing.T) {
	for _, tt := range []struct {
		name, status, pending string
		delay                 time.Duration
		scheduled             bool
	}{
		{"pending", "running", "retry_wait", time.Minute, true},
		{"published", "retry_wait", "", time.Minute, true},
		{"resumed", "running", "", time.Minute, false},
		{"stale", "retry_wait", "", -time.Minute, false},
		{"paused", "retry_wait", "paused", time.Minute, false},
		{"blocked", "running", "blocked", time.Minute, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b, _ := newFixture(t, "http://127.0.0.1:1", &fakeDocker{})
			defer b.Close()
			id := failingRun(t, b)
			if err := b.update(id, func(r *storedRun) error {
				r.Run.Status = tt.status
				r.PendingStatus = tt.pending
				r.Run.RetryAt = now().Add(tt.delay)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			b.cfg.Diagnostics = diagnostics.New(&buf)
			b.reportFailure(id, "model_completion", &completion.RequestError{Kind: completion.ErrorOverloaded})
			var event diagnostics.Event
			if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &event); err != nil {
				t.Fatal(err)
			}
			if (event.RetryAt != nil) != tt.scheduled || (event.FixableBy == "retry") != tt.scheduled {
				t.Fatalf("misrepresented retry disposition: %+v", event)
			}
		})
	}
}

// A failure whose record could not be written must not report the cooldown the
// run happened to be carrying as if this attempt had scheduled one.
func TestFailedPersistenceDoesNotReportAPreviousCooldownAsScheduled(t *testing.T) {
	b, _ := newFixture(t, "http://127.0.0.1:1", &fakeDocker{})
	defer b.Close()
	id := failingRun(t, b)
	if err := b.update(id, func(r *storedRun) error { r.Run.RetryAt = now().Add(-time.Minute); return nil }); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	b.cfg.Diagnostics = diagnostics.New(&buf)
	// Synthetic filesystem failure before rename: update must restore prior state.
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	b.cfg.StateDir = blocked
	b.modelFailureAt(id, "native_turn", errors.New("opaque failure"))
	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	if len(lines) == 0 {
		t.Fatal("no diagnostic was written at all")
	}
	var event diagnostics.Event
	if err := json.Unmarshal(lines[len(lines)-1], &event); err != nil {
		t.Fatal(err)
	}
	if event.RetryAt != nil || event.FixableBy == "retry" {
		t.Fatalf("failed persistence claimed a scheduled retry: %+v", event)
	}
	run, _ := b.snapshot(id)
	if run.PendingStatus != "" || run.Run.RetryAt.After(now()) {
		t.Fatal("failed persistence was not rolled back")
	}
}
