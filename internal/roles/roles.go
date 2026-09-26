// Package roles runs one turn of a team role as a sandboxed native coding
// session. Nothing here decides what a role is asked or what happens next.
package roles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shhac/lib-agent-harness/session"
)

// Spec is one turn for one role.
type Spec struct {
	Engine, Model, Effort string
	// Binary and Home select the installed CLI and the login it uses.
	Binary, Home string
	// RuntimeHome is the private home a Codex session runs in, sharing only the
	// login from Home.
	RuntimeHome string
	WorkDir     string
	// Write lets the role change files in WorkDir. Nothing a role runs reaches
	// the network either way.
	Write bool
	// Env adds ordinary settings, such as build caches inside WorkDir.
	Env []string
	// Read names directories outside WorkDir the role may read, such as a
	// module cache its build needs.
	Read         []string
	Instructions string
	Prompt       string
	// Resume continues an earlier session of this role, when it still matches
	// this configuration. The prompt must stand on its own either way.
	Resume json.RawMessage
	// Compact has a resumed session compact its context before the turn.
	// Only Codex can; a fresh session has nothing to compact.
	Compact bool
	// Web lets the role search and fetch the web. What it runs in its shell
	// still reaches no network.
	Web bool
	// Tools are what the role may call on the daemon while it works, such as
	// looking up the project's other tasks, answered by Handler.
	Tools   []session.ToolDefinition
	Handler session.ToolHandler
	// Observer hears how the turn goes while it runs, or nil.
	Observer Observer
}

// Observer hears a turn from when it starts to when it ends, with what its
// session reports along the way, so whoever waits on it can see it is alive.
type Observer interface {
	Started()
	Saw(session.Event)
	Ended()
}

// ToolBridge is the argument a model's CLI starts this binary with to reach
// the daemon's tools, for the assistant and the team alike.
const ToolBridge = "tool-bridge"

// Bridge is this binary as the tool bridge.
func Bridge() (session.Bridge, error) {
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return session.Bridge{}, fmt.Errorf("finding this program to reach its tools: %w", err)
	}
	return session.Bridge{Path: exe, Args: []string{ToolBridge}}, nil
}

type Result struct {
	Text string
	// Session resumes this role next time.
	Session json.RawMessage
}

type Runner interface {
	Run(ctx context.Context, spec Spec) (Result, error)
}

// Native runs roles through lib-agent-harness sessions under the CLI's own
// sandbox, which the harness proves before any credentialed launch.
type Native struct {
	// open starts or resumes a session; empty is the harness's own.
	open func(ctx context.Context, o session.Options, resume json.RawMessage) (conversation, bool, error)
}

// conversation is what a role's turn needs of its session.
type conversation interface {
	Compact(ctx context.Context) (turn, error)
	StartTurn(ctx context.Context, in session.Input) (turn, error)
	Ref() session.Ref
	Release(ctx context.Context) (session.Reclamation, error)
	Close()
}

type turn interface {
	Events() <-chan session.Event
	Wait(ctx context.Context) (session.Result, error)
}

// harnessSession is a harness session as a conversation.
type harnessSession struct{ *session.Session }

func (h harnessSession) Compact(ctx context.Context) (turn, error) {
	t, err := h.Session.Compact(ctx)
	if err != nil {
		return nil, err
	}
	return t, nil
}

func (h harnessSession) StartTurn(ctx context.Context, in session.Input) (turn, error) {
	t, err := h.Session.StartTurn(ctx, in)
	if err != nil {
		return nil, err
	}
	return t, nil
}

func (n Native) Run(ctx context.Context, spec Spec) (Result, error) {
	if spec.Observer != nil {
		spec.Observer.Started()
		defer spec.Observer.Ended()
	}
	opener := n.open
	if opener == nil {
		opener = open
	}
	o := options(spec)
	if len(spec.Tools) > 0 {
		host, cleanup, err := toolHost(spec)
		if err != nil {
			return Result{}, err
		}
		defer cleanup()
		o.Sandbox.Tools = host
	}
	s, resumed, err := opener(ctx, o, spec.Resume)
	if err != nil {
		return Result{}, err
	}
	released := false
	defer func() {
		if !released {
			s.Close()
		}
	}()
	if spec.Compact && resumed {
		if err := compact(ctx, s); err != nil {
			return Result{}, err
		}
	}
	turn, err := s.StartTurn(ctx, session.Input{Text: spec.Prompt})
	if err != nil {
		return Result{}, err
	}
	go func() {
		for e := range turn.Events() {
			if spec.Observer != nil {
				spec.Observer.Saw(e)
			}
		}
	}()
	result, err := turn.Wait(ctx)
	ref, _ := json.Marshal(s.Ref())
	released = true
	if _, releaseErr := s.Release(context.WithoutCancel(ctx)); releaseErr != nil && err == nil {
		err = releaseErr
	}
	if err != nil {
		return Result{Session: ref}, err
	}
	if result.Status != "completed" {
		return Result{Session: ref}, fmt.Errorf("the %s session ended its turn as %s", spec.Engine, result.Status)
	}
	return Result{Text: result.Text, Session: ref}, nil
}

// options is the session a role runs as. Every role runs sandboxed; only a
// Codex role gets a private runtime home.
func options(spec Spec) session.Options {
	o := session.Options{
		Engine:      session.Engine(spec.Engine),
		Binary:      spec.Binary,
		Home:        spec.Home,
		RuntimeHome: spec.RuntimeHome,
		WorkDir:     spec.WorkDir,
		Model:       spec.Model,
		Effort:      spec.Effort,
		Sandbox:     &session.Sandbox{Write: spec.Write, Read: spec.Read, Web: spec.Web},
		Env:         spec.Env,
	}
	if spec.Engine != string(session.Codex) {
		o.RuntimeHome = ""
	}
	if spec.Instructions != "" {
		o.Instructions = session.Instructions{Mode: session.Append, Text: spec.Instructions}
	}
	return o
}

// toolHost serves a turn's tools from a folder of its own, since turns run
// side by side and the folder holds the channel's lease; it is removed once
// the turn is over.
func toolHost(spec Spec) (*session.ToolHost, func(), error) {
	bridge, err := Bridge()
	if err != nil {
		return nil, nil, err
	}
	dir, err := os.MkdirTemp("", "crew-role-tools-")
	if err != nil {
		return nil, nil, err
	}
	return &session.ToolHost{Server: "crew", Tools: spec.Tools, Handler: spec.Handler, Dir: dir, Bridge: bridge, MaxResultBytes: 128 << 10}, func() { os.RemoveAll(dir) }, nil
}

// open resumes the recorded session when it can and otherwise starts a fresh
// one, saying which. A changed model or engine, or a conversation the CLI no
// longer has, means a fresh one; the prompt carries everything it needs.
func open(ctx context.Context, o session.Options, resume json.RawMessage) (conversation, bool, error) {
	var ref *session.Ref
	var stored session.Ref
	if len(resume) > 0 && json.Unmarshal(resume, &stored) == nil {
		ref = &stored
	}
	s, opened, err := session.Open(ctx, o, ref)
	if err != nil {
		return nil, false, err
	}
	return harnessSession{s}, opened.Resumed, nil
}

// compact runs the session's own context compaction to its end.
func compact(ctx context.Context, s conversation) error {
	turn, err := s.Compact(ctx)
	if err != nil {
		return fmt.Errorf("compacting the conversation: %w", err)
	}
	go func() {
		for range turn.Events() {
		}
	}()
	result, err := turn.Wait(ctx)
	if err != nil {
		return fmt.Errorf("compacting the conversation: %w", err)
	}
	if result.Status != "completed" {
		return fmt.Errorf("compacting the conversation ended as %s", result.Status)
	}
	return nil
}

// Permanent reports whether a failure will recur on retry: the installed CLI
// cannot be put under the required sandbox, or has no login to use.
func Permanent(err error) bool {
	var capability *session.CapabilityError
	return errors.As(err, &capability)
}
