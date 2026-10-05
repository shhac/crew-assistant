//go:build !windows

package work

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/integrations/github"
)

func TestStoppedMergedPRInvalidatesOtherDraftApproval(t *testing.T) {
	s := newPRScenario(t, 10)
	first := queuePR(t, s)
	s.runner.onEdit = func(dir string, _ int) bool {
		if err := os.WriteFile(filepath.Join(dir, "other.go"), []byte("package feature\n"), 0600); err != nil {
			t.Fatal(err)
		}
		return true
	}
	second, err := s.a.Core.QueueTask(s.ctx, s.p.ID, core.TaskInput{Objective: "Add B"})
	if err != nil {
		t.Fatal(err)
	}
	second = stepUntil(t, s.a, second.ID, func(task core.Task) bool { return task.Status == core.TaskWaiting })
	d := openDecision(t, s.a, second)
	if !d.Approves() {
		t.Fatal("fixture must await approval", d)
	}
	if _, err := s.a.StopTask(s.ctx, s.p.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	ownerGit(t, s.remote, "update-ref", "refs/heads/main", first.Revisions[0].Ref)
	s.gh.set(func() { s.gh.merged = first.Revisions[0].Ref })
	restartPR(t, s)
	step(t, s.a)
	snap, err := s.a.Core.Snapshot(s.ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := snap.FindTask(second.ID)
	for _, decision := range snap.Decisions {
		if decision.ID == d.ID && decision.Status == core.DecisionOpen {
			t.Fatal("obsolete approval remained actionable", decision)
		}
	}
	if len(got.Revisions) != 2 || got.Base != first.Revisions[0].Ref {
		t.Fatal("other draft did not catch up", got)
	}
}

func TestMergeBetweenPRReadsKeepsRequestedDraft(t *testing.T) {
	s := newPRScenario(t, 4)
	task := queuePR(t, s)
	restorePRAssets(t, s)
	restartPR(t, s)
	s.a.github = github.Client{Run: func(ctx context.Context, args ...string) ([]byte, error) {
		out, err := s.gh.run(ctx, args...)
		if args[0] == "pr" && args[1] == "view" {
			s.gh.set(func() { s.gh.merged, s.gh.queued = task.Revisions[0].Ref, false })
		}
		return out, err
	}}
	if err := s.a.setStatus(s.ctx, task.ID, core.TaskLanding, "Observe merge"); err != nil {
		t.Fatal(err)
	}
	got := s.current(t)
	if got.Status != core.TaskLanded || len(got.Revisions) != 1 || !got.NeedsAssetIntegration() || len(s.gh.merges) != 1 {
		t.Fatal("split observation released merge intent", got)
	}
}

func TestMergeCancellationWakesIntegrationAfterRestart(t *testing.T) {
	s := newPRScenario(t, 8)
	task := queuePR(t, s)
	restorePRAssets(t, s)
	if _, err := s.a.Core.SendTeamMessage(s.ctx, s.p.ID, task.ID, "implementer", core.FromOwner, "Integrate Frames"); err != nil {
		t.Fatal(err)
	}
	// Establish watcher baselines while enrollment is still present.
	if err := s.a.checkWakes(s.ctx, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	s.current(t)
	restartPR(t, s)
	s.gh.set(func() { s.gh.queued = false })
	if err := s.a.checkWakes(s.ctx, time.Now().Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	step(t, s.a)
	got := taskByID(t, s.a, task.ID)
	if got.Status != core.TaskWriting || got.PRMergePending() || got.Delivering != nil || !got.NeedsAssetIntegration() || got.DirectionPending != 1 || len(s.gh.merges) != 1 {
		t.Fatal("cancellation did not resume integration", got)
	}
}

func TestApprovedMergeCancellationBeforeFirstEnrollmentPoll(t *testing.T) {
	for _, gate := range []string{core.ApproveBefore, core.ApprovePM} {
		t.Run(gate, func(t *testing.T) {
			setups := []prSetup{mergeBy(gate)}
			if gate == core.ApprovePM {
				setups = append(setups, withPM)
			}
			s := newPRScenario(t, 8, setups...)
			task := s.open(t)
			s.readyPR(t)
			if gate == core.ApprovePM {
				task = stepUntil(t, s.a, task.ID, func(task core.Task) bool { return task.Status == core.TaskDeciding })
			} else {
				task = s.current(t)
			}
			// Ready watches are registered while approval is still outstanding.
			snap, _ := s.a.Core.Snapshot(s.ctx)
			before := map[string]core.Wake{}
			for _, w := range snap.Wakes {
				if w.TaskID == task.ID && w.Owner == core.WakeLoop && w.Status == core.WakeWaiting && (w.On == core.WakeOnChecks || w.On == core.WakeOnReview) {
					before[w.On] = w
					if !strings.HasSuffix(w.Baseline, "merge:false") {
						t.Fatal("fixture already observed enrollment", w)
					}
				}
			}
			if len(before) != 2 {
				t.Fatal("missing pre-approval watches", before)
			}
			s.a.github = github.Client{Run: func(ctx context.Context, args ...string) ([]byte, error) {
				if args[0] == "pr" && args[1] == "merge" {
					s.gh.set(func() { s.gh.queued = true; s.gh.merges = append(s.gh.merges, args) })
					return nil, nil
				}
				return s.gh.run(ctx, args...)
			}}
			if gate == core.ApproveBefore {
				if _, err := s.a.Core.ChooseDecision(s.ctx, openDecision(t, s.a, task).ID, choiceApprove, core.FromOwner); err != nil {
					t.Fatal(err)
				}
			}
			task = s.current(t)
			if task.Status != core.TaskAwaiting || !task.PRMergePending() || len(s.gh.merges) != 1 {
				t.Fatal("approval did not enqueue", task)
			}
			snap, _ = s.a.Core.Snapshot(s.ctx)
			for _, w := range snap.Wakes {
				if old, ok := before[w.On]; ok && w.ID == old.ID {
					if w.Baseline == old.Baseline || !strings.HasSuffix(w.Baseline, "merge:true") || !w.ExpiresAt.Equal(old.ExpiresAt) {
						t.Fatal("enqueue did not refresh the waiting enrollment baseline", w)
					}
				}
			}
			// No watcher poll occurs between approval/enqueue and cancellation.
			restorePRAssets(t, s)
			if _, err := s.a.Core.SendTeamMessage(s.ctx, s.p.ID, task.ID, "implementer", core.FromOwner, "Integrate Frames"); err != nil {
				t.Fatal(err)
			}
			restartPR(t, s)
			s.gh.set(func() { s.gh.queued = false })
			if err := s.a.checkWakes(s.ctx, time.Now().Add(3*time.Minute)); err != nil {
				t.Fatal(err)
			}
			step(t, s.a)
			got := taskByID(t, s.a, task.ID)
			if got.Status != core.TaskWriting || got.PRMergePending() || got.Delivering != nil || !got.NeedsAssetIntegration() || got.DirectionPending != 1 || len(s.gh.merges) != 1 {
				t.Fatal("cancellation before polling stranded integration", got)
			}
		})
	}
}

func TestUncertainEnqueueErrorKeepsIntentThroughOwnerRetryAndRestart(t *testing.T) {
	s := newPRScenario(t, 8)
	task := s.open(t)
	s.gh.set(func() { s.gh.checks, s.gh.decision = "SUCCESS", "APPROVED" })
	s.a.github = github.Client{Run: func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "pr" && args[1] == "merge" {
			s.gh.set(func() { s.gh.queued = true; s.gh.merges = append(s.gh.merges, args) })
			return nil, errors.New("transport failed after enqueue")
		}
		if args[0] == "pr" && args[1] == "view" && s.gh.queued {
			return nil, errors.New("confirmation read unavailable")
		}
		return s.gh.run(ctx, args...)
	}}
	if err := s.a.setStatus(s.ctx, task.ID, core.TaskLanding, "Ready"); err != nil {
		t.Fatal(err)
	}
	got := s.current(t)
	if got.Status != core.TaskWaiting || got.Delivering == nil {
		t.Fatal("uncertain merge discarded intent", got)
	}
	restorePRAssets(t, s)
	restartPR(t, s)
	if _, err := s.a.Core.ChooseDecision(s.ctx, got.DecisionID, choiceTryAgain, core.FromOwner); err != nil {
		t.Fatal(err)
	}
	got = s.current(t)
	if got.Status != core.TaskAwaiting || !got.PRMergePending() || !got.NeedsAssetIntegration() || len(got.Revisions) != 1 || len(s.gh.merges) != 1 {
		t.Fatal("uncertain enqueue repeated or rewrote", got)
	}
}

func TestEnqueueErrorWithReadableQueueIsAcknowledgedWithoutRetry(t *testing.T) {
	s := newPRScenario(t, 4)
	task := s.open(t)
	s.gh.set(func() { s.gh.checks, s.gh.decision = "SUCCESS", "APPROVED" })
	s.a.github = github.Client{Run: func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "pr" && args[1] == "merge" {
			s.gh.set(func() { s.gh.queued = true; s.gh.merges = append(s.gh.merges, args) })
			return nil, errors.New("transport failed after enqueue")
		}
		return s.gh.run(ctx, args...)
	}}
	if err := s.a.setStatus(s.ctx, task.ID, core.TaskLanding, "Ready"); err != nil {
		t.Fatal(err)
	}
	got := s.current(t)
	if got.Status != core.TaskAwaiting || !got.PRMergePending() || got.Delivering != nil || got.DecisionID != "" {
		t.Fatal("readable queue was not reconciled", got)
	}
	restorePRAssets(t, s)
	restartPR(t, s)
	got = s.current(t)
	if got.Status != core.TaskAwaiting || !got.NeedsAssetIntegration() || len(got.Revisions) != 1 || len(s.gh.merges) != 1 {
		t.Fatal("queued action rewritten or repeated", got)
	}
}

func TestMergeReadFailurePreservesOwnerDecisionAcrossScheduling(t *testing.T) {
	for _, answer := range []string{choiceStop, choiceTryAgain} {
		t.Run(answer, func(t *testing.T) {
			s := newPRScenario(t, 4)
			task := queuePR(t, s)
			failRead := github.Client{Run: func(ctx context.Context, args ...string) ([]byte, error) {
				if args[0] == "pr" && args[1] == "view" {
					return nil, errors.New("read failed")
				}
				return s.gh.run(ctx, args...)
			}}
			s.a.github = failRead
			if err := s.a.setStatus(s.ctx, task.ID, core.TaskLanding, "Observe merge"); err != nil {
				t.Fatal(err)
			}
			got := s.current(t)
			decision := got.DecisionID
			if _, err := s.a.Core.UseProjectTeam(s.ctx, s.p.ID, task.ID); !errors.Is(err, core.ErrConflict) {
				t.Fatal("team adoption could replace the recorded delivery policy", err)
			}
			restartPR(t, s)
			s.a.github = failRead
			got = s.current(t)
			if got.Status != core.TaskWaiting || got.DecisionID != decision {
				t.Fatal("recovery orphaned owner decision", got)
			}
			snap, _ := s.a.Core.Snapshot(s.ctx)
			n := 0
			for _, d := range snap.Decisions {
				if d.TaskID == task.ID && d.Kind == core.DecisionFailure && d.Status == core.DecisionOpen {
					n++
				}
			}
			if n != 1 {
				t.Fatal("duplicate failure decisions", n)
			}
			s.a.github = github.Client{Run: s.gh.run}
			if _, err := s.a.Core.ChooseDecision(s.ctx, decision, answer, core.FromOwner); err != nil {
				t.Fatal(err)
			}
			got = s.current(t)
			if answer == choiceStop && got.Status != core.TaskStopped || answer == choiceTryAgain && got.Status != core.TaskAwaiting {
				t.Fatal("owner answer ignored", got)
			}
			if len(s.gh.merges) != 1 {
				t.Fatal("repeated merge")
			}
		})
	}
}

func TestPRPolicySwitchDefersForOwnerAndPMAcrossRestart(t *testing.T) {
	for _, by := range []string{"owner", "PM"} {
		for _, merged := range []bool{false, true} {
			t.Run(by+map[bool]string{false: "queued", true: "merged"}[merged], func(t *testing.T) {
				s := newPRScenario(t, 4)
				task := queuePR(t, s)
				restorePRAssets(t, s)
				if merged {
					s.gh.set(func() { s.gh.merged = task.Revisions[0].Ref })
				}
				if _, err := s.a.Core.UpdateTask(s.ctx, task.ID, func(_ *core.Task, p *core.Project) (string, error) {
					p.Settings.Land.PullRequests = false
					if by == "PM" {
						p.PRChoices = append(p.PRChoices, task.ID)
					}
					return "", nil
				}); err != nil {
					t.Fatal(err)
				}
				if by == "PM" {
					if _, err := s.a.Core.ApplyPM(s.ctx, s.p.ID, core.PMAnswer{PRFlow: []core.PRFlowChoice{{Task: task.ID, Keep: false}}}); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := s.a.Core.EndPullRequests(s.ctx, s.p.ID); err != nil {
						t.Fatal(err)
					}
					snap, _ := s.a.Core.Snapshot(s.ctx)
					for _, d := range snap.Decisions {
						if d.TaskID == task.ID && d.Kind == core.DecisionPRFlow && d.Status == core.DecisionOpen {
							if _, err := s.a.Core.ChooseDecision(s.ctx, d.ID, core.ChoiceSwitchWay, core.FromOwner); err != nil {
								t.Fatal(err)
							}
						}
					}
				}
				got := taskByID(t, s.a, task.ID)
				if !got.PRMergePending() || !got.UsesPRs() || got.PRSwitchBy == "" || got.ClosePR != nil {
					t.Fatal("policy choice discarded delivery", got)
				}
				restartPR(t, s)
				if err := s.a.checkWakes(s.ctx, time.Now().Add(time.Minute)); err != nil {
					t.Fatal(err)
				}
				got = s.current(t)
				if merged {
					if got.Status != core.TaskLanded {
						t.Fatal(got)
					}
				} else {
					if got.Status != core.TaskAwaiting || !got.PRMergePending() || !got.UsesPRs() {
						t.Fatal(got)
					}
					s.gh.set(func() { s.gh.merged = task.Revisions[0].Ref })
					if err := s.a.checkWakes(s.ctx, time.Now().Add(3*time.Minute)); err != nil {
						t.Fatal(err)
					}
					if got = s.current(t); got.Status != core.TaskLanded {
						t.Fatal(got)
					}
				}
				if len(s.gh.merges) != 1 {
					t.Fatal("merge repeated")
				}
			})
		}
	}
}

func TestDeferredPRSwitchAppliesAfterMergeCancellation(t *testing.T) {
	s := newPRScenario(t, 8)
	task := queuePR(t, s)
	restorePRAssets(t, s)
	if _, err := s.a.Core.UpdateTask(s.ctx, task.ID, func(_ *core.Task, p *core.Project) (string, error) {
		p.Settings.Land.PullRequests = false
		p.PRChoices = []string{task.ID}
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.a.Core.ApplyPM(s.ctx, s.p.ID, core.PMAnswer{PRFlow: []core.PRFlowChoice{{Task: task.ID}}}); err != nil {
		t.Fatal(err)
	}
	restartPR(t, s)
	s.gh.set(func() { s.gh.queued = false })
	if err := s.a.checkWakes(s.ctx, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	step(t, s.a)
	claims, err := s.a.Core.Schedule(s.ctx, func(core.Role) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	got := taskByID(t, s.a, task.ID)
	if got.UsesPRs() || got.Proposal != nil || got.PRSwitchBy != "" || !got.NeedsAssetIntegration() || got.Status != core.TaskWriting || len(got.Revisions) != 1 {
		t.Fatal("deferred policy did not resume after cancellation", got)
	}
	for _, claimed := range claims {
		if err := s.a.Core.ReleaseClaim(s.ctx, claimed.Task.ID, claimed.Claim.Token); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDeferredPRSwitchCancellationWithoutPendingWork(t *testing.T) {
	for _, by := range []string{"owner", "PM"} {
		for _, retire := range []bool{false, true} {
			t.Run(by+map[bool]string{false: "/no-assets", true: "/retired-assets"}[retire], func(t *testing.T) {
				s := newPRScenario(t, 8)
				task := queuePR(t, s)
				if retire {
					restorePRAssets(t, s)
					if _, err := s.a.Core.UpdateBrief(s.ctx, s.p.ID, core.BriefInput{Goal: "Assets"}); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := s.a.Core.UpdateTask(s.ctx, task.ID, func(_ *core.Task, p *core.Project) (string, error) {
					p.Settings.Land.PullRequests = false
					if by == "PM" {
						p.PRChoices = append(p.PRChoices, task.ID)
					}
					return "", nil
				}); err != nil {
					t.Fatal(err)
				}
				if by == "PM" {
					if _, err := s.a.Core.ApplyPM(s.ctx, s.p.ID, core.PMAnswer{PRFlow: []core.PRFlowChoice{{Task: task.ID}}}); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := s.a.Core.EndPullRequests(s.ctx, s.p.ID); err != nil {
						t.Fatal(err)
					}
					snap, _ := s.a.Core.Snapshot(s.ctx)
					for _, d := range snap.Decisions {
						if d.TaskID == task.ID && d.Kind == core.DecisionPRFlow && d.Status == core.DecisionOpen {
							if _, err := s.a.Core.ChooseDecision(s.ctx, d.ID, core.ChoiceSwitchWay, core.FromOwner); err != nil {
								t.Fatal(err)
							}
						}
					}
				}
				restartPR(t, s)
				s.gh.set(func() { s.gh.queued = false })
				if err := s.a.checkWakes(s.ctx, time.Now().Add(time.Minute)); err != nil {
					t.Fatal(err)
				}
				step(t, s.a)
				claims, err := s.a.Core.Schedule(s.ctx, func(core.Role) string { return "" })
				if err != nil {
					t.Fatal(err)
				}
				got := taskByID(t, s.a, task.ID)
				if got.UsesPRs() || got.Proposal != nil || got.PRSwitchBy != "" || got.NeedsAssetIntegration() || len(s.gh.merges) != 1 {
					t.Fatal("cancelled merge bypassed deferred policy", got, s.gh.merges)
				}
				for _, c := range claims {
					if err := s.a.Core.ReleaseClaim(s.ctx, c.Task.ID, c.Claim.Token); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}
