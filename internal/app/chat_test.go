package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/lifecycle"
	"github.com/shhac/crew-assistant/internal/engine"
)

func waitTurn(t *testing.T, a *App, id, status string) core.ChatTurn {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		turns, err := a.Core.ChatTurns(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, turn := range turns {
			if turn.ID == id && turn.Status == status {
				return turn
			}
		}
		time.Sleep(time.Millisecond * 5)
	}
	t.Fatalf("turn %s never reached %s", id, status)
	return core.ChatTurn{}
}
func startTestQueue(t *testing.T, a *App) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.RunChatQueue(lifecycle.Now(ctx)) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("queue did not stop")
		}
	})
	deadline := time.Now().Add(3 * time.Second)
	for !a.chatRunning.Load() {
		if time.Now().After(deadline) {
			t.Fatal("queue did not start")
		}
		time.Sleep(time.Millisecond)
	}
	return cancel
}

func TestQueuePreservesFIFOAndHidesFutureMessages(t *testing.T) {
	a := testApp(t)
	started := make(chan string, 3)
	release := make(chan struct{})
	var mu sync.Mutex
	var seen []engine.Request
	a.chatInvoker = func(ctx context.Context, cfg engine.Config, req engine.Request, _ engine.ToolExecutor) (engine.Result, error) {
		mu.Lock()
		seen = append(seen, req)
		mu.Unlock()
		started <- req.Message
		if req.Message == "First" {
			select {
			case <-release:
			case <-ctx.Done():
				return engine.Result{}, ctx.Err()
			}
		}
		return engine.Result{Message: "Reply to " + req.Message}, nil
	}
	startTestQueue(t, a)
	acceptedCtx, cancel := context.WithCancel(context.Background())
	if _, err := a.EnqueueChat(acceptedCtx, "one", "First"); err != nil {
		t.Fatal(err)
	}
	<-started
	cancel() // HTTP request disappearing must not cancel inference.
	if _, err := a.EnqueueChat(context.Background(), "two", "Second"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.EnqueueChat(context.Background(), "three", "Cancelled"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Core.CancelChat(context.Background(), "three"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	turns, _ := a.Core.ChatTurns(context.Background())
	if turns[0].Status != "running" || turns[1].Status != "queued" {
		t.Fatal(turns)
	}
	close(release)
	waitTurn(t, a, "two", "completed")
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 || seen[0].Message != "First" || seen[1].Message != "Second" {
		t.Fatal(seen)
	}
	if len(seen[0].History) != 0 || strings.Contains(string(seen[0].Context), "Second") {
		t.Fatal("future prompt leaked", seen[0])
	}
	if len(seen[1].History) != 2 || seen[1].History[0].Content != "First" || seen[1].History[1].Content != "Reply to First" {
		t.Fatal(seen[1].History)
	}
}

func TestQueueShowsToolBeforeCompletionWithoutArguments(t *testing.T) {
	a := testApp(t)
	invoked := make(chan struct{})
	release := make(chan struct{})
	a.chatInvoker = func(ctx context.Context, cfg engine.Config, _ engine.Request, _ engine.ToolExecutor) (engine.Result, error) {
		if err := cfg.OnTool(ctx, engine.ToolEvent{ID: "MODEL-ID-MAY-CONTAIN-SECRET", Tool: "set_team", Status: "running"}); err != nil {
			return engine.Result{}, err
		}
		close(invoked)
		select {
		case <-release:
		case <-ctx.Done():
			return engine.Result{}, ctx.Err()
		}
		if err := cfg.OnTool(ctx, engine.ToolEvent{ID: "MODEL-ID-MAY-CONTAIN-SECRET", Tool: "set_team", Status: "completed"}); err != nil {
			return engine.Result{}, err
		}
		return engine.Result{Message: "Ready"}, nil
	}
	startTestQueue(t, a)
	a.EnqueueChat(context.Background(), "one", "Prepare the worker")
	<-invoked
	turn := waitTurn(t, a, "one", "running")
	if len(turn.Events) != 1 || turn.Events[0].Status != "running" || turn.Events[0].ID == "MODEL-ID-MAY-CONTAIN-SECRET" || turn.Events[0].Label != "Choose the project's team" {
		t.Fatal(turn)
	}
	close(release)
	turn = waitTurn(t, a, "one", "completed")
	if turn.Events[0].Status != "completed" || turn.AssistantMessageID == "" {
		t.Fatal(turn)
	}
}

func TestQueueShutdownNeverReplaysUncertainTurn(t *testing.T) {
	a := testApp(t)
	started := make(chan struct{})
	var calls atomic.Int32
	a.chatInvoker = func(ctx context.Context, _ engine.Config, _ engine.Request, _ engine.ToolExecutor) (engine.Result, error) {
		calls.Add(1)
		close(started)
		<-ctx.Done()
		return engine.Result{}, ctx.Err()
	}
	cancel := startTestQueue(t, a)
	a.EnqueueChat(context.Background(), "one", "Act once")
	<-started
	a.EnqueueChat(context.Background(), "two", "Wait for next start")
	cancel()
	waitTurn(t, a, "one", "interrupted")
	turns, _ := a.Core.ChatTurns(context.Background())
	if calls.Load() != 1 || turns[1].Status != "queued" {
		t.Fatal(calls.Load(), turns)
	}
}

func TestSynchronousChatWhileDaemonOwnsQueuePreservesResult(t *testing.T) {
	a := testApp(t)
	a.chatInvoker = func(context.Context, engine.Config, engine.Request, engine.ToolExecutor) (engine.Result, error) {
		return engine.Result{Message: "Ready", Usage: engine.Usage{TotalTokens: 71}, Actions: []engine.Action{{Name: "read_state", Success: true}}}, nil
	}
	startTestQueue(t, a)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := a.Chat(ctx, "Hello")
	if err != nil || result.Usage.TotalTokens != 71 || len(result.Actions) != 1 {
		t.Fatal(result, err)
	}
	turns, _ := a.Core.ChatTurns(ctx)
	if len(turns) != 1 || turns[0].Status != "completed" {
		t.Fatal(turns)
	}
}

func TestCancellingQueuedSynchronousChatWakesCaller(t *testing.T) {
	a := testApp(t)
	started := make(chan struct{})
	release := make(chan struct{})
	a.chatInvoker = func(ctx context.Context, _ engine.Config, req engine.Request, _ engine.ToolExecutor) (engine.Result, error) {
		if req.Message != "First" {
			t.Error("cancelled message executed")
		}
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return engine.Result{}, ctx.Err()
		}
		return engine.Result{Message: "Done"}, nil
	}
	startTestQueue(t, a)
	a.EnqueueChat(context.Background(), "first", "First")
	<-started
	result := make(chan error, 1)
	go func() { _, err := a.Chat(context.Background(), "Cancel me"); result <- err }()
	id := ""
	deadline := time.Now().Add(3 * time.Second)
	for id == "" && time.Now().Before(deadline) {
		turns, _ := a.Core.ChatTurns(context.Background())
		for _, turn := range turns {
			if turn.Message == "Cancel me" {
				id = turn.ID
			}
		}
		time.Sleep(time.Millisecond)
	}
	if id == "" {
		t.Fatal("synchronous turn was not queued")
	}
	if _, err := a.CancelChat(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "cancelled") {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled synchronous caller remained blocked")
	}
	close(release)
	waitTurn(t, a, "first", "completed")
}

func TestAToolNameTheAssistantWasNeverOfferedIsNotRecorded(t *testing.T) {
	a := testApp(t)
	var toolErr error
	a.chatInvoker = func(ctx context.Context, cfg engine.Config, _ engine.Request, _ engine.ToolExecutor) (engine.Result, error) {
		toolErr = cfg.OnTool(ctx, engine.ToolEvent{ID: "call", Tool: "secret-token-arbitrary-tool-name", Status: "running"})
		return engine.Result{Message: "done"}, nil
	}
	startTestQueue(t, a)
	a.EnqueueChat(context.Background(), "one", "hello")
	waitTurn(t, a, "one", "completed")
	if toolErr == nil {
		t.Fatal("an unknown tool name was accepted")
	}
	turns, _ := a.Core.ChatTurns(context.Background())
	for _, turn := range turns {
		for _, e := range turn.Events {
			if strings.Contains(e.Tool, "secret") {
				t.Fatal("an untrusted tool name was persisted")
			}
		}
	}
}
