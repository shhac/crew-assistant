package work

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/session"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/roles"
)

// commandSandbox is the daemon's proved boundary, independent of the role engine.
type commandSandbox interface {
	Run(context.Context, session.CommandRequest) (session.CommandResult, error)
	Start(context.Context, session.CommandRequest) (startedCommand, error)
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
	Result() (session.CommandResult, error)
}
type nativeCommands struct{ *session.CommandSandbox }

func (s nativeCommands) Start(ctx context.Context, req session.CommandRequest) (startedCommand, error) {
	return s.CommandSandbox.Start(ctx, req)
}

// v0.22.0's standalone sandbox shares the workbench's ten-minute ceiling.
const commandTimeout = 10 * time.Minute

func (lp *Loop) openCommands(ctx context.Context, opts session.CommandSandboxOptions) (commandSandbox, error) {
	opts.RuntimeHome = filepath.Join(lp.Core.StateDirectory(), "commands")
	if err := os.MkdirAll(opts.RuntimeHome, 0700); err != nil {
		return nil, err
	}
	opts.Write = true
	opts.Timeout = commandTimeout
	opts.Env = roles.CommandEnv(opts.Env)
	opts.Background = harness.Support(harness.OpenAICompatible, harness.Session, harness.Background).Usable()
	if lp.commands != nil {
		return lp.commands(ctx, opts)
	}
	s, err := session.OpenCommandSandbox(ctx, opts)
	if err != nil {
		return nil, err
	}
	return nativeCommands{s}, nil
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
	sandbox, err := lp.openCommands(ctx, session.CommandSandboxOptions{WorkDir: dir})
	if err != nil {
		return err
	}
	return sandbox.Close()
}
