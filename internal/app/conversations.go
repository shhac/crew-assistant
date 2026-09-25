package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/engine"
)

// compactKeep is how many of the latest messages /compact leaves word for
// word: the last two exchanges.
const compactKeep = 4

// runChatCommand runs a command turn in its place in the queue. The command
// is never said to the model; /compact asks it only for the summary.
func (a *App) runChatCommand(ctx context.Context, turn core.ChatTurn) error {
	summary, outcome := "", "Started a fresh conversation. The last one is in History."
	var runErr error
	if turn.Command == core.CommandCompact {
		runCtx, cancel := context.WithTimeout(ctx, 20*time.Minute)
		summary, outcome, runErr = a.compactNow(runCtx, turn.ID)
		cancel()
	}
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var err error
	if runErr != nil {
		status, reason := "failed", chatFailureReason(runErr)
		if ctx.Err() != nil {
			status, reason = "interrupted", "The assistant stopped before the conversation was compacted. Nothing was lost."
		}
		err = a.Core.FinishChat(saveCtx, turn.ID, status, "", reason)
	} else {
		err = a.Core.FinishChatCommand(saveCtx, turn.ID, summary, outcome)
	}
	if w, ok := a.chatWaiters.Load(turn.ID); ok {
		w.(chan chatOutcome) <- chatOutcome{engine.Result{Message: outcome}, errors.Join(runErr, err)}
	}
	return err
}

// compactNow summarizes all but the latest exchanges, whatever the length.
// Long dialogue is summarized in batches, each saved as it is made. If a later
// batch fails, the summary already saved is what the assistant now goes on
// from, so it is shown, and the outcome says compaction stopped part way.
func (a *App) compactNow(ctx context.Context, turnID string) (summary, outcome string, err error) {
	cfg := a.assistantConfig(ctx, a.Config())
	cfg.OnRetry = func(ctx context.Context, event engine.RetryEvent) error {
		return a.chatRetryStatus(ctx, turnID, event)
	}
	if err := a.Core.SetChatModelStatus(ctx, turnID, "Summarizing the conversation…", time.Time{}); err != nil {
		return "", "", err
	}
	folded, runErr := a.summarizeChat(ctx, "", cfg, compactKeep, 1)
	if folded == 0 {
		if runErr != nil {
			return "", "", runErr
		}
		return "", "Nothing to compact yet: the conversation is short enough to keep whole.", nil
	}
	// Read what was saved even if the run was stopped: it is in force.
	snap, err := a.Core.Snapshot(context.WithoutCancel(ctx))
	if err != nil {
		return "", "", errors.Join(runErr, err)
	}
	if runErr != nil {
		return snap.ChatCheckpoint.Summary, fmt.Sprintf("Summarized %d earlier messages, then stopped before the rest. Everything after this summary carries on word for word; /compact again to fold in more.", folded), nil
	}
	return snap.ChatCheckpoint.Summary, fmt.Sprintf("Summarized %d earlier messages. The latest exchanges carry on word for word.", folded), nil
}

// manageConversation compacts or starts afresh the assistant's own
// conversation, or a task implementer's. A refusal the assistant can act on
// is returned as its reason rather than as a failure it cannot read.
func (a *App) manageConversation(ctx context.Context, in engine.ManageConversationArgs) (any, error) {
	if in.Action != core.CommandCompact && in.Action != core.CommandNew {
		return nil, errors.New("action must be compact or new")
	}
	switch in.Whose {
	case "assistant":
		if _, err := a.Core.QueueChatCommand(ctx, in.Action); err != nil {
			return nil, err
		}
		select {
		case a.chatWake <- struct{}{}:
		default:
		}
		return map[string]string{"status": "Queued. It runs once this reply is finished; say what you need to in this reply."}, nil
	case "implementer":
		_, err := a.Core.SetWriterNext(ctx, in.ProjectID, in.TaskID, in.Action)
		if errors.Is(err, core.ErrConflict) {
			return map[string]string{"declined": strings.TrimSuffix(err.Error(), ": "+core.ErrConflict.Error())}, nil
		}
		if err != nil {
			return nil, err
		}
		return map[string]string{"status": "It happens at the implementer's next round."}, nil
	default:
		return nil, errors.New("whose must be assistant or implementer")
	}
}
