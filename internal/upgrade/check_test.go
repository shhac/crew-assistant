package upgrade

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
)

type fakeHTTP func(*http.Request) (*http.Response, error)

func (f fakeHTTP) Do(r *http.Request) (*http.Response, error) { return f(r) }
func response(body string, code int) *http.Response {
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body))}
}
func sources() config.UpgradeSettings {
	u := config.DefaultUpgrade()
	u.FormulaURL = "https://fixture.invalid/formula"
	u.ReleaseAPIURL = "https://fixture.invalid/releases/latest"
	return u
}

func TestCheckFormulaGatesAndComparesVersions(t *testing.T) {
	for _, tc := range []struct {
		running, formula, want string
		calls                  int
	}{
		{"v1.9.0", "1.10.0", "v1.10.0", 2}, {"1.10.0", "v1.10.0", "", 1},
		{"v2.0.0", "1.10.0", "", 1}, {"v1.0.0-rc.2", "1.0.0", "v1.0.0", 2},
		{"v1.0.0", "1.0.0-rc.3", "", 1}, {"v1.0.0+build", "1.0.0", "", 1},
	} {
		t.Run(tc.running+"/"+tc.formula, func(t *testing.T) {
			calls := 0
			c := New(tc.running, fakeHTTP(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Path == "/formula" {
					return response(fmt.Sprintf("version %q", tc.formula), 200), nil
				}
				return response(fmt.Sprintf(`{"tag_name":%q,"body":"Changes","html_url":"https://fixture.invalid/release"}`, tc.formula), 200), nil
			}), nil)
			got, err := c.Check(context.Background(), sources())
			if err != nil || got.Error != "" || got.Available != tc.want || calls != tc.calls {
				t.Fatalf("%+v %v calls=%d", got, err, calls)
			}
			if tc.want != "" && got.Notes != "Changes" {
				t.Fatal(got)
			}
		})
	}
	// Even a GitHub version ahead of the formula isn't requested or offered.
	c := New("v1.0.0", fakeHTTP(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/formula" {
			t.Fatal("notes requested before formula caught up")
		}
		return response(`url "https://fixture.invalid/archive/refs/tags/v1.0.0.tar.gz"`, 200), nil
	}), nil)
	got, err := c.Check(context.Background(), sources())
	if err != nil || got.Available != "" || got.Error != "" {
		t.Fatal(got, err)
	}
}
func TestCheckFailuresAndBounds(t *testing.T) {
	for _, bad := range []string{"network", "rate-limit", "malformed-formula", "malformed-release", "mismatch", "oversized"} {
		t.Run(bad, func(t *testing.T) {
			c := New("v1.0.0", fakeHTTP(func(r *http.Request) (*http.Response, error) {
				if bad == "network" {
					return nil, errors.New("secret should not be recorded")
				}
				if bad == "rate-limit" {
					return response("", 429), nil
				}
				if r.URL.Path == "/formula" {
					if bad == "malformed-formula" {
						return response(`version "banana"`, 200), nil
					}
					if bad == "oversized" {
						return response(strings.Repeat("x", bodyLimit+1), 200), nil
					}
					return response(`version "1.1.0"`, 200), nil
				}
				if bad == "mismatch" {
					return response(`{"tag_name":"v1.2.0","body":"later","html_url":"https://fixture.invalid/later"}`, 200), nil
				}
				return response("broken JSON", 200), nil
			}), nil)
			got, err := c.Check(context.Background(), sources())
			if err != nil || got.Error == "" || got.Available != "" || got.CheckedAt.IsZero() || strings.Contains(got.Error, "secret") {
				t.Fatal(got, err)
			}
		})
	}
	c := New("v1.0.0", fakeHTTP(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/formula" {
			return response(`version "1.1.0"`, 200), nil
		}
		if r.URL.Path == "/releases/latest" {
			return response(`{"tag_name":"v1.2.0"}`, 200), nil
		}
		if r.URL.Path != "/releases/tags/v1.1.0" {
			t.Fatal(r.URL.Path)
		}
		return response(fmt.Sprintf(`{"tag_name":"v1.1.0","body":%q,"html_url":"https://fixture.invalid/release"}`, strings.Repeat("a", NotesLimit+100)), 200), nil
	}), nil)
	got, err := c.Check(context.Background(), sources())
	if err != nil || got.Error != "" || got.Available != "v1.1.0" || len(got.Notes) > NotesLimit+12 || !strings.Contains(got.Notes, "[truncated]") {
		t.Fatal(got, err)
	}
}

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers chan *fakeTimer
}
type fakeTimer struct {
	ch       chan time.Time
	duration time.Duration
}

func (f *fakeClock) Now() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now }
func (f *fakeClock) NewTimer(d time.Duration) Timer {
	t := &fakeTimer{make(chan time.Time, 1), d}
	f.timers <- t
	return t
}
func (t *fakeTimer) C() <-chan time.Time     { return t.ch }
func (t *fakeTimer) Stop() bool              { return true }
func (f *fakeClock) advance(d time.Duration) { f.mu.Lock(); f.now = f.now.Add(d); f.mu.Unlock() }
func await[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(3 * time.Second):
		t.Fatal("loop did not advance")
		var v T
		return v
	}
}

func TestRunNudgeFastPollingAndExpiry(t *testing.T) {
	for _, caughtUp := range []bool{false, true} {
		t.Run(fmt.Sprint(caughtUp), func(t *testing.T) {
			clock := &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), timers: make(chan *fakeTimer, 8)}
			c := New("v1.0.0", fakeHTTP(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/formula" {
					if caughtUp && clock.Now().After(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
						return response(`version "1.1.0"`, 200), nil
					}
					return response(`version "1.0.0"`, 200), nil
				}
				return response(`{"tag_name":"v1.1.0","body":"New","html_url":"https://fixture.invalid/release"}`, 200), nil
			}), clock)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			recorded := make(chan Result, 8)
			done := make(chan error, 1)
			go func() {
				done <- c.Run(ctx, sources, func(_ context.Context, r Result) error { recorded <- r; return nil }, nil)
			}()
			await(t, recorded)
			if timer := await(t, clock.timers); timer.duration != 6*time.Hour {
				t.Fatal(timer.duration)
			}
			c.Nudge("v1.1.0")
			await(t, recorded)
			timer := await(t, clock.timers)
			if timer.duration != FastInterval {
				t.Fatal(timer.duration)
			}
			advance := FastWindow
			if caughtUp {
				advance = FastInterval
			}
			clock.advance(advance)
			timer.ch <- clock.Now()
			got := await(t, recorded)
			timer = await(t, clock.timers)
			if timer.duration != 6*time.Hour || (caughtUp && got.Available != "v1.1.0") {
				t.Fatal(timer.duration, got)
			}
			cancel()
			if err := await(t, done); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestDevAndCancellationNeverRecord(t *testing.T) {
	calls := 0
	c := New("dev", fakeHTTP(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("unexpected") }), nil)
	if _, err := c.Check(context.Background(), sources()); err == nil {
		t.Fatal("dev accepted")
	}
	if err := c.Run(context.Background(), sources, func(context.Context, Result) error { t.Fatal("recorded dev"); return nil }, nil); err != nil || calls != 0 {
		t.Fatal(err, calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	c = New("v1.0.0", fakeHTTP(func(r *http.Request) (*http.Response, error) {
		cancel()
		<-r.Context().Done()
		return nil, r.Context().Err()
	}), nil)
	if err := c.Run(ctx, sources, func(context.Context, Result) error { t.Fatal("recorded cancelled request"); return nil }, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRunRetriesFailedRecordOnTimerAndNudge(t *testing.T) {
	for _, nudge := range []bool{false, true} {
		t.Run(fmt.Sprint(nudge), func(t *testing.T) {
			clock := &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), timers: make(chan *fakeTimer, 8)}
			c := New("v1.0.0", fakeHTTP(func(*http.Request) (*http.Response, error) { return response(`version "1.0.0"`, 200), nil }), clock)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			attempts := make(chan Result, 8)
			failures := make(chan error, 8)
			done := make(chan error, 1)
			writeErr := errors.New("temporary state write failure")
			go func() {
				count := 0
				done <- c.Run(ctx, sources, func(_ context.Context, r Result) error {
					count++
					attempts <- r
					if count == 1 {
						return writeErr
					}
					return nil
				}, func(err error) { failures <- err })
			}()
			await(t, attempts)
			if err := await(t, failures); err != writeErr {
				t.Fatal(err)
			}
			timer := await(t, clock.timers)
			if timer.duration != 6*time.Hour {
				t.Fatal(timer.duration)
			}
			if nudge {
				c.Nudge("v1.1.0")
			} else {
				clock.advance(timer.duration)
				timer.ch <- clock.Now()
			}
			await(t, attempts)
			await(t, clock.timers)
			select {
			case err := <-failures:
				t.Fatal("successful retry reported a failure", err)
			default:
			}
			cancel()
			if err := await(t, done); err != nil {
				t.Fatal(err)
			}
		})
	}
}
