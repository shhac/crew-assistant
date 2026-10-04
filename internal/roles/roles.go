// Package roles runs one turn of a team role as a sandboxed CLI or API
// workbench session. Nothing here decides what a role is asked or happens next.
package roles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/session"
)

// Spec is one turn for one role.
type Spec struct {
	PreparationError                                    error
	Engine, Model, Effort                               string
	ProjectID, TaskID, Role, Seat, MemberID, MemberName string
	PreviousID, RetryCause, FreshReason                 string
	Opening                                             func(session.Opened, session.Ref) error
	Accepted                                            func() error
	Provider                                            harness.Provider
	// AccountIdentity distinguishes API provider selections and credential
	// source names, even when they share an endpoint. It never holds a key.
	AccountIdentity string
	// Binary and Home select the installed CLI and the login it uses.
	Binary, Home string
	// BridgeHome selects only the ChatGPT browser bridge, separately from Home.
	BridgeHome string
	// RuntimeHome is Codex's private home, or an API workbench's private
	// transcript directory. Other engines are given none.
	RuntimeHome string
	WorkDir     string
	// Write lets the role change files in WorkDir. Nothing a role runs reaches
	// the network either way.
	Write bool
	// Env adds ordinary settings, such as build caches inside WorkDir.
	Env []string
	// Read names directories outside WorkDir the role may read, such as a
	// module cache its build needs.
	Read []string
	// Skills are supplied by the daemon, outside the role's write roots.
	Skills       harness.Skills
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
	// Loopback lets what the role runs bind and reach this machine's own
	// addresses, such as an app it starts to use, and nothing else: every
	// other host stays closed. The harness proves it before launch.
	Loopback bool
	// Browser turns on the browser the engine ships, such as Claude in
	// Chrome, which drives the owner's real browser outside the sandbox.
	Browser bool
	// Tools are what the role may call on the daemon while it works, such as
	// looking up the project's other tasks, answered by Handler.
	Tools   []session.ToolDefinition
	Handler session.ToolHandler
	// Observer hears how the turn goes while it runs, or nil.
	Observer Observer
	// Opened, when set, hears which session the turn runs in once it is
	// open and before the turn starts, so its tools can find what the
	// session keeps, such as the images Codex generates.
	Opened func(session.Ref)
	// Ended, when set, hears once the session Opened heard of is over,
	// whether it was confirmed gone: one that wasn't may still be running,
	// so what it keeps must stay until a restart reclaims it.
	Ended func(confirmed bool)
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
	Text             string
	AttemptID        string
	Opening          *session.Opened
	Provider         session.Result
	Compaction       session.Result
	FailureStage     string
	CleanupConfirmed bool
	// Session resumes this role next time.
	Session json.RawMessage
}

type Runner interface {
	Run(ctx context.Context, spec Spec) (Result, error)
}

// Native runs roles through lib-agent-harness sessions whose command sandbox
// the harness proves before launch.
type Native struct {
	// open starts or resumes a session; empty is the harness's own.
	open func(ctx context.Context, o session.Options, resume json.RawMessage) (conversation, session.Opened, error)
	// cleanupTimeout is overridden only by synthetic deadline tests.
	cleanupTimeout time.Duration
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

func (n Native) Run(ctx context.Context, spec Spec) (out Result, err error) {
	if spec.PreparationError != nil {
		return out, spec.PreparationError
	}
	if spec.Observer != nil {
		spec.Observer.Started()
		defer spec.Observer.Ended()
	}
	opener := n.open
	if opener == nil {
		opener = open
	}
	o := options(spec)
	confirmed := false
	out.FailureStage = "launch"
	if o.Workbench != nil {
		if err = os.MkdirAll(o.RuntimeHome, 0o700); err != nil {
			return
		}
		if err = os.Chmod(o.RuntimeHome, 0o700); err != nil {
			return
		}
	}
	if len(spec.Tools) > 0 && o.Workbench == nil {
		var host *session.ToolHost
		var cleanup func(bool)
		host, cleanup, err = toolHost(spec)
		if err != nil {
			return
		}
		defer func() { cleanup(confirmed) }()
		o.Sandbox.Tools = host
	}
	out.FailureStage = "opening"
	s, opening, err := opener(ctx, o, spec.Resume)
	if len(spec.Resume) > 0 && errors.Is(err, session.ErrIncompatibleResume) {
		s, opening, err = opener(ctx, o, nil)
		if err == nil {
			opening.Fresh = session.FreshIncompatible
		}
	}
	if err != nil {
		if facts, ok := harness.ErrorFacts(err); (ok && facts.Phase == session.BeforeLaunch) || errors.Is(err, os.ErrNotExist) {
			out.FailureStage = "launch"
		}
		return out, err
	}
	out.Opening = &opening
	out.Session, _ = json.Marshal(s.Ref())
	var active, compaction turn
	var heard chan struct{}
	// Release on every opened-session path, even when persistence or submission fails.
	defer func() {
		out.Session, _ = json.Marshal(s.Ref())
		budget := n.cleanupTimeout
		if budget == 0 {
			budget = 10 * time.Second
		}
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), budget)
		defer cancel()
		reclaimed, releaseErr := s.Release(releaseCtx)
		confirmed = reclaimed.Confirmed
		out.CleanupConfirmed = confirmed
		// Cleanup may consume its budget. Evidence collection gets a fresh
		// bounded opportunity to read already-settled provider accounting.
		accountCtx, accountCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer accountCancel()
		if compaction != nil && ctx.Err() != nil {
			settled, _ := compaction.Wait(accountCtx)
			if settled.Status != "" || settled.Usage.Known || settled.Observed.Known {
				out.Compaction = settled
			}
		}
		if active != nil && (ctx.Err() != nil || out.FailureStage == "acceptance_record") {
			// Reclamation stops the session; collect the terminal evidence it
			// can still provide without treating cancellation as measured zero.
			settled, _ := active.Wait(accountCtx)
			if settled.Status != "" || settled.Usage.Known || settled.Observed.Known {
				out.Provider = settled
			}
			select {
			case <-heard:
			case <-accountCtx.Done():
			}
		}
		if releaseErr != nil {
			s.Close()
			if err == nil {
				err = releaseErr
				out.FailureStage = "release"
			}
		}
		if spec.Ended != nil {
			spec.Ended(confirmed)
		}
	}()
	if spec.Opened != nil {
		spec.Opened(s.Ref())
	}
	if spec.Opening != nil {
		if err = spec.Opening(opening, s.Ref()); err != nil {
			out.FailureStage = "opening_record"
			return
		}
	}
	if spec.Compact && opening.Resumed && harness.Support(o.Provider.Engine, harness.Session, harness.Compact).Usable() {
		out.FailureStage = "compaction"
		out.Compaction, compaction, err = compact(ctx, s)
		if err != nil {
			return
		}
	}
	prompt := spec.Prompt
	if !opening.Resumed && spec.FreshPrompt != "" {
		prompt = spec.FreshPrompt
	}
	out.FailureStage = "start"
	workTurn, err := s.StartTurn(ctx, session.Input{Text: prompt})
	if err != nil {
		return out, err
	}
	if spec.Observer != nil {
		spec.Observer.Asked(prompt)
	}
	active = workTurn
	heard = make(chan struct{})
	go func() {
		defer close(heard)
		for e := range workTurn.Events() {
			if spec.Observer != nil {
				spec.Observer.Saw(e)
			}
		}
	}()
	if spec.Accepted != nil {
		if err = spec.Accepted(); err != nil {
			out.FailureStage = "acceptance_record"
			return
		}
	}
	out.FailureStage = "wait"
	out.Provider, err = workTurn.Wait(ctx)
	if ctx.Err() == nil {
		<-heard
	}
	if err != nil {
		return
	}
	if out.Provider.Status != "completed" {
		err = fmt.Errorf("the %s session ended its turn as %s", spec.Engine, out.Provider.Status)
		return
	}
	out.Text = out.Provider.Text
	out.FailureStage = ""
	return
}

// options preserves CLI settings and uses the API workbench for API roles.
// Codex needs a private login home; the API needs a private transcript home.
func options(spec Spec) session.Options {
	o := session.Options{
		Provider:    harness.Provider{Engine: harness.Engine(spec.Engine), CLI: harness.CLI{Binary: spec.Binary, Home: spec.Home}},
		RuntimeHome: spec.RuntimeHome,
		WorkDir:     spec.WorkDir,
		Model:       spec.Model,
		Effort:      spec.Effort,
		Sandbox:     &session.Sandbox{Write: spec.Write, Read: spec.Read, Web: spec.Web, Loopback: spec.Loopback},
		Env:         spec.Env,
		Browser:     spec.Browser,
		Skills:      spec.Skills,
	}
	if o.Provider.Engine.Transport() == harness.APITransport {
		o.Provider = spec.Provider
		o.Provider.Engine = harness.Engine(spec.Engine)
		o.AccountIdentity = spec.AccountIdentity
		o.Sandbox, o.Env, o.Browser = nil, nil, false
		o.Workbench = &session.Workbench{Write: spec.Write}
		if harness.Support(o.Provider.Engine, harness.Session, harness.Sandbox).Usable() {
			o.Workbench.Commands = &session.Commands{Read: spec.Read, Loopback: spec.Loopback, Env: CommandEnv(spec.Env, spec.Read)}
		}
		o.Restriction = &session.Restriction{Tools: session.ToolHost{Server: "crew", Tools: spec.Tools, Handler: spec.Handler, MaxResultBytes: 128 << 10}}
		o.Loop = session.Loop{MaxSteps: 1024, MaxRequestBytes: 64 << 20, RequestTimeout: 5 * time.Minute}
		if spec.Instructions != "" {
			o.Instructions = session.Instructions{Mode: session.Append, Text: spec.Instructions}
		}
		return o
	}
	if o.Browser && o.Provider.Engine == harness.Codex && o.Sandbox != nil {
		o.BrowserBridgeHome = spec.BridgeHome
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
func open(ctx context.Context, o session.Options, resume json.RawMessage) (conversation, session.Opened, error) {
	var ref *session.Ref
	var stored session.Ref
	if len(resume) > 0 && json.Unmarshal(resume, &stored) == nil {
		ref = &stored
	}
	s, opened, err := session.Open(ctx, o, ref)
	if err != nil {
		return nil, session.Opened{}, err
	}
	if len(resume) > 0 && ref == nil && !opened.Resumed {
		opened.Fresh = session.FreshIncompatible
	}
	return harnessSession{s}, opened, nil
}

// compact runs the session's own context compaction to its end.
func compact(ctx context.Context, s conversation) (session.Result, turn, error) {
	t, err := s.Compact(ctx)
	if err != nil {
		return session.Result{}, nil, fmt.Errorf("compacting the conversation: %w", err)
	}
	go func() {
		for range t.Events() {
		}
	}()
	result, err := t.Wait(ctx)
	if err != nil {
		return result, t, fmt.Errorf("compacting the conversation: %w", err)
	}
	if result.Status != "completed" {
		return result, t, fmt.Errorf("compacting the conversation ended as %s", result.Status)
	}
	return result, t, nil
}

// Permanent reports whether a failure will recur on retry: the installed CLI
// cannot be put under the required sandbox, or has no login to use.
func Permanent(err error) bool {
	var capability *session.CapabilityError
	var unsupported *session.UnsupportedError
	return errors.As(err, &capability) || errors.As(err, &unsupported)
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

// CommandEnv carries the medium's build settings and the app's port to
// hosted commands. Only PATH and locale settings are inherited from the daemon;
// HOME and TMPDIR belong to the harness's private scratch. Other caller
// settings are passed whole so the harness refuses unsafe names before launch.
// read is what the sandbox may read beyond its system set.
func CommandEnv(extra, read []string) []string {
	var env []string
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if key == "PATH" && ok {
			entry = "PATH=" + readablePath(value, read)
		}
		if ok && (key == "PATH" || key == "LANG" || strings.HasPrefix(key, "LC_")) {
			env = append(env, entry)
		}
	}
	for _, entry := range extra {
		key, _, _ := strings.Cut(entry, "=")
		if key != "HOME" && key != "TMPDIR" {
			env = append(env, entry)
		}
	}
	return env
}

// readablePath drops PATH folders in the owner's home that the sandbox can't
// read. The sandbox still lets their programs start, so a tool found there,
// such as an nvm node behind a "#!/usr/bin/env node" script, crashes instead
// of the search moving on to one it can run.
func readablePath(path string, read []string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	var kept []string
	for _, dir := range filepath.SplitList(path) {
		if !within(home, dir) || slices.ContainsFunc(read, func(r string) bool { return within(r, dir) }) {
			kept = append(kept, dir)
		}
	}
	return strings.Join(kept, string(filepath.ListSeparator))
}

func within(root, dir string) bool {
	rel, err := filepath.Rel(root, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
