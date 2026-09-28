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

	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/session"
)

// Spec is one turn for one role.
type Spec struct {
	Engine, Model, Effort string
	// Binary and Home select the installed CLI and the login it uses.
	Binary, Home string
	// RuntimeHome is the private home a Codex session runs in, sharing only the
	// login from Home. Another engine is given none.
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
	// FreshPrompt, when set, replaces Prompt if the turn starts a fresh
	// session instead of resuming one: it brings in what the session would
	// have remembered.
	FreshPrompt string
	// Compact has a resumed session compact its context before the turn, on
	// an engine that can be asked to; a fresh session has nothing to compact.
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
	// LaunchDir is where a turn with tools records its launch, so a daemon
	// started after this one stopped can find a harness still running and
	// end it; empty is a folder of the turn's own that nothing looks for.
	LaunchDir string
}

// Observer hears a turn from when it starts to when it ends, with the prompt
// it was given and what its session reports along the way, so whoever waits
// on it can see it is alive. Every event a finished turn reported is heard
// before it ends.
type Observer interface {
	Started()
	Asked(prompt string)
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
	// The launch folder is kept while its harness can't be confirmed gone,
	// so the next start finds it.
	confirmed := false
	if len(spec.Tools) > 0 {
		host, cleanup, err := toolHost(spec)
		if err != nil {
			return Result{}, err
		}
		defer func() { cleanup(confirmed) }()
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
	if spec.Compact && resumed && harness.Support(o.Provider.Engine, harness.Session, harness.Compact).Usable() {
		if err := compact(ctx, s); err != nil {
			return Result{}, err
		}
	}
	prompt := spec.Prompt
	if !resumed && spec.FreshPrompt != "" {
		prompt = spec.FreshPrompt
	}
	turn, err := s.StartTurn(ctx, session.Input{Text: prompt})
	if err != nil {
		return Result{}, err
	}
	if spec.Observer != nil {
		spec.Observer.Asked(prompt)
	}
	heard := make(chan struct{})
	go func() {
		defer close(heard)
		for e := range turn.Events() {
			if spec.Observer != nil {
				spec.Observer.Saw(e)
			}
		}
	}()
	result, err := turn.Wait(ctx)
	// A finished turn has closed its events; one given up on may not have.
	if ctx.Err() == nil {
		<-heard
	}
	ref, _ := json.Marshal(s.Ref())
	released = true
	reclaimed, releaseErr := s.Release(context.WithoutCancel(ctx))
	confirmed = releaseErr == nil && reclaimed.Confirmed
	if releaseErr != nil && err == nil {
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
// Codex role gets a private runtime home. A stored reference names the
// runtime home, so giving one to an engine that doesn't read it would stop
// its conversations resuming.
func options(spec Spec) session.Options {
	o := session.Options{
		Provider:    harness.Provider{Engine: harness.Engine(spec.Engine), CLI: harness.CLI{Binary: spec.Binary, Home: spec.Home}},
		RuntimeHome: spec.RuntimeHome,
		WorkDir:     spec.WorkDir,
		Model:       spec.Model,
		Effort:      spec.Effort,
		Sandbox:     &session.Sandbox{Write: spec.Write, Read: spec.Read, Web: spec.Web},
		Env:         spec.Env,
	}
	if o.Provider.Engine != harness.Codex {
		o.RuntimeHome = ""
	}
	// Roles yield the machine to the owner's own use, and so does whatever
	// they start, where the harness can run them so; elsewhere it would
	// refuse the session.
	o.Background = harness.Support(o.Provider.Engine, harness.Session, harness.Background).Usable()
	if o.Provider.Engine == harness.Grok {
		o.Policy = grokPolicy(spec.Write)
	}
	if spec.Instructions != "" {
		o.Instructions = session.Instructions{Mode: session.Append, Text: spec.Instructions}
	}
	return o
}

// grokPolicy answers the permission requests a Grok role's session sends.
// Grok's agent mode edits and runs commands without asking unless a rule
// says to ask, so neither answer makes a role read-only: the sandbox bounds
// what a role can touch, and a Grok role is offered only once the harness can
// sandbox Grok sessions. A role that writes its workspace needs its edits and
// checks to go ahead when Grok does ask; a role that only reads, such as a
// reviewer, has nothing it should be asked to approve.
func grokPolicy(write bool) session.Policy {
	permission := session.GrokDenyWhenAsked
	if write {
		permission = session.GrokAllowWhenAsked
	}
	return session.Policy{GrokPermission: permission, GrokTelemetry: session.GrokTelemetryReduced}
}

// toolHost serves a turn's tools from a folder of its own, since turns run
// side by side and the folder holds the channel's lease and the record of
// the launch. Once the turn is over it is removed, unless it is the turn's
// LaunchDir and its harness wasn't confirmed gone.
func toolHost(spec Spec) (*session.ToolHost, func(confirmed bool), error) {
	bridge, err := Bridge()
	if err != nil {
		return nil, nil, err
	}
	dir := spec.LaunchDir
	if dir == "" {
		dir, err = os.MkdirTemp("", "crew-role-tools-")
	} else {
		err = os.MkdirAll(dir, 0o700)
	}
	if err != nil {
		return nil, nil, err
	}
	cleanup := func(confirmed bool) {
		if confirmed || spec.LaunchDir == "" {
			os.RemoveAll(dir)
		}
	}
	return &session.ToolHost{Server: "crew", Tools: spec.Tools, Handler: spec.Handler, Dir: dir, Bridge: bridge, MaxResultBytes: 128 << 10}, cleanup, nil
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

// KeychainLocked says a role could not start because its engine's login is
// in a locked keychain. It clears when the owner unlocks it, so it is neither
// permanent nor a failure to count.
func KeychainLocked(err error) bool {
	facts, ok := harness.ErrorFacts(err)
	return ok && facts.Code == harness.CodeKeychainUnavailable
}

// VerifySandbox runs the proof a role's session makes before it starts,
// without starting one or performing inference.
func VerifySandbox(ctx context.Context, spec Spec) error {
	return session.VerifySandbox(ctx, options(spec))
}
