package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/engine"
	"github.com/shhac/crew-assistant/internal/lifecycle"
	"github.com/shhac/crew-assistant/internal/upgrade"
)

type automaticClock struct {
	mu     sync.Mutex
	at     time.Time
	timers chan *updateTimer
}

func (c *automaticClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.at }
func (c *automaticClock) NewTimer(d time.Duration) upgrade.Timer {
	timer := &updateTimer{make(chan time.Time, 1), d}
	c.timers <- timer
	return timer
}
func (c *automaticClock) advance(timer *updateTimer, d time.Duration) {
	c.mu.Lock()
	c.at = c.at.Add(d)
	now := c.at
	c.mu.Unlock()
	timer.ch <- now
}

func TestSelfUpgradeTriggersDrainRunningReplyThenInstall(t *testing.T) {
	for _, mode := range []string{"ask", "automatic-quiet", "automatic-cap"} {
		t.Run(mode, func(t *testing.T) {
			a := testApp(t)
			cfg := a.Config()
			cfg.Upgrade.Mode = "automatic"
			if mode == "ask" {
				cfg.Upgrade.Mode = "ask"
			}
			if err := a.UpdateConfig(cfg); err != nil {
				t.Fatal(err)
			}
			a.version = "v1.0.0"
			clock := &automaticClock{at: time.Now(), timers: make(chan *updateTimer, 10)}
			a.upgradeClock = clock
			stop, cancel := lifecycle.Watch(context.Background(), make(chan os.Signal), func(string) {})
			defer cancel()
			a.setStop(stop)
			path := upgrade.RecordPath(filepath.Join(t.TempDir(), "state.db"))
			calls := []string{}
			assert := func(step upgrade.Step, name string) {
				t.Helper()
				r, err := upgrade.ReadRecord(path)
				if err != nil || r.Step != step {
					t.Fatalf("%s before record: %+v %v", name, r, err)
				}
				calls = append(calls, name)
			}
			e := &upgrade.Engine{Path: path, Drain: func() bool { assert(upgrade.Draining, "drain"); return stop.Drain("upgrade") }}
			e.Backup = func(context.Context, upgrade.Record) error { assert(upgrade.BackingUp, "backup"); return nil }
			e.Install = func(context.Context, upgrade.Record) (string, error) {
				assert(upgrade.Installing, "install")
				return "v2.0.0", nil
			}
			e.Handover = func(context.Context, upgrade.Record) error { assert(upgrade.HandingOver, "handover"); return nil }
			e.Exec = func(string, []string) error { assert(upgrade.HandingOver, "exec"); return nil }
			a.upgradeEngine = e
			a.requestUpgrade = func(v string, automatic bool) error {
				return e.Request(upgrade.Record{From: "v1.0.0", To: v, Automatic: automatic})
			}
			a.Core.OnUpgradeRequested(func(v string) error { return a.RequestUpgrade(v, false) })
			started, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			defer releaseOnce.Do(func() { close(release) })
			a.chatInvoker = func(ctx context.Context, _ engine.Config, _ engine.Request, _ engine.ToolExecutor) (engine.Result, error) {
				close(started)
				select {
				case <-release:
					return engine.Result{Message: "Finished"}, nil
				case <-ctx.Done():
					return engine.Result{}, ctx.Err()
				}
			}
			queue := make(chan error, 1)
			go func() { queue <- a.RunChatQueue(stop) }()
			first := chatAsync(a, "Running reply")
			updateAwait(t, started)
			if err := a.Core.RecordUpdateCheck(context.Background(), a.version, upgrade.Result{Available: "v2.0.0"}); err != nil {
				t.Fatal(err)
			}
			if mode == "ask" {
				snap, err := a.Core.Snapshot(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				d := snap.Decisions[len(snap.Decisions)-1]
				if _, err = a.Work.ResolveDecision(context.Background(), d.ID, core.UpgradeChoice("v2.0.0"), "", core.FromOwner); err != nil {
					t.Fatal(err)
				}
			} else {
				autoDone := make(chan struct{})
				go func() { a.runAutomaticUpgrades(stop.Graceful); close(autoDone) }()
				timer := updateAwait(t, clock.timers)
				if timer.duration != time.Minute || stop.Stopping() {
					t.Fatal("busy reply drained immediately")
				}
				if mode == "automatic-quiet" {
					releaseOnce.Do(func() { close(release) })
					if reply := updateAwait(t, first); reply.err != nil {
						t.Fatal(reply.err)
					}
					clock.advance(timer, time.Minute)
				} else {
					clock.advance(timer, 6*time.Hour)
				}
				updateAwait(t, autoDone)
			}
			if !stop.Stopping() || stop.Force.Err() != nil {
				t.Fatal("upgrade did not drain gracefully")
			}
			if _, err := a.Core.EnqueueChat(context.Background(), "behind-upgrade", "Must wait for next daemon"); err != nil {
				t.Fatal(err)
			}
			if err := a.RequestUpgrade("v2.0.0", false); !errors.Is(err, upgrade.ErrInProgress) {
				t.Fatal(err)
			}
			if err := a.RecordUpgradeDrain(context.Background(), mode != "ask"); err != nil {
				t.Fatal(err)
			}
			r, _ := upgrade.ReadRecord(path)
			if r.Step != upgrade.Draining || !reflect.DeepEqual(calls, []string{"drain"}) {
				t.Fatal(r, calls)
			}
			if mode != "automatic-quiet" {
				if len(r.WaitingOn) != 1 || r.WaitingOn[0].Kind != "chat" {
					t.Fatal(r.WaitingOn)
				}
				select {
				case err := <-queue:
					t.Fatal("queue ended while reply running", err)
				default:
				}
				if mode == "automatic-cap" {
					snap, err := a.Core.Snapshot(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					found := false
					for _, event := range snap.Activity {
						if event.Kind == "upgrade.draining" && strings.Contains(event.Summary, r.WaitingOn[0].Label) {
							found = true
						}
					}
					if !found {
						t.Fatal("drain did not name running reply in Activity")
					}
				}
				releaseOnce.Do(func() { close(release) })
				if reply := updateAwait(t, first); reply.err != nil {
					t.Fatal(reply.err)
				}
			}
			if err := updateAwait(t, queue); err != nil {
				t.Fatal(err)
			}
			waitTurn(t, a, "behind-upgrade", "queued")
			if err := e.FinishDrain(context.Background(), stop.Reason()); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(calls, []string{"drain", "backup", "install", "handover", "exec"}) {
				t.Fatal(calls)
			}
		})
	}
}

func TestWorkingPMReplyDefersAutomaticUpgradeAndNamesDrain(t *testing.T) {
	a := testApp(t)
	p := slackProject(t, a, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := a.Config()
	cfg.Upgrade.Mode = "automatic"
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	a.version = "v1.0.0"
	if _, err := a.Work.SendPMChat(ctx, p.ID, "pm-busy", "What next?"); err != nil {
		t.Fatal(err)
	}
	_, claim, _, ok, err := a.Core.ClaimPMChat(ctx, p.ID, func(core.Role) string { return "" })
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	waiting, err := a.UpgradeWaiting(ctx)
	if err != nil || len(waiting) != 1 || waiting[0].Kind != "pm-chat" {
		t.Fatal(waiting, err)
	}
	clock := &automaticClock{at: time.Now(), timers: make(chan *updateTimer, 10)}
	a.upgradeClock = clock
	path := upgrade.RecordPath(filepath.Join(t.TempDir(), "state.db"))
	a.upgradeEngine = &upgrade.Engine{Path: path, Drain: func() bool { return true }}
	requested := make(chan bool, 1)
	a.requestUpgrade = func(v string, automatic bool) error {
		if err := a.upgradeEngine.Request(upgrade.Record{From: a.version, To: v, Automatic: automatic}); err != nil {
			return err
		}
		requested <- automatic
		return nil
	}
	if err := a.Core.RecordUpdateCheck(ctx, a.version, upgrade.Result{Available: "v2.0.0", CheckedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); a.runAutomaticUpgrades(ctx) }()
	timer := <-clock.timers
	select {
	case <-requested:
		t.Fatal("drained while PM reply running")
	default:
	}
	clock.advance(timer, 6*time.Hour)
	select {
	case automatic := <-requested:
		if !automatic {
			t.Fatal("not automatic")
		}
	case <-time.After(time.Second):
		t.Fatal("cap did not drain")
	}
	if err := a.RecordUpgradeDrain(ctx, true); err != nil {
		t.Fatal(err)
	}
	r, _ := upgrade.ReadRecord(path)
	if len(r.WaitingOn) != 1 || r.WaitingOn[0].Ref != "pm-busy" {
		t.Fatal(r)
	}
	if err := a.Core.FailPMChat(ctx, p.ID, "pm-busy", claim.Token, "fixture done", nil); err != nil {
		t.Fatal(err)
	}
	waiting, err = a.UpgradeWaiting(ctx)
	if err != nil || len(waiting) != 0 {
		t.Fatal(waiting, err)
	}
	<-done
}
func TestClearRollbackRequestsExplicitRetry(t *testing.T) {
	a := testApp(t)
	path := upgrade.RecordPath(filepath.Join(t.TempDir(), "state.db"))
	a.upgradeEngine = &upgrade.Engine{Path: path}
	r := upgrade.Record{Step: upgrade.RolledBack, Pinned: true, From: "v1.0.0", To: "v2.0.0"}
	if err := upgrade.WriteRecord(path, r); err != nil {
		t.Fatal(err)
	}
	called := false
	a.requestUpgrade = func(target string, automatic bool) error {
		called = true
		if target != r.To || automatic {
			t.Fatal(target, automatic)
		}
		return nil
	}
	if err := a.ClearRollback(); err != nil || !called {
		t.Fatal(called, err)
	}
}

func TestUpgradeWaitingDeduplicatesObservedAndClaimedPMReply(t *testing.T) {
	started := time.Now()
	snapshot := core.Snapshot{PMChats: []core.PMChatMessage{{ID: "reply", ProjectID: "p", Status: "working", StartedAt: &started}, {ID: "claimed", ProjectID: "p", Status: "working", StartedAt: &started}}}
	turns := []core.Turn{{ProjectID: "p", Role: core.RolePM, MessageID: "reply", StartedAt: started.Add(time.Second)}}
	got := upgradeWaiting(snapshot, turns)
	if len(got) != 2 {
		t.Fatal(got)
	}
	seen := map[string]bool{}
	for _, item := range got {
		if seen[item.Ref] || item.Kind != "pm-chat" || !item.StartedAt.Equal(started) {
			t.Fatal(got)
		}
		seen[item.Ref] = true
	}
}
