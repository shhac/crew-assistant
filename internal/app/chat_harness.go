package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/shhac/crew-assistant/internal/engine"
	"github.com/shhac/lib-agent-harness/session"
)

// harnessChatOpener opens the chat's sessions with lib-agent-harness: a
// restricted session whose only tools are the assistant's, reached through
// this binary as the bridge. Sessions live as long as life, the chat queue's
// run, not as long as any one turn.
func harnessChatOpener(life context.Context) chatOpener {
	return func(ctx context.Context, spec chatSpec, ref *session.Ref) (chatModel, bool, string, error) {
		o, err := chatSessionOptions(spec)
		if err != nil {
			return nil, false, "", err
		}
		s, opened, err := session.Open(life, o, ref)
		var capability *session.CapabilityError
		if errors.As(err, &capability) {
			// This CLI or platform can't run a restricted session; the chat
			// runs turn by turn instead.
			return nil, false, "", errors.Join(errNoChatSession, err)
		}
		if err != nil {
			return nil, false, "", err
		}
		return &harnessChat{s: s, life: life}, opened.Resumed, opened.Fresh, nil
	}
}

// chatSessionOptions is the chat's session: the assistant's own instructions
// after the CLI's, its tools as the whole tool surface, and folders of its own
// under the state directory, apart from the teams' sessions.
func chatSessionOptions(spec chatSpec) (session.Options, error) {
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return session.Options{}, fmt.Errorf("finding this program to reach its tools: %w", err)
	}
	root := filepath.Join(spec.StateDir, "chat")
	tools, runtime, work := filepath.Join(root, "tools"), filepath.Join(root, "runtime", spec.Config.Engine), filepath.Join(root, "work")
	for _, dir := range []string{tools, runtime, work} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return session.Options{}, err
		}
	}
	defs := make([]session.ToolDefinition, 0, len(engine.Tools()))
	for _, t := range engine.Tools() {
		defs = append(defs, session.ToolDefinition{Name: t.Function.Name, Description: t.Function.Description, Schema: t.Function.Parameters})
	}
	handler := session.ToolHandlerFunc(func(ctx context.Context, call session.ToolCall) (session.ToolResult, error) {
		content, failed := spec.Tool(ctx, call.Name, call.Arguments)
		return session.ToolResult{Content: content, IsError: failed}, nil
	})
	return session.Options{
		Engine:       session.Engine(spec.Config.Engine),
		Binary:       spec.Binary,
		Home:         spec.Home,
		RuntimeHome:  runtime,
		WorkDir:      work,
		Model:        spec.Config.Model,
		Effort:       spec.Config.Effort,
		Instructions: session.Instructions{Mode: session.Append, Text: spec.Instructions},
		Restriction: &session.Restriction{Tools: session.ToolHost{
			Server:  "crew",
			Tools:   defs,
			Handler: handler,
			Dir:     tools,
			Bridge:  session.Bridge{Path: exe, Args: []string{ToolBridge}},
			// read_state and read_task answer with whole records.
			MaxResultBytes: 256 << 10,
		}},
		Context: func(ctx context.Context, reason session.ContextReason) (string, error) {
			return spec.Context(ctx, string(reason))
		},
	}, nil
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
