package work

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/lib-agent-harness/session"
)

// pausePrerequisitePublish intercepts the real handoff's prepare/publish
// boundaries without changing its fenced commit or the adapter's snapshot.
type pausePrerequisitePublish struct {
	medium
	after bool
	pause func()
}

func (m pausePrerequisitePublish) publish(ctx context.Context, t core.Task, ref, name string) error {
	if !m.after {
		m.pause()
	}
	err := m.medium.publish(ctx, t, ref, name)
	if err == nil && m.after {
		m.pause()
	}
	return err
}

func reopenPrerequisiteStore(t *testing.T, lp *Loop) *Loop {
	t.Helper()
	st, err := core.Open(filepath.Join(lp.Core.StateDirectory(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	again := New(core.NewService(st, lp.Config()), lp.Config, false)
	again.runner, again.meter = lp.runner, lp.meter
	again.reclaim = func(context.Context, string) (session.Reclamation, error) {
		return session.Reclamation{Confirmed: true}, nil
	}
	if err := again.resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	return again
}

func TestReopeningInvalidatesPreparedFirstHandoff(t *testing.T) {
	for _, custom := range []bool{false, true} {
		for _, after := range []bool{false, true} {
			for _, restart := range []bool{false, true} {
				t.Run(fmt.Sprintf("custom=%t/published=%t/restart=%t", custom, after, restart), func(t *testing.T) {
					ctx := context.Background()
					lp, p, own, m, h := handoffAt(t)
					// Settle a real plan prerequisite before this first draft's
					// handoff. Keep the synthetic fixture's branch/snapshot.
					if _, err := lp.Core.UpdateTask(ctx, own.ID, func(t *core.Task, _ *core.Project) (string, error) {
						t.Status, t.Handoff = core.TaskResearching, nil
						return "", nil
					}); err != nil {
						t.Fatal(err)
					}
					if _, err := lp.Core.RecordPlan(ctx, own.ID, core.Plan{Summary: "Build"}, nil, []string{"Someone can push the diagnostics PR"}); err != nil {
						t.Fatal(err)
					}
					settleFirstPrerequisite(t, lp, own.ID, core.ChoiceReadyPrerequisite)
					if _, err := lp.Core.UpdateTask(ctx, own.ID, func(t *core.Task, _ *core.Project) (string, error) {
						t.Status, t.Base, t.From, t.Branch = core.TaskWriting, own.Base, own.From, own.Branch
						return "", nil
					}); err != nil {
						t.Fatal(err)
					}
					claim, err := lp.Core.ClaimTask(ctx, own.ID, core.TaskWriting)
					if err != nil {
						t.Fatal(err)
					}
					paused := pausePrerequisitePublish{medium: m, after: after, pause: func() {
						prepared := taskByID(t, lp, own.ID)
						if prepared.Handoff == nil {
							t.Fatal("handoff was not prepared")
						}
						reopenFirstPrerequisite(t, lp, p, prepared, custom)
						reopened := taskByID(t, lp, own.ID)
						if reopened.Handoff != nil || len(reopened.Claims) != 1 || reopened.Claims[0].Token != claim.Token || !reopened.Claims[0].Revoked {
							t.Fatal("reopening did not atomically invalidate handoff and retain turn hold", reopened)
						}
					}}
					if err := lp.handOff(core.Fenced(ctx, own.ID, claim.Token), taskByID(t, lp, own.ID), paused, h); !errors.Is(err, core.ErrStale) {
						t.Fatal("live revoked handoff was accepted", err)
					}
					if restart {
						lp = reopenPrerequisiteStore(t, lp)
					} else if err := lp.Core.ReleaseClaim(ctx, own.ID, claim.Token); err != nil {
						t.Fatal(err)
					}
					for range 2 {
						snap, _ := lp.Core.Snapshot(ctx)
						if _, err := lp.settleAnswers(ctx, snap); err != nil {
							t.Fatal(err)
						}
					}
					got := taskByID(t, lp, own.ID)
					if len(got.Revisions) != 0 || got.Handoff != nil || got.Status == core.TaskReviewing || got.Blockers[0].ClearedAt != nil {
						t.Fatal("superseded draft survived reopening/recovery", got)
					}
					if claimed, err := lp.Core.Schedule(ctx, func(core.Role) string { return "" }); err != nil || len(claimed) != 0 {
						t.Fatal("unresolved condition allowed implementation", claimed, err)
					}
					settleFirstPrerequisite(t, lp, own.ID, core.ChoiceReadyPrerequisite)
					if restart {
						lp = reopenPrerequisiteStore(t, lp)
					}
					if got := taskByID(t, lp, own.ID); len(got.Revisions) != 0 || got.Handoff != nil {
						t.Fatal("reconfirmation revived superseded draft", got)
					}
					if claimed, err := lp.Core.Schedule(ctx, func(core.Role) string { return "" }); err != nil || len(claimed) != 1 {
						t.Fatal("invalidated handoff obstructed continuation", claimed, err)
					}
				})
			}
		}
	}
}

func TestOwnerAnswerContinuationWaitsForReopenedPrerequisite(t *testing.T) {
	for _, route := range []string{"failure", "design", "asker"} {
		for _, custom := range []bool{false, true} {
			for _, restart := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/custom=%t/restart=%t", route, custom, restart), func(t *testing.T) {
					ctx := context.Background()
					lp, p := parallelApp(t, &parallelRunner{}, 0)
					own := settledFirstTurn(t, lp, p)
					if _, err := lp.Core.UpdateTask(ctx, own.ID, func(t *core.Task, _ *core.Project) (string, error) { t.Status = core.TaskWriting; return "", nil }); err != nil {
						t.Fatal(err)
					}
					var d core.Decision
					if route == "failure" {
						cause := &session.CapabilityError{Engine: "claude", Code: session.CapabilitySandboxUnavailable, Phase: session.BeforeLaunch}
						if err := lp.roleFailed(ctx, taskByID(t, lp, own.ID), "Writer", cause); err != nil {
							t.Fatal(err)
						}
						d = openDecision(t, lp, taskByID(t, lp, own.ID))
					} else {
						var err error
						d, err = lp.Core.OpenTaskDecision(ctx, own.ID, core.DecisionQuestion, core.DecisionInput{Title: "Format?", Context: "Which format?", Recommendation: "JSON", Choices: []string{"JSON", "CSV"}})
						if err != nil {
							t.Fatal(err)
						}
						if _, err = lp.Core.UpdateTask(ctx, own.ID, func(t *core.Task, _ *core.Project) (string, error) {
							if route == "design" {
								t.Design = append(t.Design, core.DesignRequest{ID: "design-question", Step: core.TaskWriting, Decision: d.ID, Question: "Which format?", From: "Writer"})
							} else {
								t.Asker = &core.Asker{Decision: d.ID, Step: core.TaskWriting, From: "Writer"}
							}
							return "", nil
						}); err != nil {
							t.Fatal(err)
						}
					}
					if custom {
						b := taskByID(t, lp, own.ID).Blockers[0]
						if _, err := lp.Core.AnswerDecision(ctx, d.ID, "Prerequisite "+b.ID+" no longer holds: "+b.Description, core.FromOwner); err != nil {
							t.Fatal(err)
						}
					} else {
						reopenFirstPrerequisite(t, lp, p, taskByID(t, lp, own.ID), false)
						if route == "failure" {
							if _, err := lp.Core.ChooseDecision(ctx, d.ID, choiceTryAgain, core.FromOwner); err != nil {
								t.Fatal(err)
							}
						} else if _, err := lp.Core.AnswerDecision(ctx, d.ID, "Use JSON", core.FromOwner); err != nil {
							t.Fatal(err)
						}
					}
					if restart {
						lp = reopenPrerequisiteStore(t, lp)
					}
					snap, _ := lp.Core.Snapshot(ctx)
					if progressed, err := lp.settleAnswers(ctx, snap); err != nil || !progressed {
						t.Fatal("owner answer was not routed", progressed, err)
					}
					got := taskByID(t, lp, own.ID)
					if got.Status != core.TaskWriting || got.DecisionID != "" || got.Blockers[0].ClearedAt != nil {
						t.Fatal("lost continuation or invalidated reopened condition", got)
					}
					if custom || route != "failure" {
						if len(got.Direction) == 0 {
							t.Fatal("lost owner answer direction")
						}
					}
					_, jobs, err := lp.pass(ctx, true)
					for _, job := range jobs {
						<-job
					}
					if err != nil || len(jobs) != 0 {
						t.Fatal("first implementation bypassed reopened prerequisite", jobs, err)
					}
					settleFirstPrerequisite(t, lp, own.ID, core.ChoiceDropPrerequisite)
					if restart {
						lp = reopenPrerequisiteStore(t, lp)
					}
					claimed, err := lp.Core.Schedule(ctx, func(core.Role) string { return "" })
					if err != nil || len(claimed) != 1 || claimed[0].Claim.Step != core.TaskWriting {
						t.Fatal("settled condition did not release retained continuation", claimed, err)
					}
				})
			}
		}
	}
}

func TestReopeningPreservesBegunHandoffAndImplementation(t *testing.T) {
	ctx := context.Background()
	lp, p := parallelApp(t, &parallelRunner{}, 0)
	own := settledFirstTurn(t, lp, p)
	h := core.Handoff{Name: "second-draft", Revision: core.Revision{N: 2}}
	if _, err := lp.Core.UpdateTask(ctx, own.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.Status, t.Revisions, t.Handoff = core.TaskWriting, []core.Revision{{N: 1}}, &h
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
	reopenFirstPrerequisite(t, lp, p, taskByID(t, lp, own.ID), false)
	if got := taskByID(t, lp, own.ID); got.Handoff == nil || got.Handoff.Name != h.Name || len(got.Revisions) != 1 || got.Status != core.TaskWriting {
		t.Fatal("reopening discarded begun work", got)
	}
	if _, err := lp.Core.UpdateTask(ctx, own.ID, func(t *core.Task, _ *core.Project) (string, error) { t.Handoff = nil; return "", nil }); err != nil {
		t.Fatal(err)
	}
	if claimed, err := lp.Core.Schedule(ctx, func(core.Role) string { return "" }); err != nil || len(claimed) != 1 || claimed[0].Claim.Step != core.TaskWriting {
		t.Fatal("reopening held begun implementation instead of landing", claimed, err)
	}
}
