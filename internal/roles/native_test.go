package roles

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/session"
)

// fakeSession stands in for a harness session and records, in order, what
// was asked of it and when each turn finished.
type fakeSession struct {
	calls      []string
	compactErr error
	compacted  session.Result
	waitErr    error
}

type fakeTurn struct {
	s      *fakeSession
	name   string
	result session.Result
	err    error
}

func (t fakeTurn) Events() <-chan session.Event {
	events := make(chan session.Event)
	close(events)
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
	return fakeTurn{s: s, name: "turn", result: session.Result{Status: "completed", Text: "Done."}}, nil
}

func (s *fakeSession) Ref() session.Ref { return session.Ref{Engine: harness.Codex, ID: "thread"} }

func (s *fakeSession) Release(context.Context) (session.Reclamation, error) {
	s.calls = append(s.calls, "release")
	return session.Reclamation{}, nil
}

func (s *fakeSession) Close() { s.calls = append(s.calls, "close") }

// native runs roles on s, which opens as resumed or fresh.
func native(s *fakeSession, resumed bool) Native {
	return Native{open: func(context.Context, session.Options, json.RawMessage) (conversation, bool, error) {
		return s, resumed, nil
	}}
}

var codexRound = Spec{Engine: "codex", WorkDir: "/work", Write: true, Prompt: "Revise the draft", Resume: json.RawMessage(`{"engine":"codex","id":"thread"}`)}

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
			// The session is closed, not kept for the next round.
			if s.calls[len(s.calls)-1] != "close" {
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
	n := Native{open: func(_ context.Context, o session.Options, _ json.RawMessage) (conversation, bool, error) {
		seen = o
		return s, false, nil
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

type recordingObserver struct{ calls []string }

func (o *recordingObserver) Started()          { o.calls = append(o.calls, "started") }
func (o *recordingObserver) Saw(session.Event) { o.calls = append(o.calls, "saw") }
func (o *recordingObserver) Ended()            { o.calls = append(o.calls, "ended") }

// A turn is watched from before its session opens, so opening counts as
// picked up, until after it is released.
func TestAnObserverHearsTheWholeTurn(t *testing.T) {
	s := &fakeSession{}
	o := &recordingObserver{}
	spec := codexRound
	spec.Observer = o
	if _, err := native(s, true).Run(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if len(o.calls) < 2 || o.calls[0] != "started" || o.calls[len(o.calls)-1] != "ended" || !slices.Contains(s.calls, "release") {
		t.Fatalf("observer %v, session %v", o.calls, s.calls)
	}
}
