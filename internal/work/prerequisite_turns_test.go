package work

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/lib-agent-harness/session"
)

func settledFirstTurn(t *testing.T, lp *Loop, p core.Project) core.Task {
	t.Helper()
	ctx := context.Background()
	own := queue(t, lp, p, "Diagnostics")
	if _, err := lp.Core.UpdateTask(ctx, own.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.Status = core.TaskResearching
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := lp.Core.RecordPlan(ctx, own.ID, core.Plan{Summary: "Build diagnostics"}, nil, []string{"Someone can push the diagnostics PR"}); err != nil {
		t.Fatal(err)
	}
	settleFirstPrerequisite(t, lp, own.ID, core.ChoiceReadyPrerequisite)
	return taskByID(t, lp, own.ID)
}

func settleFirstPrerequisite(t *testing.T, lp *Loop, id, choice string) {
	t.Helper()
	snap, err := lp.Core.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range snap.Decisions {
		if d.TaskID == id && d.Kind == core.DecisionPrerequisite && d.Status == core.DecisionOpen {
			if _, err := lp.Core.ChooseDecision(context.Background(), d.ID, choice, core.FromOwner); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatal("no pending prerequisite")
}

func reopenFirstPrerequisite(t *testing.T, lp *Loop, p core.Project, own core.Task, custom bool) {
	t.Helper()
	ctx := context.Background()
	b := own.Blockers[0]
	if custom {
		d, err := lp.Core.OpenTaskDecision(ctx, own.ID, core.DecisionQuestion, core.DecisionInput{Title: "Format?", Context: "Which format?", Recommendation: "JSON", Choices: []string{"JSON", "CSV"}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := lp.Core.AnswerDecision(ctx, d.ID, "Prerequisite "+b.ID+" no longer holds: "+b.Description, core.FromOwner); err != nil {
			t.Fatal(err)
		}
	} else if _, err := lp.Core.ReopenPrerequisite(ctx, p.ID, own.ID, b.ID, b.Description, "Owner revoked access", core.LinkedByOwner, core.PrerequisiteReopen{Source: "owner-reopen", Settlement: core.PrerequisiteSettlementID(b)}); err != nil {
		t.Fatal(err)
	}
}

func TestReopeningFirstImplementationHoldsWorkspaceUntilTurnEnds(t *testing.T) {
	for _, custom := range []bool{false, true} {
		for _, choice := range []string{core.ChoiceReadyPrerequisite, core.ChoiceDropPrerequisite} {
			t.Run(fmt.Sprintf("custom=%t/%s", custom, choice), func(t *testing.T) {
				ctx := context.Background()
				runner := &parallelRunner{}
				lp, p := parallelApp(t, runner, 0)
				own := settledFirstTurn(t, lp, p)
				started, release := make(chan struct{}), make(chan struct{})
				runner.onTurn = func(_ context.Context, _, _ string, _ bool) error {
					close(started)
					<-release // The old implementer can still write until it exits.
					return nil
				}
				_, jobs, err := lp.pass(ctx, true)
				if err != nil || len(jobs) != 1 {
					t.Fatal(jobs, err)
				}
				released := false
				defer func() {
					if !released {
						close(release)
						<-jobs[0]
					}
				}()
				if !within(started) {
					t.Fatal("first implementer never started")
				}
				before := taskByID(t, lp, own.ID)
				reopenFirstPrerequisite(t, lp, p, before, custom)
				settleFirstPrerequisite(t, lp, own.ID, choice)
				// Replacement research would use a different, available seat.
				// The task hold, not the old writer's busy seat, must prevent it.
				book := *p.Playbook
				book.Roles = append(book.Roles, core.Role{Name: "Researcher", Kinds: []string{core.RoleResearcher}, Engine: "codex"})
				if _, err := lp.Core.SetPlaybook(ctx, p.ID, book); err != nil {
					t.Fatal(err)
				}
				for range 3 {
					_, replacement, err := lp.pass(ctx, true)
					if err != nil || len(replacement) != 0 {
						t.Fatal("started replacement while old workspace writer runs", replacement, err)
					}
				}
				held := taskByID(t, lp, own.ID)
				if len(held.Claims) != 1 || !held.Claims[0].Revoked || held.Claims[0].Token != before.Claims[0].Token {
					t.Fatalf("lost durable turn hold: %+v", held.Claims)
				}
				late := core.Fenced(ctx, own.ID, before.Claims[0].Token)
				if _, err := lp.Core.UpdateTask(late, own.ID, func(*core.Task, *core.Project) (string, error) { return "late write", nil }); !errors.Is(err, core.ErrStale) {
					t.Fatal("revoked writer retained authority", err)
				}
				close(release)
				<-jobs[0]
				released = true
				if got := taskByID(t, lp, own.ID); len(got.Claims) != 0 || len(got.Revisions) != 0 || got.Handoff != nil {
					t.Fatal("late turn recorded work or failed to release its hold", got)
				}
				claimed, err := lp.Core.Schedule(ctx, func(core.Role) string { return "" })
				if err != nil || len(claimed) != 1 || claimed[0].Claim.Token == before.Claims[0].Token {
					t.Fatal("replacement did not resume after old turn ended", claimed, err)
				}
			})
		}
	}
}

func TestReopenedFirstTurnRetainsUnreclaimedLaunchAcrossRestart(t *testing.T) {
	for _, custom := range []bool{false, true} {
		t.Run(fmt.Sprintf("custom=%t", custom), func(t *testing.T) {
			ctx := context.Background()
			lp, p := parallelApp(t, &parallelRunner{}, 0)
			own := settledFirstTurn(t, lp, p)
			claimed, err := lp.Core.Schedule(ctx, func(core.Role) string { return "" })
			if err != nil || len(claimed) != 1 {
				t.Fatal(claimed, err)
			}
			token := claimed[0].Claim.Token
			if err := os.MkdirAll(lp.launchDir(token), 0o700); err != nil {
				t.Fatal(err)
			}
			reopenFirstPrerequisite(t, lp, p, taskByID(t, lp, own.ID), custom)
			settleFirstPrerequisite(t, lp, own.ID, core.ChoiceReadyPrerequisite)
			st, err := core.Open(filepath.Join(lp.Core.StateDirectory(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			again := New(core.NewService(st, lp.Config()), lp.Config, false)
			again.runner, again.meter = lp.runner, lp.meter
			again.reclaim = func(_ context.Context, dir string) (session.Reclamation, error) {
				if dir != again.launchDir(token) {
					t.Fatal("lost original launch identity", dir)
				}
				return session.Reclamation{Found: true}, session.ErrUnreclaimed
			}
			if err := again.resume(ctx); err != nil {
				t.Fatal(err)
			}
			_, jobs, err := again.pass(ctx, true)
			if err != nil || len(jobs) != 0 {
				t.Fatal("scheduled beside unreclaimed first turn", jobs, err)
			}
			held := taskByID(t, again, own.ID)
			if len(held.Claims) != 1 || held.Claims[0].Token != token || !held.Claims[0].Revoked || held.Claims[0].Held == "" {
				t.Fatal("restart lost revoked turn hold", held.Claims)
			}
			again.reclaim = func(context.Context, string) (session.Reclamation, error) {
				return session.Reclamation{Confirmed: true}, nil
			}
			if err := again.resume(ctx); err != nil {
				t.Fatal(err)
			}
			if got := taskByID(t, again, own.ID); len(got.Claims) != 0 {
				t.Fatal("confirmed-ended turn still held", got.Claims)
			}
			claimed, err = again.Core.Schedule(ctx, func(core.Role) string { return "" })
			if err != nil || len(claimed) != 1 || claimed[0].Claim.Token == token {
				t.Fatal("replacement did not resume safely", claimed, err)
			}
		})
	}
}
