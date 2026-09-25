package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/engine"
	"github.com/shhac/lib-agent-harness/session"
)

// fakeChat stands in for a model session: it keeps what it was sent, and can
// call a tool back through the daemon as a model would.
type fakeChat struct {
	spec    chatSpec
	sent    []string
	call    string // a tool to call on the next turn
	callErr bool
	told    string // what the tool call was answered with
	failed  bool
	closed  bool
	id      string
}

func (f *fakeChat) Turn(ctx context.Context, text string, onEvent func(session.Event)) (session.Result, error) {
	f.sent = append(f.sent, text)
	if f.call != "" {
		f.told, f.failed = f.spec.Tool(ctx, f.call, json.RawMessage(`{}`))
		f.call = ""
	}
	window := int64(200000)
	onEvent(session.Event{Kind: "context", Context: &session.ContextSnapshot{CapacityTokens: &window}})
	onEvent(session.Event{Kind: "usage", Usage: &session.Usage{Known: true, Final: true, Input: 100, CacheRead: 900, Output: 20}})
	return session.Result{Status: "completed", Text: "Reply " + f.id, Usage: session.Usage{Known: true, Input: 100, CacheRead: 900, Output: 20}}, nil
}
func (f *fakeChat) Compact(context.Context) error { return session.ErrUnsupported }
func (f *fakeChat) Ref() session.Ref              { return session.Ref{Engine: session.Claude, ID: f.id} }
func (f *fakeChat) Close()                        { f.closed = true }

type openings struct {
	chats []*fakeChat
	refs  []*session.Ref
	// resume answers whether a given reference resumes.
	resume func(*session.Ref) (bool, string)
}

func (o *openings) open(_ context.Context, spec chatSpec, ref *session.Ref) (chatModel, bool, string, error) {
	o.refs = append(o.refs, ref)
	resumed, fresh := false, ""
	if ref != nil && o.resume != nil {
		resumed, fresh = o.resume(ref)
	}
	id := "s" + string(rune('0'+len(o.chats)))
	if resumed {
		id = ref.ID
	}
	chat := &fakeChat{spec: spec, id: id}
	o.chats = append(o.chats, chat)
	return chat, resumed, fresh, nil
}

func sessionApp(t *testing.T) (*App, *openings) {
	t.Helper()
	a := testApp(t)
	a.cfg.Model.Engine, a.cfg.Model.Model = "claude", "opus"
	o := &openings{}
	a.sessions.open = o.open
	return a, o
}

// runTurn queues and starts a message as the chat queue would, and runs it.
func runTurn(t *testing.T, a *App, message string) engine.Result {
	t.Helper()
	ctx := context.Background()
	if _, err := a.Core.EnqueueChat(ctx, chatID(), message); err != nil {
		t.Fatal(err)
	}
	turn, err := a.Core.StartNextChat(ctx)
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.runChatTurn(ctx, turn)
	if err != nil {
		t.Fatal(err)
	}
	a.Core.FinishChat(ctx, turn.ID, "completed", result.Message, "")
	return result
}

func TestTheChatRunsOnOneSessionAndIsToldOnlyWhatChanged(t *testing.T) {
	a, o := sessionApp(t)
	ctx := context.Background()
	if got := runTurn(t, a, "Hello"); got.Message != "Reply s0" || got.Usage.InputTokens != 1000 {
		t.Fatalf("result %+v", got)
	}
	if len(o.chats) != 1 || o.refs[0] != nil || o.chats[0].sent[0] != "Hello" {
		t.Fatalf("a new conversation should start fresh and be sent the message alone: %+v", o.chats)
	}
	overview, err := o.chats[0].spec.Context(ctx, "started")
	if err != nil || !strings.Contains(overview, `"state"`) {
		t.Fatalf("a new conversation's context should be the overview: %q %v", overview, err)
	}
	record, _, _ := a.Core.ChatSession(ctx)
	if record == nil || record.Opened != core.SessionFresh || record.CachedInput != 900 || record.ContextWindow != 200000 || len(record.Ref) == 0 {
		t.Fatalf("record %+v", record)
	}
	if snap, _ := a.Core.Snapshot(ctx); snap.ModelWindow("claude", "opus") != 200000 {
		t.Fatal("the session's window wasn't learned")
	}

	// The same session carries on, told what changed at the owner's level.
	a.Core.CreateProject(ctx, core.ProjectInput{Title: "Garden", Template: "draft", Brief: core.BriefInput{Goal: "Plant it", Criteria: []string{"Green"}}})
	runTurn(t, a, "And now?")
	if len(o.chats) != 1 || !strings.HasPrefix(o.chats[0].sent[1], "Since your last message:\n- Garden") || !strings.HasSuffix(o.chats[0].sent[1], "And now?") {
		t.Fatalf("the second turn should reuse the session with what changed: %q", o.chats[0].sent)
	}

	// After a restart the conversation resumes in the CLI.
	a.closeChat()
	o.resume = func(*session.Ref) (bool, string) { return true, "" }
	runTurn(t, a, "Still there?")
	if len(o.chats) != 2 || o.refs[1] == nil || o.refs[1].ID != "s0" {
		t.Fatalf("a restart should resume the saved session: %+v", o.refs)
	}
	if record, _, _ := a.Core.ChatSession(ctx); record.Opened != core.SessionResumed {
		t.Fatalf("record %+v", record)
	}

	// One that can't be resumed is rebuilt, and starts from the overview.
	a.closeChat()
	o.resume = func(*session.Ref) (bool, string) { return false, "unavailable" }
	runTurn(t, a, "Hello again")
	if record, _, _ := a.Core.ChatSession(ctx); record.Opened != core.SessionRebuilt || o.chats[2].sent[0] != "Hello again" {
		t.Fatalf("record %+v, sent %q", record, o.chats[2].sent)
	}

	// A Claude session can't compact on request, so /compact sets it aside
	// and the next turn starts afresh.
	if err := a.compactSession(ctx); err != nil {
		t.Fatal(err)
	}
	if !o.chats[2].closed {
		t.Fatal("the session wasn't set aside")
	}
	runTurn(t, a, "After compacting")
	if o.refs[len(o.refs)-1] != nil {
		t.Fatal("after /compact the next turn should start afresh")
	}
}

func TestASessionToolRunsLikeAnyOtherAndNeverShowsItsError(t *testing.T) {
	a, o := sessionApp(t)
	runTurn(t, a, "First")
	o.chats[0].call = "read_state"
	result := runTurn(t, a, "Read it")
	if o.chats[0].failed || !strings.Contains(o.chats[0].told, `"state"`) || len(result.Actions) != 1 || !result.Actions[0].Success {
		t.Fatalf("told %q failed %v actions %+v", o.chats[0].told, o.chats[0].failed, result.Actions)
	}
	o.chats[0].call = "stop_task"
	runTurn(t, a, "Stop nothing")
	if !o.chats[0].failed || o.chats[0].told != engine.ToolDeclined {
		t.Fatalf("a failed action should be declined without its error: %q", o.chats[0].told)
	}
	o.chats[0].call = "rm_rf"
	runTurn(t, a, "Unknown")
	if !o.chats[0].failed || strings.Contains(o.chats[0].told, "rm_rf") {
		t.Fatalf("an unknown tool was run or echoed: %q", o.chats[0].told)
	}
}

func TestTheChatRunsTurnByTurnWithoutASession(t *testing.T) {
	a, o := sessionApp(t)
	a.sessions.open = func(context.Context, chatSpec, *session.Ref) (chatModel, bool, string, error) {
		return nil, false, "", errNoChatSession
	}
	called := false
	a.chatInvoker = func(context.Context, engine.Config, engine.Request, engine.ToolExecutor) (engine.Result, error) {
		called = true
		return engine.Result{Message: "Stateless"}, nil
	}
	if got := runTurn(t, a, "Hello"); got.Message != "Stateless" || !called || len(o.chats) != 0 {
		t.Fatalf("result %+v", got)
	}
	a.cfg.Model.Engine = "openai-compatible"
	a.sessions.open = o.open
	if got := runTurn(t, a, "Hello"); got.Message != "Stateless" || len(o.chats) != 0 {
		t.Fatal("an HTTP engine opened a session")
	}
}
