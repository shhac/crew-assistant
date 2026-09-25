package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/engine"
	"github.com/shhac/lib-agent-harness/session"
)

// ToolBridge is the argument the assistant's model session starts this binary
// with, so the CLI reaches the daemon's tools through it.
const ToolBridge = "tool-bridge"

// chatSessionIdle is how long a session no turn has used stays open.
const chatSessionIdle = 30 * time.Minute

// sessionNote tells a model session how its context reaches it, after the
// assistant's standing instructions.
const sessionNote = `
Your current state arrives in a caller-context block when this conversation starts and again after its history is compacted: an overview of the projects, the conversation's summary and its latest exchanges. Between those, a message from the owner may open with "Since your last message:" and what has changed since. When you need state fresher than that, read it with read_state.`

// chatModel is a live model session the assistant's conversation runs on:
// the seam between the chat and the harness.
type chatModel interface {
	// Turn sends text and waits for the reply, passing each event on as it
	// arrives.
	Turn(ctx context.Context, text string, onEvent func(session.Event)) (session.Result, error)
	// Compact has the session compact its own history, or reports
	// session.ErrUnsupported when its engine can't be asked to.
	Compact(ctx context.Context) error
	Ref() session.Ref
	Close()
}

// chatSpec is what a chat session is opened with.
type chatSpec struct {
	Config engine.Config
	// Binary and Home are the engine's CLI and the login it uses.
	Binary, Home string
	Instructions string
	StateDir     string
	// Tool runs one of the assistant's tools for the model.
	Tool func(ctx context.Context, name string, args json.RawMessage) session.ToolResult
	// Context is the assistant's current context, asked for when the
	// conversation is new or its history was just compacted.
	Context func(ctx context.Context, reason session.ContextReason) (string, error)
}

// chatOpener opens a session, resuming ref when it can, and says how. It
// returns errNoChatSession when a session can't be run here at all.
type chatOpener func(ctx context.Context, spec chatSpec, ref *session.Ref) (chatModel, session.Opened, error)

// errNoChatSession says the chat can't run on a model session here, so it
// runs turn by turn, as it did before sessions.
var errNoChatSession = errors.New("no model session for the chat")

// liveChat is the open session, and the turn using it now.
type liveChat struct {
	model chatModel
	key   string
	used  time.Time
	turn  *sessionTurn
}

// sessionTurn is what the tool handler needs of the turn in progress. Its
// counts are kept under chatSessions.mu: a call can still be finishing when
// its turn has been stopped.
type sessionTurn struct {
	id        string
	messageID string
	calls     int
	limit     int
	cfg       engine.Config
	actions   []engine.Action
}

// chatSessions holds the one session the assistant's conversation runs on.
type chatSessions struct {
	open chatOpener
	mu   sync.Mutex
	live *liveChat
}

// runSessionTurn runs a chat turn on the conversation's model session, opening
// or resuming it first. errNoChatSession means it can't, and the turn should
// run the stateless way.
func (a *App) runSessionTurn(ctx context.Context, turn core.ChatTurn, ec engine.Config) (engine.Result, error) {
	if a.sessions.open == nil || (ec.Engine != "claude" && ec.Engine != "codex") {
		return engine.Result{}, errNoChatSession
	}
	started := time.Now().UTC()
	record, conversation, err := a.Core.ChatSession(ctx)
	if err != nil {
		return engine.Result{}, err
	}
	snap, err := a.Core.Snapshot(ctx)
	if err != nil {
		return engine.Result{}, err
	}
	cfg := a.Config()
	instructions := engine.Instructions(cfg.Assistant.Name, cfg.Assistant.Personality) + sessionNote
	binary, home := cfg.Engines.Binary(ec.Engine)
	spec := chatSpec{Config: ec, Binary: binary, Home: home, Instructions: instructions, StateDir: a.Core.StateDirectory(), Tool: a.sessionTool, Context: a.sessionContext}
	live, rec, fresh, err := a.openChat(ctx, chatKey(conversation, ec, instructions), record, spec)
	if err != nil {
		return engine.Result{}, err
	}
	text := turn.Message
	// A conversation just started in the CLI is given the whole overview; any
	// other turn is told what changed since the last, unless it is a wake-up,
	// which says what happened itself.
	if !fresh && turn.Origin != core.OriginWake {
		if since := sinceLastTurn(snap, rec.SeenAt); since != "" {
			text = since + "\n\n" + text
		}
	}
	if ec.BeforeRequest != nil {
		if err := ec.BeforeRequest(ctx); err != nil {
			return engine.Result{}, err
		}
	}
	state := &sessionTurn{id: turn.ID, messageID: turn.UserMessageID, limit: max(cfg.Limits.MaxModelTurns, 1) * 16, cfg: ec}
	a.sessions.mu.Lock()
	live.turn = state
	a.sessions.mu.Unlock()
	result, runErr := live.model.Turn(ctx, text, func(e session.Event) { observe(&rec, e) })
	a.sessions.mu.Lock()
	live.turn, live.used = nil, time.Now()
	a.sessions.mu.Unlock()
	if runErr == nil && result.Status != "completed" {
		runErr = fmt.Errorf("the model's turn ended as %s", result.Status)
	}
	rec.Ref, _ = json.Marshal(live.model.Ref())
	rec.SeenAt = started
	usage := sessionUsage(result, rec)
	a.recordWindow(ctx, ec, usage)
	if err := a.Core.SaveChatSession(context.WithoutCancel(ctx), conversation, &rec); err != nil && !errors.Is(err, core.ErrConflict) {
		runErr = errors.Join(runErr, err)
	}
	if runErr != nil {
		// A failed turn may have left the process in any state; the next turn
		// resumes the conversation from what the CLI saved.
		a.closeChat()
		return engine.Result{Usage: usage, Actions: state.actions}, runErr
	}
	return engine.Result{Message: result.Text, Usage: usage, Actions: state.actions}, nil
}

// openChat gives the live session for key, opening one when there is none or
// the one open is for another conversation, model or set of instructions.
// fresh says it opened a new conversation in the CLI.
func (a *App) openChat(ctx context.Context, key string, record *core.ChatSession, spec chatSpec) (live *liveChat, rec core.ChatSession, fresh bool, err error) {
	if record != nil {
		rec = *record
	}
	a.sessions.mu.Lock()
	live = a.sessions.live
	a.sessions.mu.Unlock()
	if live != nil && live.key == key {
		return live, rec, false, nil
	}
	a.closeChat()
	var ref *session.Ref
	if len(rec.Ref) > 0 {
		var stored session.Ref
		if json.Unmarshal(rec.Ref, &stored) == nil {
			ref = &stored
		}
	}
	model, opened, err := a.sessions.open(ctx, spec, ref)
	if err != nil {
		return nil, rec, false, err
	}
	switch {
	case opened.Resumed:
		rec.Opened = core.SessionResumed
	case ref != nil && opened.Fresh != "":
		rec = core.ChatSession{Opened: core.SessionRebuilt, StartedAt: time.Now().UTC()}
	default:
		rec = core.ChatSession{Opened: core.SessionFresh, StartedAt: time.Now().UTC()}
	}
	rec.Engine, rec.Model = spec.Config.Engine, spec.Config.Model
	live = &liveChat{model: model, key: key, used: time.Now()}
	a.sessions.mu.Lock()
	a.sessions.live = live
	a.sessions.mu.Unlock()
	return live, rec, !opened.Resumed, nil
}

// closeChat closes the open session, if any. Its conversation stays saved in
// the CLI and resumes from there.
func (a *App) closeChat() {
	a.sessions.mu.Lock()
	live := a.sessions.live
	a.sessions.live = nil
	a.sessions.mu.Unlock()
	if live != nil {
		live.model.Close()
	}
}

// compactSession shortens the model session's own history after /compact has
// made the conversation's summary. A Codex session compacts itself; a Claude
// session can't be asked to from outside, so, like a session not open now, it
// is set aside and the next turn starts a new one from that summary.
func (a *App) compactSession(ctx context.Context) error {
	a.sessions.mu.Lock()
	live := a.sessions.live
	a.sessions.mu.Unlock()
	record, conversation, err := a.Core.ChatSession(ctx)
	if err != nil || record == nil {
		return err
	}
	rec := *record
	if live != nil {
		err := live.model.Compact(ctx)
		if err == nil {
			rec.Compactions++
			return a.Core.SaveChatSession(ctx, conversation, &rec)
		}
		if !errors.Is(err, session.ErrUnsupported) {
			return err
		}
	}
	a.closeChat()
	rec.Ref = nil
	return a.Core.SaveChatSession(ctx, conversation, &rec)
}

// closeIdleChat closes a session no turn has used for a while.
func (a *App) closeIdleChat(now time.Time) {
	a.sessions.mu.Lock()
	idle := a.sessions.live != nil && a.sessions.live.turn == nil && now.Sub(a.sessions.live.used) > chatSessionIdle
	a.sessions.mu.Unlock()
	if idle {
		a.closeChat()
	}
}

// chatKey names what a session was opened for: a change to any of it means
// a different session.
func chatKey(conversation string, ec engine.Config, instructions string) string {
	sum := sha256.Sum256([]byte(instructions))
	return strings.Join([]string{conversation, ec.Engine, ec.Model, ec.Effort, hex.EncodeToString(sum[:8])}, "|")
}

// sessionTool runs a tool the model called, with the same checks and the
// same account of it on the dashboard as a turn run the stateless way. The
// model is never told an error's own words: they can carry remote data.
func (a *App) sessionTool(ctx context.Context, name string, args json.RawMessage) session.ToolResult {
	declined := session.ToolResult{Content: engine.ToolDeclined, IsError: true}
	if err := engine.CheckToolCall(name, args); err != nil {
		return session.ToolResult{Content: err.Error(), IsError: true}
	}
	a.sessions.mu.Lock()
	turn := a.sessions.live.currentTurn()
	allowed := turn != nil && turn.calls < turn.limit
	if turn != nil {
		turn.calls++
	}
	a.sessions.mu.Unlock()
	if turn == nil {
		return declined
	}
	if !allowed {
		return session.ToolResult{Content: "This reply has used as many actions as one reply may. Finish it now with what you have.", IsError: true}
	}
	// Each action leads to another model request, which counts against the
	// day's allowance.
	if turn.cfg.BeforeRequest != nil {
		if err := turn.cfg.BeforeRequest(ctx); err != nil {
			return session.ToolResult{Content: "The day's allowance of model requests is spent. Finish this reply without further actions.", IsError: true}
		}
	}
	output, action, err := engine.RunTool(ctx, a, turn.cfg.OnTool, chatID(), name, args)
	a.sessions.mu.Lock()
	current := a.sessions.live.currentTurn() == turn
	if current {
		turn.actions = append(turn.actions, action)
	}
	a.sessions.mu.Unlock()
	if err != nil || !current {
		return declined
	}
	return session.ToolResult{Content: output, IsError: !action.Success}
}

func (l *liveChat) currentTurn() *sessionTurn {
	if l == nil {
		return nil
	}
	return l.turn
}

// observe keeps what a session reports during a turn.
func observe(rec *core.ChatSession, e session.Event) {
	switch {
	case e.Kind == "compaction_completed":
		rec.Compactions++
	case e.Context != nil:
		if e.Context.UsedTokens != nil {
			rec.ContextUsed = *e.Context.UsedTokens
		}
		if e.Context.CapacityTokens != nil {
			rec.ContextWindow = *e.Context.CapacityTokens
		}
	case e.Usage != nil && e.Usage.Final && e.Usage.Known:
		rec.Input, rec.CachedInput = e.Usage.Input+e.Usage.CacheRead+e.Usage.CacheWrite, e.Usage.CacheRead
	}
}

// sessionUsage is a session turn's usage as the chat accounts for it.
func sessionUsage(result session.Result, rec core.ChatSession) engine.Usage {
	u := result.Usage
	if !u.Known {
		u = result.Observed
	}
	input := int(u.Input + u.CacheRead + u.CacheWrite)
	return engine.Usage{InputTokens: input, OutputTokens: int(u.Output), TotalTokens: input + int(u.Output), Known: u.Known, ContextWindow: int(rec.ContextWindow)}
}
