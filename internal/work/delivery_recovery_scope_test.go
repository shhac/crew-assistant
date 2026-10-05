//go:build !windows

package work

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/roles"
	"github.com/shhac/lib-agent-harness/sandbox"
)

func TestDeliveryObservationFailureDoesNotBlockTheLoop(t *testing.T) {
	t.Parallel()
	for _, problem := range []string{"read", "commit", "head"} {
		t.Run(problem, func(t *testing.T) {
			s := newPRScenario(t, 4, opensUnasked, mergeBy(core.ApproveNone))
			task := s.current(t)
			r := task.Revisions[len(task.Revisions)-1]
			if err := s.a.setStatus(s.ctx, task.ID, core.TaskLanding, "Landing"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.a.beginDelivering(s.ctx, task.ID, r); err != nil {
				t.Fatal(err)
			}
			if _, err := s.a.Core.UpdateTask(s.ctx, task.ID, func(task *core.Task, _ *core.Project) (string, error) {
				task.Delivering.Requested = true
				return "", nil
			}); err != nil {
				t.Fatal(err)
			}
			run := s.a.github.Run
			s.a.github.Run = func(ctx context.Context, args ...string) ([]byte, error) {
				if len(args) > 3 && args[0] == "api" && args[1] == "graphql" && strings.Contains(args[3], "reviewThreads") {
					switch problem {
					case "read":
						return nil, errors.New("observation unavailable")
					case "commit":
						return []byte(fmt.Sprintf(`{"data":{"repository":{"pullRequest":{"state":"MERGED","headRefOid":%q,"reviewThreads":{"nodes":[]}}}}}`, r.Ref)), nil
					case "head":
						return []byte(`{"data":{"repository":{"pullRequest":{"state":"MERGED","headRefOid":"unexpected","mergeCommit":{"oid":"merged"},"reviewThreads":{"nodes":[]}}}}}`), nil
					}
				}
				return run(ctx, args...)
			}
			p, err := s.a.Core.CreateProject(s.ctx, core.ProjectInput{Title: "Independent", Template: "draft", Brief: core.BriefInput{Goal: "A local document"}})
			if err != nil {
				t.Fatal(err)
			}
			answer := queue(t, s.a, p, "Owner answer")
			d, err := s.a.Core.OpenTaskDecision(s.ctx, answer.ID, core.DecisionFailure, core.DecisionInput{Title: "Stop?", Context: "Owner choice", Recommendation: choiceStop, Choices: []string{choiceTryAgain, choiceStop}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.a.Core.ChooseDecision(s.ctx, d.ID, choiceStop, core.FromOwner); err != nil {
				t.Fatal(err)
			}
			other := queue(t, s.a, p, "Independent work")
			for i := 0; i < 8; i++ {
				if _, err := s.a.loopStep(s.ctx, false); err != nil {
					t.Fatal(err)
				}
			}
			now := taskByID(t, s.a, task.ID)
			if now.Delivering == nil || !strings.Contains(now.Detail, "Delivery observation") {
				t.Fatal("lost observation obligation", now)
			}
			if taskByID(t, s.a, answer.ID).Status != core.TaskStopped {
				t.Fatal("owner answer blocked")
			}
			if len(taskByID(t, s.a, other.ID).Revisions) == 0 {
				t.Fatal("independent work blocked")
			}
		})
	}
}

func TestStoppedFencedMergeRecordsDefiniteRefusal(t *testing.T) {
	t.Parallel()
	s := newPRScenario(t, 4, opensUnasked, mergeBy(core.ApproveNone))
	task := s.current(t)
	s.readyPR(t)
	task = taskByID(t, s.a, task.ID)
	r := task.Revisions[len(task.Revisions)-1]
	claim, err := s.a.Core.ClaimTask(s.ctx, task.ID, core.TaskLanding)
	if err != nil {
		t.Fatal(err)
	}
	run := s.a.github.Run
	s.a.github.Run = func(ctx context.Context, args ...string) ([]byte, error) {
		if len(args) > 1 && args[0] == "pr" && args[1] == "merge" {
			if _, err := s.a.StopTask(context.Background(), s.p.ID, task.ID); err != nil {
				t.Fatal(err)
			}
			return nil, errors.New("GraphQL: Resource not accessible by integration (mergePullRequest)")
		}
		return run(ctx, args...)
	}
	pr, err := s.a.github.View(s.ctx, task.Playbook.Land.GitHub, task.Proposal.Number)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.a.mergeReady(core.Fenced(s.ctx, task.ID, claim.Token), s.p, task, task.Playbook.Land, r, *task.Proposal, pr); err != nil {
		t.Fatal(err)
	}
	restartPR(t, s)
	now := taskByID(t, s.a, task.ID)
	if now.Status != core.TaskStopped || now.Delivering != nil || now.PRMergePending() {
		t.Fatal("stopped refusal remains a delivery hold", now)
	}
	s.gh.set(func() { s.gh.closed = true })
	next := queue(t, s.a, s.p, "Another change")
	next = stepUntil(t, s.a, next.ID, func(t core.Task) bool { return t.PROpen() })
	s.readyPR(t)
	next = stepUntil(t, s.a, next.ID, func(t core.Task) bool { return t.Finished() })
	if next.Status != core.TaskLanded {
		t.Fatal("refusal blocked subsequent delivery", next)
	}
}

func TestCommandReclamationRetainsTerminalAccountingHold(t *testing.T) {
	t.Parallel()
	lp, p, task := loopApp(t, &scriptedRunner{}, "")
	ctx := context.Background()
	claim, err := lp.Core.ClaimTask(ctx, task.ID, core.TaskWriting)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	lp.runner = accountingRunner(func(context.Context, roles.Spec) (roles.Result, error) {
		calls++
		return roles.Result{Text: "known result"}, nil
	})
	lp.finishTeamTurn = func(context.Context, string, core.TeamTurnTerminal) error {
		return errors.New("accounting unavailable")
	}
	removed := false
	done := lp.run(ctx, claimed{task: task.ID, project: p.ID, token: claim.Token}, true, func(ctx context.Context) error {
		ctx = core.Fenced(ctx, task.ID, claim.Token)
		lp.keepCommandCleanup(ctx, task.ID, func() { removed = true })
		_, _ = lp.runRole(ctx, roles.Spec{ProjectID: p.ID, TaskID: task.ID, Role: core.RoleQA, Seat: "QA", Engine: "claude"})
		return errCommandRecovery
	}, func(ctx context.Context) error { return lp.Core.ReleaseClaim(ctx, task.ID, claim.Token) })
	for value := range done {
		t.Fatal(value)
	}
	lp.commands = func(context.Context, sandbox.Options) (commandSandbox, error) { return &fakeCommands{}, nil }
	d, err := lp.Core.OpenTaskDecision(ctx, task.ID, core.DecisionFailure, core.DecisionInput{Title: "Retry", Context: "Cleanup failed", Recommendation: choiceTryAgain, Choices: []string{choiceTryAgain, choiceStop}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lp.Core.ChooseDecision(ctx, d.ID, choiceTryAgain, core.FromOwner); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := lp.loopStep(ctx, false); err != nil {
			t.Fatal(err)
		}
	}
	now := taskByID(t, lp, task.ID)
	if !removed || len(now.Claims) != 1 || !strings.Contains(now.Claims[0].Held, "accounting") || calls != 1 {
		t.Fatal("resource cleanup discharged accounting", removed, calls, now.Claims)
	}
}
