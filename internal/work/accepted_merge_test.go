//go:build !windows

package work

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/integrations/github"
	"github.com/shhac/crew-assistant/internal/media/gitrepo"
	"github.com/shhac/lib-agent-harness/sandbox"
)

// acceptedCode starts with unresolved review findings at the round limit.
func acceptedCode(t *testing.T, choice string, qa bool, reopen *func(*Loop) *Loop, beforeAcceptance ...bool) (*Loop, *codeRunner, core.Project, core.Task, core.Decision, string) {
	t.Helper()
	source := ownerRepo(t)
	runner := &codeRunner{scriptedRunner: scriptedRunner{reviews: append([]string{revise}, passes(30)...)}}
	a, _, _ := loopApp(t, &runner.scriptedRunner, "", reopen)
	a.runner = runner
	p := codeProject(t, a, source)
	var err error
	pm, err := a.Core.SaveMember(context.Background(), "", core.MemberInput{Name: "Pim", Kinds: []string{core.RolePM}, Engine: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.SetTeam(context.Background(), p.ID, TeamChoice{Template: "code", MaxRounds: "1", Check: "make check", PM: pm.ID}); err != nil {
		t.Fatal(err)
	}
	if p, err = a.SetLanding(context.Background(), p.ID, core.LandPolicy{Via: core.LandPush, Target: "main"}); err != nil {
		t.Fatal(err)
	}
	if !qa {
		book := *p.Playbook
		book.Roles = slices.DeleteFunc(slices.Clone(book.Roles), func(r core.Role) bool { return r.Holds(core.RoleQA) })
		if p, err = a.Core.SetPlaybook(context.Background(), p.ID, book); err != nil {
			t.Fatal(err)
		}
	}
	task := queue(t, a, p, "Add Feature")
	task = stepUntil(t, a, task.ID, waiting)
	d := openDecision(t, a, task)
	if d.Kind != core.DecisionEscalation {
		t.Fatalf("expected round-limit decision: %+v", d)
	}

	d, err = a.Core.ChooseDecision(context.Background(), d.ID, choice, core.FromOwner)
	if err != nil {
		t.Fatal(err)
	}
	if len(beforeAcceptance) > 0 && beforeAcceptance[0] {
		return a, runner, p, task, d, source
	}
	task = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Status == core.TaskLanding })
	return a, runner, p, task, d, source
}

func followUps(t *testing.T, a *Loop, id string) []core.Task {
	t.Helper()
	snap, err := a.Core.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []core.Task
	for _, task := range snap.Tasks {
		if slices.Contains(task.DependsOn, id) {
			out = append(out, task)
		}
	}
	return out
}

func roleCalls(r *codeRunner, kind string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, spec := range r.seen {
		if spec.Role == kind {
			n++
		}
	}
	return n
}

func TestAcceptedFindingsSurviveCleanCatchUps(t *testing.T) {
	t.Parallel()
	for _, choice := range []string{choiceAcceptDraft, choiceAcceptFollowUp} {
		for _, count := range []int{1, 2} {
			for _, qa := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%d/qa=%v", choice, count, qa), func(t *testing.T) {
					var reopen func(*Loop) *Loop
					a, runner, _, task, d, source := acceptedCode(t, choice, qa, &reopen)
					reviews, qas := roleCalls(runner, core.RoleReviewer), roleCalls(runner, core.RoleQA)
					escalations := len(turns(&runner.scriptedRunner, "Judge what remains at the round limit"))
					round := task.Round
					if task.Acceptance == nil || task.Approved != 1 {
						t.Fatal("acceptance not retained")
					}
					follows := followUps(t, a, task.ID)
					wantFollow := 0
					if choice == choiceAcceptFollowUp {
						wantFollow = 1
					}
					if len(follows) != wantFollow {
						t.Fatalf("follow-ups: %+v", follows)
					}
					if wantFollow == 1 {
						if follows[0].Objective != d.FollowUp.Objective || !slices.Equal(follows[0].Criteria, d.FollowUp.Criteria) || !follows[0].HeldByOwner(core.RelationDependsOn, task.ID) {
							t.Fatal("follow-up changed")
						}
					}
					ownerCommits(t, source, "owner.go", "package main // first\n", "owner work")
					task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) == 2 })
					if task.Status != core.TaskReviewing || task.MergeValidation == nil || task.MergeValidation.Checked {
						t.Fatalf("missing validation obligation: %+v", task)
					}
					a = reopen(a)
					checks := 0
					a.commands = func(_ context.Context, o sandbox.Options) (commandSandbox, error) {
						if o.WorkDir == source || !o.Write {
							t.Error("check not isolated")
						}
						body, err := os.ReadFile(filepath.Join(o.WorkDir, "owner.go"))
						if err != nil || !strings.Contains(string(body), "first") {
							t.Error("merged result not checked")
						}
						checks++
						if count == 2 && checks == 1 {
							ownerCommits(t, source, "owner2.go", "package main // second\n", "more owner work")
						}
						if checks == 2 {
							if _, err := os.Stat(filepath.Join(o.WorkDir, "owner2.go")); err != nil {
								t.Error("second merge not checked")
							}
						}
						return &fakeCommands{}, nil
					}
					task = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Finished() || t.Status == core.TaskWaiting })
					if task.Status != core.TaskLanded || checks != count || len(task.Revisions) != count+1 || task.Round != round || runner.edits != 1 {
						t.Fatalf("accepted draft did not land unchanged: status %s checks %d revisions %d round %d", task.Status, checks, len(task.Revisions), task.Round)
					}
					if len(turns(&runner.scriptedRunner, "Judge what remains at the round limit")) != escalations {
						t.Fatal("PM escalated accepted work again")
					}
					if roleCalls(runner, core.RoleReviewer) != reviews {
						t.Fatal("reviewers ran again")
					}
					wantQA := qas
					if qa {
						wantQA += count
					}
					if roleCalls(runner, core.RoleQA) != wantQA {
						t.Fatalf("QA calls %d want %d", roleCalls(runner, core.RoleQA), wantQA)
					}
					if got := followUps(t, a, task.ID); len(got) != wantFollow || (wantFollow == 1 && got[0].ID != follows[0].ID) {
						t.Fatal("duplicate follow-up")
					}
					snap, _ := a.Core.Snapshot(context.Background())
					for _, other := range snap.Decisions {
						if other.TaskID == task.ID && other.ID != d.ID {
							t.Fatalf("unexpected new decision: %+v", other)
						}
					}
				})
			}
		}
	}
}

func TestAcceptedCatchUpConflictNamesTheCause(t *testing.T) {
	t.Parallel()
	var reopen func(*Loop) *Loop
	a, runner, _, task, d, source := acceptedCode(t, choiceAcceptFollowUp, true, &reopen)
	ownerCommits(t, source, "feature.go", "package main\nfunc Feature() { panic(\"owner\") }\n", "conflicting owner work")
	task = stepUntil(t, a, task.ID, waitingAgain(d))
	failure := openDecision(t, a, task)
	if failure.Kind != core.DecisionFailure || !strings.Contains(failure.Context, "conflicts") || strings.Contains(failure.Context, "rounds") || len(task.Revisions) != 1 || task.Acceptance != nil || runner.edits != 1 {
		t.Fatalf("conflict did not hold accepted draft: %+v %+v", task, failure)
	}
	if len(followUps(t, a, task.ID)) != 1 {
		t.Fatal("follow-up lost")
	}
	a = reopen(a)
	if _, err := a.Core.ChooseDecision(context.Background(), failure.ID, choiceResolve, core.FromOwner); err != nil {
		t.Fatal(err)
	}
	task = stepUntil(t, a, task.ID, waitingAgain(failure))
	if runner.edits != 2 || task.Acceptance != nil || len(task.Revisions) != 2 || !task.Judged("Reviewer", 2, task.Revisions[1].BriefVersion) {
		t.Fatalf("resolved conflict skipped ordinary checks: %+v", task)
	}
}

func TestAcceptedCatchUpCheckFailuresAreSpecificAndRetryable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		result sandbox.CommandResult
		err    error
	}{
		{"exit", sandbox.CommandResult{ExitCode: 2, Stderr: "synthetic failure", Stdout: strings.Repeat("progress ", 1000)}, nil},
		{"timeout", sandbox.CommandResult{TimedOut: true, ExitCode: -1, Stdout: strings.Repeat("progress ", 1000)}, nil},
		{"sandbox", sandbox.CommandResult{}, errors.New("synthetic sandbox refusal")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reopen func(*Loop) *Loop
			a, runner, _, task, d, source := acceptedCode(t, choiceAcceptFollowUp, false, &reopen)
			ownerCommits(t, source, "owner.go", "package main\n", "owner work")
			a.commands = func(context.Context, sandbox.Options) (commandSandbox, error) {
				if tc.err != nil {
					return nil, tc.err
				}
				return &fakeCommands{result: tc.result}, nil
			}
			task = stepUntil(t, a, task.ID, waitingAgain(d))
			failure := openDecision(t, a, task)
			if failure.Kind != core.DecisionFailure || !strings.Contains(failure.Context, "merged draft 2") || strings.Contains(failure.Context, "rounds") || task.MergeValidation.Failure == "" || task.Status == core.TaskLanded {
				t.Fatalf("wrong failure: %+v", failure)
			}
			if tc.name == "exit" && !strings.Contains(failure.Context, "synthetic failure") {
				t.Fatal("lost diagnostic")
			}
			if tc.name == "timeout" && !strings.Contains(failure.Context, "timed_out=true") {
				t.Fatal("lost timeout classification")
			}
			if len(followUps(t, a, task.ID)) != 1 || runner.edits != 1 {
				t.Fatal("failure changed work")
			}
			a = reopen(a)
			a.commands = func(context.Context, sandbox.Options) (commandSandbox, error) { return &fakeCommands{}, nil }
			if _, err := a.Core.ChooseDecision(context.Background(), failure.ID, choiceTryAgain, core.FromOwner); err != nil {
				t.Fatal(err)
			}
			task = stepUntil(t, a, task.ID, func(t core.Task) bool {
				return t.Finished() || (t.Status == core.TaskWaiting && t.DecisionID != failure.ID)
			})
			if task.Status != core.TaskLanded || task.Round != 1 || len(followUps(t, a, task.ID)) != 1 {
				t.Fatalf("retry did not land: %+v", task)
			}
		})
	}
}

func TestAcceptanceReplayCreatesOneFollowUp(t *testing.T) {
	t.Parallel()
	var reopen func(*Loop) *Loop
	a, _, _, task, d, _ := acceptedCode(t, choiceAcceptFollowUp, false, &reopen)
	follows := followUps(t, a, task.ID)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, follow, err := a.Core.AcceptWithFollowUp(context.Background(), task.ID, d.ID, approveLatest)
			if err != nil || follow.ID != follows[0].ID || out.Status != core.TaskLanding {
				t.Errorf("replay: %+v %+v %v", out, follow, err)
			}
		}()
	}
	wg.Wait()
	a = reopen(a)
	if err := a.applyAnswer(context.Background(), task, d); err != nil {
		t.Fatal(err)
	}
	if len(followUps(t, a, task.ID)) != 1 {
		t.Fatal("duplicate follow-up")
	}
}

func TestAcceptedCatchUpQAFailureReturnsToValidationOrTeam(t *testing.T) {
	t.Parallel()
	for _, custom := range []bool{false, true} {
		t.Run(fmt.Sprint(custom), func(t *testing.T) {
			var reopen func(*Loop) *Loop
			a, runner, _, task, d, source := acceptedCode(t, choiceAcceptFollowUp, true, &reopen)
			runner.mu.Lock()
			runner.reviews = append([]string{revise}, passes(20)...)
			runner.mu.Unlock()
			ownerCommits(t, source, "owner.go", "package main\n", "owner work")
			a.commands = func(context.Context, sandbox.Options) (commandSandbox, error) { return &fakeCommands{}, nil }
			task = stepUntil(t, a, task.ID, waitingAgain(d))
			failure := openDecision(t, a, task)
			if failure.Kind != core.DecisionFailure || !strings.Contains(failure.Context, "merged draft 2") || !strings.Contains(failure.Context, "Soften the opening") || task.Round != 1 {
				t.Fatalf("QA failure escalated: %+v", failure)
			}
			a = reopen(a)
			a.commands = func(context.Context, sandbox.Options) (commandSandbox, error) { return &fakeCommands{}, nil }
			if custom {
				if _, err := a.Core.AnswerDecision(context.Background(), failure.ID, "Fix the QA finding", core.FromOwner); err != nil {
					t.Fatal(err)
				}
				task = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Status == core.TaskWriting })
				if task.Acceptance != nil || !slices.Contains(task.Direction, "Fix the QA finding") || task.Round != 2 {
					t.Fatalf("direction did not return to team: %+v", task)
				}
			} else {
				if _, err := a.Core.ChooseDecision(context.Background(), failure.ID, choiceTryAgain, core.FromOwner); err != nil {
					t.Fatal(err)
				}
				task = stepUntil(t, a, task.ID, func(t core.Task) bool {
					return t.Finished() || (t.Status == core.TaskWaiting && t.DecisionID != failure.ID)
				})
				if task.Status != core.TaskLanded || task.Round != 1 || runner.edits != 1 {
					t.Fatalf("QA retry failed: %+v", task)
				}
			}
			if len(followUps(t, a, task.ID)) != 1 {
				t.Fatal("duplicate follow-up")
			}
		})
	}
}

func TestAcceptedMergeRejectsStaleCommandResults(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"text", "brief", "direction", "revision", "stop"} {
		t.Run(change, func(t *testing.T) {
			var reopen func(*Loop) *Loop
			a, _, p, task, _, source := acceptedCode(t, choiceAcceptDraft, true, &reopen)
			ownerCommits(t, source, "owner.go", "package main\n", "owner work")
			task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) == 2 })
			claims, err := a.Core.Schedule(context.Background(), func(core.Role) string { return "" })
			if err != nil || len(claims) != 1 || claims[0].Claim.Step != core.StepValidateMerge {
				t.Fatalf("missing exclusive command: %+v %v", claims, err)
			}
			a.commands = func(context.Context, sandbox.Options) (commandSandbox, error) {
				var err error
				if change == "stop" {
					_, err = a.StopTask(context.Background(), p.ID, task.ID)
				} else {
					_, err = a.Core.UpdateTask(context.Background(), task.ID, func(t *core.Task, p *core.Project) (string, error) {
						switch change {
						case "text":
							t.TextVersion++
						case "brief":
							p.Brief.Version++
						case "direction":
							t.DirectionPending++
						case "revision":
							t.Revisions = append(t.Revisions, core.Revision{N: 3, Ref: "superseding"})
						}
						return "", nil
					})
				}
				if err != nil {
					t.Error(err)
				}
				return &fakeCommands{}, nil
			}
			err = a.step(core.Fenced(context.Background(), task.ID, claims[0].Claim.Token), claims[0])
			if !errors.Is(err, core.ErrStale) {
				t.Fatalf("stale result saved: %v", err)
			}
			now := taskByID(t, a, task.ID)
			if now.MergeValidation != nil && now.MergeValidation.Checked {
				t.Fatal("stale check passed")
			}
			if now.AcceptedMergeReady(p.Brief.Version) {
				t.Fatal("stale result authorized delivery")
			}
		})
	}
}

func TestAcceptedCatchUpWithoutProjectCommandLands(t *testing.T) {
	t.Parallel()
	var reopen func(*Loop) *Loop
	a, runner, _, task, _, source := acceptedCode(t, choiceAcceptDraft, false, &reopen)
	if _, err := a.Core.UpdateTask(context.Background(), task.ID, func(t *core.Task, _ *core.Project) (string, error) { t.Playbook.Check = ""; return "", nil }); err != nil {
		t.Fatal(err)
	}
	ownerCommits(t, source, "owner.go", "package main\n", "owner work")
	a.commands = func(context.Context, sandbox.Options) (commandSandbox, error) {
		t.Error("no command configured")
		return &fakeCommands{}, nil
	}
	task = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Finished() })
	if task.Status != core.TaskLanded || roleCalls(runner, core.RoleQA) != 0 || task.MergeValidation.Evidence != "No project check configured." {
		t.Fatal("draft without a command did not land")
	}
}

func TestAcceptedCatchUpRestartRetainsEachValidationStep(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"prepared", "published", "committed", "command", "QA", "delivery", "delivery-brief", "delivery-text"} {
		t.Run(phase, func(t *testing.T) {
			var reopen func(*Loop) *Loop
			a, _, p, task, _, source := acceptedCode(t, choiceAcceptFollowUp, true, &reopen)
			ownerCommits(t, source, "owner.go", "package main\n", "owner work")
			m, task := a.testMedium(t, p.ID, task.ID)
			c, l, err := lag(context.Background(), m, task)
			if err != nil || l == nil {
				t.Fatalf("no lag: %v", err)
			}
			moved, merge, err := c.cleanMerge(context.Background(), task, *l)
			if err != nil || merge == "" {
				t.Fatalf("no merge: %v", err)
			}
			files, err := c.files(context.Background(), moved, merge)
			if err != nil {
				t.Fatal(err)
			}
			h := core.Handoff{Revision: core.Revision{N: 2, Ref: merge, Files: files}, CatchUp: &core.CatchUp{Base: moved.Base, From: moved.From, Carry: true, Name: l.Name, What: l.What}}
			if _, err = a.Core.UpdateTask(context.Background(), task.ID, func(t *core.Task, _ *core.Project) (string, error) {
				t.Attempt++
				h.Name = gitrepo.TaskRef(t.ID, 2, t.Attempt)
				t.Handoff = &h
				return "", nil
			}); err != nil {
				t.Fatal(err)
			}
			if phase != "prepared" {
				if err = m.publish(context.Background(), task, merge, h.Name); err != nil {
					t.Fatal(err)
				}
			}
			if phase != "prepared" && phase != "published" {
				if err = a.commitHandoff(context.Background(), task.ID, h.Name); err != nil {
					t.Fatal(err)
				}
			}
			checks := 0
			commands := func(context.Context, sandbox.Options) (commandSandbox, error) { checks++; return &fakeCommands{}, nil }
			a.commands = commands
			if phase == "command" || phase == "QA" || strings.HasPrefix(phase, "delivery") {
				task = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.MergeValidation != nil && t.MergeValidation.Checked })
			}
			if phase == "QA" || strings.HasPrefix(phase, "delivery") {
				task = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Status == core.TaskLanding })
			}
			if strings.HasPrefix(phase, "delivery") {
				r := task.Revisions[1]
				if held, err := a.beginDelivering(context.Background(), task.ID, r); err != nil || len(held) > 0 {
					t.Fatal(held, err)
				}
				if _, err := m.deliver(context.Background(), task, r); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "delivery-brief" || phase == "delivery-text" {
				editAcceptedRequirements(t, a, p.ID, task.ID, phase == "delivery-brief")
				if _, err := a.Core.Schedule(context.Background(), func(core.Role) string { return "test hold" }); err != nil {
					t.Fatal(err)
				}
				current := taskByID(t, a, task.ID)
				if current.Status != core.TaskLanding || current.Delivering == nil {
					t.Fatal("delivery intent rescheduled before reconciliation")
				}
			}
			a = reopen(a)
			a.commands = commands
			task = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Finished() || t.Status == core.TaskWaiting })
			if task.Status != core.TaskLanded || len(task.Revisions) != 2 || checks != 1 || task.Round != 1 || len(followUps(t, a, task.ID)) != 1 {
				t.Fatalf("recovery lost obligation: %s revisions %d checks %d", task.Status, len(task.Revisions), checks)
			}
			tip := ownerGit(t, source, "rev-parse", "HEAD")
			a = reopen(a)
			if after := ownerGit(t, source, "rev-parse", "HEAD"); tip != after {
				t.Fatal("delivery repeated")
			}
		})
	}
}

type refusedCatchUpPublication struct{ gitMedium }

func (m refusedCatchUpPublication) publish(context.Context, core.Task, string, string) error {
	return errors.New("synthetic catch-up publication failure")
}

func TestAcceptedCatchUpPublicationFailureKeepsAcceptance(t *testing.T) {
	t.Parallel()
	var reopen func(*Loop) *Loop
	a, runner, p, task, _, source := acceptedCode(t, choiceAcceptFollowUp, true, &reopen)
	ownerCommits(t, source, "owner.go", "package main\n", "owner work")
	m, task := a.testMedium(t, p.ID, task.ID)
	_, l, err := lag(context.Background(), m, task)
	if err != nil || l == nil {
		t.Fatal(err)
	}
	if err = a.catchUpRound(context.Background(), task, refusedCatchUpPublication{m}, *l); err != nil {
		t.Fatal(err)
	}
	task = taskByID(t, a, task.ID)
	if task.Status != core.TaskLanding || task.Handoff != nil || !task.AcceptanceStands(task.Acceptance.BriefVersion) || len(task.Revisions) != 1 {
		t.Fatalf("failed publication sent work to review: %+v", task)
	}
	a = reopen(a)
	if _, err = a.Core.UpdateTask(context.Background(), task.ID, func(t *core.Task, _ *core.Project) (string, error) { t.RetryAt = time.Time{}; return "", nil }); err != nil {
		t.Fatal(err)
	}
	checks := 0
	a.commands = func(context.Context, sandbox.Options) (commandSandbox, error) { checks++; return &fakeCommands{}, nil }
	task = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Finished() || t.Status == core.TaskWaiting })
	if task.Status != core.TaskLanded || checks != 1 || len(task.Revisions) != 2 || runner.edits != 1 || len(followUps(t, a, task.ID)) != 1 {
		t.Fatal("publication recovery did not validate and land")
	}
}

func TestAcceptedMergeDeliveryGuardRequiresCurrentValidation(t *testing.T) {
	t.Parallel()
	var reopen func(*Loop) *Loop
	a, _, _, task, _, source := acceptedCode(t, choiceAcceptDraft, true, &reopen)
	ownerCommits(t, source, "owner.go", "package main\n", "owner work")
	task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) == 2 })
	for _, step := range []string{"pending command", "pending QA", "failed QA", "passed"} {
		t.Run(step, func(t *testing.T) {
			if _, err := a.Core.UpdateTask(context.Background(), task.ID, func(t *core.Task, p *core.Project) (string, error) {
				t.Status = core.TaskLanding
				t.MergeValidation.Checked = step != "pending command"
				t.Verdicts = slices.DeleteFunc(t.Verdicts, func(v core.Verdict) bool { return v.Revision == 2 })
				if step == "failed QA" || step == "passed" {
					outcome := core.VerdictPass
					if step == "failed QA" {
						outcome = core.VerdictRevise
					}
					t.Verdicts = append(t.Verdicts, core.Verdict{Role: "QA", Revision: 2, BriefVersion: p.Brief.Version, TextVersion: t.TextVersion, Outcome: outcome})
				}
				return "", nil
			}); err != nil {
				t.Fatal(err)
			}
			held, err := a.beginDelivering(context.Background(), task.ID, task.Revisions[1])
			if err != nil {
				t.Fatal(err)
			}
			now := taskByID(t, a, task.ID)
			if step == "passed" {
				if len(held) != 0 || now.Delivering == nil {
					t.Fatal("valid merged result held")
				}
			} else if len(held) == 0 || now.Delivering != nil || now.Status != core.TaskReviewing {
				t.Fatal("unvalidated merge entered delivery")
			}
		})
	}
}

func TestAcceptedContinuationKeepsMergeApprovalGate(t *testing.T) {
	t.Parallel()
	for _, choice := range []string{choiceAcceptDraft, choiceAcceptFollowUp} {
		for _, mode := range []string{"land", "brief", "text", "brief-answer", "text-answer", "conflict", "recover-brief", "recover-text", "direction-brief", "direction-text", "changes-brief", "changes-text", "publication-retry", "publication-retry-restart", "create-brief", "create-text", "dismiss-brief", "dismiss-text", "queued-brief", "queued-text", "lost-brief", "lost-text", "pm-create-brief", "pm-create-text"} {
			t.Run(choice+"/"+mode, func(t *testing.T) {
				t.Parallel()
				var reopen func(*Loop) *Loop
				a, _, p, task, _, source := acceptedCode(t, choice, false, &reopen)
				ownerCommits(t, source, "owner.go", "package main\n", "owner work")
				task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) == 2 })
				a.commands = func(context.Context, sandbox.Options) (commandSandbox, error) { return &fakeCommands{}, nil }
				task = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Status == core.TaskDeciding })
				task, err := a.Core.UpdateTask(context.Background(), task.ID, func(t *core.Task, _ *core.Project) (string, error) {
					t.Playbook.Land.PullRequests = true
					t.Playbook.Land.GitHub = "o/r"
					t.Playbook.Land.Approve = core.ApproveBefore
					t.Proposal = &core.Proposal{Branch: "crew/accepted", Number: 7, Pushed: t.Revisions[1].Ref, Observed: &core.Observed{Ready: true}}
					return "", nil
				})
				if err != nil {
					t.Fatal(err)
				}
				remote := t.TempDir()
				ownerGit(t, remote, "init", "-q", "--bare")
				ownerGit(t, source, "push", "-q", remote, "main")
				m, err := a.mediumFor(context.Background(), p, taskPlaybook(p, task))
				if err != nil {
					t.Fatal(err)
				}
				ownerGit(t, m.(gitMedium).repo.Workspace(), "push", "-q", remote, task.Revisions[1].Ref+":refs/heads/crew/accepted")
				gh := &fakeGitHub{t: t, remote: remote, head: "crew/accepted", opened: 1, checks: "SUCCESS", decision: "APPROVED"}
				a.github = github.Client{Run: gh.run}
				a.githubURL = func(string) string { return remote }
				if strings.HasPrefix(mode, "create-") || strings.HasPrefix(mode, "pm-create-") {
					edited := editingApprovalMedium{medium: m, edit: func() { editAcceptedRequirements(t, a, p.ID, task.ID, mode == "create-brief") }}
					var continuationErr error
					if strings.HasPrefix(mode, "pm-create-") {
						land := task.Playbook.Land
						land.Approve = core.ApprovePM
						p, err = a.SetLanding(context.Background(), p.ID, land)
						if err != nil {
							t.Fatal(err)
						}
						var once sync.Once
						a.githubURL = func(string) string {
							once.Do(func() { editAcceptedRequirements(t, a, p.ID, task.ID, mode == "pm-create-brief") })
							return remote
						}
						continuationErr = a.askForDelivery(context.Background(), p, task, task.Revisions[1])
					} else {
						continuationErr = a.askOwnerToLand(context.Background(), p, task, task.Revisions[1], edited, core.DecisionInput{})
					}
					if err := continuationErr; !errors.Is(err, core.ErrStale) {
						t.Fatal("stale deciding created approval", err)
					}
					if _, err := a.Core.Schedule(context.Background(), func(core.Role) string { return "test hold" }); err != nil {
						t.Fatal(err)
					}
					current := taskByID(t, a, task.ID)
					if current.Status != core.TaskReviewing || current.DecisionID != "" || current.Acceptance != nil {
						t.Fatal("stale continuation retained authority", current)
					}
					if err := a.approveMerge(context.Background(), task); !errors.Is(err, core.ErrStale) {
						t.Fatal("stale approval applied", err)
					}
					if len(gh.merges) != 0 {
						t.Fatal("unchecked work merged")
					}
					return
				}
				if err = a.decide(context.Background(), p, task); err != nil {
					t.Fatal(err)
				}
				now := taskByID(t, a, task.ID)
				if now.Status != core.TaskWaiting || openDecision(t, a, now).Kind != core.DecisionDelivery {
					t.Fatalf("merge gate bypassed: %+v", now)
				}
				d := openDecision(t, a, now)
				if strings.HasPrefix(mode, "dismiss-") {
					editAcceptedRequirements(t, a, p.ID, task.ID, mode == "dismiss-brief")
					d, err = a.Core.DismissDecision(context.Background(), d.ID, "Owner closed this choice")
					if err != nil {
						t.Fatal(err)
					}
					if _, err = a.Core.Schedule(context.Background(), func(core.Role) string { return "test hold" }); err != nil {
						t.Fatal(err)
					}
					current := taskByID(t, a, task.ID)
					snap, _ := a.Core.Snapshot(context.Background())
					saved, _ := findDecision(snap, d.ID)
					if current.DecisionID != d.ID || saved.ResolutionReason != "Owner closed this choice" {
						t.Fatal("owner dismissal consumed", current, saved)
					}
					if err := a.applyAnswer(context.Background(), current, saved); err != nil {
						t.Fatal(err)
					}
					if taskByID(t, a, task.ID).Status != core.TaskStopped {
						t.Fatal("dismissal did not stop task")
					}
					return
				}
				if strings.HasPrefix(mode, "queued-") || strings.HasPrefix(mode, "lost-") {
					if _, err = a.Core.ChooseDecision(context.Background(), d.ID, choiceApprove, core.FromOwner); err != nil {
						t.Fatal(err)
					}
					if err = a.approveMerge(context.Background(), now); err != nil {
						t.Fatal(err)
					}
					now = taskByID(t, a, task.ID)
					pr, err := a.github.View(context.Background(), "o/r", 7)
					if err != nil {
						t.Fatal(err)
					}
					lost := strings.HasPrefix(mode, "lost-")
					a.github = github.Client{Run: func(ctx context.Context, args ...string) ([]byte, error) {
						if args[0] == "pr" && args[1] == "merge" {
							out, err := gh.run(ctx, args...)
							if err != nil {
								return out, err
							}
							if lost {
								return nil, errors.New("response lost after remote merge")
							}
							gh.set(func() { gh.merged = ""; gh.queued = true })
							return out, nil
						}
						if lost {
							return nil, errors.New("outcome unavailable")
						}
						return gh.run(ctx, args...)
					}}
					err = a.mergeReady(context.Background(), p, now, now.Playbook.Land, now.Revisions[1], *now.Proposal, pr)
					if !lost && err != nil {
						t.Fatal(err)
					}
					if current := taskByID(t, a, task.ID); current.Delivering == nil && !current.PRMergePending() {
						t.Fatal("uncertain request lost intent")
					}
					editAcceptedRequirements(t, a, p.ID, task.ID, strings.HasSuffix(mode, "brief"))
					a = reopen(a)
					a.github, a.githubURL = github.Client{Run: gh.run}, func(string) string { return remote }
					before := roleCalls(a.runner.(*codeRunner), core.RoleReviewer)
					if !lost {
						snap, _ := a.Core.Snapshot(context.Background())
						if err := a.settleDeliveries(context.Background(), snap); err != nil {
							t.Fatal(err)
						}
						if _, err := a.Core.Schedule(context.Background(), func(core.Role) string { return "test hold" }); err != nil {
							t.Fatal(err)
						}
						current := taskByID(t, a, task.ID)
						if current.Delivering == nil && !current.PRMergePending() || current.Status != core.TaskLanding && current.Status != core.TaskAwaiting || current.Acceptance == nil {
							t.Fatal("OPEN treated as non-delivery", current)
						}
						gh.set(func() { gh.merged = "queued-merge-commit" })
					}
					now = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Finished() })
					expected := 0
					if choice == choiceAcceptFollowUp {
						expected = 1
					}
					if now.Status != core.TaskLanded || len(gh.merges) != 1 || roleCalls(a.runner.(*codeRunner), core.RoleReviewer) != before || len(followUps(t, a, task.ID)) != expected {
						t.Fatal("uncertain merge did not reconcile once", now)
					}
					return
				}

				if strings.HasPrefix(mode, "publication-retry") {
					ownerCommits(t, source, "owner-again.go", "package main\n", "main moves while approval waits")
					ownerGit(t, source, "push", "-q", remote, "main")
					for i := 0; i <= roleRetries; i++ {
						current := taskByID(t, a, task.ID)
						gm, current := a.testMedium(t, p.ID, current.ID)
						// GitHub requires it, as it does of a branch behind a
						// target that wants branches up to date.
						behind, err := gm.behindFor(context.Background(), current, true)
						if err != nil || behind == nil {
							t.Fatal("no catch-up", err)
						}
						if err := a.catchUpRound(context.Background(), current, refusedCatchUpPublication{gm}, *behind); err != nil {
							t.Fatal(err)
						}
					}
					current := taskByID(t, a, task.ID)
					failure := openDecision(t, a, current)
					if failure.Kind != core.DecisionFailure || current.ResumeStatus != core.TaskLanding || !strings.Contains(failure.Context, "publication failure") {
						t.Fatal("failure has no runnable continuation", current, failure)
					}
					if _, err := a.Core.ChooseDecision(context.Background(), failure.ID, choiceTryAgain, core.FromOwner); err != nil {
						t.Fatal(err)
					}
					if mode == "publication-retry-restart" {
						a = reopen(a)
						a.github = github.Client{Run: gh.run}
						a.githubURL = func(string) string { return remote }
						a.commands = func(context.Context, sandbox.Options) (commandSandbox, error) { return &fakeCommands{}, nil }
					}
					current = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Status == core.TaskWaiting && t.DecisionID != failure.ID })
					next := openDecision(t, a, current)
					// Retried, the pull request leaves main's clean move to GitHub
					// rather than taking it in again.
					if len(current.Revisions) != 2 || current.Round != 1 || next.Kind != core.DecisionDelivery {
						t.Fatal("retry did not validate and resume merge approval", current, next)
					}
					if _, err := a.Core.ChooseDecision(context.Background(), next.ID, choiceApprove, core.FromOwner); err != nil {
						t.Fatal(err)
					}
					current = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Finished() })
					expected := 0
					if choice == choiceAcceptFollowUp {
						expected = 1
					}
					if current.Status != core.TaskLanded || len(gh.merges) != 1 || len(followUps(t, a, task.ID)) != expected {
						t.Fatal("retry did not land once", current)
					}
					return
				}

				if strings.HasPrefix(mode, "recover-") {
					if _, err = a.Core.ChooseDecision(context.Background(), d.ID, choiceApprove, core.FromOwner); err != nil {
						t.Fatal(err)
					}
					if err = a.approveMerge(context.Background(), now); err != nil {
						t.Fatal(err)
					}
					now = taskByID(t, a, task.ID)
					pr, err := a.github.View(context.Background(), "o/r", 7)
					if err != nil {
						t.Fatal(err)
					}
					if err = a.mergeReady(context.Background(), p, now, now.Playbook.Land, now.Revisions[1], *now.Proposal, pr); err != nil {
						t.Fatal(err)
					}
					if current := taskByID(t, a, task.ID); current.Delivering == nil && !current.PRMergePending() {
						t.Fatal("mergeReady lost delivery intent before recording MERGED")
					}
					gh.set(func() { gh.merged = "actual-squash-commit" })
					editAcceptedRequirements(t, a, p.ID, task.ID, mode == "recover-brief")
					a = reopen(a)
					a.github = github.Client{Run: func(context.Context, ...string) ([]byte, error) { return nil, errors.New("observation unavailable") }}
					current := taskByID(t, a, task.ID)
					err = a.settleDelivery(context.Background(), p, current)
					retained := taskByID(t, a, task.ID)
					if err == nil || retained.Delivering == nil && !retained.PRMergePending() {
						t.Fatal("uncertain PR delivery lost its intent", err)
					}
					a.github = github.Client{Run: gh.run}
					a.githubURL = func(string) string { return remote }
					before := roleCalls(a.runner.(*codeRunner), core.RoleReviewer)
					now = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Finished() })
					if now.Status != core.TaskLanded || now.Delivering != nil || len(gh.merges) != 1 || roleCalls(a.runner.(*codeRunner), core.RoleReviewer) != before {
						t.Fatal("PR intent not reconciled", now)
					}
					snap, _ := a.Core.Snapshot(context.Background())
					project, _ := findProject(snap, p.ID)
					expected := 0
					if choice == choiceAcceptFollowUp {
						expected = 1
					}
					if project.Landed.Commit != "actual-squash-commit" || len(followUps(t, a, task.ID)) != expected {
						t.Fatal("recovery lost target commit or duplicated follow-up", project.Landed)
					}
					return
				}
				if strings.HasPrefix(mode, "direction-") || strings.HasPrefix(mode, "changes-") {
					editAcceptedRequirements(t, a, p.ID, task.ID, strings.HasSuffix(mode, "brief"))
					if strings.HasPrefix(mode, "direction-") {
						d, err = a.Core.AnswerDecision(context.Background(), d.ID, "Use the new requirements", core.FromOwner)
					} else {
						d, err = a.Core.ChooseDecision(context.Background(), d.ID, choiceChanges, core.FromOwner)
					}
					if err != nil {
						t.Fatal(err)
					}
					if _, err = a.Core.Schedule(context.Background(), func(core.Role) string { return "test hold" }); err != nil {
						t.Fatal(err)
					}
					current := taskByID(t, a, task.ID)
					if current.DecisionID != d.ID {
						t.Fatal("owner answer discarded")
					}
					if err = a.applyAnswer(context.Background(), current, d); err != nil {
						t.Fatal(err)
					}
					current = taskByID(t, a, task.ID)
					if current.Status != core.TaskWriting || (strings.HasPrefix(mode, "direction-") && !slices.Contains(current.Direction, "Use the new requirements")) {
						t.Fatal("owner direction lost", current)
					}
					return
				}

				if mode == "conflict" {
					ownerCommits(t, source, "feature.go", "package main\nfunc Feature() { panic(\"owner\") }\n", "conflicting owner work")
					ownerGit(t, source, "push", "-q", remote, "main")
					if err := a.supersedeStaleApprovals(context.Background(), p.ID); err != nil {
						t.Fatal(err)
					}
					current := taskByID(t, a, task.ID)
					failure := openDecision(t, a, current)
					if failure.Kind != core.DecisionFailure || !strings.Contains(failure.Context, "conflicts") || current.Acceptance != nil {
						t.Fatal("lost conflict-specific continuation", failure)
					}
					expected := 0
					if choice == choiceAcceptFollowUp {
						expected = 1
					}
					if len(followUps(t, a, task.ID)) != expected {
						t.Fatal("duplicate follow-up")
					}
					return
				}
				if mode != "land" {
					editAcceptedRequirements(t, a, p.ID, task.ID, strings.HasPrefix(mode, "brief"))
					if strings.HasSuffix(mode, "answer") {
						d, err = a.Core.ChooseDecision(context.Background(), d.ID, choiceApprove, core.FromOwner)
						if err != nil {
							t.Fatal(err)
						}
						if err := a.applyAnswer(context.Background(), now, d); err != nil {
							t.Fatal(err)
						}
					} else {
						if _, err := a.Core.Schedule(context.Background(), func(core.Role) string { return "test hold" }); err != nil {
							t.Fatal(err)
						}
					}
					current := taskByID(t, a, task.ID)
					if current.Status != core.TaskReviewing || current.DecisionID != "" || current.Acceptance != nil || current.Proposal.MergeApproved != 0 || current.Delivering != nil || len(gh.merges) != 0 {
						t.Fatal("stale approval authorized delivery", current)
					}
					return
				}
				if _, err = a.Core.ChooseDecision(context.Background(), d.ID, choiceApprove, core.FromOwner); err != nil {
					t.Fatal(err)
				}
				now = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Finished() })
				if now.Status != core.TaskLanded || len(gh.merges) != 1 {
					t.Fatalf("accepted PR did not land once: %+v %v", now, gh.merges)
				}
				expected := 0
				if choice == choiceAcceptFollowUp {
					expected = 1
				}
				if len(followUps(t, a, task.ID)) != expected {
					t.Fatal("wrong follow-up count")
				}

			})
		}
	}
}

func TestAcceptedQAFailurePreservesQuestion(t *testing.T) {
	t.Parallel()
	var reopen func(*Loop) *Loop
	a, _, p, task, _, source := acceptedCode(t, choiceAcceptDraft, true, &reopen)
	ownerCommits(t, source, "owner.go", "package main\n", "owner work")
	task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) == 2 })
	task, err := a.Core.UpdateTask(context.Background(), task.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.Status = core.TaskDeciding
		t.MergeValidation.Checked = true
		for _, r := range t.Checkers() {
			if r.Holds(core.RoleQA) {
				t.Verdicts = append(t.Verdicts, core.Verdict{Role: r.Name, Revision: 2, BriefVersion: p.Brief.Version, TextVersion: t.TextVersion, Outcome: core.VerdictQuestion, Summary: strings.Repeat("Long summary ", 500), Findings: []core.Finding{{Note: strings.Repeat("Long finding ", 500)}}, Question: "Which locale must the merged result support?"})
			}
		}
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.decide(context.Background(), p, task); err != nil {
		t.Fatal(err)
	}
	d := openDecision(t, a, taskByID(t, a, task.ID))
	if !strings.Contains(d.Context, "Which locale") {
		t.Fatalf("lost specific blocker: %+v", d)
	}
}

func TestAcceptedValidationRetryFencesOldQATurns(t *testing.T) {
	t.Parallel()
	for _, late := range []string{"pass", "failure"} {
		t.Run(late, func(t *testing.T) {
			var reopen func(*Loop) *Loop
			a, _, _, task, _, source := acceptedCode(t, choiceAcceptFollowUp, true, &reopen)
			ownerCommits(t, source, "owner.go", "package main\n", "owner work")
			task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) == 2 })
			task, err := a.Core.UpdateTask(context.Background(), task.ID, func(t *core.Task, _ *core.Project) (string, error) {
				t.MergeValidation.Checked = true
				qa := core.Role{Name: "Second QA", Kinds: []string{core.RoleQA}}
				t.Roles = append(t.Roles, qa)
				t.Claims = []core.Claim{
					{Token: "old-qa", Step: core.TaskReviewing, Revision: 2, Shared: true, Group: "QA"},
					{Token: "failed-qa", Step: core.TaskReviewing, Revision: 2, Shared: true, Group: "Second QA"},
				}
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if err = a.acceptedMergeFailure(core.Fenced(context.Background(), task.ID, "failed-qa"), task, "other QA failed"); err != nil {
				t.Fatal(err)
			}
			if err = a.Core.ReleaseClaim(context.Background(), task.ID, "failed-qa"); err != nil {
				t.Fatal(err)
			}
			task = taskByID(t, a, task.ID)
			d := openDecision(t, a, task)
			d, err = a.Core.ChooseDecision(context.Background(), d.ID, choiceTryAgain, core.FromOwner)
			if err != nil {
				t.Fatal(err)
			}
			if err = a.applyAnswer(context.Background(), task, d); err != nil {
				t.Fatal(err)
			}
			ctx := core.Fenced(context.Background(), task.ID, "old-qa")
			if late == "failure" {
				err = a.acceptedMergeFailure(ctx, task, "late old error")
			} else {
				_, err = a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
					t.Verdicts = append(t.Verdicts, core.Verdict{Revision: 2, Outcome: core.VerdictPass})
					return "", nil
				})
			}
			if !errors.Is(err, core.ErrStale) {
				t.Fatalf("old result admitted: %v", err)
			}
			now := taskByID(t, a, task.ID)
			if now.Status != core.TaskReviewing || !now.Claims[0].Revoked || now.MergeValidation.Checked {
				t.Fatalf("retry not fenced: %+v", now)
			}
		})
	}
}

func TestCommandRecoveryHoldsAcceptedValidationWorkspace(t *testing.T) {
	t.Parallel()
	var reopen func(*Loop) *Loop
	lp, _, p, task, _, source := acceptedCode(t, choiceAcceptDraft, false, &reopen)
	ownerCommits(t, source, "owner.go", "package main\n", "owner work")
	task = stepUntil(t, lp, task.ID, func(t core.Task) bool { return len(t.Revisions) == 2 })
	claims, err := lp.Core.Schedule(context.Background(), func(core.Role) string { return "" })
	if err != nil || len(claims) != 1 || claims[0].Claim.Step != core.StepValidateMerge {
		t.Fatalf("no validation claim: %v %v", claims, err)
	}
	m, err := lp.mediumFor(context.Background(), p, taskPlaybook(p, task))
	if err != nil {
		t.Fatal(err)
	}
	copy, err := m.check(context.Background(), task, task.Revisions[1], false, false)
	if err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(lp.Core.StateDirectory(), "commands", "commands", "interrupted")
	if err = os.MkdirAll(stale, 0700); err != nil {
		t.Fatal(err)
	}
	lp.commands = func(context.Context, sandbox.Options) (commandSandbox, error) {
		return &fakeCommands{closeErr: errors.New("cleanup unavailable")}, nil
	}
	if err = lp.resume(context.Background()); !errors.Is(err, errCommandRecovery) {
		t.Fatalf("recovery advanced: %v", err)
	}
	if _, err = os.Stat(copy.checkDir); err != nil {
		t.Fatalf("workspace removed before cleanup: %v", err)
	}
	now := taskByID(t, lp, task.ID)
	if len(now.Claims) != 1 || now.Claims[0].Token != claims[0].Claim.Token {
		t.Fatal("claim lost before cleanup")
	}
	lp.commands = func(context.Context, sandbox.Options) (commandSandbox, error) { return &fakeCommands{}, nil }
	if err = lp.resume(context.Background()); !errors.Is(err, errCommandRecovery) {
		t.Fatalf("successful Close advanced with unreclaimed state: %v", err)
	}
	if _, err = os.Stat(copy.checkDir); err != nil || len(taskByID(t, lp, task.ID).Claims) != 1 {
		t.Fatal("silent reclamation failure lost its hold", err)
	}
	lp.commands = func(context.Context, sandbox.Options) (commandSandbox, error) {
		return &fakeCommands{onClose: func() { os.RemoveAll(stale) }}, nil
	}
	if err = lp.resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(taskByID(t, lp, task.ID).Claims) != 0 {
		t.Fatal("confirmed recovery retained claim")
	}
	if _, err = os.Stat(copy.checkDir); !os.IsNotExist(err) {
		t.Fatalf("confirmed recovery retained workspace: %v", err)
	}
}

func TestAcceptedCommandCloseFailureHoldsImmediateOwnerAnswers(t *testing.T) {
	t.Parallel()
	for _, answer := range []string{choiceTryAgain, "Fix the check", choiceStop} {
		t.Run(answer, func(t *testing.T) {
			t.Parallel()
			var reopen func(*Loop) *Loop
			a, _, _, task, _, source := acceptedCode(t, choiceAcceptFollowUp, false, &reopen)
			ownerCommits(t, source, "owner.go", "package main\n", "owner work")
			task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) == 2 })
			stale := filepath.Join(a.Core.StateDirectory(), "commands", "commands", "unconfirmed")
			var workspace string
			a.commands = func(_ context.Context, o sandbox.Options) (commandSandbox, error) {
				if workspace == "" {
					workspace = o.WorkDir
				}
				if err := os.MkdirAll(stale, 0700); err != nil {
					t.Fatal(err)
				}
				return &fakeCommands{closeErr: errors.New("cannot confirm cleanup")}, nil
			}
			task = stepUntil(t, a, task.ID, waiting)
			d := openDecision(t, a, task)
			if !strings.Contains(d.Context, "cleanup is unconfirmed") || len(task.Claims) != 1 || task.Claims[0].Held != errCommandRecovery.Error() {
				t.Fatal("cleanup failure lost hold", task, d)
			}
			if answer == choiceTryAgain || answer == choiceStop {
				d, _ = a.Core.ChooseDecision(context.Background(), d.ID, answer, core.FromOwner)
			} else {
				d, _ = a.Core.AnswerDecision(context.Background(), d.ID, answer, core.FromOwner)
			}
			if _, err := a.loopStep(context.Background(), false); err != nil {
				t.Fatal(err)
			}
			if current := taskByID(t, a, task.ID); (answer != choiceStop && current.DecisionID != "") || (answer == choiceStop && current.Status != core.TaskStopped) {
				t.Fatal("cleanup blocked owner answer", current)
			}
			otherProject, err := a.Core.CreateProject(context.Background(), core.ProjectInput{Title: "Independent work", Template: "draft", Brief: core.BriefInput{Goal: "Continue independently"}})
			if err != nil {
				t.Fatal(err)
			}
			other, err := a.Core.QueueTask(context.Background(), otherProject.ID, core.TaskInput{Objective: "Write a note"})
			if err != nil {
				t.Fatal(err)
			}
			a.commands = func(context.Context, sandbox.Options) (commandSandbox, error) { return &fakeCommands{}, nil }
			for i := 0; i < 5 && taskByID(t, a, other.ID).Status == core.TaskQueued; i++ {
				if _, err := a.loopStep(context.Background(), false); err != nil {
					t.Fatal("cleanup blocked independent work", err)
				}
			}
			if current := taskByID(t, a, other.ID); current.Status == core.TaskQueued {
				t.Fatal("unrelated project blocked", current)
			}
			if _, err := os.Stat(workspace); err != nil {
				t.Fatal("command workspace lost", err)
			}
			claims, err := a.Core.Schedule(context.Background(), func(core.Role) string { return "" })
			if err != nil || slices.ContainsFunc(claims, func(s core.Scheduled) bool { return s.Task.ID == task.ID }) {
				t.Fatal("replacement admitted", claims, err)
			}
			a.commands = func(context.Context, sandbox.Options) (commandSandbox, error) {
				return &fakeCommands{onClose: func() { os.RemoveAll(stale) }}, nil
			}
			if _, err := a.loopStep(context.Background(), false); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(workspace); !os.IsNotExist(err) {
				t.Fatal("confirmed reclamation retained the command workspace", err)
			}
			if slices.ContainsFunc(taskByID(t, a, task.ID).Claims, func(c core.Claim) bool { return c.Held == errCommandRecovery.Error() }) {
				t.Fatal("confirmed cleanup kept hold")
			}
			if answer == choiceStop && taskByID(t, a, task.ID).Status != core.TaskStopped {
				t.Fatal("cleanup restarted stopped task")
			}
		})
	}
}

func TestAcceptedBranchCatchUpCanLandLaterWithoutReviews(t *testing.T) {
	t.Parallel()
	for _, choice := range []string{choiceAcceptDraft, choiceAcceptFollowUp} {
		t.Run(choice, func(t *testing.T) {
			var reopen func(*Loop) *Loop
			a, runner, p, task, _, source := acceptedCode(t, choice, false, &reopen)
			ownerCommits(t, source, "owner.go", "package main\n", "owner work")
			task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) == 2 })
			a.commands = func(context.Context, sandbox.Options) (commandSandbox, error) { return &fakeCommands{}, nil }
			task = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Status == core.TaskLanding })
			if _, err := a.Core.UpdateTask(context.Background(), task.ID, func(t *core.Task, _ *core.Project) (string, error) {
				t.Playbook.Land = core.LandPolicy{Via: core.LandBranch}
				return "", nil
			}); err != nil {
				t.Fatal(err)
			}
			task = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Status == core.TaskDelivered })
			before := roleCalls(runner, core.RoleReviewer)
			if _, err := a.LandTask(context.Background(), p.ID, task.ID); err != nil {
				t.Fatal(err)
			}
			task = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Finished() })
			expected := 0
			if choice == choiceAcceptFollowUp {
				expected = 1
			}
			if task.Status != core.TaskLanded || roleCalls(runner, core.RoleReviewer) != before || len(followUps(t, a, task.ID)) != expected {
				t.Fatal("deferred landing lost acceptance", task)
			}
		})
	}
}

func TestStaleAcceptanceRetiresContinuationAfterRestart(t *testing.T) {
	t.Parallel()
	for _, choice := range []string{choiceAcceptDraft, choiceAcceptFollowUp} {
		for _, change := range []string{"brief", "text"} {
			t.Run(choice+"/"+change, func(t *testing.T) {
				var reopen func(*Loop) *Loop
				a, _, p, task, _, _ := acceptedCode(t, choice, false, &reopen, true)
				_, err := a.Core.UpdateTask(context.Background(), task.ID, func(t *core.Task, p *core.Project) (string, error) {
					if change == "brief" {
						p.Brief.Version++
					} else {
						t.TextVersion++
					}
					return "", nil
				})
				if err != nil {
					t.Fatal(err)
				}
				other, err := a.Core.QueueTask(context.Background(), p.ID, core.TaskInput{Objective: "Another runnable task"})
				if err != nil {
					t.Fatal(err)
				}
				_, err = a.Core.UpdateTask(context.Background(), other.ID, func(t *core.Task, _ *core.Project) (string, error) {
					t.Status = core.TaskWriting
					t.Playbook = task.Playbook
					t.Roles = task.Roles
					return "", nil
				})
				if err != nil {
					t.Fatal(err)
				}
				a = reopen(a)
				if _, _, err = a.pass(context.Background(), false); err != nil {
					t.Fatal(err)
				}
				now := taskByID(t, a, task.ID)
				if now.DecisionID != "" || now.Status != core.TaskReviewing || now.Acceptance != nil || len(followUps(t, a, task.ID)) != 0 {
					t.Fatalf("stale answer not retired: %+v", now)
				}
				claims, err := a.Core.Schedule(context.Background(), func(core.Role) string { return "" })
				if err != nil {
					t.Fatal(err)
				}
				if !slices.ContainsFunc(claims, func(c core.Scheduled) bool { return c.Task.ID == other.ID }) {
					t.Fatalf("other work blocked: %+v", claims)
				}
			})
		}
	}
}

func editAcceptedRequirements(t *testing.T, a *Loop, projectID, taskID string, brief bool) {
	t.Helper()
	var err error
	if brief {
		_, err = a.Core.UpdateBrief(context.Background(), projectID, core.BriefInput{Goal: "Updated requirements", Criteria: []string{"Check the updated requirements"}})
	} else {
		_, err = a.Core.EditTask(context.Background(), core.EditInput{Project: projectID, Task: taskID, By: core.FromOwner, Kind: core.RolePM, Add: []string{"Check the updated requirements"}})
	}
	if err != nil {
		t.Fatal(err)
	}
}

type editingApprovalMedium struct {
	medium
	edit func()
}

func (m editingApprovalMedium) deliveryNote(t core.Task) string {
	note := m.medium.deliveryNote(t)
	m.edit()
	return note
}
