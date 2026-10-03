package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/engine"
	"github.com/shhac/crew-assistant/internal/testbridge"
	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/session"
)

func bridgeChatSpec(t *testing.T) chatSpec {
	binary, owner, crew := testbridge.Fixture(t)
	return chatSpec{Config: engine.Config{Provider: harness.Provider{Engine: harness.Codex, CLI: harness.CLI{Binary: binary, Home: crew}}}, Browser: config.Browser{On: true}, BridgeHome: owner, StateDir: t.TempDir()}
}

func TestChatReadsOnlyBridgeFromOwnerHome(t *testing.T) {
	spec := bridgeChatSpec(t)
	o, err := chatSessionOptions(spec)
	if err != nil {
		t.Fatal(err)
	}
	if o.BrowserBridgeHome != spec.BridgeHome || o.Provider.CLI.Home != spec.Config.Provider.CLI.Home || o.RuntimeHome != filepath.Join(spec.StateDir, "chat", "runtime", "codex") || o.WorkDir != filepath.Join(spec.StateDir, "chat", "work") {
		t.Fatalf("options %+v", o)
	}
	if err := session.CheckBrowserBridge(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	o.BrowserBridgeHome = ""
	var capability *session.CapabilityError
	if err := session.CheckBrowserBridge(context.Background(), o); !errors.As(err, &capability) || capability.Reason != session.BridgeNotDeclared {
		t.Fatalf("missing bridge: %v", err)
	}
	for _, e := range []harness.Engine{harness.Claude, harness.Codex} {
		spec.Config.Provider.Engine = e
		if e == harness.Codex {
			spec = withoutBrowser(spec)
		}
		o, err = chatSessionOptions(spec)
		if err != nil || o.BrowserBridgeHome != "" {
			t.Fatalf("unexpected bridge: %+v %v", o, err)
		}
	}
	if spec.BridgeHome != "" {
		t.Fatal("browserless spec kept bridge home")
	}
	if chatKey("c", spec.Config, "i", spec.Browser, "a") == chatKey("c", spec.Config, "i", spec.Browser, "b") {
		t.Fatal("bridge home not in session key")
	}
}

func TestChatFallsBackAtOpenForMissingBridge(t *testing.T) {
	spec := bridgeChatSpec(t)
	spec.BridgeHome = spec.Config.Provider.CLI.Home
	previous := openChatSession
	t.Cleanup(func() { openChatSession = previous })
	calls := 0
	openChatSession = func(ctx context.Context, got chatSpec, ref *session.Ref) (*session.Session, session.Opened, error) {
		calls++
		if calls == 1 {
			o, err := chatSessionOptions(got)
			if err != nil {
				t.Fatal(err)
			}
			return nil, session.Opened{}, session.CheckBrowserBridge(ctx, o)
		}
		if got.Browser.On || got.BridgeHome != "" {
			t.Fatalf("fallback spec %+v", got)
		}
		return nil, session.Opened{}, nil
	}
	model, _, err := harnessChatOpener(context.Background())(context.Background(), spec, nil)
	if err != nil || model == nil || calls != 2 {
		t.Fatalf("model %v calls %d err %v", model, calls, err)
	}
	h := model.(*harnessChat)
	if !strings.Contains(h.BrowserReason(), spec.BridgeHome) {
		t.Fatal(h.BrowserReason())
	}
}

func TestChatTurnBrowserFallback(t *testing.T) {
	for _, code := range []string{session.CapabilityBrowserToolsMissing, session.CapabilityBrowserSandboxUnproven, session.CapabilityBrowserSandboxNotEnforced} {
		t.Run(code, func(t *testing.T) {
			previous := openChatSession
			t.Cleanup(func() { openChatSession = previous })
			opens := 0
			openChatSession = func(_ context.Context, spec chatSpec, _ *session.Ref) (*session.Session, session.Opened, error) {
				opens++
				if spec.Browser.On || spec.BridgeHome != "" {
					t.Fatal("browser retained")
				}
				return nil, session.Opened{}, nil
			}
			h := &harnessChat{spec: chatSpec{Browser: config.Browser{On: true}, BridgeHome: "owner"}}
			sent := []string{}
			h.turner = func(_ context.Context, text string, _ func(session.Event)) (session.Result, error) {
				sent = append(sent, text)
				if len(sent) == 1 {
					return session.Result{}, &session.CapabilityError{Code: code}
				}
				return session.Result{Status: "completed"}, nil
			}
			if _, err := h.Turn(context.Background(), "hello", nil); err != nil {
				t.Fatal(err)
			}
			if _, err := h.Turn(context.Background(), "again", nil); err != nil {
				t.Fatal(err)
			}
			if opens != 1 || len(sent) != 3 || !strings.Contains(sent[1], "You have no browser") || strings.Contains(sent[2], "You have no browser") || h.BrowserReason() == "" {
				t.Fatalf("opens %d sent %q reason %q", opens, sent, h.BrowserReason())
			}
		})
	}
	for _, cancelled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if cancelled {
			cancel()
		} else {
			defer cancel()
		}
		calls := 0
		h := &harnessChat{spec: chatSpec{Browser: config.Browser{On: true}}}
		h.turner = func(context.Context, string, func(session.Event)) (session.Result, error) {
			calls++
			if cancelled {
				return session.Result{}, &session.CapabilityError{Code: session.CapabilityBrowserToolsMissing}
			}
			return session.Result{}, errors.New("ordinary error")
		}
		if _, err := h.Turn(ctx, "hello", nil); err == nil || calls != 1 || h.BrowserReason() != "" {
			t.Fatalf("calls %d err %v", calls, err)
		}
	}
}

type fallenBackChat struct {
	*fakeChat
	reason         string
	h              *harnessChat
	fallbackOnTurn bool
}

func (f *fallenBackChat) BrowserReason() string {
	if f.h != nil {
		return f.h.BrowserReason()
	}
	return f.reason
}
func (f *fallenBackChat) Turn(ctx context.Context, text string, event func(session.Event)) (session.Result, error) {
	if f.fallbackOnTurn {
		f.reason = "Chrome is not connected."
	}
	if f.h != nil {
		return f.h.Turn(ctx, text, event)
	}
	return f.fakeChat.Turn(ctx, text, event)
}

func TestChatRecordsBrowserFallbackDiscoveredDuringTurn(t *testing.T) {
	a, _ := sessionApp(t)
	seated(&a.cfg).Browser.On = true
	fake := &fallenBackChat{fakeChat: &fakeChat{id: "fallback"}, fallbackOnTurn: true}
	a.sessions.open = func(_ context.Context, spec chatSpec, _ *session.Ref) (chatModel, session.Opened, error) {
		fake.spec = spec
		return fake, session.Opened{}, nil
	}
	runTurn(t, a, "hello")
	snap, _ := a.Core.Snapshot(context.Background())
	if snap.ChatTurns[0].BrowserNote != "Running without the browser. Chrome is not connected." {
		t.Fatal(snap.ChatTurns[0])
	}
}

func TestChatBrowserNotesPersistOnEveryAffectedTurn(t *testing.T) {
	for _, browser := range []bool{false, true} {
		t.Run(map[bool]string{false: "off", true: "on"}[browser], func(t *testing.T) {
			a, _ := sessionApp(t)
			seated(&a.cfg).Browser.On = browser
			fake := &fakeChat{id: "fallback"}
			h := &harnessChat{turner: fake.Turn}
			a.sessions.open = func(_ context.Context, spec chatSpec, _ *session.Ref) (chatModel, session.Opened, error) {
				fake.spec = spec
				reason := ""
				if spec.Browser.On {
					reason = "Chrome is not connected."
				}
				h.reason = reason
				return &fallenBackChat{fakeChat: fake, h: h}, session.Opened{}, nil
			}
			runTurn(t, a, "hello")
			runTurn(t, a, "again")
			if browser != strings.Contains(fake.sent[0], "You have no browser") || strings.Contains(fake.sent[1], "You have no browser") {
				t.Fatalf("sent %q", fake.sent)
			}
			snap, _ := a.Core.Snapshot(context.Background())
			for _, turn := range snap.ChatTurns {
				if browser != strings.HasPrefix(turn.BrowserNote, "Running without the browser.") {
					t.Fatalf("turn %+v", turn)
				}
			}
		})
	}
}

func TestTurnByTurnChatReportsMissingBrowser(t *testing.T) {
	a, _ := sessionApp(t)
	seated(&a.cfg).Browser.On = true
	a.sessions.open = func(context.Context, chatSpec, *session.Ref) (chatModel, session.Opened, error) {
		return nil, session.Opened{}, errNoChatSession
	}
	a.chatInvoker = func(_ context.Context, _ engine.Config, req engine.Request, _ engine.ToolExecutor) (engine.Result, error) {
		if !strings.Contains(req.Message, "You have no browser") {
			t.Fatal(req.Message)
		}
		return engine.Result{Message: "reply"}, nil
	}
	runTurn(t, a, "hello")
	snap, _ := a.Core.Snapshot(context.Background())
	if len(snap.ChatTurns) != 1 || snap.ChatTurns[0].BrowserNote == "" {
		t.Fatalf("%+v", snap.ChatTurns)
	}
}

func TestChatConfiguredBridgeHomeReopensSession(t *testing.T) {
	a, o := sessionApp(t)
	seated(&a.cfg).Model.Engine = "codex"
	seated(&a.cfg).Browser.On = true
	a.cfg.Engines.Codex.BrowserBridgeHome = t.TempDir()
	runTurn(t, a, "hello")
	if o.chats[0].spec.BridgeHome != a.cfg.Engines.BridgeHome("codex") {
		t.Fatal(o.chats[0].spec.BridgeHome)
	}
	a.cfg.Engines.Codex.BrowserBridgeHome = t.TempDir()
	runTurn(t, a, "again")
	if len(o.chats) != 2 || !o.chats[0].closed || o.chats[1].spec.BridgeHome != a.cfg.Engines.BridgeHome("codex") {
		t.Fatal("bridge home change did not reopen the chat")
	}
}

func TestChatBrowserlessReopenFailure(t *testing.T) {
	for _, capability := range []bool{false, true} {
		spec := bridgeChatSpec(t)
		spec.BridgeHome = spec.Config.Provider.CLI.Home
		previous := openChatSession
		calls := 0
		failure := errors.New("reopen failed")
		if capability {
			failure = &session.CapabilityError{Code: session.CapabilityBrowserToolsMissing}
		}
		openChatSession = func(ctx context.Context, got chatSpec, ref *session.Ref) (*session.Session, session.Opened, error) {
			calls++
			if got.Browser.On {
				o, err := chatSessionOptions(got)
				if err != nil {
					t.Fatal(err)
				}
				return nil, session.Opened{}, session.CheckBrowserBridge(ctx, o)
			}
			return nil, session.Opened{}, failure
		}
		model, _, err := harnessChatOpener(context.Background())(context.Background(), spec, nil)
		openChatSession = previous
		if model != nil || calls != 2 || !errors.Is(err, failure) || errors.Is(err, errNoChatSession) != capability {
			t.Fatalf("model %v calls %d err %v", model, calls, err)
		}
	}
}
