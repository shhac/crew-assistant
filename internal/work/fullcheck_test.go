//go:build !windows

package work

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/checktest"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/lib-agent-harness/sandbox"
)

func fullCheckFixture(t *testing.T, reopen *func(*Loop) *Loop, scoped bool) (*Loop, *codeRunner, core.Project, core.Task) {
	t.Helper()
	if reopen == nil {
		var unused func(*Loop) *Loop
		reopen = &unused
	}
	runner := &codeRunner{scriptedRunner: scriptedRunner{reviews: passes(30)}}
	a, _, _ := loopApp(t, &runner.scriptedRunner, "", reopen)
	a.runner = runner
	p := codeProject(t, a, ownerRepo(t))
	book := *p.Playbook
	if scoped {
		book.ScopedCheck = "make check-scoped"
	}
	var err error
	p, err = a.Core.SetPlaybook(context.Background(), p.ID, book)
	if err != nil {
		t.Fatal(err)
	}
	task := queue(t, a, p, "Add feature")
	task = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Status == core.TaskDeciding })
	return a, runner, p, task
}

func checkpointFake(t *testing.T, o sandbox.Options, progress *checktest.Progress, result sandbox.CommandResult) commandSandbox {
	t.Helper()
	return &fakeCommands{run: func(_ context.Context, req sandbox.CommandRequest) (sandbox.CommandResult, error) {
		if req.Command != "make check" {
			t.Errorf("pre-approval command=%s", req.Command)
		}
		if progress != nil {
			var path, invocation string
			for _, e := range o.Env {
				if v, ok := strings.CutPrefix(e, checktest.ProgressEnv+"="); ok {
					path = v
				}
				if v, ok := strings.CutPrefix(e, checktest.InvocationEnv+"="); ok {
					invocation = v
				}
			}
			p := *progress
			p.Invocation = invocation
			if err := checktest.SaveProgress(path, p); err != nil {
				t.Fatal(err)
			}
		}
		return result, nil
	}}
}

func TestFullCheckGatesApprovalAndSurvivesRestart(t *testing.T) {
	t.Parallel()
	var reopen func(*Loop) *Loop
	a, _, _, task := fullCheckFixture(t, &reopen, true)
	task = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.FullCheckPending(1) })
	if task.Status != core.TaskReviewing || task.DecisionID != "" || task.Approved != 0 {
		t.Fatalf("approval before full check: %+v", task)
	}
	a = reopen(a)
	calls := 0
	a.commands = func(_ context.Context, o sandbox.Options) (commandSandbox, error) {
		calls++
		return checkpointFake(t, o, &checktest.Progress{Stage: "complete", Done: true, Report: checktest.Report{Complete: true}}, sandbox.CommandResult{}), nil
	}
	task = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Status == core.TaskWaiting || t.Status == core.TaskLanding })
	if calls != 1 || !task.FullCheckPassed(1) {
		t.Fatalf("calls=%d task=%+v", calls, task)
	}
	if task.Status == core.TaskWaiting && openDecision(t, a, task).Revision != task.Revisions[len(task.Revisions)-1].N {
		t.Fatal("approval not on checked draft")
	}
}

func TestFullCheckFailuresReturnToWriter(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"exit", "timeout", "required skip", "incomplete"} {
		t.Run(kind, func(t *testing.T) {
			a, _, p, task := fullCheckFixture(t, nil, true)
			task = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.FullCheckPending(p.Brief.Version) })
			progress := &checktest.Progress{Stage: "complete", Done: true, Report: checktest.Report{Complete: true}}
			result := sandbox.CommandResult{Stderr: "synthetic failure"}
			switch kind {
			case "exit":
				result.ExitCode = 1
			case "timeout":
				result.TimedOut = true
			case "required skip":
				progress.Report.Skips = []checktest.Skip{{Package: "fixture", Test: "TestRequired", Reason: "loopback denied"}}
			case "incomplete":
				progress.Stage = "Go tests"
				progress.Done = false
			}
			a.commands = func(_ context.Context, o sandbox.Options) (commandSandbox, error) {
				return checkpointFake(t, o, progress, result), nil
			}
			task = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Status == core.TaskWriting && t.Round == 2 })
			if task.FullCheck != nil || task.Approved != 0 || !strings.Contains(writerPrompt(p, task, "", true), "Project check failed") {
				t.Fatalf("finding missing from next round: %+v", task)
			}
		})
	}
}

func TestNoScopedCheckDoesNotGateApproval(t *testing.T) {
	t.Parallel()
	a, _, _, task := fullCheckFixture(t, nil, false)
	a.commands = func(context.Context, sandbox.Options) (commandSandbox, error) {
		t.Fatal("unexpected pre-approval check")
		return nil, errors.New("unexpected")
	}
	task = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Status == core.TaskWaiting || t.Status == core.TaskLanding })
	if task.FullCheck != nil {
		t.Fatal("legacy task gained full-check record")
	}
}

func TestFullCheckRejectsStaleDraftAndHoldsUnconfirmedCleanup(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"new revision", "cleanup"} {
		t.Run(mode, func(t *testing.T) {
			a, _, p, task := fullCheckFixture(t, nil, true)
			t.Cleanup(func() {
				a.commandChecks.Range(func(key, value any) bool {
					value.(commandCleanupObligation).remove()
					a.commandChecks.Delete(key)
					return true
				})
			})
			task = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.FullCheckPending(p.Brief.Version) })
			a.commands = func(_ context.Context, o sandbox.Options) (commandSandbox, error) {
				fake := checkpointFake(t, o, &checktest.Progress{Stage: "complete", Done: true, Report: checktest.Report{Complete: true}}, sandbox.CommandResult{}).(*fakeCommands)
				if mode == "cleanup" {
					fake.closeErr = errors.New("synthetic unsettled child")
				} else {
					run := fake.run
					fake.run = func(ctx context.Context, req sandbox.CommandRequest) (sandbox.CommandResult, error) {
						_, err := a.updateOpen(ctx, task.ID, func(now *core.Task, _ *core.Project) (string, error) {
							r := now.Revisions[len(now.Revisions)-1]
							r.N++
							now.Revisions = append(now.Revisions, r)
							return "new draft", nil
						})
						if err != nil {
							t.Fatal(err)
						}
						return run(ctx, req)
					}
				}
				return fake, nil
			}
			m, err := a.mediumFor(context.Background(), p, taskPlaybook(p, task))
			if err != nil {
				t.Fatal(err)
			}
			err = a.validateFullCheck(context.Background(), p, task, m)
			want := core.ErrStale
			if mode == "cleanup" {
				want = errCommandRecovery
			}
			if !errors.Is(err, want) {
				t.Fatalf("error=%v want=%v", err, want)
			}
			snap, _ := a.Core.Snapshot(context.Background())
			now, _ := findTask(snap, "", task.ID)
			if now.FullCheckPassed(p.Brief.Version) || now.Approved != 0 || now.DecisionID != "" {
				t.Fatal("stale or unsettled check approved")
			}
			if mode == "cleanup" && (now.FullCheck.Checked || !now.FullCheckPending(p.Brief.Version) || !strings.Contains(now.FullCheck.Failure, "cleanup is unconfirmed")) {
				t.Fatal(now.FullCheck)
			}
		})
	}
}

func TestFullCheckFailureAtRoundLimitEscalates(t *testing.T) {
	t.Parallel()
	a, _, p, task := fullCheckFixture(t, nil, true)
	task = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.FullCheckPending(p.Brief.Version) })
	_, err := a.updateOpen(context.Background(), task.ID, func(now *core.Task, _ *core.Project) (string, error) {
		now.MaxRounds = now.Round
		return "round limit", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	a.commands = func(_ context.Context, o sandbox.Options) (commandSandbox, error) {
		return checkpointFake(t, o, &checktest.Progress{Stage: "complete", Done: true, Report: checktest.Report{Complete: true}}, sandbox.CommandResult{ExitCode: 1, Stderr: "limit failure"}), nil
	}
	task = stepUntil(t, a, task.ID, waiting)
	d := openDecision(t, a, task)
	if d.Kind != core.DecisionEscalation || !strings.Contains(d.Context, "limit failure") {
		t.Fatalf("%+v", d)
	}

	if _, err := a.Core.ChooseDecision(context.Background(), d.ID, choiceAcceptDraft, core.FromOwner); err != nil {
		t.Fatal(err)
	}
	task = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.FullCheckPending(p.Brief.Version) })
	if task.Status != core.TaskReviewing || !task.AcceptanceStands(p.Brief.Version) {
		t.Fatalf("accepted draft bypassed gate: approved=%d acceptance=%+v brief=%d check=%+v status=%s", task.Approved, task.Acceptance, p.Brief.Version, task.FullCheck, task.Status)
	}
	task = stepUntil(t, a, task.ID, waiting)
	d = openDecision(t, a, task)
	if d.Kind != core.DecisionFailure || !strings.Contains(d.Context, "limit failure") {
		t.Fatalf("%+v", d)
	}
}

func TestFullCheckPrecedesAutomaticAndPMApproval(t *testing.T) {
	t.Parallel()
	for _, approval := range []string{core.ApproveNone, core.ApprovePM} {
		t.Run(approval, func(t *testing.T) {
			w := newPMPush(t, approval, "", passes(20)...)
			book := *w.p.Playbook
			book.ScopedCheck = "make check-scoped"
			p, err := w.a.Core.SetPlaybook(context.Background(), w.p.ID, book)
			if err != nil {
				t.Fatal(err)
			}
			task := queue(t, w.a, p, "Add feature")
			task = stepUntil(t, w.a, task.ID, func(t core.Task) bool { return t.FullCheckPending(p.Brief.Version) })
			if task.Status != core.TaskReviewing || task.DecisionID != "" || task.LandDecision != nil || task.Approved != 0 {
				t.Fatal("approval preceded full check")
			}
			calls := 0
			w.a.commands = func(_ context.Context, o sandbox.Options) (commandSandbox, error) {
				calls++
				return checkpointFake(t, o, &checktest.Progress{Stage: "complete", Done: true, Report: checktest.Report{Complete: true}}, sandbox.CommandResult{}), nil
			}
			task = stepUntil(t, w.a, task.ID, func(t core.Task) bool { return t.Status == core.TaskLanding || t.Status == core.TaskWaiting })
			if calls != 1 || !task.FullCheckPassed(p.Brief.Version) {
				t.Fatalf("calls=%d full=%+v", calls, task.FullCheck)
			}
		})
	}
}

func TestScopedPromptsAndValidatedMergeReuse(t *testing.T) {
	t.Parallel()
	p := core.Project{}
	pb := core.Playbook{Medium: core.MediumGit, Check: "make check"}
	task := core.Task{Playbook: &pb, Revisions: []core.Revision{{N: 1, Ref: "draft"}}}
	for _, scoped := range []string{"", "make check-scoped"} {
		pb.ScopedCheck = scoped
		for _, prompt := range []string{writerPrompt(p, task, "", true), checkerPrompt(p, task, task.Revisions[0], core.Role{Name: "QA", Kinds: []string{core.RoleQA}}, &pb), checkerPrompt(p, task, task.Revisions[0], core.Role{Name: "Reviewer", Kinds: []string{core.RoleReviewer}}, &pb)} {
			if strings.Contains(prompt, "full project check runs separately before approval") != (scoped != "") {
				t.Fatalf("scoped=%q prompt=%s", scoped, prompt)
			}
		}
	}
	task.MergeValidation = &core.MergeValidation{Revision: 1, Ref: "draft", Checked: true}
	if !task.FullCheckPassed(0) {
		t.Fatal("validated merged draft requires duplicate full check")
	}
}

func TestReadyPRWaitsForFullCheckBeforeMergeApproval(t *testing.T) {
	t.Parallel()
	a, _, p, task := fullCheckFixture(t, nil, true)
	var err error
	task, err = a.updateOpen(context.Background(), task.ID, func(now *core.Task, _ *core.Project) (string, error) {
		now.Proposal = &core.Proposal{Number: 7, Pushed: now.Revisions[len(now.Revisions)-1].Ref, Observed: &core.Observed{Ready: true}}
		return "ready PR fixture", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	r := task.Revisions[len(task.Revisions)-1]
	if !readyToMerge(task, r) {
		t.Fatal("fixture is not ready to merge")
	}
	if err = a.askForDelivery(context.Background(), p, task, r); err != nil {
		t.Fatal(err)
	}
	snap, _ := a.Core.Snapshot(context.Background())
	task, _ = findTask(snap, "", task.ID)
	if !task.FullCheckPending(p.Brief.Version) || task.DecisionID != "" || task.Proposal.MergeApproved != 0 {
		t.Fatal("merge approval bypassed full check")
	}
}
