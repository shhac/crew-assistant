package work

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/quota"
	harness "github.com/shhac/lib-agent-harness"
)

func spentReading(engine harness.Engine) harness.AccountReport {
	now := time.Now()
	used, minutes, resets := 100.0, int64(300), now.Add(time.Hour)
	observation := harness.Observation{Quality: harness.Measured, ObservedAt: now}
	id, scope := "five_hour", "five_hour"
	if engine == harness.Codex {
		id, scope = "codex/primary", "codex"
	}
	return harness.AccountReport{Quota: harness.QuotaSnapshot{Observation: observation, Complete: true, Windows: []harness.QuotaWindow{{Observation: observation, ID: id, Kind: harness.QuotaSession, Scope: scope, UsedPercent: &used, WindowMinutes: &minutes, ResetsAt: &resets}}}}
}

// The sidebar reads the meter the hold reads, and says the same thing of it.
func TestUsageAgreesWithTheHold(t *testing.T) {
	t.Parallel()
	a := testLoop(t)
	a.meter = &quota.Meter{Inspect: func(_ context.Context, o harness.Provider) (harness.AccountReport, error) {
		return spentReading(o.Engine), nil
	}}
	ctx := context.Background()
	for _, engine := range config.EnginesFor(config.UseUsage) {
		got := a.Usage(ctx, engine, time.Second)
		if got.Level != quota.LevelExhausted || len(got.Windows) != 1 || got.ResetsAt == nil {
			t.Fatalf("%s: %+v", engine, got)
		}
		if wait, _ := a.UsageWait(ctx, engine); !wait.Equal(*got.ResetsAt) {
			t.Fatalf("%s: the hold waits until %v, the display says %v", engine, wait, got.ResetsAt)
		}
	}
}

// A check that fails after a login was measured spent measures nothing, so
// the sidebar still shows it out of usage, as the small models still skip it.
func TestUsageStaysOutUntilMeasuredOtherwise(t *testing.T) {
	t.Parallel()
	a := testLoop(t)
	cfg := a.Config()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Engines.Codex.Bin = self // Installed, so a failed check is only that.
	a.Config = func() config.Config { return cfg }
	var mu sync.Mutex
	fail := false
	a.meter = &quota.Meter{Inspect: func(_ context.Context, o harness.Provider) (harness.AccountReport, error) {
		mu.Lock()
		defer mu.Unlock()
		if fail {
			return harness.AccountReport{}, errors.New("app-server exited")
		}
		return spentReading(o.Engine), nil
	}}
	ctx := context.Background()
	if got := a.Usage(ctx, "codex", time.Second); got.Level != quota.LevelExhausted {
		t.Fatalf("%+v", got)
	}
	mu.Lock()
	fail = true
	mu.Unlock()
	a.meter.Forget()
	got := a.Usage(ctx, "codex", time.Second)
	if got.Level != quota.LevelExhausted || got.Missing != "out of usage when last checked; usage check failed" || len(got.Windows) != 1 || got.AsOf == nil {
		t.Fatalf("%+v", got)
	}
	if !a.OutOfUsage(a.Config().Harness("codex", "gpt-6-luna", "low")) {
		t.Fatal("the small models would use a login last measured spent")
	}
}

// A slow reading answers within the wait, and fills the meter for the next.
func TestUsageIsBoundedAndNeverSpendsALook(t *testing.T) {
	// Serial: checks a real wall-clock bound without competing package tests.
	a := testLoop(t)
	release := make(chan struct{})
	var mu sync.Mutex
	reads := 0
	a.meter = &quota.Meter{Inspect: func(_ context.Context, o harness.Provider) (harness.AccountReport, error) {
		mu.Lock()
		reads++
		mu.Unlock()
		<-release
		return spentReading(o.Engine), nil
	}}
	claude := a.Config().Harness("claude", "haiku", "")
	if a.OutOfUsage(claude) {
		t.Fatal("out of usage before any reading")
	}
	started := time.Now()
	got := a.Usage(context.Background(), "claude", 20*time.Millisecond)
	if time.Since(started) > time.Second || got.Missing != "usage check timed out" || len(got.Windows) != 0 {
		t.Fatalf("%v %+v", time.Since(started), got)
	}
	close(release)
	awaitUsageReading(t, a, claude)
	if !a.OutOfUsage(claude) {
		t.Fatal("the slow reading never reached the meter")
	}
	if got := a.Usage(context.Background(), "claude", time.Second); got.Level != quota.LevelExhausted {
		t.Fatalf("%+v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if reads != 1 {
		t.Fatalf("OutOfUsage or a cached reading inspected the login: %d reads", reads)
	}
}

// A pool only one model draws on, such as Claude's weekly Opus allowance,
// counts for neither the sidebar nor the small models, so the two never
// disagree; an engine-wide window counts for both.
func TestTheSidebarAndTheSmallModelsReadOneRule(t *testing.T) {
	t.Parallel()
	now := time.Now()
	used, minutes, resets := 100.0, int64(10080), now.Add(time.Hour)
	observation := harness.Observation{Quality: harness.Measured, ObservedAt: now}
	window := func(id string) harness.QuotaWindow {
		w := harness.QuotaWindow{Observation: observation, ID: id, Kind: harness.QuotaWeekly, Scope: id, UsedPercent: &used, WindowMinutes: &minutes, ResetsAt: &resets}
		if model, found := strings.CutPrefix(id, "seven_day_"); found {
			w.Kind, w.Model = harness.QuotaWeeklyModel, model
		}
		if model, found := strings.CutPrefix(id, "model:"); found {
			w.Kind, w.Model, w.Scope = harness.QuotaWeeklyModel, model, model
		}
		return w
	}
	for id, spent := range map[string]bool{"seven_day_opus": false, "model:Haiku": false, "seven_day": true} {
		a := testLoop(t)
		a.meter = &quota.Meter{Inspect: func(context.Context, harness.Provider) (harness.AccountReport, error) {
			return harness.AccountReport{Quota: harness.QuotaSnapshot{Observation: observation, Complete: true, Windows: []harness.QuotaWindow{window(id)}}}, nil
		}}
		sidebar := a.Usage(context.Background(), "claude", time.Second).Level == quota.LevelExhausted
		fallback := a.OutOfUsage(a.Config().Harness("claude", "haiku", ""))
		if sidebar != spent || fallback != spent {
			t.Errorf("%s spent: sidebar says %v, the small models %v; want %v", id, sidebar, fallback, spent)
		}
	}
}

// A refused small-model request has the login read again, so running out
// shows wherever usage does.
func TestARefusalHasTheLoginReadAgain(t *testing.T) {
	t.Parallel()
	a := testLoop(t)
	looked := make(chan string, 1)
	var mu sync.Mutex
	reads := 0
	a.meter = &quota.Meter{Inspect: func(_ context.Context, o harness.Provider) (harness.AccountReport, error) {
		mu.Lock()
		reads++
		mu.Unlock()
		looked <- string(o.Engine)
		return spentReading(o.Engine), nil
	}}
	a.RecheckUsage(a.Config().Harness("claude", "haiku", ""))
	select {
	case engine := <-looked:
		if engine != "claude" {
			t.Fatalf("read %s", engine)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the login wasn't read again")
	}
	// The reading is kept once the look returns, just after it was seen.
	h := a.Config().Harness("claude", "haiku", "")
	awaitUsageReading(t, a, h)
	if !a.OutOfUsage(h) {
		t.Fatal("the new reading didn't count")
	}
	mu.Lock()
	defer mu.Unlock()
	if reads != 1 {
		t.Fatalf("waiting for the refused request's reading started another inspection: %d", reads)
	}
}

// Observe joins the inspection already in flight under the meter's lock;
// once it returns, that inspection's cached reading is available.
func awaitUsageReading(t *testing.T, a *Loop, h config.Harness) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		a.meter.Observe(context.Background(), h)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the usage inspection never completed")
	}
}

// Kept figures survive failures and restarts, but never become admission data.
func TestKeptUsage(t *testing.T) {
	t.Parallel()
	a := testLoop(t)
	h := a.Config().Harness("codex", "", "")
	report := spentReading(harness.Codex)
	used := 40.0
	report.Quota.Windows[0].UsedPercent = &used
	a.meter = &quota.Meter{Inspect: func(context.Context, harness.Provider) (harness.AccountReport, error) { return report, nil }}
	first := a.Usage(context.Background(), "codex", time.Second)
	r, _ := a.meter.Cached(h)
	if first.AsOf != nil || len(first.Windows) != 1 {
		t.Fatal(first)
	}
	fail := func(context.Context, harness.Provider) (harness.AccountReport, error) {
		return harness.AccountReport{}, errors.New("failed")
	}
	a.meter = &quota.Meter{Inspect: fail}
	got := a.Usage(context.Background(), "codex", time.Second)
	if len(got.Windows) != 1 || got.Windows[0].LeftPercent != 60 || got.AsOf == nil || !got.AsOf.Equal(r.At) || got.Missing != "usage check failed" {
		t.Fatal(got)
	}

	path := filepath.Join(a.Core.StateDirectory(), "usage.json")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
	restarted := New(a.Core, a.Config, false)
	restarted.meter = &quota.Meter{Inspect: fail}
	last, ok := restarted.LastUsage("codex")
	if !ok || last.AsOf == nil || !last.AsOf.Equal(r.At) || len(last.Windows) != 1 {
		t.Fatal(last, ok)
	}
	got = restarted.Usage(context.Background(), "codex", time.Second)
	if got.AsOf == nil || len(got.Windows) != 1 || restarted.meter.Spent(h) {
		t.Fatal(got)
	}

	// An older snapshot loses its windows in Describe, then uses the kept ones.
	old := r
	old.Quota.ObservedAt = time.Now().Add(-3 * quota.CacheAge)
	a.meter = &quota.Meter{Inspect: func(context.Context, harness.Provider) (harness.AccountReport, error) {
		return harness.AccountReport{Quota: old.Quota}, nil
	}}
	// Cache an inspection with an old quota snapshot, then read only the cache.
	a.meter.Observe(context.Background(), h)
	got, ok = a.LastUsage("codex")
	if !ok {
		t.Fatal("LastUsage lost the cached reading")
	}
	if got.AsOf == nil || len(got.Windows) != 1 {
		t.Fatal(got)
	}
	different := h
	different.Home = "another-login"
	if got := a.describeUsage("codex", different, quota.Reading{}); got.AsOf != nil || len(got.Windows) != 0 {
		t.Fatal(got)
	}
	signedOut := false
	if got := a.describeUsage("codex", h, quota.Reading{LoggedIn: &signedOut}); got.Missing != "not signed in" || got.AsOf != nil {
		t.Fatal(got)
	}

	// A fresh reading replaces the saved one, and a late older one cannot win.
	newer := r
	newer.At = r.At.Add(time.Second)
	newer.Quota.Windows = append([]harness.QuotaWindow(nil), r.Quota.Windows...)
	more := 20.0
	newer.Quota.Windows[0].UsedPercent = &more
	got = a.describeUsage("codex", h, newer)
	if got.AsOf != nil || got.Windows[0].LeftPercent != 80 {
		t.Fatal(got)
	}
	a.describeUsage("codex", h, r)
	got = a.describeUsage("codex", h, quota.Reading{Err: context.DeadlineExceeded})
	if got.AsOf == nil || !got.AsOf.Equal(newer.At) || got.Windows[0].LeftPercent != 80 || got.Missing != "usage check timed out" {
		t.Fatal(got)
	}
}

func TestCorruptKeptUsageIsReplaced(t *testing.T) {
	t.Parallel()
	a := testLoop(t)
	path := filepath.Join(a.Core.StateDirectory(), "usage.json")
	if err := os.WriteFile(path, []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, ok := a.LastUsage("codex"); ok || got.AsOf != nil {
		t.Fatal(got)
	}
	a.meter = &quota.Meter{Inspect: func(context.Context, harness.Provider) (harness.AccountReport, error) {
		return spentReading(harness.Codex), nil
	}}
	a.Usage(context.Background(), "codex", time.Second)
	restarted := New(a.Core, a.Config, false)
	if got, ok := restarted.LastUsage("codex"); !ok || got.AsOf == nil || len(got.Windows) != 1 {
		t.Fatal(got)
	}
}
