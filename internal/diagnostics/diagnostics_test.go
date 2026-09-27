package diagnostics

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/completion"
	"github.com/shhac/lib-agent-harness/session"
)

func TestFailuresAreStructuredAndDoNotCopySensitiveErrorText(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf)
	secret := "secret-token-and-private-prompt"
	logger.Failure(Event{Component: "worker", Stage: "context_preparation", RunID: "run-1"}, fmt.Errorf("%s: %w", secret, &os.PathError{Op: "open", Path: secret, Err: os.ErrPermission}))
	var event Event
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &event); err != nil {
		t.Fatal(err)
	}
	if event.Code != "permission_denied" || event.RunID != "run-1" || event.Time.IsZero() || len(event.ErrorTypes) != 3 || event.FixableBy == "retry" {
		t.Fatalf("unexpected event: %+v", event)
	}
	if strings.Contains(buf.String(), secret) || strings.Contains(buf.String(), `"Path"`) {
		t.Fatalf("sensitive data leaked: %s", &buf)
	}
}

func TestTypedFailureKeepsProviderFactsAndRetryDisposition(t *testing.T) {
	for _, scheduled := range []bool{false, true} {
		var buf bytes.Buffer
		at := time.Now().Add(time.Minute)
		event := Event{Component: "worker", Stage: "model_completion"}
		if scheduled {
			event.RetryAt = &at
		}
		code := 1
		New(&buf).Failure(event, &completion.RequestError{Cause: harness.CauseOverloaded, Engine: harness.Claude, Phase: completion.PhaseResponse, Code: "overloaded", ExitCode: &code})
		var got Event
		if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &got); err != nil {
			t.Fatal(err)
		}
		if got.Engine != "claude" || got.Operation != "complete" || got.Family != "request" || got.Cause != "overloaded" || got.Phase != "response" || got.Code != "overloaded" || got.ExitCode == nil || *got.ExitCode != 1 || (got.FixableBy == "retry") != scheduled {
			t.Fatalf("unexpected event: %+v", got)
		}
	}
}

func TestConcurrentFailuresRemainSingleJSONRecords(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf)
	var wg sync.WaitGroup
	for range 30 {
		wg.Go(func() { logger.Failure(Event{Component: "worker", Stage: "test"}, errors.New("private payload")) })
	}
	wg.Wait()
	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	if len(lines) != 30 {
		t.Fatalf("got %d lines", len(lines))
	}
	for _, line := range lines {
		if !json.Valid(line) {
			t.Fatalf("invalid line: %s", line)
		}
	}
}

// A coding harness classifies its own failures. Reporting them as untyped meant
// an operator whose CLI login had expired saw a diagnostic with nothing in it
// they could act on.
func TestNativeHarnessFailureKeepsItsCodeAndEngine(t *testing.T) {
	status := 3
	for _, tc := range []struct {
		name   string
		err    error
		code   string
		engine string
	}{
		{"turn", &session.TurnError{Engine: "claude", Code: "authentication_failed"}, "authentication_failed", "claude"},
		{"process", &session.ProcessError{Engine: "codex", Code: session.ProcessExited, ExitCode: &status}, session.ProcessExited, "codex"},
		{"capability", &session.CapabilityError{Engine: "codex", Code: session.CapabilityNativeToolsPresent, Phase: session.BeforeLaunch}, session.CapabilityNativeToolsPresent, "codex"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			// Wrapped, because that is how the daemon receives it.
			New(&buf).Failure(Event{Component: "worker", Stage: "native_turn"}, fmt.Errorf("worker turn: %w", tc.err))
			var event Event
			if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &event); err != nil {
				t.Fatal(err)
			}
			if event.Code != tc.code {
				t.Fatalf("native diagnostic code lost: got %q", event.Code)
			}
			if event.Engine != tc.engine || event.Family == "" {
				t.Fatalf("native failure lost its engine or family: %+v", event)
			}
			if tc.name == "process" && (event.ExitCode == nil || *event.ExitCode != status) {
				t.Fatalf("exit status lost: %+v", event.ExitCode)
			}
		})
	}
}

// A caller that already knows the code keeps it. A harness diagnostic arrives
// with one, and overwriting it threw away better information than this package
// has.
func TestACallerSuppliedCodeSurvives(t *testing.T) {
	var buf bytes.Buffer
	New(&buf).Failure(Event{Component: "worker", Stage: "harness_startup", Code: "harness_stderr"}, errors.New("opaque"))
	var event Event
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &event); err != nil {
		t.Fatal(err)
	}
	if event.Code != "harness_stderr" {
		t.Fatalf("a supplied code was replaced: %q", event.Code)
	}
}
