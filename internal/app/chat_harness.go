package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/engine"
	"github.com/shhac/crew-assistant/internal/roles"
	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/session"
)

// harnessChatOpener opens the chat's sessions with lib-agent-harness: a
// restricted session whose only tools are the assistant's, reached through
// this binary as the bridge for a CLI, or called directly by the library's
// own loop for an endpoint; or, when the assistant may use the browser, a
// sandboxed session with the browser and the assistant's tools beside its
// own read-only ones. Sessions live as long as life, the chat queue's run,
// not as long as any one turn.
func harnessChatOpener(life context.Context) chatOpener {
	return func(ctx context.Context, spec chatSpec, ref *session.Ref) (chatModel, session.Opened, error) {
		s, opened, err := openChatSession(life, spec, ref)
		if spec.Config.Browser.On && roles.BrowserUnreachable(err) {
			spec = withoutBrowser(spec)
			s, opened, err = openChatSession(life, spec, ref)
		}
		var capability *session.CapabilityError
		if errors.As(err, &capability) {
			// This CLI or platform can't run a restricted session; the chat
			// runs turn by turn instead.
			return nil, session.Opened{}, errors.Join(errNoChatSession, err)
		}
		if err != nil {
			return nil, session.Opened{}, err
		}
		chat := &harnessChat{s: s, life: life}
		if spec.Config.Browser.On {
			restricted := withoutBrowser(spec)
			chat.withoutBrowser = func() (*session.Session, error) {
				s, _, err := openChatSession(life, restricted, ref)
				return s, err
			}
		}
		return chat, opened, nil
	}
}

// openChatSession opens the session spec describes, resuming ref when it can.
func openChatSession(life context.Context, spec chatSpec, ref *session.Ref) (*session.Session, session.Opened, error) {
	o, err := chatSessionOptions(spec)
	if err != nil {
		return nil, session.Opened{}, err
	}
	s, opened, err := session.Open(life, o, ref)
	if ref != nil && unreadableTranscript(err) {
		// The conversation itself is kept here, so a new session carries
		// it on from its summary and latest exchanges rather than every
		// turn failing on a transcript nothing can read.
		s, opened, err = session.Open(life, o, nil)
		opened.Fresh = session.FreshUnavailable
	}
	return s, opened, err
}

// withoutBrowser is spec for a chat whose browser isn't connected: it goes on
// limited to the assistant's tools until the chat next opens a session.
func withoutBrowser(spec chatSpec) chatSpec {
	spec.Config.Browser = config.Browser{}
	return spec
}

func unreadableTranscript(err error) bool {
	var state *session.StateError
	return errors.As(err, &state) && state.Code == session.StateCorrupt
}

// chatSessionOptions is the chat's session: the assistant's own instructions
// after the CLI's, its tools as the whole tool surface, and folders of its own
// under the state directory, apart from the teams' sessions.
func chatSessionOptions(spec chatSpec) (session.Options, error) {
	root := filepath.Join(spec.StateDir, "chat")
	runtime := filepath.Join(root, "runtime", spec.Config.Engine())
	if err := os.MkdirAll(runtime, 0o700); err != nil {
		return session.Options{}, err
	}
	defs := make([]session.ToolDefinition, 0, len(engine.Tools()))
	for _, t := range engine.Tools() {
		defs = append(defs, session.ToolDefinition{Name: t.Function.Name, Description: t.Function.Description, Schema: t.Function.Parameters})
	}
	handler := session.ToolHandlerFunc(func(ctx context.Context, call session.ToolCall) (session.ToolResult, error) {
		return spec.Tool(ctx, call.Name, call.Arguments), nil
	})
	host := session.ToolHost{
		Server:  "crew",
		Tools:   defs,
		Handler: handler,
		// read_state and read_task answer with whole records.
		MaxResultBytes: 256 << 10,
	}
	o := session.Options{
		Provider:     spec.Config.Provider,
		RuntimeHome:  runtime,
		Model:        spec.Config.Model,
		Effort:       spec.Config.Effort,
		Instructions: session.Instructions{Mode: session.Append, Text: spec.Instructions},
		Context:      spec.Context,
	}
	if o.Provider.Engine.Transport() == harness.APITransport {
		// The library runs the loop against the endpoint and calls the
		// handler itself: there is no process, workspace or bridge. The
		// transcript under RuntimeHome is the conversation.
		o.Restriction = &session.Restriction{Tools: host}
		o.Loop = chatLoop(spec.Config)
		return o, nil
	}
	if b := spec.Config.Browser; b.On {
		// The browser's tools come only with the CLI's own, so the session
		// is sandboxed, reading but never writing, its shell reaching no
		// network, rather than restricted to the assistant's tools.
		o.Sandbox, o.Browser = &session.Sandbox{Tools: &host}, true
		o.Instructions.Text = strings.TrimSpace(o.Instructions.Text + "\n\n" + roles.BrowserGuide(assistantBrowserOpening, b.Name))
	} else {
		o.Restriction = &session.Restriction{Tools: host}
	}
	return withChatBridge(o, root)
}

const assistantBrowserOpening = "You can use a browser in this conversation, to look things up and read pages for the owner; do everything else with your own tools."

// chatTools is the assistant's tools as the session hosts them.
func chatTools(o *session.Options) *session.ToolHost {
	if o.Sandbox != nil {
		return o.Sandbox.Tools
	}
	return &o.Restriction.Tools
}

// withChatBridge gives a CLI's session the bridge that reaches the
// assistant's tools, and a workspace of its own it never uses.
func withChatBridge(o session.Options, root string) (session.Options, error) {
	bridge, err := roles.Bridge()
	if err != nil {
		return session.Options{}, err
	}
	tools, work := filepath.Join(root, "tools"), filepath.Join(root, "work")
	for _, dir := range []string{tools, work} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return session.Options{}, err
		}
	}
	o.WorkDir = work
	host := chatTools(&o)
	host.Dir, host.Bridge = tools, bridge
	if o.Provider.Engine == harness.Grok {
		// The assistant never writes files or runs a shell, so it refuses
		// anything Grok asks to do. Grok can't yet hold a session restricted
		// to the assistant's tools, and Support keeps its chat turn by turn
		// until it can.
		o.Policy = session.Policy{GrokPermission: session.GrokDenyWhenAsked, GrokTelemetry: session.GrokTelemetryReduced}
	}
	return o, nil
}

// maxLoopSteps and maxLoopRequestBytes are the most the library lets its own
// loop be given.
const (
	maxLoopSteps        = 1024
	maxLoopRequestBytes = 64 << 20
)

// chatLoop bounds the library's loop as a turn run turn by turn is bounded:
// by the actions one reply may take, with room to be told to stop and then
// answer, by the model's own window once it is known, and by the reply cap an
// endpoint has always been given.
func chatLoop(ec engine.Config) session.Loop {
	return session.Loop{
		MaxSteps:        min(sessionToolLimit(ec.MaxTurns)+2, maxLoopSteps),
		MaxRequestBytes: min(ec.MaxContextBytes, maxLoopRequestBytes),
		RequestTimeout:  ec.Timeout,
		MaxOutputTokens: ec.MaxOutputTokens,
	}
}

// harnessChat is a chat session held by lib-agent-harness.
type harnessChat struct {
	mu   sync.Mutex
	s    *session.Session
	life context.Context
	// withoutBrowser opens the session again without the browser, for a
	// session given one that turns out not to be connected.
	withoutBrowser func() (*session.Session, error)
}

func (h *harnessChat) session() *session.Session {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.s
}

func (h *harnessChat) Turn(ctx context.Context, text string, onEvent func(session.Event)) (session.Result, error) {
	result, err := h.turn(ctx, text, onEvent)
	if h.withoutBrowser == nil || !roles.BrowserUnreachable(err) {
		return result, err
	}
	// Claude says whether the browser is connected only once its first
	// turn starts, before the model sees it, so that turn runs again on a
	// session without the browser.
	s, reopenErr := h.withoutBrowser()
	if reopenErr != nil {
		return result, errors.Join(err, reopenErr)
	}
	h.Close()
	h.mu.Lock()
	h.s, h.withoutBrowser = s, nil
	h.mu.Unlock()
	return h.turn(ctx, text, onEvent)
}

func (h *harnessChat) turn(ctx context.Context, text string, onEvent func(session.Event)) (session.Result, error) {
	s := h.session()
	// The turn is bound to the session's life, not the request's: cancelling
	// the context a turn started with ends the whole session.
	t, err := s.StartTurn(h.life, session.Input{Text: text})
	if err != nil {
		return session.Result{}, err
	}
	drained := drain(t, onEvent)
	result, err := t.Wait(ctx)
	if ctx.Err() != nil {
		// The owner stopped the reply, or it ran too long: stop the turn and
		// keep the session.
		stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		_ = s.Interrupt(stop, t.ID())
		result, _ = t.Wait(stop)
		cancel()
		err = ctx.Err()
	}
	<-drained
	return result, err
}

func (h *harnessChat) Compact(ctx context.Context) error {
	t, err := h.session().Compact(h.life)
	if err != nil {
		return err
	}
	drained := drain(t, func(session.Event) {})
	result, err := t.Wait(ctx)
	<-drained
	if err == nil && result.Status != "completed" {
		err = fmt.Errorf("compacting ended as %s", result.Status)
	}
	return err
}

func (h *harnessChat) Ref() session.Ref { return h.session().Ref() }

func (h *harnessChat) Recovered() session.Recovery { return h.session().Recovered() }

// Close lets the CLI go, confirming its process is gone and, for Codex,
// keeping any refreshed login. Its conversation stays saved to resume.
func (h *harnessChat) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, _ = h.session().Release(ctx)
}

// drain passes a turn's events on until its stream closes.
func drain(t *session.Turn, onEvent func(session.Event)) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for e := range t.Events() {
			onEvent(e)
		}
	}()
	return done
}
