package work

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/lifecycle"
	"github.com/shhac/crew-assistant/internal/roles"
)

// gatedRunner holds each turn until released, so a test can stop the loop
// while a turn is in progress.
type gatedRunner struct {
	*scriptedRunner
	turns   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (g *gatedRunner) Run(ctx context.Context, spec roles.Spec) (roles.Result, error) {
	g.turns.Add(1)
	g.entered <- struct{}{}
	select {
	case <-g.release:
	case <-ctx.Done():
		return roles.Result{}, ctx.Err()
	}
	return g.scriptedRunner.Run(ctx, spec)
}

func gatedLoop(t *testing.T) (*Loop, *gatedRunner) {
	t.Helper()
	scripted := &scriptedRunner{reviews: []string{pass}}
	a, _, _ := loopApp(t, scripted, "")
	g := &gatedRunner{scriptedRunner: scripted, entered: make(chan struct{}, 8), release: make(chan struct{})}
	a.runner = g
	return a, g
}

func runLoop(a *Loop, stop lifecycle.Stop) chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.Run(stop, false)
	}()
	return done
}

func waitFor(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal(what)
	}
}

// The first stop lets the turn in progress finish and be recorded, and
// starts no other.
func TestAStopFinishesTheStepInProgressAndStartsNoOther(t *testing.T) {
	a, g := gatedLoop(t)
	graceful, stopTaking := context.WithCancel(context.Background())
	done := runLoop(a, lifecycle.Stop{Graceful: graceful, Force: context.Background()})
	waitFor(t, g.entered, "no turn started")
	stopTaking()
	close(g.release)
	waitFor(t, done, "the loop didn't stop after its step")
	if n := g.turns.Load(); n != 1 {
		t.Fatalf("%d turns ran; a stop should start none after the one in progress", n)
	}
	snap, _ := a.Core.Snapshot(context.Background())
	if task := snap.Tasks[0]; len(task.Revisions) != 1 || task.Status == core.TaskWriting {
		t.Fatalf("the finished turn wasn't recorded: %+v", task)
	}
}

// The second stop ends the turn in progress.
func TestASecondStopEndsTheStepInProgress(t *testing.T) {
	a, g := gatedLoop(t)
	graceful, stopTaking := context.WithCancel(context.Background())
	force, stopNow := context.WithCancel(context.Background())
	done := runLoop(a, lifecycle.Stop{Graceful: graceful, Force: force})
	waitFor(t, g.entered, "no turn started")
	stopTaking()
	stopNow()
	waitFor(t, done, "the loop kept waiting for a turn it was told to end")
}

// The wake watcher looks no more once stopping.
func TestTheWakeWatcherStopsWithTheFirstStop(t *testing.T) {
	a := testLoop(t)
	graceful, stopTaking := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.RunWakes(lifecycle.Stop{Graceful: graceful, Force: context.Background()})
	}()
	stopTaking()
	waitFor(t, done, "the wake watcher kept watching after the stop")
}
