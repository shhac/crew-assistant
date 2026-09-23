package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/testutil"
	"github.com/shhac/lib-agent-harness/completion"
)

func retryFixture(t *testing.T, handler http.HandlerFunc) (*Engine, *atomic.Int32, *[]time.Duration) {
	t.Helper()
	server := testutil.NewServer(t, handler)
	t.Cleanup(server.Close)
	var admissions atomic.Int32
	e, err := New(Config{Model: "fixture", Endpoint: server.URL, BeforeRequest: func(context.Context) error { admissions.Add(1); return nil }}, ExecutorFunc(func(context.Context, string, json.RawMessage) (any, error) {
		return map[string]string{"ok": "yes"}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	waits := []time.Duration{}
	now := time.Now()
	e.retryNow = func() time.Time { return now }
	e.retryRandom = func() float64 { return 0 }
	e.retrySleep = func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		now = now.Add(d)
		return ctx.Err()
	}
	return e, &admissions, &waits
}
func TestRetriesOnlyCurrentCompletionAfterToolExecution(t *testing.T) {
	var calls, tools atomic.Int32
	var retryMessages []string
	e, admissions, waits := retryFixture(t, func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		var body struct {
			Messages []Message `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if n > 1 {
			b, _ := json.Marshal(body.Messages)
			retryMessages = append(retryMessages, string(b))
		}
		switch n {
		case 1:
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"once","type":"function","function":{"name":"read_state","arguments":"{}"}}]}}],"usage":{"prompt_tokens":4,"completion_tokens":1,"total_tokens":5}}`)
		case 2:
			w.WriteHeader(503)
			io.WriteString(w, `{"error":{"type":"unavailable_error","message":"secret fixture"}}`)
		case 3:
			w.WriteHeader(529)
			io.WriteString(w, `{"error":{"type":"overloaded_error"}}`)
		default:
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"Recovered"}}],"usage":{"prompt_tokens":8,"completion_tokens":2,"total_tokens":10}}`)
		}
	})
	e.executor = ExecutorFunc(func(context.Context, string, json.RawMessage) (any, error) { tools.Add(1); return "Evidence", nil })
	var events []RetryEvent
	e.cfg.OnRetry = func(_ context.Context, event RetryEvent) error { events = append(events, event); return nil }
	got, err := e.Chat(context.Background(), Request{Message: "Read once"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Message != "Recovered" || tools.Load() != 1 || calls.Load() != 4 || admissions.Load() != 4 {
		t.Fatalf("replayed work or missing admission: %+v calls=%d tools=%d admission=%d", got, calls.Load(), tools.Load(), admissions.Load())
	}
	if got.Usage.Known || got.Usage.TotalTokens != 15 {
		t.Fatalf("rejected unknown usage was misreported: %+v", got.Usage)
	}
	if !reflect.DeepEqual(*waits, []time.Duration{500 * time.Millisecond, time.Second}) {
		t.Fatalf("backoff %v", *waits)
	}
	if len(retryMessages) != 3 || retryMessages[0] != retryMessages[1] || retryMessages[1] != retryMessages[2] {
		t.Fatal("retried request changed dialogue or replayed its tool")
	}
	if len(events) != 5 || events[0].Status != "waiting" || events[1].Status != "retrying" || events[4].Status != "recovered" {
		t.Fatalf("retry lifecycle: %+v", events)
	}
}
func TestRetryAfterHonouredWithoutExceedingRecoveryBudget(t *testing.T) {
	for _, tc := range []struct {
		name, header string
		budget       time.Duration
		wantCalls    int
		wantWait     time.Duration
	}{
		{"within", "7", 20 * time.Second, 2, 7 * time.Second},
		{"outside", "60", 20 * time.Second, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			e, _, waits := retryFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					w.Header().Set("Retry-After", tc.header)
					w.WriteHeader(429)
					io.WriteString(w, `{"error":{"type":"rate_limit_error"}}`)
					return
				}
				io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
			})
			e.cfg.Retry.MaxElapsed = tc.budget
			_, _, err := e.complete(context.Background(), []Message{{Role: "user", Content: "Try"}})
			if tc.wantCalls == 1 && err == nil {
				t.Fatal("provider retry hint was shortened to fit budget")
			}
			if tc.wantCalls == 2 && err != nil {
				t.Fatal(err)
			}
			if int(calls.Load()) != tc.wantCalls {
				t.Fatalf("calls %d", calls.Load())
			}
			if tc.wantWait > 0 && (len(*waits) != 1 || (*waits)[0] != tc.wantWait) {
				t.Fatalf("retry-after wait %v", *waits)
			}
			if tc.wantWait == 0 && len(*waits) != 0 {
				t.Fatal("slept after budget exhausted")
			}
		})
	}
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	if got := parseRetryAfter(now.Add(31*time.Second).Format(http.TimeFormat), now); got != 31*time.Second {
		t.Fatalf("HTTP date Retry-After %v", got)
	}
	for _, value := range []string{"-1", "not a date", "999999999999999999999"} {
		if got := parseRetryAfter(value, now); got != 0 {
			t.Fatalf("invalid retry-after %q = %v", value, got)
		}
	}
}
func TestNonTransientFailuresNeverRetry(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"auth", 401, `{"error":{"type":"authentication_error"}}`},
		{"insufficient quota", 429, `{"error":{"code":"insufficient_quota"}}`},
		{"unclassified rate", 429, `{"error":{"message":"secret"}}`},
		{"context", 503, `{"error":{"code":"context_length_exceeded"}}`},
		{"server unknown", 500, `{"error":{"message":"secret"}}`},
		{"malformed rejection", 503, `not json secret`},
		{"malformed output", 200, `not json secret`},
		{"partial output", 200, `{"choices":[{"message":{"role":"assistant","content":"partial"},"finish_reason":"length"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			e, admissions, waits := retryFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			})
			_, _, err := e.complete(context.Background(), []Message{{Role: "user", Content: "Try"}})
			if err == nil || calls.Load() != 1 || admissions.Load() != 1 || len(*waits) != 0 || strings.Contains(err.Error(), "secret") {
				t.Fatalf("unsafe retry/classification: err=%v calls=%d waits=%v", err, calls.Load(), *waits)
			}
		})
	}
}
func TestRetryCancellationAndAttemptLimit(t *testing.T) {
	t.Run("cancel during backoff", func(t *testing.T) {
		var calls atomic.Int32
		e, admissions, _ := retryFixture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(503) })
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		e.retrySleep = func(ctx context.Context, _ time.Duration) error { cancel(); return sleepForRetry(ctx, time.Hour) }
		_, _, err := e.complete(ctx, []Message{{Role: "user", Content: "Try"}})
		if !errors.Is(err, context.Canceled) || calls.Load() != 1 || admissions.Load() != 1 {
			t.Fatalf("cancellation did not stop retry: %v %d", err, calls.Load())
		}
	})
	t.Run("attempt allowance", func(t *testing.T) {
		var calls atomic.Int32
		e, admissions, waits := retryFixture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(529) })
		_, u, err := e.complete(context.Background(), []Message{{Role: "user", Content: "Try"}})
		var classified *completion.RequestError
		if !errors.As(err, &classified) || !classified.Retryable() || calls.Load() != 4 || admissions.Load() != 4 || len(*waits) != 3 || u.Known {
			t.Fatalf("unbounded/unknown retry: err=%v calls=%d usage=%+v", err, calls.Load(), u)
		}
	})
	t.Run("disabled", func(t *testing.T) {
		var calls atomic.Int32
		e, _, waits := retryFixture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(503) })
		e.cfg.Retry.MaxRetries = 0
		_, _, err := e.complete(context.Background(), []Message{{Role: "user", Content: "Try"}})
		if err == nil || calls.Load() != 1 || len(*waits) != 0 {
			t.Fatal("disabled retry executed")
		}
	})
}
func TestRetryAdmissionErrorCannotMasqueradeAsProviderRejection(t *testing.T) {
	var calls atomic.Int32
	e, _, waits := retryFixture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(503) })
	var admissions atomic.Int32
	e.cfg.BeforeRequest = func(context.Context) error {
		admissions.Add(1)
		return &completion.RequestError{Kind: completion.ErrorOverloaded}
	}
	_, _, err := e.complete(context.Background(), []Message{{Role: "user", Content: "Try"}})
	if err == nil || admissions.Load() != 1 || calls.Load() != 0 || len(*waits) != 0 {
		t.Fatalf("admission failure retried: %v requests=%d admissions=%d", err, calls.Load(), admissions.Load())
	}
}

type retryRoundTripper func(*http.Request) (*http.Response, error)

func (f retryRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestUnknownTransportIsNeverRetried(t *testing.T) {
	e, _, waits := retryFixture(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("unexpected real transport") })
	var calls int
	e.client.Transport = retryRoundTripper(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("connection dropped after request accepted")
	})
	_, _, err := e.complete(context.Background(), []Message{{Role: "user", Content: "Try"}})
	if err == nil || calls != 1 || len(*waits) != 0 {
		t.Fatalf("unknown effect retried: %v calls=%d", err, calls)
	}
}

func TestRetryJitterCapAndDeadlineBound(t *testing.T) {
	var calls atomic.Int32
	e, _, waits := retryFixture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(503) })
	e.cfg.Retry = &RetryPolicy{MaxRetries: 10, InitialDelay: time.Second, MaxDelay: 2 * time.Second, MaxElapsed: 2500 * time.Millisecond}
	e.retryRandom = func() float64 { return 1 }
	_, _, err := e.complete(context.Background(), []Message{{Role: "user", Content: "Try"}})
	if err == nil || calls.Load() != 2 || !reflect.DeepEqual(*waits, []time.Duration{time.Second}) {
		t.Fatalf("recovery deadline not enforced: calls=%d waits=%v err=%v", calls.Load(), *waits, err)
	}
	if delay := e.retryDelay(9, 0); delay != 2*time.Second {
		t.Fatalf("backoff cap exceeded: %v", delay)
	}
	e.retryRandom = func() float64 { return 0 }
	if delay := e.retryDelay(9, 0); delay != time.Second {
		t.Fatalf("equal jitter lower bound: %v", delay)
	}
}
func TestRetryReservationCanStopRecoveryBeforeAnotherRequest(t *testing.T) {
	var calls atomic.Int32
	e, _, _ := retryFixture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(503) })
	var reserved int
	e.cfg.BeforeRequest = func(context.Context) error {
		reserved++
		if reserved > 1 {
			return errors.New("allowance exhausted")
		}
		return nil
	}
	_, _, err := e.complete(context.Background(), []Message{{Role: "user", Content: "Try"}})
	if err == nil || calls.Load() != 1 || reserved != 2 {
		t.Fatalf("retry bypassed reservation: %v calls=%d reservations=%d", err, calls.Load(), reserved)
	}
}
func TestRetryHookFailureStopsBeforeAnotherInference(t *testing.T) {
	var calls atomic.Int32
	e, _, waits := retryFixture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(503) })
	auditFailure := errors.New("cannot persist retry state")
	e.cfg.OnRetry = func(context.Context, RetryEvent) error { return auditFailure }
	_, _, err := e.complete(context.Background(), []Message{{Role: "user", Content: "Try"}})
	if !errors.Is(err, auditFailure) || calls.Load() != 1 || len(*waits) != 0 {
		t.Fatalf("retry escaped failed lifecycle audit: %v calls=%d", err, calls.Load())
	}
}

func TestAdmissionFailureDoesNotEscapeAsRetryableProviderError(t *testing.T) {
	denied := &requestAdmissionError{&completion.RequestError{Kind: completion.ErrorOverloaded}}
	var rejection *completion.RequestError
	if errors.As(denied, &rejection) {
		t.Fatal("outer durable scheduler could retry an admission failure")
	}
	if !errors.Is(&requestAdmissionError{context.Canceled}, context.Canceled) {
		t.Fatal("admission cancellation identity lost")
	}
}
