package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/engine"
)

var ErrChatQueueUnavailable = errors.New("conversation queue unavailable; restart the daemon to recover pending messages")

type chatOutcome struct {
	result engine.Result
	err    error
}

func chatID() string {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(id[:])
}

func (a *App) EnqueueChat(ctx context.Context, id, message string) (core.ChatTurn, error) {
	if a.Demo {
		return core.ChatTurn{}, fmt.Errorf("demo mode does not invoke models or workers; start without --demo and configure a model to chat: %w", core.ErrChatValidation)
	}
	if a.chatFailed.Load() {
		return core.ChatTurn{}, ErrChatQueueUnavailable
	}
	t, err := a.Core.EnqueueChat(ctx, id, message)
	if err == nil {
		select {
		case a.chatWake <- struct{}{}:
		default:
		}
	}
	return t, err
}

// CancelChat retires only a message that has not started and wakes any legacy
// synchronous caller waiting for it (for example Slack or project coordination).
func (a *App) CancelChat(ctx context.Context, id string) (core.ChatTurn, error) {
	turn, err := a.Core.CancelChat(ctx, id)
	if err == nil {
		if waiter, ok := a.chatWaiters.Load(id); ok {
			select {
			case waiter.(chan chatOutcome) <- chatOutcome{err: errors.New("queued message was cancelled")}:
			default:
			}
		}
	}
	return turn, err
}

// Chat retains the synchronous integration API. With the daemon running it waits
// for a durable queue turn; cancellation stops waiting, never an accepted job.
// Standalone callers drain the same queue under the same conversation lock.
func (a *App) Chat(ctx context.Context, message string) (engine.Result, error) {
	id := chatID()
	result := make(chan chatOutcome, 1)
	a.chatWaiters.Store(id, result)
	defer a.chatWaiters.Delete(id)
	if _, err := a.EnqueueChat(ctx, id, message); err != nil {
		return engine.Result{}, err
	}
	if !a.chatRunning.Load() {
		for {
			if err := ctx.Err(); err != nil {
				return engine.Result{}, err
			}
			if a.chatRunning.Load() {
				break
			}
			_, err := a.processNextChat(ctx, true)
			if err != nil && !errors.Is(err, core.ErrNotFound) {
				return engine.Result{}, err
			}
			select {
			case out := <-result:
				return out.result, out.err
			default:
			}
			if errors.Is(err, core.ErrNotFound) || errors.Is(err, core.ErrConflict) {
				break
			}
		}
	}
	select {
	case <-ctx.Done():
		return engine.Result{}, ctx.Err()
	case out := <-result:
		return out.result, out.err
	}
}

// RunChatQueue owns inference independently of dashboard HTTP connections. The
// single conversation lock is shared with synchronous callers, including during
// recovery, so startup cannot mark a live in-process turn interrupted.
func (a *App) RunChatQueue(ctx context.Context) (queueErr error) {
	owned := false
	defer func() {
		if owned && queueErr != nil && ctx.Err() == nil {
			a.chatFailed.Store(true)
			a.Status("chat", "Conversation", "error", ErrChatQueueUnavailable.Error())
		}
	}()
	select {
	case a.chat <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	if a.chatRunning.Load() {
		<-a.chat
		return errors.New("chat queue already running")
	}
	owned = true
	if err := a.Core.RecoverChatTurns(ctx); err != nil {
		<-a.chat
		return err
	}
	a.chatRunning.Store(true)
	<-a.chat
	defer a.chatRunning.Store(false)
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		worked, err := a.processNextChat(ctx, false)
		if err != nil && !errors.Is(err, core.ErrNotFound) {
			return err
		}
		if worked {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-a.chatWake:
		case <-tick.C:
		}
	}
}

func (a *App) processNextChat(ctx context.Context, standalone bool) (bool, error) {
	select {
	case a.chat <- struct{}{}:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	defer func() { <-a.chat }()
	// A daemon may have acquired ownership while a standalone caller waited.
	if standalone && a.chatRunning.Load() {
		return false, core.ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	turn, err := a.Core.StartNextChat(ctx)
	if errors.Is(err, core.ErrChatHeld) {
		// The owner is changing the queue. Nothing is wrong and nothing starts.
		return false, nil
	}
	if err != nil {
		return false, err
	}
	runCtx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	result, runErr := a.runChatTurn(runCtx, turn)
	cancel()
	saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer saveCancel()
	status, reason := "completed", ""
	if runErr != nil {
		status = "failed"
		reason = chatFailureReason(runErr)
		if ctx.Err() != nil {
			status = "interrupted"
			reason = "The assistant stopped before this message finished. Recorded actions were preserved; no automatic replay was attempted."
		}
	}
	if err = a.Core.FinishChat(saveCtx, turn.ID, status, result.Message, reason); err != nil {
		if w, ok := a.chatWaiters.Load(turn.ID); ok {
			w.(chan chatOutcome) <- chatOutcome{result, err}
		}
		return true, err // Never advance past an uncertain durable completion.
	}
	if w, ok := a.chatWaiters.Load(turn.ID); ok {
		w.(chan chatOutcome) <- chatOutcome{result, runErr}
	}
	return true, nil
}

func (a *App) runChatTurn(ctx context.Context, turn core.ChatTurn) (engine.Result, error) {
	cfg := a.Config()
	eventIDs := map[string]string{}
	ec := engine.Config{WorkDirRoot: a.Core.StateDirectory(), Engine: cfg.Model.Engine, Effort: cfg.Model.Effort, CodexBin: cfg.Model.CodexBin, CodexHome: cfg.Model.CodexHome, ClaudeBin: cfg.Model.ClaudeBin, ClaudeHome: cfg.Model.ClaudeHome, Endpoint: strings.TrimRight(cfg.Model.BaseURL, "/") + "/chat/completions", Model: cfg.Model.Model, APIKeyEnv: cfg.Model.APIKeyEnv, AssistantName: cfg.Assistant.Name, Personality: cfg.Assistant.Personality, MaxTurns: cfg.Limits.MaxModelTurns, MaxOutputTokens: cfg.Model.MaxTokens,
		BeforeRequest: func(ctx context.Context) error {
			return a.Core.ReserveModelCall(ctx, a.Config().Limits.MaxModelCallsPerDay)
		},
		OnTool: func(ctx context.Context, event engine.ToolEvent) error {
			id := eventIDs[event.ID]
			if event.Status == "running" {
				id = chatID()
				eventIDs[event.ID] = id
			}
			// The model's call ID is never persisted or shown, nor are args or
			// results; a tool name the assistant was never offered is refused.
			label, ok := engine.ToolLabel(event.Tool)
			if !ok {
				return errors.New("invalid chat tool event")
			}
			return a.Core.RecordChatTool(ctx, turn.ID, id, event.Tool, label, event.Status)
		}}
	ec.OnRetry = func(ctx context.Context, event engine.RetryEvent) error {
		return a.chatRetryStatus(ctx, turn.ID, event)
	}
	ec.OnContext = func(ctx context.Context, checkpoint engine.ContextCheckpoint, transcript []engine.Message) error {
		if err := a.archiveContext(ctx, checkpoint, transcript); err != nil {
			return err
		}
		return a.Core.SetChatModelStatus(ctx, turn.ID, "Earlier context summarized; continuing with saved progress.", time.Time{})
	}
	if err := a.compactChatHistory(ctx, turn.UserMessageID, ec); err != nil {
		return engine.Result{}, err
	}
	raw, history, err := a.chatContext(ctx, turn.UserMessageID)
	if err != nil {
		return engine.Result{}, err
	}
	go a.startChatLoading(ctx, turn.ID, turn.Message, history)
	req := engine.Request{Message: turn.Message, History: history, Context: raw}
	executor := a
	if a.chatInvoker != nil {
		return a.chatInvoker(ctx, ec, req, executor)
	}
	e, err := engine.New(ec, executor)
	if err != nil {
		return engine.Result{}, err
	}
	return e.Chat(ctx, req)
}
