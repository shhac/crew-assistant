package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/shhac/crew-assistant/internal/engine"
	"github.com/shhac/crew-assistant/internal/roles"
	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/session"
)

// harnessChatOpener opens the chat's sessions with lib-agent-harness: a
// restricted session whose only tools are the assistant's, reached through
// this binary as the bridge for a CLI, or called directly by the library's
// own loop for an endpoint. Sessions live as long as life, the chat queue's
// run, not as long as any one turn.
func harnessChatOpener(life context.Context) chatOpener {
	return func(ctx context.Context, spec chatSpec, ref *session.Ref) (chatModel, session.Opened, error) {
		o, err := chatSessionOptions(spec)
		if err != nil {
			return nil, session.Opened{}, err
		}
		s, opened, err := session.Open(life, o, ref)
		var capability *session.CapabilityError
		if errors.As(err, &capability) {
			// This CLI or platform can't run a restricted session; the chat
			// runs turn by turn instead.
			return nil, session.Opened{}, errors.Join(errNoChatSession, err)
		}
		if ref != nil && unreadableTranscript(err) {
			// The conversation itself is kept here, so a new session carries
			// it on from its summary and latest exchanges rather than every
			// turn failing on a transcript nothing can read.
			s, opened, err = session.Open(life, o, nil)
			opened.Fresh = session.FreshUnavailable
		}
		if err != nil {
			return nil, session.Opened{}, err
		}
		return &harnessChat{s: s, life: life}, opened, nil
	}
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
	o := session.Options{
		Provider:     spec.Config.Provider,
		RuntimeHome:  runtime,
		Model:        spec.Config.Model,
		Effort:       spec.Config.Effort,
		Instructions: session.Instructions{Mode: session.Append, Text: spec.Instructions},
		Restriction: &session.Restriction{Tools: session.ToolHost{
			Server:  "crew",
			Tools:   defs,
			Handler: handler,
			// read_state and read_task answer with whole records.
			MaxResultBytes: 256 << 10,
		}},
		Context: spec.Context,
	}
	if o.Provider.Engine.Transport() == harness.APITransport {
		// The library runs the loop against the endpoint and calls the
		// handler itself: there is no process, workspace or bridge. The
		// transcript under RuntimeHome is the conversation.
		o.Loop = chatLoop(spec.Config)
		return o, nil
	}
	return withChatBridge(o, root)
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
	o.Restriction.Tools.Dir, o.Restriction.Tools.Bridge = tools, bridge
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
// answer, and by the model's own window once it is known.
func chatLoop(ec engine.Config) session.Loop {
	return session.Loop{
		MaxSteps:        min(sessionToolLimit(ec.MaxTurns)+2, maxLoopSteps),
		MaxRequestBytes: min(ec.MaxContextBytes, maxLoopRequestBytes),
		RequestTimeout:  ec.Timeout,
	}
}

// harnessChat is a chat session held by lib-agent-harness.
type harnessChat struct {
	s    *session.Session
	life context.Context
}

func (h *harnessChat) Turn(ctx context.Context, text string, onEvent func(session.Event)) (session.Result, error) {
	// The turn is bound to the session's life, not the request's: cancelling
	// the context a turn started with ends the whole session.
	t, err := h.s.StartTurn(h.life, session.Input{Text: text})
	if err != nil {
		return session.Result{}, err
	}
	drained := drain(t, onEvent)
	result, err := t.Wait(ctx)
	if ctx.Err() != nil {
		// The owner stopped the reply, or it ran too long: stop the turn and
		// keep the session.
		stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		_ = h.s.Interrupt(stop, t.ID())
		result, _ = t.Wait(stop)
		cancel()
		err = ctx.Err()
	}
	<-drained
	return result, err
}

func (h *harnessChat) Compact(ctx context.Context) error {
	t, err := h.s.Compact(h.life)
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

func (h *harnessChat) Ref() session.Ref { return h.s.Ref() }

func (h *harnessChat) Recovered() session.Recovery { return h.s.Recovered() }

// Close lets the CLI go, confirming its process is gone and, for Codex,
// keeping any refreshed login. Its conversation stays saved to resume.
func (h *harnessChat) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, _ = h.s.Release(ctx)
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
