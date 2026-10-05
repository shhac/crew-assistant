//go:build !windows

package work

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/checktest"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/lifecycle"
	"github.com/shhac/crew-assistant/internal/roles"
	"github.com/shhac/lib-agent-harness/sandbox"
)

func TestFinalAccountingHoldPrecedesJobRemoval(t *testing.T) {
	lp, p, task := loopApp(t, &scriptedRunner{}, "")
	ctx := context.Background()
	claim, err := lp.Core.ClaimTask(ctx, task.ID, core.TaskWriting)
	if err != nil {
		t.Fatal(err)
	}
	entered, finish := make(chan struct{}), make(chan struct{})
	done := lp.run(ctx, claimed{task: task.ID, project: p.ID, token: claim.Token}, true, func(ctx context.Context) error {
		if err := lp.Core.HoldClaim(ctx, task.ID, p.ID, claim.Token, errCommandRecovery.Error()); err != nil {
			return err
		}
		ctx.Value(accountingFailureKey{}).(*accountingFailureState).failure = &teamAccountingFailure{accounting: errors.New("accounting unavailable")}
		close(entered)
		<-finish
		return errCommandRecovery
	}, func(ctx context.Context) error { return lp.Core.ReleaseClaim(ctx, task.ID, claim.Token) })
	<-entered
	// Freeze the exact job-removal boundary while final settlement proceeds.
	// Before the fix this mutex also prevents recording the accounting hold.
	lp.jobs.mu.Lock()
	close(finish)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		now := taskByID(t, lp, task.ID)
		if len(now.Claims) == 1 && strings.Contains(now.Claims[0].Held, "accounting") {
			break
		}
		time.Sleep(time.Millisecond)
	}
	err = lp.Core.ReleaseHeldClaim(ctx, task.ID, claim.Token, errCommandRecovery.Error())
	now := taskByID(t, lp, task.ID)
	lp.jobs.mu.Unlock()
	for value := range done {
		t.Fatal(value)
	}
	if err != nil || len(now.Claims) != 1 || !strings.Contains(now.Claims[0].Held, "accounting") {
		t.Fatal("reclamation beat final accounting settlement", now.Claims, err)
	}
}

func TestFailedFinalSettlementKeepsCleanupRegisteredAsRunning(t *testing.T) {
	lp, p, task := loopApp(t, &scriptedRunner{}, "")
	ctx := context.Background()
	claim, err := lp.Core.ClaimTask(ctx, task.ID, core.TaskWriting)
	if err != nil {
		t.Fatal(err)
	}
	removed := false
	done := lp.run(ctx, claimed{task: task.ID, project: p.ID, token: claim.Token}, true, func(ctx context.Context) error {
		lp.keepCommandCleanup(core.Fenced(ctx, task.ID, claim.Token), task.ID, func() { removed = true })
		return lp.Core.HoldClaim(ctx, task.ID, p.ID, claim.Token, errCommandRecovery.Error())
	}, func(context.Context) error { return errors.New("final claim write unavailable") })
	for value := range done {
		t.Fatal(value)
	}
	if !lp.jobs.hasTask(task.ID) {
		t.Fatal("failed final settlement exposed a completed job")
	}
	_, started, err := lp.pass(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, done := range started {
		for value := range done {
			t.Fatal(value)
		}
	}
	if removed || len(taskByID(t, lp, task.ID).Claims) != 1 {
		t.Fatal("cleanup released unsettled claim")
	}
}

func TestAcceptedPMHoldKeepsJudgedVersions(t *testing.T) {
	for _, choice := range []string{choiceAcceptDraft, choiceAcceptFollowUp} {
		for _, brief := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/brief=%t", choice, brief), func(t *testing.T) {
				var reopen func(*Loop) *Loop
				a, _, p, task, _, source := acceptedCode(t, choice, true, &reopen, true)
				if _, err := a.Core.UpdateTask(context.Background(), task.ID, func(t *core.Task, _ *core.Project) (string, error) {
					for i := range t.Verdicts {
						if t.Verdicts[i].Role == "QA" {
							t.Verdicts[i].Outcome = core.VerdictRevise
						} else {
							t.Verdicts[i].Outcome = core.VerdictPass
						}
					}
					return "", nil
				}); err != nil {
					t.Fatal(err)
				}
				task = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Status == core.TaskLanding })
				ownerCommits(t, source, "owner.go", "package main\n", "owner work")
				task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) == 2 })
				ctx := context.Background()
				// The PM path requires reviewer passes as well as merged QA.
				var err error
				land := task.Playbook.Land
				land.Approve = core.ApprovePM
				p, err = a.SetLanding(ctx, p.ID, land)
				if err != nil {
					t.Fatal(err)
				}
				task, err = a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, p *core.Project) (string, error) {
					t.Playbook.Land.Approve = core.ApprovePM
					t.Status = core.TaskDeciding
					t.MergeValidation.Checked = true
					for _, seat := range t.Checkers() {
						t.Verdicts = append(t.Verdicts, core.Verdict{Role: seat.Name, Revision: 2, BriefVersion: p.Brief.Version, TextVersion: t.TextVersion, Outcome: core.VerdictPass})
					}
					return "", nil
				})
				if err != nil {
					t.Fatal(err)
				}
				m, err := a.mediumFor(ctx, p, taskPlaybook(p, task))
				if err != nil {
					t.Fatal(err)
				}
				called := false
				a.runner = accountingRunner(func(context.Context, roles.Spec) (roles.Result, error) {
					called = true
					editAcceptedRequirements(t, a, p.ID, task.ID, brief)
					return roles.Result{Text: `{"land":false,"reason":"Wait for another change"}`}, nil
				})
				if err := a.pmLanding(ctx, p, task, task.Revisions[1], m); err != nil {
					t.Fatal(err)
				}
				if !called {
					t.Fatal("PM hold branch was not exercised")
				}
				if _, err := a.Core.Schedule(ctx, func(core.Role) string { return "test hold" }); err != nil {
					t.Fatal(err)
				}
				now := taskByID(t, a, task.ID)
				if now.DecisionID != "" || now.Acceptance != nil || now.Status != core.TaskReviewing {
					t.Fatal("stale PM hold opened approval", now)
				}
			})
		}
	}
}

func TestRetiringAcceptanceClearsAppliedMergeApproval(t *testing.T) {
	for _, choice := range []string{choiceAcceptDraft, choiceAcceptFollowUp} {
		for _, brief := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/brief=%t", choice, brief), func(t *testing.T) {
				var reopen func(*Loop) *Loop
				a, _, p, task, _, _ := acceptedCode(t, choice, false, &reopen)
				ctx := context.Background()
				_, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
					t.Playbook.Land.PullRequests = true
					t.Playbook.Land.GitHub = "o/r"
					t.Playbook.Land.Approve = core.ApproveBefore
					t.Proposal = &core.Proposal{Number: 7, Pushed: t.Revisions[0].Ref, Observed: &core.Observed{Ready: true}}
					return "", nil
				})
				if err != nil {
					t.Fatal(err)
				}
				d, err := a.Core.OpenTaskDecision(ctx, task.ID, core.DecisionDelivery, core.DecisionInput{Title: "Merge", Context: "Ready", Recommendation: choiceApprove, Choices: []string{choiceApprove, choiceChanges}})
				if err != nil {
					t.Fatal(err)
				}
				d, err = a.Core.ChooseDecision(ctx, d.ID, choiceApprove, core.FromOwner)
				if err != nil {
					t.Fatal(err)
				}
				if err := a.applyAnswer(ctx, taskByID(t, a, task.ID), d); err != nil {
					t.Fatal(err)
				}
				if taskByID(t, a, task.ID).Proposal.MergeApproved != 1 {
					t.Fatal("approval not applied")
				}
				editAcceptedRequirements(t, a, p.ID, task.ID, brief)
				if _, err := a.Core.Schedule(ctx, func(core.Role) string { return "test hold" }); err != nil {
					t.Fatal(err)
				}
				now := taskByID(t, a, task.ID)
				if now.Acceptance != nil || now.Proposal.MergeApproved != 0 {
					t.Fatal("edited requirements retained merge approval", now)
				}
				// After current checks pass, the same revision still needs a new gate.
				now, err = a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, p *core.Project) (string, error) {
					t.Status = core.TaskDeciding
					for _, seat := range t.Checkers() {
						t.Verdicts = append(t.Verdicts, core.Verdict{Role: seat.Name, Revision: 1, BriefVersion: p.Brief.Version, TextVersion: t.TextVersion, Outcome: core.VerdictPass})
					}
					return "", nil
				})
				if err != nil {
					t.Fatal(err)
				}
				snap, err := a.Core.Snapshot(ctx)
				if err != nil {
					t.Fatal(err)
				}
				p, _ = findProject(snap, p.ID)
				m, err := a.mediumFor(ctx, p, taskPlaybook(p, now))
				if err != nil {
					t.Fatal(err)
				}
				if err := a.askToMerge(ctx, p, now, now.Revisions[0], m); err != nil {
					t.Fatal(err)
				}
				if current := taskByID(t, a, task.ID); current.Status != core.TaskWaiting || current.DecisionID == "" {
					t.Fatal("fresh checks bypassed new merge gate", current)
				}
			})
		}
	}
}

func TestStartupCommandHoldAllowsAnswersAndIndependentWork(t *testing.T) {
	lp, p, task := loopApp(t, &scriptedRunner{reviews: passes(10)}, "")
	ctx := context.Background()
	root := t.TempDir()
	if _, err := lp.Core.UpdateTask(ctx, task.ID, func(_ *core.Task, p *core.Project) (string, error) {
		p.ScratchDirectory = root
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
	copy := filepath.Join(root, "checks", "old-copy")
	if err := os.MkdirAll(copy, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := lp.Core.ClaimTask(ctx, task.ID, core.StepValidateMerge); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(lp.Core.StateDirectory(), "commands", "commands", "unreclaimed")
	if err := os.MkdirAll(stale, 0700); err != nil {
		t.Fatal(err)
	}
	lp.commands = func(context.Context, sandbox.Options) (commandSandbox, error) {
		return &fakeCommands{closeErr: errors.New("still running")}, nil
	}
	d, err := lp.Core.OpenTaskDecision(ctx, task.ID, core.DecisionFailure, core.DecisionInput{Title: "Cleanup", Context: "Unknown cleanup", Recommendation: choiceStop, Choices: []string{choiceStop, choiceTryAgain}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lp.Core.ChooseDecision(ctx, d.ID, choiceStop, core.FromOwner); err != nil {
		t.Fatal(err)
	}
	other, err := lp.Core.CreateProject(ctx, core.ProjectInput{Title: "Independent", Template: "draft", Brief: core.BriefInput{Goal: "Write independently"}})
	if err != nil {
		t.Fatal(err)
	}
	next := queue(t, lp, other, "Independent draft")
	graceful, cancel := context.WithCancel(ctx)
	done := runLoop(lp, lifecycle.Stop{Graceful: graceful, Force: ctx})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if taskByID(t, lp, task.ID).Status == core.TaskStopped && len(taskByID(t, lp, next.ID).Revisions) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	waitFor(t, done, "loop did not stop")
	if taskByID(t, lp, task.ID).Status != core.TaskStopped || len(taskByID(t, lp, next.ID).Revisions) == 0 {
		t.Fatal("startup cleanup blocked answers or independent work", p.ID)
	}
	if _, err := os.Stat(stale); err != nil {
		t.Fatal("uncertain command state removed", err)
	}
	if _, err := os.Stat(copy); err != nil {
		t.Fatal("uncertain copy removed", err)
	}
	lp.commands = func(context.Context, sandbox.Options) (commandSandbox, error) {
		return &fakeCommands{onClose: func() { _ = os.RemoveAll(stale) }}, nil
	}
	_, started, err := lp.pass(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, done := range started {
		for value := range done {
			t.Fatal(value)
		}
	}
	if _, err := os.Stat(copy); !os.IsNotExist(err) {
		t.Fatal("stopped task stranded startup copy", err)
	}
	if taskByID(t, lp, task.ID).Status != core.TaskStopped {
		t.Fatal("cleanup reopened stopped task")
	}
}

func TestAcceptedCheckRetainsCoverageBeyondOutputTail(t *testing.T) {
	var reopen func(*Loop) *Loop
	lp, _, p, task, _, source := acceptedCode(t, choiceAcceptDraft, false, &reopen)
	ownerCommits(t, source, "owner.go", "package main\n", "owner work")
	task = stepUntil(t, lp, task.ID, func(t core.Task) bool { return len(t.Revisions) == 2 })
	ctx := context.Background()
	m, err := lp.mediumFor(ctx, p, taskPlaybook(p, task))
	if err != nil {
		t.Fatal(err)
	}
	report := checktest.Report{Complete: true, Failed: true}
	for i := 0; i < 150; i++ {
		report.Skips = append(report.Skips, checktest.Skip{Package: "fixture", Test: fmt.Sprintf("TestRequired/%03d", i), Reason: strings.Repeat("capability denied ", 30)})
	}
	runs := completedCoverageRun(t, lp, m.(gitMedium), "", report)
	defer runs.close()
	if err := lp.validateAcceptedMerge(ctx, p, task, m); err != nil {
		t.Fatal(err)
	}
	steps, err := lp.Core.TurnSteps(ctx, p.ID, task.ID, "Project check")
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for _, step := range steps {
		if step.Clipped {
			t.Fatal("coverage note clipped")
		}
		all.WriteString(step.Text)
	}
	for i := 0; i < 150; i++ {
		if !strings.Contains(all.String(), fmt.Sprintf("TestRequired/%03d", i)) {
			t.Fatal("skip identity lost", i)
		}
	}
	if !strings.Contains(all.String(), "Accepted merged draft 2") || !strings.Contains(all.String(), "observed skip count: 150") {
		t.Fatal("revision-bound summary lost")
	}
}
