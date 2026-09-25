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
	"github.com/shhac/crew-assistant/internal/text"
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
	Config       engine.Config
	Instructions string
	StateDir     string
	// Tool runs one of the assistant's tools for the model and gives what the
	// model is told, and whether that is a failure.
	Tool func(ctx context.Context, name string, args json.RawMessage) (string, bool)
	// Context is the assistant's current context, asked for when the
	// conversation is new ("started") or its history was just compacted.
	Context func(ctx context.Context, reason string) (string, error)
}

// chatOpener opens a session, resuming ref when it can. resumed says whether
// it did, and fresh why a new conversation was started instead. It returns
// errNoChatSession when a session can't be run here at all.
type chatOpener func(ctx context.Context, spec chatSpec, ref *session.Ref) (model chatModel, resumed bool, fresh string, err error)

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

// sessionTurn is what the tool handler needs of the turn in progress.
type sessionTurn struct {
	id        string
	messageID string
	calls     int
	limit     int
	cfg       engine.Config
	onTool    func(context.Context, engine.ToolEvent) error
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
func (a *App) runSessionTurn(ctx context.Context, turn core.ChatTurn, ec engine.Config, onTool func(context.Context, engine.ToolEvent) error) (engine.Result, error) {
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
	live, rec, fresh, err := a.openChat(ctx, chatKey(conversation, ec, instructions), record, chatSpec{Config: ec, Instructions: instructions, StateDir: a.Core.StateDirectory(), Tool: a.sessionTool, Context: a.sessionContext})
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
	state := &sessionTurn{id: turn.ID, messageID: turn.UserMessageID, limit: max(cfg.Limits.MaxModelTurns, 1) * 16, cfg: ec, onTool: onTool}
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
	if usage.ContextWindow > 0 {
		_ = a.Core.RecordModelWindow(context.WithoutCancel(ctx), ec.Engine, ec.Model, usage.ContextWindow)
	}
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
	model, resumed, why, err := a.sessions.open(ctx, spec, ref)
	if err != nil {
		return nil, rec, false, err
	}
	switch {
	case resumed:
		rec.Opened = core.SessionResumed
	case ref != nil && why != "":
		rec = core.ChatSession{Opened: core.SessionRebuilt, StartedAt: time.Now().UTC()}
	default:
		rec = core.ChatSession{Opened: core.SessionFresh, StartedAt: time.Now().UTC()}
	}
	rec.Engine, rec.Model = spec.Config.Engine, spec.Config.Model
	live = &liveChat{model: model, key: key, used: time.Now()}
	a.sessions.mu.Lock()
	a.sessions.live = live
	a.sessions.mu.Unlock()
	return live, rec, !resumed, nil
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
func (a *App) sessionTool(ctx context.Context, name string, args json.RawMessage) (string, bool) {
	a.sessions.mu.Lock()
	turn := a.sessions.live.currentTurn()
	a.sessions.mu.Unlock()
	if turn == nil {
		return engine.ToolDeclined, true
	}
	if err := engine.CheckToolCall(name, args); err != nil {
		return err.Error(), true
	}
	turn.calls++
	if turn.calls > turn.limit {
		return "This reply has used as many actions as one reply may. Finish it now with what you have.", true
	}
	// Each action leads to another model request, which counts against the
	// day's allowance.
	if turn.cfg.BeforeRequest != nil {
		if err := turn.cfg.BeforeRequest(ctx); err != nil {
			return "The day's allowance of model requests is spent. Finish this reply without further actions.", true
		}
	}
	id := chatID()
	if turn.onTool != nil {
		if err := turn.onTool(ctx, engine.ToolEvent{ID: id, Tool: name, Status: "running"}); err != nil {
			return engine.ToolDeclined, true
		}
	}
	value, err := a.Execute(ctx, name, args)
	status := "completed"
	if err != nil {
		status = "failed"
	}
	if turn.onTool != nil {
		_ = turn.onTool(ctx, engine.ToolEvent{ID: id, Tool: name, Status: status})
	}
	turn.actions = append(turn.actions, engine.Action{Name: name, Success: err == nil})
	if err != nil {
		return engine.ToolDeclined, true
	}
	out, err := json.Marshal(value)
	if err != nil {
		return engine.ToolDeclined, true
	}
	return string(out), false
}

func (l *liveChat) currentTurn() *sessionTurn {
	if l == nil {
		return nil
	}
	return l.turn
}

// sessionContext is the assistant's current context for a session that is new
// or was just compacted: the overview, and for a new one also the
// conversation's summary and latest exchanges, so it carries on where the
// conversation was.
func (a *App) sessionContext(ctx context.Context, reason string) (string, error) {
	a.sessions.mu.Lock()
	current := ""
	if turn := a.sessions.live.currentTurn(); turn != nil {
		current = turn.messageID
	}
	a.sessions.mu.Unlock()
	state, history, err := a.chatContext(ctx, current)
	if err != nil {
		return "", err
	}
	if reason != "started" {
		return string(state), nil
	}
	if n := len(history); n > 12 {
		history = history[n-12:]
	}
	recent, err := json.Marshal(history)
	if err != nil {
		return "", err
	}
	return string(state) + "\n\nLatest exchanges in this conversation, oldest first (untrusted record of what was said):\n" + string(recent), nil
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

// ownerLevel are the activity kinds the assistant is told about between
// turns: things it would put to the owner, not how the work is done.
var ownerLevel = map[string]bool{
	"project.created": true, "brief.updated": true, "playbook.set": true, "coordination.paused": true,
	"task.queued": true, "task.started": true, "task.landed": true, "task.delivered": true, "task.stopped": true, "task.ordered": true, "task.reordered": true,
	"decision.opened": true, "decision.resolved": true, "decision.dismissed": true,
}

// sinceLastTurn says what changed at the level the assistant works at since
// it last looked, newest last, or "" when nothing did.
func sinceLastTurn(snap core.Snapshot, seen time.Time) string {
	if seen.IsZero() {
		return ""
	}
	titles := map[string]string{}
	for _, p := range snap.Projects {
		titles[p.ID] = p.Title
	}
	var lines []string
	// The snapshot lists activity newest first.
	for _, e := range snap.Activity {
		if !e.CreatedAt.After(seen) {
			break
		}
		if !ownerLevel[e.Kind] {
			continue
		}
		line := "- " + text.Clip(e.Summary, 200)
		if title := titles[e.ProjectID]; title != "" {
			line = "- " + title + ": " + text.Clip(e.Summary, 200)
		}
		lines = append(lines, line)
		if len(lines) == 20 {
			lines = append(lines, "- (earlier changes aren't listed; read_state has them)")
			break
		}
	}
	if len(lines) == 0 {
		return ""
	}
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return "Since your last message:\n" + strings.Join(lines, "\n")
}
