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
	"github.com/shhac/crew-assistant/internal/diagnostics"
	"github.com/shhac/crew-assistant/internal/engine"
	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/session"
)

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
	// Recovered is what opening the session found cut off when the process
	// that last had it open stopped.
	Recovered() session.Recovery
	Close()
}

// chatSpec is what a chat session is opened with.
type chatSpec struct {
	// Config names the engine and the CLI and login it runs on.
	Config       engine.Config
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
	// unknown are calls opening the session found cut off, until the next
	// turn is told of them.
	unknown []session.RecoveredCall
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
// run the stateless way: the engine can't hold a session whose only tools
// are the assistant's.
func (a *App) runSessionTurn(ctx context.Context, turn core.ChatTurn, ec engine.Config) (engine.Result, error) {
	if a.sessions.open == nil || !harness.Support(ec.Provider.Engine, harness.Session, harness.RestrictTools).Usable() {
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
	instructions := engine.Instructions(ec.AssistantName, ec.Personality) + sessionNote
	spec := chatSpec{Config: ec, Instructions: instructions, StateDir: a.Core.StateDirectory(), Tool: a.sessionTool, Context: a.sessionContext}
	key := chatKey(conversation, ec, instructions)
	if err := a.foldBeforeNewSession(ctx, turn.UserMessageID, ec, key, record); err != nil {
		return engine.Result{}, err
	}
	live, rec, fresh, err := a.openChat(ctx, key, record, spec)
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
	if note := a.takeRecoveryNote(live, ec); note != "" {
		text = note + "\n\n" + text
	}
	if ec.BeforeRequest != nil {
		if err := ec.BeforeRequest(ctx); err != nil {
			return engine.Result{}, err
		}
	}
	state := &sessionTurn{id: turn.ID, messageID: turn.UserMessageID, limit: sessionToolLimit(ec.MaxTurns), cfg: ec}
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
	if outgrown(ec, runErr) {
		// A session that can't compact itself can't go on once its history
		// is too long for the model. It is set aside, and the next message
		// starts a new one from the conversation's summary.
		rec.Ref = nil
	}
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
	rec.Engine, rec.Model = spec.Config.Engine(), spec.Config.Model
	live = &liveChat{model: model, key: key, used: time.Now(), unknown: model.Recovered().UnknownOutcomes}
	a.sessions.mu.Lock()
	a.sessions.live = live
	a.sessions.mu.Unlock()
	return live, rec, !opened.Resumed, nil
}

// foldBeforeNewSession brings the conversation's summary up to the
// exchanges a new session is given word for word, when the session about to
// open is new and its engine can't compact its own history. Such a session
// is only ever set aside, so what it is started from has to be current.
func (a *App) foldBeforeNewSession(ctx context.Context, currentID string, ec engine.Config, key string, record *core.ChatSession) error {
	resumable := record != nil && len(record.Ref) > 0 && record.Engine == ec.Engine()
	if selfCompacting(ec) || resumable {
		return nil
	}
	a.sessions.mu.Lock()
	open := a.sessions.live != nil && a.sessions.live.key == key
	a.sessions.mu.Unlock()
	if open {
		return nil
	}
	_, err := a.summarizeChat(ctx, currentID, ec, sessionExchanges, 1)
	return err
}

// selfCompacting says whether ec's engine compacts a session's history
// itself as it fills: a CLI does, and the library's own loop for an
// endpoint never does.
func selfCompacting(ec engine.Config) bool {
	return ec.Provider.Engine.Transport() == harness.CLITransport
}

// outgrown reports a turn that failed because its session's history no
// longer fits the model, on a session that can't compact it.
func outgrown(ec engine.Config, err error) bool {
	facts, ok := harness.ErrorFacts(err)
	return ok && facts.Cause == harness.CauseContextLimit && !selfCompacting(ec)
}

// sessionToolLimit is how many actions one reply on a session may take.
func sessionToolLimit(maxModelTurns int) int {
	return max(maxModelTurns, 1) * 16
}

// errToolOutcomeUnknown says an action was cut off by a stop and its
// outcome is unknown.
var errToolOutcomeUnknown = errors.New("a chat action's outcome is unknown after a stop")

// takeRecoveryNote tells the next turn, once, which of the assistant's
// actions were cut off when the daemon last stopped. The session never runs
// them again and has told the model their outcome is unknown; this asks it to
// find out and tell the owner.
func (a *App) takeRecoveryNote(live *liveChat, ec engine.Config) string {
	a.sessions.mu.Lock()
	calls := live.unknown
	live.unknown = nil
	a.sessions.mu.Unlock()
	if len(calls) == 0 {
		return ""
	}
	labels := make([]string, 0, len(calls))
	for _, c := range calls {
		label, ok := engine.ToolLabel(c.Tool)
		if !ok {
			label = "an action"
		}
		labels = append(labels, label)
		a.Diagnostics.Failure(diagnostics.Event{Component: "chat", Stage: "session_recovery", Engine: ec.Engine(), Code: "tool_outcome_unknown"}, errToolOutcomeUnknown)
	}
	return "Before this message, crew-assistant stopped while you were doing this, so whether it took effect is unknown: " + strings.Join(labels, "; ") + ". It was not done again. Check what happened before relying on it or doing it again, and tell the owner."
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
// made the conversation's summary. A session whose engine can be asked to
// compacts itself; any other, like a session not open now, is set aside and
// the next turn starts a new one from that summary.
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
	return strings.Join([]string{conversation, ec.Engine(), ec.Model, ec.Effort, hex.EncodeToString(sum[:8])}, "|")
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
		// Input counts every prompt token, cached ones included.
		rec.Input, rec.CachedInput = e.Usage.Input, e.Usage.CacheRead
	}
}

// sessionUsage is a session turn's usage as the chat accounts for it.
func sessionUsage(result session.Result, rec core.ChatSession) engine.Usage {
	u := result.Usage
	if !u.Known {
		u = result.Observed
	}
	input := int(u.Input)
	return engine.Usage{InputTokens: input, OutputTokens: int(u.Output), TotalTokens: input + int(u.Output), Known: u.Known, ContextWindow: int(rec.ContextWindow)}
}
