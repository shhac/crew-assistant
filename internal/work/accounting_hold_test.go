package work

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/roles"
	"github.com/shhac/lib-agent-harness/sandbox"
	"github.com/shhac/lib-agent-harness/session"
)

// A claim held only because its finished turn's accounting failed is let go
// at startup even when the command sweep fails: its session had ended, and
// keeping it would hold its member in every project.
func TestAnAccountingHoldGoesAtStartupEvenWhenTheSweepFails(t *testing.T) {
	a, p, _ := loopApp(t, &scriptedRunner{}, "")
	ctx := context.Background()
	claims, err := a.Core.Schedule(ctx, func(r core.Role) string { return "" })
	if err != nil || len(claims) == 0 {
		t.Fatalf("no claim: %v %v", claims, err)
	}
	claim := claims[0]
	if err := a.Core.HoldClaim(ctx, claim.Task.ID, p.ID, claim.Claim.Token, accountingHold); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(a.commandsHome(), "commands", "interrupted"), 0700); err != nil {
		t.Fatal(err)
	}
	a.commands = func(context.Context, sandbox.Options) (commandSandbox, error) {
		return &fakeCommands{closeErr: errors.New("cleanup unavailable")}, nil
	}
	_ = a.resume(ctx)
	if held := taskByID(t, a, claim.Task.ID).Claims; len(held) != 0 {
		t.Fatalf("the accounting hold survived a failed sweep: %+v", held)
	}
}

// skewedClock has the loop's clock run ahead of the wall clock by whatever
// it returns holds, so a test can move past a wait without sleeping.
func skewedClock(lp *Loop) *atomic.Int64 {
	ahead := &atomic.Int64{}
	lp.Now = func() time.Time { return time.Now().Add(time.Duration(ahead.Load())) }
	return ahead
}

// A claim held because its finished turn's accounting could not be recorded
// is let go once a later try records it, while the daemon runs, even when
// the loop cannot sweep up an unrelated command meanwhile.
func TestAnAccountingHoldGoesOnceItsAccountingIsRecorded(t *testing.T) {
	for _, sweepFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("sweep fails %v", sweepFails), func(t *testing.T) {
			lp, _, task := loopApp(t, &scriptedRunner{}, "")
			ctx := context.Background()
			ahead := skewedClock(lp)
			var failing atomic.Bool
			failing.Store(true)
			lp.runner = accountingRunner(func(ctx context.Context, spec roles.Spec) (roles.Result, error) {
				if err := spec.Opening(session.Opened{}, session.Ref{ID: "thread"}); err != nil {
					return roles.Result{}, err
				}
				return roles.Result{Text: "finished", Provider: session.Result{Status: "completed"}, CleanupConfirmed: true}, nil
			})
			lp.finishTeamTurn = func(ctx context.Context, id string, end core.TeamTurnTerminal) error {
				if failing.Load() {
					return errors.New("accounting unavailable")
				}
				return lp.Core.FinishTeamTurn(ctx, id, end)
			}
			if sweepFails {
				if err := os.MkdirAll(filepath.Join(lp.commandsHome(), "commands", "interrupted"), 0700); err != nil {
					t.Fatal(err)
				}
				lp.commands = func(context.Context, sandbox.Options) (commandSandbox, error) {
					return &fakeCommands{closeErr: errors.New("cleanup unavailable")}, nil
				}
				lp.commandChecks.Store("unrelated copy", commandCleanupObligation{remove: func() {}})
			}
			step(t, lp)
			held := taskByID(t, lp, task.ID).Claims
			if len(held) != 1 || held[0].Held != accountingHold {
				t.Fatalf("not held for accounting: %+v", held)
			}
			failing.Store(false)
			step(t, lp)
			if now := taskByID(t, lp, task.ID).Claims; len(now) != 1 || now[0].Held != accountingHold {
				t.Fatalf("let go before its retry was due: %+v", now)
			}
			ahead.Store(int64(accountingRetryWait))
			step(t, lp)
			if slices.ContainsFunc(taskByID(t, lp, task.ID).Claims, func(c core.Claim) bool { return c.Token == held[0].Token }) {
				t.Fatal("the accounting hold outlived its recorded accounting")
			}
			turns, err := lp.Core.TeamTurns(ctx, core.TeamTurnFilter{TaskID: task.ID})
			if err != nil || len(turns) == 0 || turns[0].Terminal == nil {
				t.Fatalf("accounting not recorded: %+v %v", turns, err)
			}
			if _, kept := lp.commandChecks.Load("unrelated copy"); sweepFails && !kept {
				t.Fatal("the unswept copy was dropped")
			}
		})
	}
}

func heldNotices(t *testing.T, lp *Loop) []core.Decision {
	t.Helper()
	snap, err := lp.Core.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []core.Decision
	for _, d := range snap.Decisions {
		if d.Kind == core.DecisionHeldClaim {
			out = append(out, d)
		}
	}
	return out
}

// A step held for longer than HeldNotice brings the owner one decision,
// naming the task, the seat and why, whose "Let it go" frees the seat.
func TestALongHoldIsNoticedOnce(t *testing.T) {
	lp, _, task := loopApp(t, &scriptedRunner{}, "")
	ctx := context.Background()
	ahead := skewedClock(lp)
	claims, err := lp.Core.Schedule(ctx, func(core.Role) string { return "" })
	if err != nil || len(claims) == 0 {
		t.Fatalf("no claim: %v %v", claims, err)
	}
	claim := claims[0].Claim
	why := "The previous turn's cleanup or accounting could not be confirmed"
	if err := lp.Core.RecoverClaims(ctx, map[string]string{claim.Token: why}); err != nil {
		t.Fatal(err)
	}
	ahead.Store(int64(core.HeldNotice - time.Minute))
	step(t, lp)
	if got := heldNotices(t, lp); len(got) != 0 {
		t.Fatalf("noticed too soon: %+v", got)
	}
	ahead.Store(int64(core.HeldNotice + time.Minute))
	step(t, lp)
	step(t, lp)
	got := heldNotices(t, lp)
	if len(got) != 1 {
		t.Fatalf("want one notice, got %+v", got)
	}
	d := got[0]
	if d.Status != core.DecisionOpen || d.TaskID != task.ID || d.Claim != claim.Token || !strings.Contains(d.Title, task.Objective) || !strings.Contains(d.Title, claim.Seat) || !strings.Contains(d.Context, "cleanup or accounting could not be confirmed") || !slices.Contains(d.Choices, core.ChoiceLetGo) {
		t.Fatalf("notice %+v", d)
	}
	if taskByID(t, lp, task.ID).DecisionID == d.ID {
		t.Fatal("the notice took over the task's own decision")
	}
	if _, err := lp.Core.ChooseDecision(ctx, d.ID, core.ChoiceLetGo, core.FromOwner); err != nil {
		t.Fatal(err)
	}
	if held := taskByID(t, lp, task.ID).Claims; slices.ContainsFunc(held, func(c core.Claim) bool { return c.Token == claim.Token }) {
		t.Fatalf("letting it go kept it: %+v", held)
	}
	step(t, lp)
	if got := heldNotices(t, lp); len(got) != 1 || got[0].Status != core.DecisionResolved {
		t.Fatalf("noticed again: %+v", got)
	}
}

// A notice whose step was let go some other way settles itself.
func TestAHeldNoticeSettlesOnceTheStepGoes(t *testing.T) {
	lp, p, _ := loopApp(t, &scriptedRunner{}, "")
	ctx := context.Background()
	ahead := skewedClock(lp)
	claims, err := lp.Core.Schedule(ctx, func(core.Role) string { return "" })
	if err != nil || len(claims) == 0 {
		t.Fatalf("no claim: %v %v", claims, err)
	}
	c := claims[0]
	if err := lp.Core.HoldClaim(ctx, c.Task.ID, p.ID, c.Claim.Token, accountingHold); err != nil {
		t.Fatal(err)
	}
	ahead.Store(int64(core.HeldNotice + time.Minute))
	step(t, lp)
	if got := heldNotices(t, lp); len(got) != 1 || got[0].Recommendation != core.ChoiceLetGo {
		t.Fatalf("an accounting hold's notice: %+v", got)
	}
	if err := lp.Core.ReleaseHeldClaim(ctx, c.Task.ID, "", c.Claim.Token, accountingHold); err != nil {
		t.Fatal(err)
	}
	step(t, lp)
	if got := heldNotices(t, lp); len(got) != 1 || got[0].Status != core.DecisionResolved {
		t.Fatalf("notice not settled: %+v", got)
	}
}
