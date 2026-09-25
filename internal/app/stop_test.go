package app

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/engine"
	slackapi "github.com/shhac/crew-assistant/internal/integrations/slack"
	"github.com/shhac/crew-assistant/internal/lifecycle"
)

func queuedTurn(t *testing.T, a *App, message string) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		turns, _ := a.Core.ChatTurns(context.Background())
		for _, turn := range turns {
			if turn.Message == message {
				return
			}
		}
	}
	t.Fatalf("%q was never queued", message)
}

type chatReply struct {
	result engine.Result
	err    error
}

func chatAsync(a *App, message string) chan chatReply {
	reply := make(chan chatReply, 1)
	go func() {
		result, err := a.Chat(context.Background(), message)
		reply <- chatReply{result, err}
	}()
	return reply
}

// A stop lets the reply in progress finish and reach whoever waits for it.
// Messages behind it stay queued for the next run, and their senders hear
// so rather than waiting.
func TestAStopFinishesTheReplyInProgressAndLeavesTheRestQueued(t *testing.T) {
	a := testApp(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	a.chatInvoker = func(ctx context.Context, _ engine.Config, _ engine.Request, _ engine.ToolExecutor) (engine.Result, error) {
		calls.Add(1)
		close(started)
		select {
		case <-release:
			return engine.Result{Message: "Done"}, nil
		case <-ctx.Done():
			return engine.Result{}, ctx.Err()
		}
	}
	graceful, stopTaking := context.WithCancel(context.Background())
	stop := lifecycle.Stop{Graceful: graceful, Force: context.Background()}
	a.setStop(stop)
	queue := make(chan error, 1)
	go func() { queue <- a.RunChatQueue(stop) }()

	first := chatAsync(a, "First")
	<-started
	second := chatAsync(a, "Second")
	queuedTurn(t, a, "Second")
	stopTaking()
	close(release)

	if r := <-first; r.err != nil || r.result.Message != "Done" {
		t.Fatalf("the reply in progress was lost: %+v", r)
	}
	select {
	case r := <-second:
		if !errors.Is(r.err, ErrStopping) {
			t.Fatalf("a queued message's sender heard %v", r.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a queued message's sender was left waiting")
	}
	if err := <-queue; err != nil {
		t.Fatal(err)
	}
	if _, err := a.Chat(context.Background(), "Third"); !errors.Is(err, ErrStopping) {
		t.Fatalf("a message sent after the stop heard %v", err)
	}
	turns, _ := a.Core.ChatTurns(context.Background())
	if calls.Load() != 1 || turns[0].Status != "completed" || turns[1].Status != "queued" || turns[2].Status != "queued" {
		t.Fatalf("%d calls, turns %+v", calls.Load(), turns)
	}
}

// Once stopping, what somebody asks for that would start a model or a CLI
// is refused rather than started.
func TestNewWorkIsRefusedWhileStopping(t *testing.T) {
	a := testApp(t)
	stopped, stopTaking := context.WithCancel(context.Background())
	stopTaking()
	a.setStop(lifecycle.Stop{Graceful: stopped, Force: context.Background()})
	ctx := context.Background()
	if _, err := a.InterviewIdentity(ctx, "Hello"); !errors.Is(err, ErrStopping) {
		t.Errorf("interview: %v", err)
	}
	if _, err := a.ApplyIdentity(ctx, "proposal", true); !errors.Is(err, ErrStopping) {
		t.Errorf("apply: %v", err)
	}
	if _, err := a.SuggestNextMessage(ctx, "reply"); !errors.Is(err, ErrStopping) {
		t.Errorf("suggestion: %v", err)
	}
	if _, err := a.DiscoverConnectionProfiles(ctx, "lin"); !errors.Is(err, ErrStopping) {
		t.Errorf("profiles: %v", err)
	}
	if err := a.markDrawing("assistant"); !errors.Is(err, ErrStopping) {
		t.Errorf("drawing: %v", err)
	}
	if snap, _ := a.Snapshot(ctx); !snap.Stopping {
		t.Error("the dashboard isn't told the daemon is stopping")
	}
}

// A Slack message that reaches a stopping daemon is queued, its receipt is
// settled rather than left for inspection, and the owner is told where the
// answer will be.
func TestASlackMessageAsTheDaemonStopsIsQueuedAndSaysSo(t *testing.T) {
	a := testApp(t)
	stopped, stopTaking := context.WithCancel(context.Background())
	stopTaking()
	a.setStop(lifecycle.Stop{Graceful: stopped, Force: context.Background()})
	a.closeChatWaiters()
	ctx := context.Background()
	if claimed, err := a.Core.ClaimEvent(ctx, "slack:event-one"); err != nil || !claimed {
		t.Fatal(claimed, err)
	}
	reply, err := a.answerSlack(ctx, slackapi.Message{ID: "event-one", Text: "Anything new?"})
	if err != nil || !strings.Contains(reply, "queued") {
		t.Fatalf("reply %q, %v", reply, err)
	}
	if pending, _ := a.Core.PendingEvents(ctx); len(pending) != 0 {
		t.Fatalf("the receipt was left for inspection: %+v", pending)
	}
	queuedTurn(t, a, "Anything new?")
}

// A stop that comes while the queue waits for the conversation, which an
// identity interview holds, still tells whoever waits for an answer.
func TestAStopBeforeTheQueueStartsStillAnswersWaiters(t *testing.T) {
	a := testApp(t)
	a.chat <- struct{}{} // an interview holds the conversation
	graceful, stopTaking := context.WithCancel(context.Background())
	stop := lifecycle.Stop{Graceful: graceful, Force: context.Background()}
	a.setStop(stop)
	queue := make(chan error, 1)
	go func() { queue <- a.RunChatQueue(stop) }()
	waiting := chatAsync(a, "Hello")
	queuedTurn(t, a, "Hello")
	stopTaking()
	select {
	case r := <-waiting:
		if !errors.Is(r.err, ErrStopping) {
			t.Fatalf("heard %v", r.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the sender waited through the stop")
	}
	<-a.chat
	if err := <-queue; err != nil {
		t.Fatal(err)
	}
}

// Once the run waits for its drawings, none starts, even before a stop.
func TestNoDrawingStartsOnceTheRunWaitsForThem(t *testing.T) {
	a := testApp(t)
	a.closeDrawings()
	if err := a.markDrawing("assistant"); !errors.Is(err, ErrStopping) {
		t.Fatalf("drawing: %v", err)
	}
}
