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

type commandCleanupObligation struct {
	task, token string
	remove      func()
}

func (lp *Loop) keepCommandCleanup(ctx context.Context, taskID string, cleanup func()) {
	_, token, ok := core.FenceOf(ctx)
	if !ok {
		token = taskID
	}
	lp.commandChecks.Store(token, commandCleanupObligation{task: taskID, token: token, remove: cleanup})
}

func (lp *Loop) commandCleanupFor(observer roles.Observer) func(error) {
	return func(err error) {
		lp.commandCleanup(err)
		lp.commandNoteFor(observer, "Command sandbox cleanup could not be confirmed; recovery state was kept for the next sandbox open: "+err.Error())
	}
}

// A forced turn end has no live tool reply. Preserve recovered check coverage
// beside that turn, without misclassifying ordinary cancellation as cleanup failure.
func (lp *Loop) commandNoteFor(observer roles.Observer, text string) {
	switch wrapped := observer.(type) {
	case *screenshots:
		lp.commandNoteFor(wrapped.next, text)
		return
	case browserFallbackObserver:
		lp.commandNoteFor(wrapped.Observer, text)
		return
	}
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
		step.Item = fmt.Sprintf("command-note-%d", time.Now().UnixNano())
		step.Kind, step.At = core.StepNote, time.Now().UTC()
		step.Text = text
		if step.TaskID != "" {
			lp.keepStep(step)
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
	lp.commandMu.Lock()
	defer lp.commandMu.Unlock()
	return lp.openCommandsLocked(ctx, opts)
}

type trackedCommands struct {
	commandSandbox
	loop *Loop
	dirs []string
}

func (s *trackedCommands) Close() error {
	err := s.commandSandbox.Close()
	s.loop.commandMu.Lock()
	defer s.loop.commandMu.Unlock()
	for _, dir := range s.dirs {
		delete(s.loop.liveCommands, dir)
	}
	return err
}

// Serialize directory discovery with other opens and recovery. The harness
// preserves live sandboxes through their lifetime locks during its own sweep.
func (lp *Loop) openCommandsLocked(ctx context.Context, opts sandbox.Options) (commandSandbox, error) {
	opts.RuntimeHome = filepath.Join(lp.Core.StateDirectory(), "commands")
	if err := os.MkdirAll(opts.RuntimeHome, 0700); err != nil {
		return nil, err
	}
	opts.Write = true
	opts.Timeout = commandTimeout
	opts.Env = roles.CommandEnv(opts.Env, opts.Read)
	opts.Background = harness.Support(harness.OpenAICompatible, harness.Session, harness.Background).Usable()
	base := filepath.Join(opts.RuntimeHome, "commands")
	before, err := os.ReadDir(base)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	known := map[string]bool{}
	for _, entry := range before {
		known[entry.Name()] = true
	}
	var box commandSandbox
	if lp.commands != nil {
		box, err = lp.commands(ctx, opts)
	} else {
		var native *sandbox.Sandbox
		native, err = sandbox.Open(ctx, opts)
		if err == nil {
			box = &nativeCommands{inner: native}
		}
	}
	if err != nil {
		return nil, commandError(err)
	}
	after, err := os.ReadDir(base)
	if err != nil && !os.IsNotExist(err) {
		return nil, errors.Join(err, box.Close())
	}
	tracked := &trackedCommands{commandSandbox: box, loop: lp}
	if lp.liveCommands == nil {
		lp.liveCommands = map[string]bool{}
	}
	for _, entry := range after {
		if entry.IsDir() && !known[entry.Name()] {
			tracked.dirs = append(tracked.dirs, entry.Name())
			lp.liveCommands[entry.Name()] = true
		}
	}
	return tracked, nil
}

// Opening a sandbox reclaims the harness's stale command supervisors. Do this
// before restart cleanup removes the workspace copies they were running in.
func (lp *Loop) sweepCommands(ctx context.Context) error {
	lp.commandMu.Lock()
	defer lp.commandMu.Unlock()
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
	box, err := lp.openCommandsLocked(ctx, sandbox.Options{WorkDir: dir})
	if err != nil {
		return err
	}
	tracked := box.(*trackedCommands)
	err = tracked.commandSandbox.Close()
	for _, name := range tracked.dirs {
		delete(lp.liveCommands, name)
	}
	if err != nil {
		return err
	}
	// Open silently preserves supervisors it cannot reclaim. Verify that
	// every prior non-live directory disappeared before releasing its workspace.
	for _, entry := range entries {
		if !entry.IsDir() || lp.liveCommands[entry.Name()] {
			continue
		}
		_, err := os.Lstat(filepath.Join(lp.Core.StateDirectory(), "commands", "commands", entry.Name()))
		if !os.IsNotExist(err) {
			return errors.Join(errCommandRecovery, err)
		}
	}
	return nil
}
