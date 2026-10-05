//go:build !windows

package work

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/media/gitrepo"
	"github.com/shhac/lib-agent-harness/sandbox"
)

type answeringCatchUp struct {
	gitMedium
	merged          func()
	publishedAnswer func()
}

func (m answeringCatchUp) cleanMerge(ctx context.Context, t core.Task, l line) (core.Task, string, error) {
	moved, ref, err := m.gitMedium.cleanMerge(ctx, t, l)
	if err == nil && m.merged != nil {
		m.merged()
	}
	return moved, ref, err
}
func (m answeringCatchUp) publish(ctx context.Context, t core.Task, ref, name string) error {
	if err := m.gitMedium.publish(ctx, t, ref, name); err != nil {
		return err
	}
	if m.publishedAnswer != nil {
		m.publishedAnswer()
	}
	return nil
}

func TestCatchUpPreservesConcurrentOwnerAnswer(t *testing.T) {
	t.Parallel()
	for _, answer := range []string{choiceAcceptDraft, choiceAcceptFollowUp, "Custom direction", choiceChanges} {
		for _, phase := range []string{"before-prepare", "published", "restart"} {
			t.Run(answer+"/"+phase, func(t *testing.T) {
				t.Parallel()
				ctx := context.Background()
				var reopen func(*Loop) *Loop
				accepting := answer == choiceAcceptDraft || answer == choiceAcceptFollowUp
				initial := choiceAcceptFollowUp
				if accepting {
					initial = answer
				}
				a, _, p, task, d, source := acceptedCode(t, initial, false, &reopen, accepting)
				if accepting {
					// Leave the escalation open until the merge hook supplies its answer.
					if _, err := a.Core.UpdateTaskWithDecision(ctx, task.ID, func(t *core.Task, _ *core.Project, d *core.Decision) (string, error) {
						d.Status, d.Answer, d.Disposition, d.ResolvedAt = core.DecisionOpen, "", "", nil
						return "", nil
					}); err != nil {
						t.Fatal(err)
					}
				} else {
					if _, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
						t.Proposal = &core.Proposal{Number: 7, Branch: "crew/accepted", Pushed: t.Revisions[0].Ref}
						return "", nil
					}); err != nil {
						t.Fatal(err)
					}
					var err error
					d, err = a.Core.OpenTaskDecision(ctx, task.ID, core.DecisionDelivery, core.DecisionInput{Title: "Merge accepted draft", Context: "Ready to merge", Recommendation: choiceApprove, Choices: []string{choiceApprove, choiceChanges}})
					if err != nil {
						t.Fatal(err)
					}
				}
				ownerCommits(t, source, "owner.go", "package main\n", "owner work")
				m, task := a.testMedium(t, p.ID, task.ID)
				_, l, err := lag(ctx, m, task)
				if err != nil || l == nil {
					t.Fatal("no catch-up", err)
				}
				resolve := func() {
					var err error
					if answer == "Custom direction" {
						_, err = a.Core.AnswerDecision(ctx, d.ID, "Preserve my instruction", core.FromOwner)
					} else {
						_, err = a.Core.ChooseDecision(ctx, d.ID, answer, core.FromOwner)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				if phase == "restart" {
					moved, ref, err := m.cleanMerge(ctx, task, *l)
					if err != nil || ref == "" {
						t.Fatal(err)
					}
					files, err := m.files(ctx, moved, ref)
					if err != nil {
						t.Fatal(err)
					}
					h := core.Handoff{Name: gitrepo.TaskRef(task.ID, 2, 1), CatchUpDecision: d.ID, Revision: core.Revision{N: 2, Ref: ref, Files: files}, CatchUp: &core.CatchUp{Base: moved.Base, From: moved.From, Carry: true, Name: l.Name, What: l.What}}
					if _, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) { t.Handoff = &h; return "", nil }); err != nil {
						t.Fatal(err)
					}
					if err := m.publish(ctx, task, ref, h.Name); err != nil {
						t.Fatal(err)
					}
					resolve()
					a = reopen(a)
				} else {
					hook := answeringCatchUp{gitMedium: m}
					if phase == "before-prepare" {
						hook.merged = resolve
					} else {
						hook.publishedAnswer = resolve
					}
					if err := a.catchUpRound(ctx, task, hook, *l); err != nil {
						t.Fatal(err)
					}
				}
				current := taskByID(t, a, task.ID)
				if current.Status != core.TaskWaiting || current.DecisionID != d.ID || len(current.Revisions) != 1 || current.Handoff != nil {
					t.Fatal("catch-up detached owner answer", current)
				}
				snap, err := a.Core.Snapshot(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if progressed, err := a.settleAnswers(ctx, snap); err != nil || !progressed {
					t.Fatal("answer did not settle", err)
				}
				current = taskByID(t, a, task.ID)
				expected := 0
				if answer == choiceAcceptFollowUp || !accepting {
					expected = 1
				}
				if len(followUps(t, a, task.ID)) != expected {
					t.Fatal("lost or duplicated follow-up")
				}
				if accepting {
					if current.Status != core.TaskLanding || !current.AcceptanceStands(p.Brief.Version) {
						t.Fatal("acceptance was lost", current)
					}
				} else {
					if current.Status != core.TaskWriting || current.Acceptance != nil || (answer == "Custom direction" && !slices.Contains(current.Direction, "Preserve my instruction")) {
						t.Fatal("direction did not return to team", current)
					}
				}
			})
		}
	}
}

func TestStaleAcceptedDecidingCannotResumeLanding(t *testing.T) {
	t.Parallel()
	for _, choice := range []string{choiceAcceptDraft, choiceAcceptFollowUp} {
		for _, brief := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/brief=%v", choice, brief), func(t *testing.T) {
				t.Parallel()
				ctx := context.Background()
				var reopen func(*Loop) *Loop
				a, runner, p, task, _, source := acceptedCode(t, choice, false, &reopen)
				ownerCommits(t, source, "owner.go", "package main\n", "owner work")
				a.commands = func(context.Context, sandbox.Options) (commandSandbox, error) { return &fakeCommands{}, nil }
				task = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Status == core.TaskDeciding })
				suspended, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
					t.Playbook.Land.Approve = core.ApproveNone
					t.Claims = []core.Claim{{Token: "suspended-deciding", Step: core.TaskDeciding}}
					return "", nil
				})
				if err != nil {
					t.Fatal(err)
				}
				before := roleCalls(runner, core.RoleReviewer)
				editAcceptedRequirements(t, a, p.ID, task.ID, brief)
				if _, err := a.Core.Schedule(ctx, func(core.Role) string { return "test hold" }); err != nil {
					t.Fatal(err)
				}
				current := taskByID(t, a, task.ID)
				if current.Status != core.TaskReviewing || current.Acceptance != nil || !current.Claims[0].Revoked {
					t.Fatal("changed requirements did not fence deciding", current)
				}
				for _, continuation := range []context.Context{core.Fenced(ctx, task.ID, "suspended-deciding"), ctx} {
					if err := a.resumeLanding(continuation, suspended, p.Brief.Version); !errors.Is(err, core.ErrStale) {
						t.Fatal("stale deciding authorized landing", err)
					}
				}
				if err := a.Core.ReleaseClaim(ctx, task.ID, "suspended-deciding"); err != nil {
					t.Fatal(err)
				}
				current = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Finished() || t.Status == core.TaskWaiting })
				snap, _ := a.Core.Snapshot(ctx)
				fresh, _ := findProject(snap, p.ID)
				if roleCalls(runner, core.RoleReviewer) != before+1 || !current.Judged("Reviewer", 2, fresh.Brief.Version) {
					t.Fatal("ordinary checks did not run before delivery", current)
				}
			})
		}
	}
}

func TestChangedRequirementsDuringCatchUpFailureResumeChecks(t *testing.T) {
	t.Parallel()
	for _, brief := range []bool{false, true} {
		t.Run(fmt.Sprintf("brief=%v", brief), func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			var reopen func(*Loop) *Loop
			a, runner, p, task, _, source := acceptedCode(t, choiceAcceptFollowUp, false, &reopen)
			ownerCommits(t, source, "owner.go", "package main\n", "owner work")
			for i := 0; i <= roleRetries; i++ {
				m, current := a.testMedium(t, p.ID, task.ID)
				_, l, err := lag(ctx, m, current)
				if err != nil || l == nil {
					t.Fatal(err)
				}
				if err := a.catchUpRound(ctx, current, refusedCatchUpPublication{m}, *l); err != nil {
					t.Fatal(err)
				}
			}
			current := taskByID(t, a, task.ID)
			d := openDecision(t, a, current)
			if current.ResumeStatus != core.TaskLanding {
				t.Fatal("failure did not retain landing continuation")
			}
			before := roleCalls(runner, core.RoleReviewer)
			editAcceptedRequirements(t, a, p.ID, task.ID, brief)
			if _, err := a.Core.Schedule(ctx, func(core.Role) string { return "test hold" }); err != nil {
				t.Fatal(err)
			}
			current = taskByID(t, a, task.ID)
			if current.Acceptance != nil || current.ResumeStatus != core.TaskReviewing {
				t.Fatal("changed requirements retained stale landing retry", current)
			}
			d, err := a.Core.ChooseDecision(ctx, d.ID, choiceTryAgain, core.FromOwner)
			if err != nil {
				t.Fatal(err)
			}
			if err := a.applyAnswer(ctx, current, d); err != nil {
				t.Fatal(err)
			}
			current = taskByID(t, a, task.ID)
			if current.Status != core.TaskReviewing || current.Delivering != nil {
				t.Fatal("retry bypassed ordinary checks", current)
			}
			current = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Status == core.TaskDeciding })
			snap, _ := a.Core.Snapshot(ctx)
			fresh, _ := findProject(snap, p.ID)
			if roleCalls(runner, core.RoleReviewer) != before+1 || !current.Judged("Reviewer", 1, fresh.Brief.Version) || len(followUps(t, a, task.ID)) != 1 {
				t.Fatal("retry lost checks or follow-up", current)
			}
		})
	}
}
