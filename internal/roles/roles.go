// Package roles runs one turn of a team role as a sandboxed native coding
// session. Nothing here decides what a role is asked or what happens next.
package roles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

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
	Write        bool
	Instructions string
	Prompt       string
	// Resume continues an earlier session of this role, when it still matches
	// this configuration. The prompt must stand on its own either way.
	Resume json.RawMessage
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
type Native struct{}

func (Native) Run(ctx context.Context, spec Spec) (Result, error) {
	o := session.Options{
		Engine:      session.Engine(spec.Engine),
		Binary:      spec.Binary,
		Home:        spec.Home,
		RuntimeHome: spec.RuntimeHome,
		WorkDir:     spec.WorkDir,
		Model:       spec.Model,
		Effort:      spec.Effort,
		Sandbox:     &session.Sandbox{Write: spec.Write},
	}
	if spec.Engine != string(session.Codex) {
		o.RuntimeHome = ""
	}
	if spec.Instructions != "" {
		o.Instructions = session.Instructions{Mode: session.Append, Text: spec.Instructions}
	}
	s, err := open(ctx, o, spec.Resume)
	if err != nil {
		return Result{}, err
	}
	released := false
	defer func() {
		if !released {
			s.Close()
		}
	}()
	turn, err := s.StartTurn(ctx, session.Input{Text: spec.Prompt})
	if err != nil {
		return Result{}, err
	}
	go func() {
		for range turn.Events() {
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

// open resumes the recorded session when it is still compatible and otherwise
// starts a fresh one. A changed model or engine makes an old session unusable;
// the prompt carries everything a fresh session needs.
func open(ctx context.Context, o session.Options, resume json.RawMessage) (*session.Session, error) {
	if len(resume) == 0 {
		return session.Start(ctx, o)
	}
	var ref session.Ref
	if json.Unmarshal(resume, &ref) != nil || ref.Engine != o.Engine {
		return session.Start(ctx, o)
	}
	s, err := session.Resume(ctx, o, ref)
	if errors.Is(err, session.ErrIncompatibleResume) || errors.Is(err, session.ErrRejected) {
		return session.Start(ctx, o)
	}
	return s, err
}

// Permanent reports whether a failure will recur on retry: the installed CLI
// cannot be put under the required sandbox, or has no login to use.
func Permanent(err error) bool {
	var capability *session.CapabilityError
	return errors.As(err, &capability)
}
