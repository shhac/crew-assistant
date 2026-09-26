package quota

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/lib-agent-harness/session"
)

// twoWindows is a Codex reading with a 5-hour and a weekly window.
func twoWindows(fiveHourUsed, weekUsed float64, resets *time.Time) session.QuotaSnapshot {
	q := fixture(fiveHourUsed)
	q.Windows[0].ResetsAt = resets
	week := fixture(weekUsed).Windows[0]
	minutes := int64(10080)
	week.ID, week.WindowMinutes = "codex/secondary", &minutes
	q.Windows = append(q.Windows, week)
	return q
}

func TestDescribeShowsEachMeasuredWindow(t *testing.T) {
	now := time.Now()
	resets := now.Add(90 * time.Minute)
	got := Describe(Reading{Quota: twoWindows(30, 60, &resets)}, codex, tenAndTen, now)
	if got.Level != LevelOK || got.Missing != "" || len(got.Windows) != 2 || got.ResetsAt != nil {
		t.Fatalf("%+v", got)
	}
	five, week := got.Windows[0], got.Windows[1]
	if five.Name != "5-hour" || five.LeftPercent != 70 || five.ResetsAt == nil || !five.ResetsAt.Equal(resets) || five.FloorPercent != 10 {
		t.Fatalf("5-hour window: %+v", five)
	}
	// A window that gives no reset shows none, rather than a guess.
	if week.Name != "weekly" || week.LeftPercent != 40 || week.ResetsAt != nil {
		t.Fatalf("weekly window: %+v", week)
	}
	named := twoWindows(30, 60, nil)
	named.Windows[0].Name = "Primary"
	if got := Describe(Reading{Quota: named}, codex, tenAndTen, now); got.Windows[0].Name != "Primary" {
		t.Fatalf("the CLI's own name was not kept: %+v", got.Windows[0])
	}
}

// Low is exactly what holds team work; exhausted is nothing left.
func TestDescribeLevelsAgreeWithTheHold(t *testing.T) {
	now := time.Now()
	resets := now.Add(time.Hour)
	for _, tc := range []struct {
		name       string
		used       float64
		level      Level
		fiveHourAt int
	}{{"above the floor", 89, LevelOK, 10}, {"at the floor", 90, LevelOK, 10}, {"below the floor", 95, LevelLow, 10}, {"floor off", 95, LevelOK, 0}, {"spent", 100, LevelExhausted, 10}, {"over", 120, LevelExhausted, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			floors := Floors{FiveHour: tc.fiveHourAt, Week: 10}
			q := twoWindows(tc.used, 0, &resets)
			got := Describe(Reading{Quota: q}, codex, floors, now)
			if got.Level != tc.level || got.Windows[0].Level != tc.level {
				t.Fatalf("%+v", got)
			}
			if held := Evaluate(q, codex, floors, now).Held; held != (tc.level != LevelOK) && tc.fiveHourAt > 0 {
				t.Fatalf("the hold says held=%v, the display %s", held, tc.level)
			}
			if tc.level != LevelOK && (got.ResetsAt == nil || !got.ResetsAt.Equal(resets)) {
				t.Fatalf("reset missing: %+v", got)
			}
			if tc.level == LevelExhausted && got.Windows[0].LeftPercent != 0 {
				t.Fatalf("left below zero: %+v", got.Windows[0])
			}
		})
	}
	over := true
	q := twoWindows(100, 0, nil)
	q.UsingOverage = &over
	if got := Describe(Reading{Quota: q}, codex, tenAndTen, now); !got.Overage || got.ResetsAt != nil {
		t.Fatalf("overage or an unknown reset: %+v", got)
	}
}

// Nothing measured is said plainly, never shown as a figure.
func TestDescribeSaysWhyNothingWasMeasured(t *testing.T) {
	now := time.Now()
	no := false
	for _, tc := range []struct {
		name    string
		reading Reading
		missing string
	}{
		{"not installed", Reading{Err: fmt.Errorf("start codex: %w", exec.ErrNotFound)}, "not installed"},
		{"not signed in", Reading{LoggedIn: &no, Quota: fixture(10)}, "not signed in"},
		{"not reported", Reading{Quota: session.QuotaSnapshot{Observation: session.Observation{Quality: session.Measured, ObservedAt: now}, Complete: true}}, "usage not reported"},
		{"nothing at all", Reading{}, "usage not reported"},
		{"unrelated pool", func() Reading { q := fixture(10); q.Windows[0].Scope = "code-review"; return Reading{Quota: q} }(), "usage not reported"},
		{"stale", func() Reading { q := fixture(10); q.ObservedAt = now.Add(-time.Hour); return Reading{Quota: q} }(), "usage not reported"},
		{"failed", Reading{Err: errors.New("app-server exited")}, "usage check failed"},
		{"timed out", Reading{Err: context.DeadlineExceeded}, "usage check timed out"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Describe(tc.reading, codex, tenAndTen, now)
			if got.Missing != tc.missing || got.Level != LevelUnknown || len(got.Windows) != 0 || got.Windows == nil {
				t.Fatalf("%+v", got)
			}
		})
	}
	// A partial failure keeps what was measured.
	if got := Describe(Reading{Quota: fixture(40), Err: errors.New("account read failed")}, codex, tenAndTen, now); got.Missing != "" || len(got.Windows) != 1 {
		t.Fatalf("%+v", got)
	}
}

func TestExhaustedIsANothingLeftWindowThatGovernsTheModel(t *testing.T) {
	if !Exhausted(twoWindows(100, 0, nil), codex) || !Exhausted(twoWindows(0, 120, nil), codex) {
		t.Fatal("a spent window not counted")
	}
	invalid := twoWindows(100, 0, nil)
	invalid.Windows[0].Invalidated = true
	if Exhausted(invalid, codex) || Exhausted(twoWindows(99, 0, nil), codex) || Exhausted(session.QuotaSnapshot{}, codex) {
		t.Fatal("an invalid, unspent or missing window counted as spent")
	}
	claudeWeekly := fixture(100)
	claudeWeekly.Windows[0].ID, claudeWeekly.Windows[0].Scope = "seven_day_opus", "opus"
	if Exhausted(claudeWeekly, config.Harness{Engine: "claude", Model: "haiku"}) || !Exhausted(claudeWeekly, config.Harness{Engine: "claude", Model: "opus"}) {
		t.Fatal("an Opus pool governed the wrong model")
	}
}

// A reading that measures only another pool, such as Codex code reviews,
// says nothing of the spent window: only a reading of that window clears it.
func TestOutOfUsageIsClearedOnlyByTheSpentWindow(t *testing.T) {
	unrelated := fixture(10)
	unrelated.Windows[0].ID, unrelated.Windows[0].Scope = "codex/code-review", "code-review"
	script := []session.QuotaSnapshot{twoWindows(100, 0, nil), unrelated, twoWindows(30, 0, nil)}
	looks := 0
	meter := Meter{Inspect: func(context.Context, session.Options) (session.Inspection, error) {
		q := script[looks]
		looks++
		return session.Inspection{Quota: q}, nil
	}}
	model := config.Harness{Engine: "codex", Model: "gpt-6-luna"}
	for i, want := range []bool{true, true, false} {
		meter.Forget()
		meter.Observe(context.Background(), model)
		if meter.OutOfUsage(model) != want || meter.Spent(model) != want {
			t.Fatalf("after look %d: out=%v, want %v", i+1, !want, want)
		}
	}
}

// A login measured spent stays spent until a later reading measures usage
// left: not when time passes, its reset goes by or a look fails. While it is
// spent it is only looked at, in the background, once per Recheck.
func TestOutOfUsageHoldsUntilAReadingMeasuresUsageLeft(t *testing.T) {
	type look struct {
		quota session.QuotaSnapshot
		err   error
	}
	resets := time.Now().Add(time.Millisecond)
	invalidated := fixture(50)
	invalidated.Windows[0].Invalidated = true
	script := []look{{quota: twoWindows(100, 0, &resets)}, {err: errors.New("app-server exited")}, {quota: invalidated}, {quota: twoWindows(40, 0, nil)}}
	var mu sync.Mutex
	looks, gate := 0, make(chan struct{}, 1)
	meter := Meter{Inspect: func(context.Context, session.Options) (session.Inspection, error) {
		mu.Lock()
		next, first := script[looks], looks == 0
		looks++
		mu.Unlock()
		if !first {
			<-gate
		}
		return session.Inspection{Quota: next.quota}, next.err
	}}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	model := config.Harness{Engine: "codex", Bin: self}
	counted := func() int { mu.Lock(); defer mu.Unlock(); return looks }
	if meter.OutOfUsage(model) {
		t.Fatal("out of usage before any reading")
	}
	meter.Observe(context.Background(), model)
	time.Sleep(5 * time.Millisecond) // Its reset has passed; no reading says so.
	if !meter.OutOfUsage(model) || counted() != 1 {
		t.Fatal("a spent login was not skipped, or was looked at again too soon", counted())
	}
	due := func() {
		meter.update(IdentityFor(model), func(e *entry) { e.last.At = time.Now().Add(-Recheck) })
	}
	settled := func() {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for {
			meter.mu.Lock()
			busy := meter.entries[IdentityFor(model)].rechecking
			meter.mu.Unlock()
			if !busy {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("the background look never finished")
			}
			time.Sleep(time.Millisecond)
		}
	}
	for i, want := range []bool{true, true, false} {
		due()
		started := time.Now()
		// The look under way holds the gate; asking again neither waits nor
		// starts another.
		if !meter.OutOfUsage(model) || !meter.OutOfUsage(model) || time.Since(started) > time.Second {
			t.Fatal("asking waited, or cleared before a reading")
		}
		gate <- struct{}{}
		settled()
		if got := meter.OutOfUsage(model); got != want || counted() != i+2 {
			t.Fatalf("look %d: out=%v after %d looks", i+2, got, counted())
		}
	}
}

// The harness only says a CLI couldn't start; the meter says when it isn't
// there at all.
func TestObserveTellsAMissingCLIFromAFailedCheck(t *testing.T) {
	meter := Meter{Inspect: func(context.Context, session.Options) (session.Inspection, error) {
		return session.Inspection{}, errors.New("codex harness could not be started")
	}}
	missing := config.Harness{Engine: "codex", Bin: filepath.Join(t.TempDir(), "codex")}
	if got := Describe(meter.Observe(context.Background(), missing), missing, tenAndTen, time.Now()); got.Missing != "not installed" {
		t.Fatalf("%+v", got)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	present := config.Harness{Engine: "codex", Bin: self}
	if got := Describe(meter.Observe(context.Background(), present), present, tenAndTen, time.Now()); got.Missing != "usage check failed" {
		t.Fatalf("%+v", got)
	}
}

// The last reading is there without waiting on an inspection under way.
func TestCachedNeverWaitsOnAnInspection(t *testing.T) {
	release := make(chan struct{})
	calls := 0
	meter := Meter{Inspect: func(context.Context, session.Options) (session.Inspection, error) {
		calls++
		if calls == 2 {
			<-release
		}
		return session.Inspection{Quota: fixture(20)}, nil
	}}
	model := config.Default().AssistantHarness()
	if _, ok := meter.Cached(model); ok {
		t.Fatal("a reading before any inspection")
	}
	meter.Observe(context.Background(), model)
	meter.mu.Lock()
	for key, e := range meter.entries {
		e.last.At = time.Now().Add(-2 * CacheAge)
		meter.entries[key] = e
	}
	meter.mu.Unlock()
	done := make(chan struct{})
	go func() { meter.Observe(context.Background(), model); close(done) }()
	time.Sleep(10 * time.Millisecond)
	r, ok := meter.Cached(model)
	if !ok || len(r.Quota.Windows) != 1 {
		t.Fatalf("%+v", r)
	}
	close(release)
	<-done
}
