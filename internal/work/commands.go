package work

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/sandbox"
	"github.com/shhac/lib-agent-harness/session"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/roles"
)

// commandSandbox is the daemon's proved boundary, independent of the role engine.
type commandSandbox interface {
	Run(context.Context, sandbox.CommandRequest) (sandbox.CommandResult, error)
	Start(context.Context, sandbox.CommandRequest) (startedCommand, error)
	Close() error
}

func (lp *Loop) commandCleanupFor(observer roles.Observer) func(error) {
	return func(err error) {
		lp.commandCleanup(err)
		// Native observers have already ended when deferred command cleanup runs.
		// Keep a note tied to their last turn instead of sending it to a closed stream.
		if live, ok := observer.(*liveTurn); ok {
			live.steps.mu.Lock()
			step := live.steps.who
			step.Turn = live.steps.run
			live.steps.mu.Unlock()
			if step.Turn == "" {
				return
			}
			step.Item = fmt.Sprintf("command-cleanup-%d", time.Now().UnixNano())
			step.Kind, step.At = core.StepNote, time.Now().UTC()
			step.Text = "Command sandbox cleanup could not be confirmed; recovery state was kept for the next sandbox open: " + err.Error()
			if step.TaskID != "" {
				lp.keepStep(step)
			}
		}
	}
}

type startedCommand interface {
	Stop()
	Done() <-chan struct{}
	Result() (sandbox.CommandResult, error)
}
type nativeCommands struct {
	inner    *sandbox.Sandbox
	once     sync.Once
	closeErr error
}

func (s *nativeCommands) Run(ctx context.Context, req sandbox.CommandRequest) (sandbox.CommandResult, error) {
	result, err := s.inner.Run(ctx, req)
	return result, commandError(err)
}

func (s *nativeCommands) Start(ctx context.Context, req sandbox.CommandRequest) (startedCommand, error) {
	h, err := s.inner.Start(ctx, req)
	if err != nil {
		return nil, commandError(err)
	}
	return &nativeStartedCommand{inner: h}, nil
}

func (s *nativeCommands) Close() error {
	s.once.Do(func() { s.closeErr = commandError(s.inner.Close()) })
	return s.closeErr
}

type nativeStartedCommand struct {
	inner  *sandbox.StartedCommand
	once   sync.Once
	result sandbox.CommandResult
	err    error
}

func (h *nativeStartedCommand) Stop()                 { h.inner.Stop() }
func (h *nativeStartedCommand) Done() <-chan struct{} { return h.inner.Done() }
func (h *nativeStartedCommand) Result() (sandbox.CommandResult, error) {
	h.once.Do(func() { r, err := h.inner.Result(); h.result, h.err = r, commandError(err) })
	return h.result, h.err
}

// commandError keeps the task-facing wording run_check had under session's wrapper.
func commandError(err error) error {
	if err == nil {
		return nil
	}
	var refusal *sandbox.RefusalError
	if errors.As(err, &refusal) {
		return &session.UnsupportedError{Engine: harness.OpenAICompatible, Operation: refusal.Operation, Code: refusal.Code, Capability: refusal.Capability}
	}
	var proof *sandbox.ProofError
	if errors.As(err, &proof) {
		return &session.CapabilityError{Engine: harness.OpenAICompatible, Code: proof.Code, Phase: session.BeforeLaunch, Tools: proof.Tools}
	}
	var command *sandbox.CommandError
	if errors.As(err, &command) {
		return &session.TurnError{Engine: harness.OpenAICompatible, Code: command.Code}
	}
	var state *sandbox.StateError
	if errors.As(err, &state) {
		return &session.StateError{Engine: harness.OpenAICompatible, Code: state.Code}
	}
	if errors.Is(err, sandbox.ErrClosed) {
		return session.ErrClosed
	}
	return err
}

// The standalone sandbox shares the workbench's ten-minute ceiling.
const commandTimeout = 10 * time.Minute

func (lp *Loop) openCommands(ctx context.Context, opts sandbox.Options) (commandSandbox, error) {
	opts.RuntimeHome = filepath.Join(lp.Core.StateDirectory(), "commands")
	if err := os.MkdirAll(opts.RuntimeHome, 0700); err != nil {
		return nil, err
	}
	opts.Write = true
	opts.Timeout = commandTimeout
	opts.Env = roles.CommandEnv(opts.Env)
	opts.Background = harness.Support(harness.OpenAICompatible, harness.Session, harness.Background).Usable()
	if lp.commands != nil {
		box, err := lp.commands(ctx, opts)
		return box, commandError(err)
	}
	s, err := sandbox.Open(ctx, opts)
	if err != nil {
		return nil, commandError(err)
	}
	return &nativeCommands{inner: s}, nil
}

// Opening a sandbox reclaims the harness's stale command supervisors. Do this
// before restart cleanup removes the workspace copies they were running in.
func (lp *Loop) sweepCommands(ctx context.Context) error {
	entries, err := os.ReadDir(filepath.Join(lp.Core.StateDirectory(), "commands", "commands"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || len(entries) == 0 {
		return err
	}
	dir, err := os.MkdirTemp(lp.Core.StateDirectory(), "command-recovery-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	box, err := lp.openCommands(ctx, sandbox.Options{WorkDir: dir})
	if err != nil {
		return err
	}
	return box.Close()
}
