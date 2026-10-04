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
	"sync"
	"testing"
	"time"

	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/session"
)

// fakeSession stands in for a harness session and records, in order, what
// was asked of it and when each turn finished.
type fakeSession struct {
	ref        *session.Ref
	calls      []string
	compactErr error
	compacted  session.Result
	waitErr    error
	// events are what its work turn reports.
	events []session.Event
	// confirmed is whether releasing it confirms it is gone.
	confirmed bool
}

type fakeTurn struct {
	s      *fakeSession
	name   string
	result session.Result
	err    error
	events []session.Event
}

// Events are the turn's events, delivered slowly, as a busy session might
// still be delivering them when its turn finishes.
func (t fakeTurn) Events() <-chan session.Event {
	events := make(chan session.Event)
	go func() {
		defer close(events)
		for _, e := range t.events {
			time.Sleep(time.Millisecond)
			events <- e
		}
	}()
	return events
}

func (t fakeTurn) Wait(context.Context) (session.Result, error) {
	t.s.calls = append(t.s.calls, t.name+" finished")
	return t.result, t.err
}

func (s *fakeSession) Compact(context.Context) (turn, error) {
	s.calls = append(s.calls, "compact")
	if s.compactErr != nil {
		return nil, s.compactErr
	}
	return fakeTurn{s: s, name: "compact", result: s.compacted, err: s.waitErr}, nil
}

func (s *fakeSession) StartTurn(_ context.Context, in session.Input) (turn, error) {
	s.calls = append(s.calls, "turn: "+in.Text)
	return fakeTurn{s: s, name: "turn", result: session.Result{Status: "completed", Text: "Done."}, events: s.events}, nil
}

func (s *fakeSession) Ref() session.Ref {
	if s.ref != nil {
		return *s.ref
	}
	return session.Ref{Engine: harness.Codex, ID: "thread"}
}

func (s *fakeSession) Release(context.Context) (session.Reclamation, error) {
	s.calls = append(s.calls, "release")
	return session.Reclamation{Confirmed: s.confirmed}, nil
}

func (s *fakeSession) Close() { s.calls = append(s.calls, "close") }

// native runs roles on s, which opens as resumed or fresh.
func native(s *fakeSession, resumed bool) Native {
	return Native{open: func(context.Context, session.Options, json.RawMessage) (conversation, session.Opened, error) {
		return s, session.Opened{Resumed: resumed}, nil
	}}
}

var codexRound = Spec{Engine: "codex", WorkDir: "/work", Write: true, Prompt: "Revise the draft", Resume: json.RawMessage(`{"engine":"codex","id":"thread"}`)}

func TestChangedAPISettingsOpenFreshAndCarryTheFreshPrompt(t *testing.T) {
	for _, problem := range []error{fmt.Errorf("changed workbench: %w", session.ErrIncompatibleResume), errors.New("provider unavailable")} {
		s := &fakeSession{confirmed: true, ref: &session.Ref{Engine: harness.OpenAICompatible, ID: "fresh", ConfigHash: "new-workbench"}}
		calls := 0
		n := Native{open: func(_ context.Context, o session.Options, ref json.RawMessage) (conversation, session.Opened, error) {
			calls++
			if calls == 1 {
				if len(ref) == 0 || o.Workbench == nil {
					t.Fatal("missing stored API reference or workbench")
				}
				return nil, session.Opened{}, problem
			}
			if len(ref) != 0 {
				t.Fatal("fresh open retained the incompatible reference")
			}
			return s, session.Opened{}, nil
		}}
		spec := Spec{Engine: "openai-compatible", RuntimeHome: t.TempDir(), Resume: json.RawMessage(`{"engine":"openai-compatible","id":"old"}`), Prompt: "Continue", FreshPrompt: "The task and its latest revision", Compact: true}
		result, err := n.Run(context.Background(), spec)
		if errors.Is(problem, session.ErrIncompatibleResume) {
			if err != nil || result.Opening == nil || result.Opening.Fresh != session.FreshIncompatible || calls != 2 || len(result.Session) == 0 || !slices.Contains(s.calls, "turn: "+spec.FreshPrompt) || slices.Contains(s.calls, "compact") {
				t.Fatalf("%+v %v %v", result, err, s.calls)
			}
		} else if err != problem || calls != 1 || len(s.calls) != 0 {
			t.Fatalf("other error retried: %v %d", err, calls)
		}
	}
}

func TestAPIRuntimeHomeIsPrivateBecauseTheLibraryNeedsIt(t *testing.T) {
	home := filepath.Join(t.TempDir(), "transcripts")
	if err := os.Mkdir(home, 0755); err != nil {
		t.Fatal(err)
	}
	s := &fakeSession{confirmed: true}
	n := Native{open: func(_ context.Context, o session.Options, _ json.RawMessage) (conversation, session.Opened, error) {
		info, err := os.Stat(o.RuntimeHome)
		if err != nil || info.Mode().Perm() != 0700 || o.RuntimeHome != home {
			t.Fatalf("API transcript home: %v %v", info, err)
		}
		if o.Sandbox != nil || o.Restriction.Tools.Dir != "" || o.Restriction.Tools.Bridge.Path != "" {
			t.Fatal("API got CLI launch state", o)
		}
		return s, session.Opened{}, nil
	}}
	launch := filepath.Join(t.TempDir(), "must-not-be-created")
	if _, err := n.Run(context.Background(), Spec{Engine: "openai-compatible", RuntimeHome: home, LaunchDir: launch, Tools: []session.ToolDefinition{{Name: "read_task"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(launch); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("API role created a CLI launch directory", err)
	}
}

func TestAResumedSessionFinishesCompactingBeforeItsTurnStarts(t *testing.T) {
	s := &fakeSession{compacted: session.Result{Status: "completed"}}
	spec := codexRound
	spec.Compact = true
	result, err := native(s, true).Run(context.Background(), spec)
	if err != nil || result.Text != "Done." {
		t.Fatal(result, err)
	}
	want := []string{"compact", "compact finished", "turn: Revise the draft", "turn finished", "release"}
	if !slices.Equal(s.calls, want) {
		t.Fatalf("got %q, want %q", s.calls, want)
	}
}

func TestAFreshSessionOrOneNotAskedToCompactGoesStraightToItsTurn(t *testing.T) {
	for name, run := range map[string]struct {
		resumed, compact bool
		engine           string
	}{
		"fresh session asked to compact":   {false, true, "codex"},
		"resumed session not asked":        {true, false, "codex"},
		"fresh session with nothing to do": {false, false, "codex"},
		// The harness says Claude can't be asked to compact from outside.
		"resumed session that can't compact": {true, true, "claude"},
	} {
		t.Run(name, func(t *testing.T) {
			s := &fakeSession{compacted: session.Result{Status: "completed"}}
			spec := codexRound
			spec.Compact, spec.Engine = run.compact, run.engine
			if _, err := native(s, run.resumed).Run(context.Background(), spec); err != nil {
				t.Fatal(err)
			}
			if want := []string{"turn: Revise the draft", "turn finished", "release"}; !slices.Equal(s.calls, want) {
				t.Fatalf("got %q, want %q", s.calls, want)
			}
		})
	}
}

// A turn that can't carry its conversation on is given what the
// conversation would have remembered; one that does is not told it twice.
func TestAFreshSessionIsGivenItsFreshPrompt(t *testing.T) {
	for _, resumed := range []bool{true, false} {
		s := &fakeSession{}
		spec := codexRound
		spec.FreshPrompt = "Everything so far, then revise the draft"
		if _, err := native(s, resumed).Run(context.Background(), spec); err != nil {
			t.Fatal(err)
		}
		want := "turn: " + spec.FreshPrompt
		if resumed {
			want = "turn: " + spec.Prompt
		}
		if s.calls[0] != want {
			t.Fatalf("resumed %v: got %q", resumed, s.calls)
		}
	}
}

// A turn's tools hear which session it runs in before the turn starts, so
// they can find what the session keeps while the turn is still running,
// and once it is over whether the session was confirmed gone.
func TestATurnHearsItsSessionBeforeItStartsAndWhetherItEnded(t *testing.T) {
	for _, confirmed := range []bool{true, false} {
		s := &fakeSession{confirmed: confirmed}
		spec := codexRound
		var heard []session.Ref
		spec.Opened = func(ref session.Ref) {
			heard = append(heard, ref)
			s.calls = append(s.calls, "opened")
		}
		spec.Ended = func(confirmed bool) { s.calls = append(s.calls, fmt.Sprintf("ended, confirmed %v", confirmed)) }
		if _, err := native(s, false).Run(context.Background(), spec); err != nil {
			t.Fatal(err)
		}
		if len(heard) != 1 || heard[0].ID != "thread" {
			t.Fatalf("heard %+v", heard)
		}
		want := []string{"opened", "turn: Revise the draft", "turn finished", "release", fmt.Sprintf("ended, confirmed %v", confirmed)}
		if !slices.Equal(s.calls, want) {
			t.Fatalf("got %q, want %q", s.calls, want)
		}
	}
}

// A failed submission still releases its session and reports confirmed cleanup.
func TestAFailedSubmissionReportsReclamation(t *testing.T) {
	s := &fakeSession{confirmed: true}
	n := Native{open: func(context.Context, session.Options, json.RawMessage) (conversation, session.Opened, error) {
		return failingStart{s}, session.Opened{}, nil
	}}
	spec := codexRound
	spec.Opened = func(session.Ref) {}
	var ended []bool
	spec.Ended = func(confirmed bool) { ended = append(ended, confirmed) }
	if _, err := n.Run(context.Background(), spec); err == nil {
		t.Fatal("the turn did not fail")
	}
	if !slices.Equal(ended, []bool{true}) || !slices.Equal(s.calls, []string{"release"}) {
		t.Fatalf("ended %v, calls %q", ended, s.calls)
	}
}

// failingStart is a session whose turn can't start, as one stopped would.
type failingStart struct{ *fakeSession }

func (failingStart) StartTurn(context.Context, session.Input) (turn, error) {
	return nil, context.Canceled
}

// What a stored conversation may be resumed under is the harness's to
// judge: the reference names the working directory and a digest of the
// model, effort, instructions and sandbox, and nothing names the seat. A
// reference that doesn't match is refused before anything is launched, and
// a role then starts afresh. So seats that share a member, a task and a
// workspace can carry on one conversation, and nothing else can.
func TestAStoredConversationResumesOnlyWhereItWasStarted(t *testing.T) {
	work := t.TempDir()
	spec := Spec{Engine: "claude", Binary: filepath.Join(t.TempDir(), "claude"), Home: t.TempDir(), WorkDir: work, Write: true, Model: "a-model", Instructions: "Be brief."}
	stored := session.Ref{Engine: "claude", ID: "conversation", Home: spec.Home, WorkDir: filepath.Join(t.TempDir(), "another-task")}
	if _, err := session.Resume(context.Background(), options(spec), stored); !errors.Is(err, session.ErrIncompatibleResume) {
		t.Fatalf("a conversation from another working directory: %v", err)
	}
	stored.WorkDir = work
	stored.ConfigHash = "another-configuration"
	if _, err := session.Resume(context.Background(), options(spec), stored); !errors.Is(err, session.ErrIncompatibleResume) {
		t.Fatalf("a conversation under another configuration: %v", err)
	}
}

func TestAFailedCompactionStopsTheTurnFromStarting(t *testing.T) {
	for name, s := range map[string]*fakeSession{
		"refused":              {compactErr: errors.New("compact unsupported")},
		"failed while running": {waitErr: errors.New("thread lost")},
		"ended unfinished":     {compacted: session.Result{Status: "interrupted"}},
	} {
		t.Run(name, func(t *testing.T) {
			spec := codexRound
			spec.Compact = true
			_, err := native(s, true).Run(context.Background(), spec)
			if err == nil || !strings.Contains(err.Error(), "compacting the conversation") {
				t.Fatal(err)
			}
			for _, call := range s.calls {
				if strings.HasPrefix(call, "turn") {
					t.Fatalf("the work turn started: %q", s.calls)
				}
			}
			// The failed session is released before returning.
			if s.calls[len(s.calls)-1] != "release" {
				t.Fatalf("got %q", s.calls)
			}
		})
	}
}

// A turn with tools serves them from a folder of its own outside the work,
// through this binary as the bridge, and the folder goes with the turn; a
// turn without tools hosts none. Web is the spec's to ask for.
func TestATurnsToolsAreHostedForThatTurnOnly(t *testing.T) {
	var seen session.Options
	s := &fakeSession{}
	n := Native{open: func(_ context.Context, o session.Options, _ json.RawMessage) (conversation, session.Opened, error) {
		seen = o
		return s, session.Opened{}, nil
	}}
	spec := Spec{Engine: "claude", WorkDir: t.TempDir(), Prompt: "Plan it", Web: true, Tools: []session.ToolDefinition{{Name: "list_tasks"}}, Handler: session.ToolHandlerFunc(func(context.Context, session.ToolCall) (session.ToolResult, error) {
		return session.ToolResult{}, nil
	})}
	if _, err := n.Run(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	host := seen.Sandbox.Tools
	if host == nil || !seen.Sandbox.Web || host.Server != "crew" || host.Bridge.Args[0] != ToolBridge || strings.HasPrefix(host.Dir, spec.WorkDir) {
		t.Fatalf("sandbox %+v, host %+v", seen.Sandbox, host)
	}
	if _, err := os.Stat(host.Dir); !os.IsNotExist(err) {
		t.Fatal("the turn's tool folder outlived it")
	}
	spec.Tools, spec.Web = nil, false
	n.Run(context.Background(), spec)
	if seen.Sandbox.Tools != nil || seen.Sandbox.Web {
		t.Fatalf("a turn without tools got %+v", seen.Sandbox)
	}
}

type recordingObserver struct {
	mu    sync.Mutex
	calls []string
}

func (o *recordingObserver) add(call string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls = append(o.calls, call)
}
func (o *recordingObserver) Started()            { o.add("started") }
func (o *recordingObserver) Asked(prompt string) { o.add("asked: " + prompt) }
func (o *recordingObserver) Saw(e session.Event) { o.add("saw: " + e.Kind) }
func (o *recordingObserver) Ended()              { o.add("ended") }
func (o *recordingObserver) heard() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.calls)
}

// A turn is watched from before its session opens, so opening counts as
// picked up, until after it is released. It is told the prompt the session
// was given, and hears every event before it ends, even ones still on their
// way when the turn finished.
func TestAnObserverHearsTheWholeTurn(t *testing.T) {
	s := &fakeSession{events: []session.Event{{Kind: "text"}, {Kind: "tool_started"}, {Kind: "tool_completed"}}}
	o := &recordingObserver{}
	spec := codexRound
	spec.FreshPrompt = "Everything so far, then revise the draft"
	spec.Observer = o
	if _, err := native(s, false).Run(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	want := []string{"started", "asked: " + spec.FreshPrompt, "saw: text", "saw: tool_started", "saw: tool_completed", "ended"}
	if got := o.heard(); !slices.Equal(got, want) || !slices.Contains(s.calls, "release") {
		t.Fatalf("observer %q, session %v", got, s.calls)
	}
}
