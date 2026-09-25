package roles

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

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

func (s *fakeSession) Ref() session.Ref { return session.Ref{Engine: session.Codex, ID: "thread"} }

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
	}{
		"fresh session asked to compact":   {false, true},
		"resumed session not asked":        {true, false},
		"fresh session with nothing to do": {false, false},
	} {
		t.Run(name, func(t *testing.T) {
			s := &fakeSession{compacted: session.Result{Status: "completed"}}
			spec := codexRound
			spec.Compact = run.compact
			if _, err := native(s, run.resumed).Run(context.Background(), spec); err != nil {
				t.Fatal(err)
			}
			if want := []string{"turn: Revise the draft", "turn finished", "release"}; !slices.Equal(s.calls, want) {
				t.Fatalf("got %q, want %q", s.calls, want)
			}
		})
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
