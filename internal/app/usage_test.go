package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/lifecycle"
	"github.com/shhac/crew-assistant/internal/quota"
	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/completion"
)

// usageApp is an app whose CLIs are missing, so no login is ever reached.
func usageApp(t *testing.T, demo bool) *App {
	t.Helper()
	cfg := config.Default()
	missing := filepath.Join(t.TempDir(), "missing")
	cfg.Engines.Codex.Bin, cfg.Engines.Claude.Bin = filepath.Join(missing, "codex"), filepath.Join(missing, "claude")
	s, err := core.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return New(core.NewService(s, cfg), cfg, filepath.Join(t.TempDir(), "config.json"), Options{Demo: demo})
}

func TestUsageSaysPlainlyWhenACLIIsMissing(t *testing.T) {
	a := usageApp(t, false)
	got := a.Usage(context.Background())
	if len(got) != 2 || got[0].Engine != "codex" || got[1].Engine != "claude" {
		t.Fatalf("%+v", got)
	}
	for _, u := range got {
		if u.Missing != "not installed" || u.Level != quota.LevelUnknown || len(u.Windows) != 0 || u.RateLimitedUntil != nil {
			t.Fatalf("%+v", u)
		}
	}
}

// While the daemon stops, nothing new starts a CLI: usage shows only the
// last reading, or says it wasn't checked.
func TestUsageStartsNoLookWhileStopping(t *testing.T) {
	stopped, stop := context.WithCancel(context.Background())
	stop()
	a := usageApp(t, false)
	a.setStop(lifecycle.Stop{Graceful: stopped, Force: context.Background()})
	for _, u := range a.Usage(context.Background()) {
		if u.Missing != "not checked while stopping" || u.Level != quota.LevelUnknown {
			t.Fatalf("%+v", u)
		}
	}
	looked := usageApp(t, false)
	looked.Usage(context.Background())
	looked.setStop(lifecycle.Stop{Graceful: stopped, Force: context.Background()})
	for _, u := range looked.Usage(context.Background()) {
		if u.Missing != "not installed" {
			t.Fatalf("the last reading was not shown: %+v", u)
		}
	}
}

func TestUsageReadsNoLoginInTheDemo(t *testing.T) {
	for _, u := range usageApp(t, true).Usage(context.Background()) {
		if u.Missing != "not checked in the demo" || u.Level != quota.LevelUnknown {
			t.Fatalf("%+v", u)
		}
	}
}

// An engine the small models rest for its rate limit shows as limited until
// they try it again; another failure doesn't.
func TestUsageShowsTheSmallModelsRateLimitRest(t *testing.T) {
	a := usageApp(t, true)
	now := time.Now()
	a.small.setResting("claude", restingEngine{until: now.Add(10 * time.Minute), cause: &completion.RequestError{Cause: harness.CauseRateLimited}})
	a.small.setResting("codex", restingEngine{until: now.Add(10 * time.Minute), cause: errors.New("overloaded")})
	got := a.Usage(context.Background())
	if got[1].RateLimitedUntil == nil || !got[1].RateLimitedUntil.Equal(now.Add(10*time.Minute)) || got[0].RateLimitedUntil != nil {
		t.Fatalf("%+v", got)
	}
	a.small.now = func() time.Time { return now.Add(11 * time.Minute) }
	if got := a.Usage(context.Background()); got[1].RateLimitedUntil != nil {
		t.Fatalf("still limited after its rest: %+v", got[1])
	}
}

// The small models skip an engine whose login last reported nothing left,
// without asking the CLI, and go to the other one.
func TestSmallModelsSkipAnEngineOutOfUsage(t *testing.T) {
	f := newFakeCLIs(t)
	s := f.models()
	var asked []string
	s.outOfUsage = func(h config.Harness) bool { asked = append(asked, h.Engine+"/"+h.Model); return h.Engine == "codex" }
	reply, err := s.ask(context.Background(), smallModelsFor(t, "codex"), nil, nil)
	if err != nil || reply.Content == "" {
		t.Fatal(err)
	}
	if !equalStrings(f.used(), []string{"claude/haiku/low"}) || !equalStrings(f.discovered, []string{"claude"}) {
		t.Fatal(f.used(), f.discovered)
	}
	if !equalStrings(asked, []string{"codex/gpt-6-luna", "claude/haiku"}) {
		t.Fatal(asked)
	}
	// Both out: nothing runs, and neither is rested as if it had failed.
	s.outOfUsage = func(config.Harness) bool { return true }
	before := f.calls()
	var failure *smallModelFailure
	if _, err := s.ask(context.Background(), smallModelsFor(t, "claude"), nil, nil); !errors.As(err, &failure) || f.calls() != before {
		t.Fatal(err, f.calls()-before)
	}
	if s.restingCause("codex") != nil {
		t.Fatal("an engine out of usage was rested as failed")
	}
}

// The app's small models ask the loop's meter, the one the sidebar reads.
func TestSmallModelsAskTheSidebarsMeter(t *testing.T) {
	a := usageApp(t, false)
	if a.small.outOfUsage == nil || a.small.outOfUsage(a.Config().Harness("codex", "gpt-6-luna", "low")) {
		t.Fatal("not wired to the loop's meter, or out of usage with no reading")
	}
}

// The test binary is a synthetic Codex account server, never a real login.
func init() {
	if os.Getenv("CREW_USAGE_TEST_CLI") != "1" || !slices.Contains(os.Args[1:], "app-server") {
		return
	}
	os.Exit(usageTestCLI())
}

func usageTestCLI() int {
	home := os.Getenv("CODEX_HOME")
	mode, err := os.ReadFile(filepath.Join(home, "used"))
	if err != nil {
		return 1
	}
	lines := bufio.NewScanner(os.Stdin)
	out := json.NewEncoder(os.Stdout)
	for lines.Scan() {
		var request struct {
			ID     json.RawMessage
			Method string
		}
		if json.Unmarshal(lines.Bytes(), &request) != nil {
			return 2
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{}
		case "initialized":
			continue
		case "account/read":
			result = map[string]any{"account": map[string]any{"type": "chatgpt", "email": "fixture@example.test", "planType": "plus"}, "requiresOpenaiAuth": true}
		case "account/rateLimits/read":
			var used float64
			if json.Unmarshal(mode, &used) != nil {
				return 3
			}
			result = map[string]any{"rateLimits": map[string]any{"limitId": "codex",
				"primary": map[string]any{"usedPercent": used, "windowDurationMins": 300, "resetsAt": time.Now().Add(time.Hour).Unix()}}}
		default:
			return 4 // No inference or other account action is supported.
		}
		if out.Encode(map[string]any{"id": request.ID, "result": result}) != nil {
			return 5
		}
	}
	return 0
}

func TestStoppingUsageLoadsKeptFigures(t *testing.T) {
	for _, used := range []string{"58", "100"} {
		t.Run(used, func(t *testing.T) {
			t.Setenv("CREW_USAGE_TEST_CLI", "1")
			seed := usageApp(t, false)
			cfg := seed.Config()
			bin, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cfg.Engines.Codex.Bin, cfg.Engines.Codex.Home = bin, t.TempDir()
			if err := os.WriteFile(filepath.Join(cfg.Engines.Codex.Home, "used"), []byte(used), 0600); err != nil {
				t.Fatal(err)
			}
			previous := New(seed.Core, cfg, seed.configPath, Options{})
			before := time.Now()
			good := previous.Usage(context.Background())[0]
			after := time.Now()
			if len(good.Windows) != 1 || good.AsOf != nil {
				t.Fatal(good)
			}

			// The second app shares state, but has an empty meter after restarting.
			restarted := New(seed.Core, cfg, seed.configPath, Options{})
			stopped, stop := context.WithCancel(context.Background())
			stop()
			restarted.setStop(lifecycle.Stop{Graceful: stopped, Force: context.Background()})
			got := restarted.Usage(context.Background())[0]
			reason := "not checked while stopping"
			if used == "100" {
				reason = "out of usage when last checked; " + reason
			}
			if len(got.Windows) != 1 || got.Windows[0].LeftPercent != good.Windows[0].LeftPercent ||
				got.AsOf == nil || got.AsOf.Before(before) || got.AsOf.After(after) ||
				got.Missing != reason || got.Level != good.Level {
				t.Fatal(got)
			}
			// Stopping must not start the CLI, even when it would now return other data.
			if err := os.WriteFile(filepath.Join(cfg.Engines.Codex.Home, "used"), []byte("0"), 0600); err != nil {
				t.Fatal(err)
			}
			if again := restarted.Usage(context.Background())[0]; again.Windows[0].LeftPercent != got.Windows[0].LeftPercent {
				t.Fatal(again)
			}
		})
	}
}
