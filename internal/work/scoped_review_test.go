//go:build !windows

package work

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/lib-agent-harness/sandbox"
	"github.com/shhac/lib-agent-harness/session"
)

func TestFullCheckWithoutCoverageProgressPasses(t *testing.T) {
	t.Parallel()
	a, _, p, task := fullCheckFixture(t, nil, true)
	a.commands = func(context.Context, sandbox.Options) (commandSandbox, error) {
		return &fakeCommands{result: sandbox.CommandResult{Stdout: "generic repository tests passed"}}, nil
	}
	task = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Status == core.TaskWaiting || t.Status == core.TaskLanding })
	if !task.FullCheckPassed(p.Brief.Version) {
		t.Fatal(task.FullCheck)
	}
}

func TestAcceptedMergeWithoutProgressPassesAndIsNotCheckedTwice(t *testing.T) {
	t.Parallel()
	var reopen func(*Loop) *Loop
	a, _, p, task, _, source := acceptedCode(t, choiceAcceptDraft, false, &reopen)
	_, err := a.updateOpen(context.Background(), task.ID, func(now *core.Task, _ *core.Project) (string, error) {
		book := *now.Playbook
		book.ScopedCheck = "make scoped"
		now.Playbook = &book
		return "scoped fixture", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ownerCommits(t, source, "owner.go", "package main\n", "owner work")
	task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) == 2 })
	calls := 0
	a.commands = func(context.Context, sandbox.Options) (commandSandbox, error) { calls++; return &fakeCommands{}, nil }
	task = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Status == core.TaskLanding })
	if calls != 1 || !task.FullCheckPassed(p.Brief.Version) || task.FullCheck != nil {
		t.Fatalf("calls=%d merge=%+v full=%+v", calls, task.MergeValidation, task.FullCheck)
	}
}

func TestScopedAcceptedMergeInfrastructureRemainsPending(t *testing.T) {
	for _, kind := range []string{"open", "cleanup"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			var reopen func(*Loop) *Loop
			a, _, p, task, _, source := acceptedCode(t, choiceAcceptDraft, false, &reopen)
			t.Cleanup(func() {
				a.commandChecks.Range(func(key, value any) bool {
					value.(commandCleanupObligation).remove()
					a.commandChecks.Delete(key)
					return true
				})
			})
			_, err := a.updateOpen(context.Background(), task.ID, func(now *core.Task, _ *core.Project) (string, error) {
				book := *now.Playbook
				book.ScopedCheck = "scoped"
				now.Playbook = &book
				return "scoped fixture", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			ownerCommits(t, source, "owner.go", "package main\n", "owner work")
			task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) == 2 })
			a.commands = func(context.Context, sandbox.Options) (commandSandbox, error) {
				if kind == "open" {
					return nil, errors.New("sandbox unavailable")
				}
				return &fakeCommands{closeErr: errors.New("cleanup unavailable")}, nil
			}
			m, err := a.mediumFor(context.Background(), p, taskPlaybook(p, task))
			if err != nil {
				t.Fatal(err)
			}
			err = a.validateAcceptedMerge(context.Background(), p, task, m)
			if kind == "cleanup" {
				if !errors.Is(err, errCommandRecovery) {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			now := taskByID(t, a, task.ID)
			if now.Round != task.Round || !now.MergeCheckPending(p.Brief.Version) || now.MergeValidation.Checked || now.MergeValidation.Failure != "" || now.DecisionID != "" {
				t.Fatalf("infrastructure failed accepted draft: %+v", now)
			}
			if kind == "cleanup" && !strings.Contains(now.MergeValidation.Evidence, "cleanup is unconfirmed") {
				t.Fatal(now.MergeValidation)
			}
		})
	}
}

func TestAcceptedScopedDraftPreservesAssetHoldsAndProjectFallback(t *testing.T) {
	p := core.Project{Playbook: &core.Playbook{Medium: core.MediumGit, Check: "full", ScopedCheck: "scoped"}}
	for _, kind := range []string{"project fallback", "integration", "landing reply"} {
		t.Run(kind, func(t *testing.T) {
			task := core.Task{Revisions: []core.Revision{{N: 1, Ref: "draft"}}}
			want := core.TaskDeciding
			switch kind {
			case "integration":
				want = core.TaskWriting
				task.Design = []core.DesignRequest{{IntegrationPending: true, Production: &core.Production{Delivered: []core.DeliveredAsset{{Attachment: "asset"}}}}}
			case "landing reply":
				want = core.TaskWriting
				task.Roles = []core.Role{{Name: "Designer", Kinds: []string{core.RoleDesigner}}}
				asset := true
				task.Unreachable = []core.Unreachable{{Source: "landing", AssetCreation: &asset}}
			}
			acceptAfterFullCheck(p, 1)(&task)
			if task.Status != want {
				t.Fatalf("%s bypassed: status=%s", kind, task.Status)
			}
		})
	}
	book := core.Playbook{Medium: core.MediumDocuments, Check: "full", ScopedCheck: "scoped"}
	if roundCheckCommand(&book) != "full" || scopedCheckGuide(&book) != "" {
		t.Fatal("document checks offered scoped execution without a change record")
	}
	prompt := checkerPrompt(core.Project{}, core.Task{}, core.Revision{}, core.Role{Name: "QA", Kinds: []string{core.RoleQA}}, &book)
	if !strings.Contains(prompt, "once:\nfull") || strings.Contains(prompt, "once:\nscoped") {
		t.Fatal(prompt)
	}
}

func TestFullCheckInfrastructureRetriesWithoutSpendingRound(t *testing.T) {
	for _, failure := range []string{"open", "start", "checkout"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			a, _, p, task := fullCheckFixture(t, nil, true)
			task = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.FullCheckPending(p.Brief.Version) })
			if failure == "checkout" {
				var err error
				task, err = a.updateOpen(context.Background(), task.ID, func(now *core.Task, _ *core.Project) (string, error) {
					now.Revisions[len(now.Revisions)-1].Ref = "missing-commit"
					now.FullCheck.Ref = "missing-commit"
					return "unavailable revision fixture", nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			a.commands = func(context.Context, sandbox.Options) (commandSandbox, error) {
				if failure == "open" {
					return nil, errors.New("synthetic sandbox unavailable")
				}
				if failure == "checkout" {
					t.Fatal("checkout failure ran a command")
				}
				return &fakeCommands{runErr: errors.New("synthetic command start failure")}, nil
			}
			progressed, err := a.loopStep(context.Background(), false)
			if err != nil || !progressed {
				t.Fatalf("step=%v err=%v", progressed, err)
			}
			now := taskByID(t, a, task.ID)
			if now.Round != task.Round || now.Status != core.TaskReviewing || !now.FullCheckPending(p.Brief.Version) || now.FullCheck.Checked || now.FullCheck.Failure != "" || now.DecisionID != "" || now.Failures != 1 || !now.RetryAt.After(time.Now()) {
				t.Fatalf("infrastructure spent a round or lost retry: %+v", now)
			}
			for _, v := range now.Verdicts {
				if v.Role == "Project check" {
					t.Fatal("infrastructure became a draft finding")
				}
			}
		})
	}
}

func TestSetTeamPreservesRepositoryScopedCheck(t *testing.T) {
	t.Parallel()
	a := testLoop(t)
	p := codeProject(t, a, ownerRepo(t))
	book := *p.Playbook
	book.ScopedCheck = "make check-scoped"
	p, err := a.Core.SetPlaybook(context.Background(), p.ID, book)
	if err != nil {
		t.Fatal(err)
	}
	p, err = a.SetTeam(context.Background(), p.ID, TeamChoice{Template: "code", Check: book.Check, MaxRounds: "4"})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := a.Core.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p.Playbook.ScopedCheck != book.ScopedCheck || snap.Repository(p.Scope.Repositories[0].ID).ScopedCheck != book.ScopedCheck {
		t.Fatal("team selection cleared repository scoped check")
	}
}

// Exercise the actual role tool setup, rather than installing a record by
// hand: the implementer works in a live clone with uncommitted files.
func TestImplementerRunCheckRecordsLiveDraftFromTaskBase(t *testing.T) {
	for _, missingBase := range []bool{false, true} {
		t.Run(map[bool]string{false: "live draft", true: "unavailable base"}[missingBase], func(t *testing.T) {
			lp, m, source := checkFixture(t)
			cmd := exec.Command("git", "rev-parse", "HEAD")
			cmd.Dir = source
			base, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			baseRef := strings.TrimSpace(string(base))
			if missingBase {
				baseRef = "missing-base"
			}
			if err = os.WriteFile(filepath.Join(source, "main.go"), []byte("package main // uncommitted\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(source, "new.go"), []byte("package main\n"), 0600); err != nil {
				t.Fatal(err)
			}
			m.playbook.ScopedCheck = "make scoped"
			lp.commands = func(_ context.Context, o sandbox.Options) (commandSandbox, error) {
				return &fakeCommands{run: func(_ context.Context, req sandbox.CommandRequest) (sandbox.CommandResult, error) {
					want := "make scoped"
					if missingBase {
						want = "make check"
					}
					if req.Command != want {
						t.Errorf("command=%s want=%s", req.Command, want)
					}
					if !missingBase {
						var list string
						for _, e := range o.Env {
							if v, ok := strings.CutPrefix(e, "CREW_CHANGED_FILES="); ok {
								list = v
							}
						}
						data, err := os.ReadFile(list)
						if err != nil {
							t.Fatal(err)
						}
						for _, name := range []string{"main.go", "new.go"} {
							if !strings.Contains(string(data), name) {
								t.Fatalf("missing %s in %s", name, data)
							}
						}
					}
					return sandbox.CommandResult{Stdout: "passed"}, nil
				}}, nil
			}
			spec, cleanup, err := lp.roleSpec(core.Task{ID: "task", Base: baseRef, Status: core.TaskWriting}, core.Role{Name: "Implementer", Engine: "codex", Kinds: []string{core.RoleImplementer}}, source, true, m, "work", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			result, err := spec.Handler.CallTool(context.Background(), session.ToolCall{Name: "run_check", Arguments: []byte("{}")})
			if err != nil || result.IsError {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if missingBase && !strings.Contains(result.Content, "No changed-file record") {
				t.Fatal(result.Content)
			}
		})
	}
}
