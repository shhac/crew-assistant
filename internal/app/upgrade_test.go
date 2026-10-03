package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/diagnostics"
	"github.com/shhac/crew-assistant/internal/upgrade"
)

type updateHTTP func(*http.Request) (*http.Response, error)

func (f updateHTTP) Do(r *http.Request) (*http.Response, error) { return f(r) }

type updateClock struct{ timers chan *updateTimer }
type updateTimer struct {
	ch       chan time.Time
	duration time.Duration
}

func (c updateClock) Now() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }
func (c updateClock) NewTimer(d time.Duration) upgrade.Timer {
	timer := &updateTimer{make(chan time.Time, 1), d}
	c.timers <- timer
	return timer
}
func (t *updateTimer) C() <-chan time.Time { return t.ch }
func (t *updateTimer) Stop() bool          { return true }
func updateAwait[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(3 * time.Second):
		t.Fatal("update loop didn't advance")
		var v T
		return v
	}
}

func TestUpdatesSnapshotSettingsAndMatchingReleaseNudge(t *testing.T) {
	for _, mode := range []string{"off", "ask"} {
		t.Run(mode, func(t *testing.T) {
			cfg := config.Default()
			cfg.Upgrade.Mode = mode
			cfg.Upgrade.SourceRepo = "fixture/application"
			store, err := core.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			clock := updateClock{make(chan *updateTimer, 10)}
			checker := upgrade.New("v1.0.0", updateHTTP(func(r *http.Request) (*http.Response, error) {
				body := `version "1.1.0"`
				if strings.Contains(r.URL.Path, "releases") {
					body = `{"tag_name":"v1.1.0","body":"Notes","html_url":"https://fixture.invalid/release"}`
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
			}), clock)
			path := filepath.Join(t.TempDir(), "config.json")
			a := New(core.NewService(store, cfg), cfg, path, Options{Version: "v1.0.0", Checker: checker})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- a.runUpdates(ctx) }()
			timer := updateAwait(t, clock.timers)
			if timer.duration != 6*time.Hour {
				t.Fatal(timer.duration)
			}
			snap, err := a.Snapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			decisions := 0
			if mode == "ask" {
				decisions = 1
			}
			if snap.Update.Running != "v1.0.0" || snap.Update.Available != "v1.1.0" || snap.Update.Notes != "Notes" || snap.Update.Mode != mode || len(snap.Decisions) != decisions {
				t.Fatal(snap.Update, snap.Decisions)
			}
			a.releaseRecorded("other/application", "v1.2.0")
			select {
			case extra := <-clock.timers:
				t.Fatal("nonmatching release triggered check", extra)
			case <-time.After(50 * time.Millisecond):
			}
			a.releaseRecorded("fixture/application", "v1.2.0")
			timer = updateAwait(t, clock.timers)
			if timer.duration != upgrade.FastInterval {
				t.Fatal(timer.duration)
			}
			// A nonmatching release didn't queue a second check.
			select {
			case extra := <-clock.timers:
				t.Fatal("nonmatching release triggered check", extra)
			default:
			}
			// Both dashboard saves and disk reloads must leave unrelated settings
			// outside the release polling schedule.
			cfg.Assistant.Theme = config.ThemeLight
			if err := a.UpdateConfig(cfg); err != nil {
				t.Fatal(err)
			}
			cfg.Limits.MaxModelCallsPerDay++
			if err := config.Save(path, cfg); err != nil {
				t.Fatal(err)
			}
			if changed, err := a.ReloadConfig(); err != nil || !changed {
				t.Fatal(changed, err)
			}
			select {
			case extra := <-clock.timers:
				t.Fatal("unrelated config woke checker", extra)
			case <-time.After(50 * time.Millisecond):
			}
			cfg.Upgrade.Mode = "off"
			cfg.Upgrade.CheckInterval = "3h"
			if err := a.UpdateConfig(cfg); err != nil {
				t.Fatal(err)
			}
			updateAwait(t, clock.timers)
			saved, err := config.Load(path)
			if err != nil || saved.Upgrade.Mode != "off" {
				t.Fatal(saved.Upgrade, err)
			}
			cancel()
			if err := updateAwait(t, done); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestUpdatesDisabledForDemoAndDev(t *testing.T) {
	for _, demo := range []bool{false, true} {
		t.Run(fmt.Sprint(demo), func(t *testing.T) {
			a := testApp(t)
			a.Demo = demo
			a.version = "dev"
			if demo {
				a.version = "v1.0.0"
			}
			a.checker = upgrade.New(a.version, updateHTTP(func(*http.Request) (*http.Response, error) {
				t.Fatal("disabled build accessed network")
				return nil, nil
			}), nil)
			if err := a.runUpdates(context.Background()); err != nil {
				t.Fatal(err)
			}
			v, err := a.Snapshot(context.Background())
			if err != nil || v.Update.Unavailable == "" || v.Update.Running != a.version {
				t.Fatal(v.Update, err)
			}
		})
	}
}

func TestUpdateStartupAndRecordFailuresRetryAndStayVisible(t *testing.T) {
	for _, stage := range []string{"reconcile", "record"} {
		t.Run(stage, func(t *testing.T) {
			a := testApp(t)
			a.version = "v1.0.0"
			clock := updateClock{make(chan *updateTimer, 8)}
			a.checker = upgrade.New(a.version, updateHTTP(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`version "1.0.0"`))}, nil
			}), clock)
			var logs bytes.Buffer
			a.Diagnostics = diagnostics.New(&logs)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			reconciles, records := 0, 0
			go func() {
				done <- a.runUpdateChecks(ctx, func(ctx context.Context) error {
					reconciles++
					if stage == "reconcile" && reconciles <= 2 {
						return errors.New("secret must not reach dashboard or logs")
					}
					return a.Core.ReconcileRunningUpdate(ctx, a.version)
				}, func(ctx context.Context, r upgrade.Result) error {
					records++
					if stage == "record" && records == 1 {
						return errors.New("secret must not reach dashboard or logs")
					}
					return a.Core.RecordUpdateCheck(ctx, a.version, r)
				})
			}()
			timer := updateAwait(t, clock.timers)
			v, err := a.Snapshot(ctx)
			if err != nil || !strings.Contains(v.Update.Error, "will retry") {
				t.Fatal(v.Update, err)
			}
			if !strings.Contains(logs.String(), `"stage":"update_checks"`) || strings.Contains(logs.String(), "secret") || strings.Contains(v.Update.Error, "secret") {
				t.Fatal(logs.String(), v.Update.Error)
			}
			timer.ch <- clock.Now()
			updateAwait(t, clock.timers)
			v, err = a.Snapshot(ctx)
			if err != nil || v.Update.Error != "" || v.Update.CheckedAt.IsZero() {
				t.Fatal(v.Update, err)
			}
			cancel()
			if err := updateAwait(t, done); err != nil {
				t.Fatal(err)
			}
			if stage == "reconcile" && reconciles != 3 {
				t.Fatal(reconciles)
			}
			if stage == "record" && records != 2 {
				t.Fatal(records)
			}
		})
	}
}
