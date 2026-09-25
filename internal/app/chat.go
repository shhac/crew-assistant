package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/engine"
	"github.com/shhac/crew-assistant/internal/lifecycle"
)

var ErrChatQueueUnavailable = errors.New("the chat has stopped; restart crew-assistant to pick up waiting messages")

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
		return core.ChatTurn{}, fmt.Errorf("the demo doesn't run models: %w", core.ErrChatValidation)
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

// Chat queues a message and waits for the chat queue to answer it.
// Cancellation stops waiting, never an accepted message.
func (a *App) Chat(ctx context.Context, message string) (engine.Result, error) {
	id := chatID()
	result := make(chan chatOutcome, 1)
	a.chatWaiters.Store(id, result)
	defer a.chatWaiters.Delete(id)
	if _, err := a.EnqueueChat(ctx, id, message); err != nil {
		return engine.Result{}, err
	}
	// Checked after the waiter is in place: the queue closes the waiters it
	// can see, and one it couldn't see sees this.
	if a.chatClosed.Load() {
		return engine.Result{}, a.chatClosedErr()
	}
	select {
	case <-ctx.Done():
		return engine.Result{}, ctx.Err()
	case out := <-result:
		return out.result, out.err
	}
}

// RunChatQueue owns inference independently of dashboard HTTP connections. It
// holds the conversation lock while recovering, so startup cannot mark a live
// in-process turn interrupted. A turn is taken only while stop.Graceful
// lasts and runs on stop.Force, so a stop lets the reply in progress finish
// and leaves the rest queued for the next run.
func (a *App) RunChatQueue(stop lifecycle.Stop) (queueErr error) {
	owned := false
	defer func() {
		// A stop ends this run's answers whether or not the queue ever took
		// the conversation; a queue that found another running leaves that
		// one's waiters alone.
		if stop.Stopping() {
			a.closeChatWaiters()
			return
		}
		if owned {
			if queueErr != nil {
				a.chatFailed.Store(true)
				a.Status("chat", "Conversation", "error", ErrChatQueueUnavailable.Error())
			}
			a.closeChatWaiters()
		}
	}()
	select {
	case a.chat <- struct{}{}:
	case <-stop.Graceful.Done():
		return nil
	}
	if a.chatRunning.Load() {
		<-a.chat
		return errors.New("chat queue already running")
	}
	owned = true
	if err := a.Core.RecoverChatTurns(stop.Force); err != nil {
		<-a.chat
		return err
	}
	a.chatRunning.Store(true)
	<-a.chat
	defer a.chatRunning.Store(false)
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	// The model session lives with the queue; its conversation stays saved in
	// the CLI and is resumed next time.
	if a.sessions.open == nil && !a.Demo {
		a.sessions.open = harnessChatOpener(stop.Force)
	}
	defer a.closeChat()
	for !stop.Stopping() {
		a.closeIdleChat(time.Now())
		worked, err := a.processNextChat(stop)
		if err != nil && !errors.Is(err, core.ErrNotFound) {
			return err
		}
		if worked {
			continue
		}
		select {
		case <-stop.Graceful.Done():
			return nil
		case <-a.chatWake:
		case <-tick.C:
		}
	}
	return nil
}

func (a *App) processNextChat(stop lifecycle.Stop) (bool, error) {
	select {
	case a.chat <- struct{}{}:
	case <-stop.Graceful.Done():
		return false, nil
	}
	defer func() { <-a.chat }()
	// The lock may have come free just as the stop was asked for.
	if stop.Stopping() {
		return false, nil
	}
	ctx := stop.Force
	turn, err := a.Core.StartNextChat(ctx)
	if errors.Is(err, core.ErrChatHeld) {
		// The owner is changing the queue. Nothing is wrong and nothing starts.
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if turn.Command != "" {
		return true, a.runChatCommand(ctx, turn)
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
	ec := a.assistantConfig(ctx, cfg)
	ec.AssistantName, ec.Personality, ec.MaxTurns = cfg.Assistant.Name, cfg.Assistant.Personality, cfg.Limits.MaxModelTurns
	ec.OnTool = func(ctx context.Context, event engine.ToolEvent) error {
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
	}
	ec.OnRetry = func(ctx context.Context, event engine.RetryEvent) error {
		return a.chatRetryStatus(ctx, turn.ID, event)
	}
	ec.OnContext = func(ctx context.Context, checkpoint engine.ContextCheckpoint, transcript []engine.Message) error {
		if err := a.archiveContext(ctx, checkpoint, transcript); err != nil {
			return err
		}
		return a.Core.SetChatModelStatus(ctx, turn.ID, "Earlier context summarized; continuing with saved progress.", time.Time{})
	}
	// On a model session the CLI keeps the conversation and compacts it
	// itself; the turn-by-turn way below sends everything each time.
	if result, err := a.runSessionTurn(ctx, turn, ec); !errors.Is(err, errNoChatSession) {
		return result, err
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
	result, err := a.chatOnce(ctx, ec, req)
	a.recordWindow(ctx, ec, result.Usage)
	// A model can turn out to have a smaller window than the turn was sized
	// for, such as one just chosen in Settings. Its refusal says so; the turn
	// is sized to it and tried once more, compacting what it must, but only if
	// nothing it did yet would be done twice.
	if budget := smallerBudget(ec, result.Usage.ContextWindow); budget > 0 && len(result.Actions) == 0 && providerContextLimit(err) {
		ec.MaxContextBytes = budget
		return a.chatOnce(ctx, ec, req)
	}
	return result, err
}

func (a *App) chatOnce(ctx context.Context, ec engine.Config, req engine.Request) (engine.Result, error) {
	if a.chatInvoker != nil {
		return a.chatInvoker(ctx, ec, req, a)
	}
	e, err := engine.New(ec, a)
	if err != nil {
		return engine.Result{}, err
	}
	return e.Chat(ctx, req)
}

// assistantConfig is the assistant's own model, as configured, with each
// request counted against the daily allowance and sized to the model's window.
func (a *App) assistantConfig(ctx context.Context, cfg config.Config) engine.Config {
	ec := EngineConfig(cfg.AssistantHarness())
	ec.WorkDirRoot = a.Core.StateDirectory()
	ec.BeforeRequest = func(ctx context.Context) error {
		return a.Core.ReserveModelCall(ctx, a.Config().Limits.MaxModelCallsPerDay)
	}
	if snap, err := a.Core.Snapshot(ctx); err == nil {
		ec.MaxContextBytes = contextBudget(snap.ModelWindow(ec.Engine, ec.Model), ec.MaxOutputTokens)
	}
	return ec
}

func (a *App) chatRetryStatus(ctx context.Context, id string, e engine.RetryEvent) error {
	text := ""
	switch e.Status {
	case "waiting":
		text = fmt.Sprintf("Model provider is busy. Retry %d of %d scheduled.", e.Attempt, e.MaxRetries)
	case "retrying":
		text = fmt.Sprintf("Retrying the model request (%d of %d)…", e.Attempt, e.MaxRetries)
	case "exhausted":
		text = "Model provider recovery stopped; saved actions are preserved."
	}
	return a.Core.SetChatModelStatus(ctx, id, text, e.RetryAt)
}

// closeChatWaiters tells whoever still waits for an answer that none is
// coming in this run. Their messages stay queued for the next one.
func (a *App) closeChatWaiters() {
	a.chatClosed.Store(true)
	err := a.chatClosedErr()
	a.chatWaiters.Range(func(_, w any) bool {
		select {
		case w.(chan chatOutcome) <- chatOutcome{err: err}:
		default: // It already has its answer.
		}
		return true
	})
}

func (a *App) chatClosedErr() error {
	if a.Stopping() {
		return errStoppingQueued
	}
	return ErrChatQueueUnavailable
}
