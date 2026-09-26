package work

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/quota"
	"github.com/shhac/lib-agent-harness/session"
)

func spentReading(engine session.Engine) session.Inspection {
	now := time.Now()
	used, minutes, resets := 100.0, int64(300), now.Add(time.Hour)
	observation := session.Observation{Quality: session.Measured, ObservedAt: now}
	id, scope := "five_hour", ""
	if engine == session.Codex {
		id, scope = "codex/primary", "codex"
	}
	return session.Inspection{Quota: session.QuotaSnapshot{Observation: observation, Complete: true, Windows: []session.QuotaWindow{{Observation: observation, ID: id, Scope: scope, UsedPercent: &used, WindowMinutes: &minutes, ResetsAt: &resets}}}}
}

// The sidebar reads the meter the hold reads, and says the same thing of it.
func TestUsageAgreesWithTheHold(t *testing.T) {
	a := testLoop(t)
	a.meter = &quota.Meter{Inspect: func(_ context.Context, o session.Options) (session.Inspection, error) {
		return spentReading(o.Engine), nil
	}}
	ctx := context.Background()
	for _, engine := range config.CLIEngineNames {
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
	a.meter = &quota.Meter{Inspect: func(_ context.Context, o session.Options) (session.Inspection, error) {
		mu.Lock()
		defer mu.Unlock()
		if fail {
			return session.Inspection{}, errors.New("app-server exited")
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
	if got.Level != quota.LevelExhausted || got.Missing != "out of usage when last checked; usage check failed" || len(got.Windows) != 0 {
		t.Fatalf("%+v", got)
	}
	if !a.OutOfUsage(a.Config().Harness("codex", "gpt-6-luna", "low")) {
		t.Fatal("the small models would use a login last measured spent")
	}
}

// A slow reading answers within the wait, and fills the meter for the next.
func TestUsageIsBoundedAndNeverSpendsALook(t *testing.T) {
	a := testLoop(t)
	release := make(chan struct{})
	var mu sync.Mutex
	reads := 0
	a.meter = &quota.Meter{Inspect: func(_ context.Context, o session.Options) (session.Inspection, error) {
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
	deadline := time.Now().Add(2 * time.Second)
	for !a.OutOfUsage(claude) {
		if time.Now().After(deadline) {
			t.Fatal("the slow reading never reached the meter")
		}
		time.Sleep(5 * time.Millisecond)
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
	now := time.Now()
	used, minutes, resets := 100.0, int64(10080), now.Add(time.Hour)
	observation := session.Observation{Quality: session.Measured, ObservedAt: now}
	window := func(id string) session.QuotaWindow {
		scope, _ := strings.CutPrefix(id, "model:")
		return session.QuotaWindow{Observation: observation, ID: id, Scope: scope, UsedPercent: &used, WindowMinutes: &minutes, ResetsAt: &resets}
	}
	for id, spent := range map[string]bool{"seven_day_opus": false, "model:Haiku": false, "seven_day": true} {
		a := testLoop(t)
		a.meter = &quota.Meter{Inspect: func(context.Context, session.Options) (session.Inspection, error) {
			return session.Inspection{Quota: session.QuotaSnapshot{Observation: observation, Complete: true, Windows: []session.QuotaWindow{window(id)}}}, nil
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
	a := testLoop(t)
	looked := make(chan string, 1)
	a.meter = &quota.Meter{Inspect: func(_ context.Context, o session.Options) (session.Inspection, error) {
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
	if !a.OutOfUsage(a.Config().Harness("claude", "haiku", "")) {
		t.Fatal("the new reading didn't count")
	}
}
