package work

import (
	"context"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
)

// askImplementer makes a request of the task's implementer the way the
// assistant's tool does. Compacting needs a Codex implementer, so it makes the
// writer one.
func askImplementer(t *testing.T, a *Loop, task core.Task, next string) {
	t.Helper()
	ctx := context.Background()
	if next == core.WriterCompact {
		if _, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
			for i := range t.Roles {
				if t.Roles[i].Kind == core.RoleImplementer {
					t.Roles[i].Engine = "codex"
				}
			}
			return "", nil
		}); err != nil {
			t.Error(err)
		}
	}
	if _, err := a.Core.SetWriterNext(ctx, task.ProjectID, task.ID, next); err != nil {
		t.Error(err)
	}
}

// An implementer told to start afresh or compact does so at its next round.
// The round under way when it was asked finishes as it is.
func TestTheImplementerStartsAfreshOrCompactsAtItsNextRound(t *testing.T) {
	for _, next := range []string{core.WriterFresh, core.WriterCompact} {
		t.Run(next, func(t *testing.T) {
			runner := &scriptedRunner{reviews: []string{revise, revise, pass}}
			a, _, task := loopApp(t, runner, "")
			runner.onWriter = func(string) {
				if runner.writes == 2 {
					askImplementer(t, a, task, next) // Mid-round.
				}
			}
			done := settle(t, a)
			if len(runner.seen) != 6 || done.Round != 3 {
				t.Fatalf("rounds %d %+v", len(runner.seen), done)
			}
			during, after := runner.seen[2], runner.seen[4]
			if len(during.Resume) == 0 || during.Compact {
				t.Fatalf("the round under way changed: %+v", during)
			}
			switch next {
			case core.WriterFresh:
				if len(after.Resume) != 0 || after.Compact {
					t.Fatalf("the next round did not start afresh: %+v", after)
				}
			case core.WriterCompact:
				if len(after.Resume) == 0 || !after.Compact {
					t.Fatalf("the next round did not compact: %+v", after)
				}
			}
			if done.WriterNext != "" || len(done.WriterSession) == 0 {
				t.Fatalf("the request was not settled: %+v", done)
			}
		})
	}
}

// Asking again for the same thing while a round is carrying it out is a new
// request: it applies to the round after, rather than being cleared with the
// one the running round took.
func TestTheSameRequestMadeMidRoundAppliesToTheNextRoundToo(t *testing.T) {
	for _, next := range []string{core.WriterFresh, core.WriterCompact} {
		t.Run(next, func(t *testing.T) {
			runner := &scriptedRunner{reviews: []string{revise, revise, revise, pass}}
			a, p, task := loopApp(t, runner, "")
			playbook := *p.Playbook
			playbook.MaxRounds = 5
			if _, err := a.Core.SetPlaybook(context.Background(), p.ID, playbook); err != nil {
				t.Fatal(err)
			}
			runner.onWriter = func(string) {
				if runner.writes == 2 || runner.writes == 3 {
					askImplementer(t, a, task, next)
				}
			}
			done := settle(t, a)
			if len(runner.seen) != 8 || done.Round != 4 {
				t.Fatalf("rounds %d %+v", len(runner.seen), done)
			}
			for _, i := range []int{4, 6} {
				round := runner.seen[i]
				applied := len(round.Resume) == 0 && !round.Compact
				if next == core.WriterCompact {
					applied = len(round.Resume) != 0 && round.Compact
				}
				if !round.Write || !applied {
					t.Fatalf("writer round %d did not carry out its request: %+v", i/2+1, round)
				}
			}
			if done.WriterNext != "" {
				t.Fatalf("the last request was not settled: %+v", done)
			}
		})
	}
}
