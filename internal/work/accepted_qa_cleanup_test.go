//go:build !windows

package work

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/roles"
	"github.com/shhac/lib-agent-harness/sandbox"
	"github.com/shhac/lib-agent-harness/session"
)

type cleanupQARunner struct {
	*codeRunner
	hosted bool
	output string
}

func (r *cleanupQARunner) Run(ctx context.Context, spec roles.Spec) (roles.Result, error) {
	if spec.Role == core.RoleQA && r.hosted {
		result, err := spec.Handler.CallTool(ctx, session.ToolCall{Name: "run_check", Arguments: json.RawMessage("{}")})
		if err != nil || result.IsError {
			return roles.Result{}, errors.New("hosted check failed: " + result.Content)
		}
		r.output = result.Content
	}
	return r.codeRunner.Run(ctx, spec)
}

func TestAcceptedQACleanupHoldsValidationAndResources(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"hosted", "app"} {
		for _, action := range []string{"retry", "direction", "restart", "stop"} {
			t.Run(kind+"/"+action, func(t *testing.T) {
				t.Parallel()
				ctx := context.Background()
				var reopen func(*Loop) *Loop
				a, runner, p, task, _, source := acceptedCode(t, choiceAcceptFollowUp, true, &reopen)
				ownerCommits(t, source, "owner.go", "package main\n", "owner work")
				a.commands = func(context.Context, sandbox.Options) (commandSandbox, error) { return &fakeCommands{}, nil }
				task = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.MergeValidation != nil && t.MergeValidation.Checked })
				if kind == "app" {
					var err error
					task, err = a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
						t.Playbook.Run = &testRecipe
						for i := range t.Roles {
							if t.Roles[i].Holds(core.RoleQA) {
								t.Roles[i].Browser.On = true
							}
						}
						return "", nil
					})
					if err != nil {
						t.Fatal(err)
					}
					a.ports.find = func() (int, error) { return 43210, nil }
				}
				qr := &cleanupQARunner{codeRunner: runner, hosted: kind == "hosted"}
				a.runner = qr
				stale := filepath.Join(a.Core.StateDirectory(), "commands", "commands", "uncertain-qa")
				var workspace string
				fake := &fakeCommands{result: sandbox.CommandResult{Stdout: "known QA command result"}, closeErr: errors.New("QA cleanup uncertain"), onClose: func() {
					if err := os.MkdirAll(stale, 0700); err != nil {
						t.Error(err)
					}
				}}
				a.commands = func(_ context.Context, o sandbox.Options) (commandSandbox, error) {
					if workspace == "" {
						workspace = o.WorkDir
					}
					return fake, nil
				}
				before := roleCalls(runner, core.RoleReviewer)
				task = stepUntil(t, a, task.ID, waiting)
				d := openDecision(t, a, task)
				if d.Kind != core.DecisionFailure || !strings.Contains(d.Context, "cleanup") || len(task.Claims) != 1 || task.Claims[0].Held != errCommandRecovery.Error() {
					t.Fatal("QA cleanup released acceptance", task, d)
				}
				known := slices.ContainsFunc(task.Verdicts, func(v core.Verdict) bool {
					return v.Revision == 2 && v.Outcome == core.VerdictPass && strings.Contains(v.Role, "QA")
				})
				// Team seats may be named after their member rather than their role.
				if !known {
					for _, seat := range task.Checkers() {
						if seat.Holds(core.RoleQA) && task.Judged(seat.Name, 2, p.Brief.Version) {
							known = true
						}
					}
				}
				if !known || (kind == "hosted" && !strings.Contains(qr.output, "known QA command result")) {
					t.Fatal("known QA result discarded", task.Verdicts, qr.output)
				}
				if _, err := os.Stat(workspace); err != nil {
					t.Fatal("QA command tree removed", err)
				}
				if kind == "app" && (!a.ports.held[43210] || len(fake.starts) == 0) {
					t.Fatal("unconfirmed app released its port")
				}
				if action == "direction" {
					d, _ = a.Core.AnswerDecision(ctx, d.ID, "Revise with my direction", core.FromOwner)
				} else if action == "stop" {
					d, _ = a.Core.ChooseDecision(ctx, d.ID, choiceStop, core.FromOwner)
				} else {
					d, _ = a.Core.ChooseDecision(ctx, d.ID, choiceTryAgain, core.FromOwner)
				}
				if _, err := a.loopStep(ctx, false); err != nil {
					t.Fatal(err)
				}
				if action == "restart" {
					if err := a.resume(ctx); !errors.Is(err, errCommandRecovery) {
						t.Fatal("restart advanced before reclaim", err)
					}
				} else {
					if _, _, err := a.pass(ctx, true); err != nil {
						t.Fatal("retry/direction advanced before reclaim", err)
					}
				}
				if _, err := os.Stat(workspace); err != nil {
					t.Fatal("unconfirmed QA workspace lost", err)
				}
				if kind == "app" && !a.ports.held[43210] {
					t.Fatal("unconfirmed QA port lost")
				}
				a.commands = func(context.Context, sandbox.Options) (commandSandbox, error) {
					return &fakeCommands{onClose: func() { os.RemoveAll(stale) }}, nil
				}
				if action == "restart" {
					if err := a.resume(ctx); err != nil {
						t.Fatal(err)
					}
					a = reopen(a)
					a.ports.find = func() (int, error) { return 43210, nil }
					a.commands = func(context.Context, sandbox.Options) (commandSandbox, error) { return &fakeCommands{}, nil }
				}
				if action == "stop" {
					if _, err := a.loopStep(ctx, false); err != nil {
						t.Fatal(err)
					}
					if taskByID(t, a, task.ID).Status != core.TaskStopped {
						t.Fatal("cleanup restarted stopped QA task")
					}
				} else if action == "direction" {
					if _, err := a.loopStep(ctx, false); err != nil {
						t.Fatal(err)
					}
				} else {
					task = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Finished() || t.Status == core.TaskWaiting })
					if task.Status != core.TaskLanded || roleCalls(runner, core.RoleReviewer) != before || len(followUps(t, a, task.ID)) != 1 {
						t.Fatal("QA retry failed accepted landing", task)
					}
				}
				if action != "restart" {
					if _, err := os.Stat(workspace); !os.IsNotExist(err) {
						t.Fatal("confirmed cleanup retained old QA tree", err)
					}
					if kind == "app" && a.ports.held[43210] {
						t.Fatal("confirmed cleanup retained QA port")
					}
				}
			})
		}
	}
}
