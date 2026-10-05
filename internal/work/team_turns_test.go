package work

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/roles"
	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/session"
)

type accountingRunner func(context.Context, roles.Spec) (roles.Result, error)

func TestAskPMHoldsClaimOnAccountingOnlyFailure(t *testing.T) {
	lp, p, _, _ := pmTeam(t, &scriptedRunner{})
	ctx := context.Background()
	calls, writes := 0, 0
	reported := session.Result{Status: "completed", Usage: session.Usage{Usage: harness.Usage{Known: true, Input: 22, Output: 5}, Final: true}}
	lp.runner = accountingRunner(func(ctx context.Context, spec roles.Spec) (roles.Result, error) {
		calls++
		if err := spec.Opening(session.Opened{}, session.Ref{ID: "pm-thread"}); err != nil {
			return roles.Result{}, err
		}
		if err := spec.Accepted(); err != nil {
			return roles.Result{}, err
		}
		return roles.Result{Text: "The first request comes first.", Provider: reported, CleanupConfirmed: true}, nil
	})
	lp.finishTeamTurn = func(context.Context, string, core.TeamTurnTerminal) error {
		writes++
		return errors.New("accounting unavailable")
	}
	_, err := lp.AskPM(ctx, p.ID, "Which request is first?")
	var failure *teamAccountingFailure
	if !errors.As(err, &failure) || failure.result.Text == "" || !reflect.DeepEqual(failure.result.Provider, reported) {
		t.Fatalf("lost successful result: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := lp.AskPM(ctx, p.ID, "Ask again"); !errors.Is(err, core.ErrConflict) {
			t.Fatalf("held AskPM launched: %v", err)
		}
		if _, _, ok, err := lp.Core.ClaimPM(ctx, p.ID, func(core.Role) string { return "" }); err != nil || ok {
			t.Fatalf("scheduled PM reused held claim: %v %v", ok, err)
		}
	}
	snap, err := lp.Core.Snapshot(ctx)
	project, _ := findProject(snap, p.ID)
	if err != nil || calls != 1 || writes != 3 || len(project.Claims) != 1 || project.Claims[0].Held == "" {
		t.Fatalf("claim %+v calls=%d writes=%d err=%v", project.Claims, calls, writes, err)
	}
	turns, err := lp.Core.TeamTurns(ctx, core.TeamTurnFilter{ProjectID: p.ID})
	if err != nil || len(turns) != 1 || turns[0].Terminal != nil || turns[0].AcceptedAt == nil || turns[0].TaskID != "" || turns[0].ClaimToken != project.Claims[0].Token {
		t.Fatalf("attempt %+v err=%v", turns, err)
	}
}

func TestResumeHoldsFinalizedUntrackedTurnsWithoutChangingAccounting(t *testing.T) {
	for _, engine := range []string{"claude", "openai-compatible"} {
		for _, projectOnly := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/project=%v", engine, projectOnly), func(t *testing.T) {
				lp, p, task, _ := pmTeam(t, &scriptedRunner{})
				ctx := context.Background()
				var claim core.Claim
				var err error
				spec := roles.Spec{ProjectID: p.ID, TaskID: task.ID, Role: core.RolePM, Seat: "PM", Engine: engine}
				if projectOnly {
					claim, _, _, err = lp.Core.ClaimPM(ctx, p.ID, func(core.Role) string { return "" })
					spec.TaskID, spec.Role = "", core.RoleQA
					ctx = core.FencedProject(ctx, p.ID, claim.Token)
				} else {
					claim, err = lp.Core.ClaimTask(ctx, task.ID, core.TaskWriting)
					ctx = core.Fenced(ctx, task.ID, claim.Token)
				}
				if err != nil {
					t.Fatal(err)
				}
				reported := session.Result{Status: "completed", TurnID: "provider-turn", Usage: session.Usage{Usage: harness.Usage{Known: true, CacheKnown: true, Input: 33, CacheRead: 11, Output: 5}, Final: true}}
				lp.runner = accountingRunner(func(ctx context.Context, spec roles.Spec) (roles.Result, error) {
					if err := spec.Opening(session.Opened{}, session.Ref{ID: "thread"}); err != nil {
						return roles.Result{}, err
					}
					if err := spec.Accepted(); err != nil {
						return roles.Result{}, err
					}
					return roles.Result{Provider: reported, FailureStage: "release"}, errors.New("cleanup unconfirmed")
				})
				if _, err := lp.runRole(context.WithValue(ctx, slotKey{}, engine), spec); err == nil {
					t.Fatal("expected release error")
				}
				before, err := lp.Core.TeamTurns(context.Background(), core.TeamTurnFilter{ClaimToken: claim.Token})
				if err != nil || len(before) != 1 || before[0].Terminal == nil || before[0].Terminal.CleanupConfirmed {
					t.Fatalf("turn %+v: %v", before, err)
				}
				// Simulate restart after accounting but before claim release. Missing
				// launch markers cannot prove cleanup of no-tools CLI or API turns.
				again := New(lp.Core, lp.Config, false)
				for i := 0; i < 2; i++ {
					if err := again.resume(context.Background()); err != nil {
						t.Fatal(err)
					}
				}
				after, err := again.Core.TeamTurns(context.Background(), core.TeamTurnFilter{ClaimToken: claim.Token})
				if err != nil || len(after) != 1 || !after[0].Held || !reflect.DeepEqual(before[0].Terminal, after[0].Terminal) || after[0].CleanupConfirmedAt != nil {
					t.Fatalf("accounting changed: %+v %v", after, err)
				}
				snap, _ := again.Core.Snapshot(context.Background())
				claims := taskByID(t, again, task.ID).Claims
				if projectOnly {
					project, _ := findProject(snap, p.ID)
					claims = project.Claims
				}
				if len(claims) != 1 || claims[0].Held == "" {
					t.Fatalf("uncertain claim released: %+v", claims)
				}
				if projectOnly {
					if _, _, ok, err := again.Core.ClaimPMQuestion(context.Background(), p.ID, func(core.Role) string { return "" }); err != nil || ok {
						t.Fatalf("held project claim reused: %v %v", ok, err)
					}
				} else if _, err := again.Core.ClaimTask(context.Background(), task.ID, core.TaskWriting); !errors.Is(err, core.ErrConflict) {
					t.Fatalf("held task claim reused: %v", err)
				}
			})
		}
	}
}

func TestResumeRetainsConfirmedReclamationAlongsideError(t *testing.T) {
	for _, marker := range []bool{false, true} {
		t.Run(fmt.Sprint(marker), func(t *testing.T) {
			lp, p, task := loopApp(t, &scriptedRunner{}, "")
			ctx := context.Background()
			claim, err := lp.Core.ClaimTask(ctx, task.ID, core.TaskWriting)
			if err != nil {
				t.Fatal(err)
			}
			dir := lp.launchDir(claim.Token)
			if marker {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			turn := core.TeamTurn{ID: "finalized", ProjectID: p.ID, TaskID: task.ID, ClaimToken: claim.Token, LaunchDir: dir, Role: core.RoleImplementer, Seat: "Writer", Engine: "claude", AdmittedAt: time.Now()}
			if err := lp.Core.AdmitTeamTurn(ctx, turn); err != nil {
				t.Fatal(err)
			}
			terminal := core.TeamTurnTerminal{At: time.Now().UTC().Round(0), Outcome: "failed", FailureStage: "release", ProviderStatus: "completed", Usage: session.Usage{Usage: harness.Usage{Known: true, Input: 33}, Final: true}}
			if err := lp.Core.FinishTeamTurn(ctx, turn.ID, terminal); err != nil {
				t.Fatal(err)
			}
			again := New(lp.Core, lp.Config, false)
			again.reclaim = func(context.Context, string) (session.Reclamation, error) {
				return session.Reclamation{Confirmed: true}, errors.New("synthetic workspace cleanup error")
			}
			for i := 0; i < 2; i++ {
				if err := again.resume(ctx); err != nil {
					t.Fatal(err)
				}
			}
			got, err := again.Core.TeamTurns(ctx, core.TeamTurnFilter{ClaimToken: claim.Token})
			if err != nil || len(got) != 1 || got[0].Held || got[0].CleanupConfirmedAt == nil || !reflect.DeepEqual(*got[0].Terminal, terminal) {
				t.Fatalf("reclamation %+v: %v", got, err)
			}
			if task := taskByID(t, again, task.ID); len(task.Claims) != 0 {
				t.Fatalf("confirmed cleanup held claim: %+v", task.Claims)
			}
		})
	}
}

func (r accountingRunner) Run(ctx context.Context, spec roles.Spec) (roles.Result, error) {
	return r(ctx, spec)
}

func TestTeamAttemptAdmissionOpeningErrorsAndUsage(t *testing.T) {
	lp, p, task := loopApp(t, &scriptedRunner{}, "")
	ctx := context.Background()
	for _, kind := range []string{core.RoleResearcher, core.RoleDesigner, core.RoleImplementer, core.RoleReviewer, core.RoleQA, core.RolePM} {
		for i, usage := range []session.Usage{{}, {Usage: harness.Usage{Known: true}, Final: true}, {Usage: harness.Usage{Known: true, CacheKnown: true, Input: 30, CacheRead: 10, CacheWrite: 5, Output: 4}, Final: true}} {
			spec := roles.Spec{ProjectID: p.ID, TaskID: task.ID, Role: kind, Seat: "seat", MemberID: "member", Engine: "claude", FreshReason: core.FreshNoThread}
			if kind == core.RolePM {
				spec.TaskID = ""
			}
			lp.runner = accountingRunner(func(ctx context.Context, spec roles.Spec) (roles.Result, error) {
				before, err := lp.Core.TeamTurns(ctx, core.TeamTurnFilter{Incomplete: true})
				if err != nil || len(before) != 1 {
					t.Fatalf("runner launched before admission: %+v %v", before, err)
				}
				opening := session.Opened{Fresh: session.FreshUnavailable}
				if err := spec.Opening(opening, session.Ref{ID: "provider-thread"}); err != nil {
					return roles.Result{}, err
				}
				if err := spec.Accepted(); err != nil {
					return roles.Result{}, err
				}
				return roles.Result{Opening: &opening, Provider: session.Result{Status: "failed", Usage: usage, Observed: session.Usage{Usage: harness.Usage{Known: true, Input: 999}}}}, errors.New("provider failed")
			})
			result, err := lp.runAttempt(ctx, spec)
			if err == nil {
				t.Fatal("lost provider error")
			}
			turns, err := lp.Core.TeamTurns(ctx, core.TeamTurnFilter{})
			if err != nil {
				t.Fatal(err)
			}
			got := turns[len(turns)-1]
			if got.ID != result.AttemptID || got.TaskID != spec.TaskID || got.Role != kind || got.MemberID != "member" || got.Model != "" || got.AcceptedAt == nil || got.Opening.FreshReason != core.FreshHarnessUnavailable || got.Terminal.Usage != usage || got.Terminal.Observed.Input != 999 {
				t.Fatalf("attempt %d: %+v", i, got)
			}
		}
	}
}

func TestCorrectionAndBrowserFallbackAreDistinctAttempts(t *testing.T) {
	lp, p, task := loopApp(t, &scriptedRunner{}, "")
	ctx := context.WithValue(context.Background(), slotKey{}, "claude")
	calls := 0
	lp.runner = accountingRunner(func(ctx context.Context, spec roles.Spec) (roles.Result, error) {
		calls++
		if spec.Browser {
			return roles.Result{}, &session.CapabilityError{Code: session.CapabilityBrowserToolsMissing}
		}
		if err := spec.Opening(session.Opened{}, session.Ref{ID: "fresh"}); err != nil {
			return roles.Result{}, err
		}
		if err := spec.Accepted(); err != nil {
			return roles.Result{}, err
		}
		return roles.Result{Text: "malformed", Provider: session.Result{Status: "completed"}}, nil
	})
	spec := roles.Spec{ProjectID: p.ID, TaskID: task.ID, Role: core.RoleReviewer, Seat: "reviewer", Engine: "claude", Browser: true}
	_, _, _, runErr := lp.askForJSON(ctx, spec, func(string) error { return errors.New("bad JSON") })
	if runErr != nil || calls != 4 {
		t.Fatalf("calls %d: %v", calls, runErr)
	}
	turns, err := lp.Core.TeamTurns(context.Background(), core.TeamTurnFilter{TaskID: task.ID})
	if err != nil || len(turns) != 4 {
		t.Fatalf("%+v %v", turns, err)
	}
	for i, got := range turns {
		if got.Terminal == nil || (i > 0 && got.PreviousID != turns[i-1].ID) {
			t.Fatalf("retry attribution: %+v", turns)
		}
	}
	if turns[1].RetryCause != "browser_unavailable" || turns[2].RetryCause != "malformed_reply" || turns[3].RetryCause != "browser_unavailable" {
		t.Fatal(turns)
	}
}

func TestAccountingFailureNeverReplaysInference(t *testing.T) {
	st, err := core.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	lp := testLoop(t)
	lp.Core = core.NewService(st, config.Default())
	calls := 0
	lp.runner = accountingRunner(func(context.Context, roles.Spec) (roles.Result, error) {
		calls++
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		return roles.Result{}, &session.CapabilityError{Code: session.CapabilityBrowserToolsMissing}
	})
	spec := roles.Spec{ProjectID: "project", Role: core.RolePM, Seat: "PM", Engine: "claude", Browser: true}
	ctx := context.WithValue(context.Background(), slotKey{}, "claude")
	if _, err := lp.runRole(ctx, spec); err == nil || calls != 1 {
		t.Fatalf("terminal failure replayed inference: %d %v", calls, err)
	}
	if _, err := lp.runAttempt(ctx, spec); err == nil || calls != 1 {
		t.Fatalf("admission failure launched inference: %d %v", calls, err)
	}
}

func TestAccountingRecoveryKeepsUncertainAndCompletedAttempts(t *testing.T) {
	lp, p, task := loopApp(t, &scriptedRunner{}, "")
	ctx := context.Background()
	for _, id := range []string{"done", "old", "held"} {
		turn := core.TeamTurn{ID: id, ProjectID: p.ID, TaskID: task.ID, Role: core.RoleQA, Seat: "QA", Engine: "claude", ClaimToken: id, LaunchDir: lp.launchDir(id), AdmittedAt: time.Now()}
		if err := lp.Core.AdmitTeamTurn(ctx, turn); err != nil {
			t.Fatal(err)
		}
		if id == "done" {
			if err := lp.Core.FinishTeamTurn(ctx, id, core.TeamTurnTerminal{At: time.Now(), Outcome: "completed"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	lp.reclaim = func(ctx context.Context, dir string) (session.Reclamation, error) {
		if dir == lp.launchDir("held") {
			return session.Reclamation{}, session.ErrUncertainLaunch
		}
		return session.Reclamation{Confirmed: true}, nil
	}
	for i := 0; i < 2; i++ {
		held := map[string]string{}
		if !lp.recoverTeamTurns(ctx, held, nil) || held["held"] == "" {
			t.Fatal("uncertain launch not held")
		}
	}
	turns, err := lp.Core.TeamTurns(ctx, core.TeamTurnFilter{TaskID: task.ID})
	if err != nil || len(turns) != 3 {
		t.Fatalf("%+v %v", turns, err)
	}
	if turns[0].Terminal.Outcome != "completed" || turns[1].Terminal.Outcome != "interrupted" || turns[1].Terminal.Usage.Known || !turns[2].Held || turns[2].Terminal != nil {
		t.Fatal(turns)
	}
}

func TestWriterFreshReasonsMatchExistingThreadSelection(t *testing.T) {
	r := core.Role{Name: "writer", Member: "member", Engine: "claude", Model: "model"}
	for _, reason := range []string{core.FreshNoThread, core.FreshEngineChanged, core.FreshModelChanged, core.FreshOwnerRequested, "compatible"} {
		task := core.Task{}
		if reason != core.FreshNoThread {
			task.KeepThread(core.RoleImplementer, r, []byte(`{"id":"thread"}`))
		}
		switch reason {
		case core.FreshEngineChanged:
			task.Threads[0].Engine = "codex"
		case core.FreshModelChanged:
			task.Threads[0].Model = "other"
		case core.FreshOwnerRequested:
			task.WriterNext = core.WriterFresh
		}
		want := reason
		if reason == "compatible" {
			want = core.FreshNoThread
		}
		if got := writerFreshReason(task, r); got != want {
			t.Fatalf("%s: %s", reason, got)
		}
		if got := task.Resumable(core.RoleImplementer, r); (len(got) > 0) != (reason == "compatible" || reason == core.FreshOwnerRequested) {
			t.Fatalf("changed resumption behavior for %s", reason)
		}
	}
}

func TestCancelledRevokedTurnStillFinalizesAndPreAdmissionCancellationLaunchesNothing(t *testing.T) {
	lp, p, task := loopApp(t, &scriptedRunner{}, "")
	ctx, cancel := context.WithCancel(core.Fenced(context.Background(), task.ID, "revoked"))
	defer cancel()
	calls := 0
	lp.runner = accountingRunner(func(ctx context.Context, spec roles.Spec) (roles.Result, error) {
		calls++
		if err := spec.Opening(session.Opened{Resumed: true}, session.Ref{ID: "resumed"}); err != nil {
			return roles.Result{}, err
		}
		if err := spec.Accepted(); err != nil {
			return roles.Result{}, err
		}
		cancel()
		return roles.Result{Provider: session.Result{Status: "interrupted", Usage: session.Usage{Usage: harness.Usage{Known: true, Input: 10}, Final: true}}}, context.Canceled
	})
	spec := roles.Spec{ProjectID: p.ID, TaskID: task.ID, Role: core.RoleImplementer, Seat: "writer", Engine: "claude"}
	if _, err := lp.runAttempt(ctx, spec); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := lp.runAttempt(ctx, spec); err == nil || calls != 1 {
		t.Fatalf("pre-admission cancellation launched work: %d %v", calls, err)
	}
	turns, err := lp.Core.TeamTurns(context.Background(), core.TeamTurnFilter{TaskID: task.ID})
	if err != nil || len(turns) != 1 || turns[0].Terminal == nil || turns[0].Terminal.Usage.Input != 10 || turns[0].Terminal.Outcome != "interrupted" || !turns[0].Opening.Resumed {
		t.Fatalf("cancellation accounting: %+v %v", turns, err)
	}
}

func TestOwnerFreshProductionCorrectionRecordsItsOwnResumedOpening(t *testing.T) {
	script := &scriptedRunner{writerReplies: []string{"```production\nmissing description\n```", "Finished the draft."}}
	lp, p, task := loopApp(t, script, "")
	ctx := context.WithValue(context.Background(), slotKey{}, "claude")
	var writer core.Role
	for _, r := range p.Playbook.Roles {
		if r.Holds(core.RoleImplementer) {
			writer = r
			break
		}
	}
	var err error
	task, err = lp.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.Roles = append([]core.Role(nil), p.Playbook.Roles...)
		t.Roles = append(t.Roles, core.Role{Name: "Designer", Kinds: []string{core.RoleDesigner}, Engine: "codex"})
		t.Status, t.Round, t.WriterNext = core.TaskWriting, 1, core.WriterFresh
		t.KeepThread(core.RoleImplementer, writer, []byte(`{"engine":"claude","id":"old"}`))
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	lp.runner = accountingRunner(func(ctx context.Context, spec roles.Spec) (roles.Result, error) {
		opening := session.Opened{Resumed: len(spec.Resume) > 0}
		if err := spec.Opening(opening, session.Ref{ID: "writer"}); err != nil {
			return roles.Result{}, err
		}
		if err := spec.Accepted(); err != nil {
			return roles.Result{}, err
		}
		result, err := script.Run(ctx, spec)
		result.Opening = &opening
		result.Provider = session.Result{Status: "completed"}
		return result, err
	})
	m, err := lp.mediumFor(ctx, p, p.Playbook)
	if err != nil {
		t.Fatal(err)
	}
	if err := lp.write(ctx, p, task, m, writer); err != nil {
		t.Fatal(err)
	}
	turns, err := lp.Core.TeamTurns(context.Background(), core.TeamTurnFilter{TaskID: task.ID})
	if err != nil || len(turns) != 2 {
		t.Fatalf("production retry: %+v %v", turns, err)
	}
	if turns[0].Opening.Resumed || turns[0].Opening.FreshReason != core.FreshOwnerRequested || !turns[1].Opening.Resumed || turns[1].Opening.FreshReason != "" || turns[1].PreviousID != turns[0].ID || turns[1].RetryCause != "malformed_production" {
		t.Fatalf("owner request incorrectly carried to correction: %+v", turns)
	}
}

func TestTransientTerminalWritesRetryAccountingOnly(t *testing.T) {
	lp, p, task := loopApp(t, &scriptedRunner{}, "")
	ctx := context.Background()
	calls, writes := 0, 0
	usage := session.Usage{Usage: harness.Usage{Known: true, CacheKnown: true, Input: 12, CacheRead: 3, Output: 4}, Final: true}
	lp.runner = accountingRunner(func(ctx context.Context, spec roles.Spec) (roles.Result, error) {
		calls++
		if err := spec.Opening(session.Opened{Resumed: true}, session.Ref{ID: "thread"}); err != nil {
			return roles.Result{}, err
		}
		if err := spec.Accepted(); err != nil {
			return roles.Result{}, err
		}
		return roles.Result{Text: "finished", Provider: session.Result{Status: "completed", Usage: usage}, CleanupConfirmed: true}, nil
	})
	lp.finishTeamTurn = func(ctx context.Context, id string, end core.TeamTurnTerminal) error {
		writes++
		if writes < 3 {
			return errors.New("synthetic accounting write unavailable")
		}
		return lp.Core.FinishTeamTurn(ctx, id, end)
	}
	result, err := lp.runAttempt(ctx, roles.Spec{ProjectID: p.ID, TaskID: task.ID, Role: core.RoleImplementer, Seat: "Writer", Engine: "claude"})
	if err != nil || calls != 1 || writes != 3 || result.Text != "finished" {
		t.Fatalf("calls %d writes %d result %+v: %v", calls, writes, result, err)
	}
	// Open another store as a restarted daemon would, without reconstructing a transcript.
	st, err := core.Open(filepath.Join(lp.Core.StateDirectory(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	restarted := core.NewService(st, lp.Config())
	turns, err := restarted.TeamTurns(ctx, core.TeamTurnFilter{TaskID: task.ID})
	if err != nil || len(turns) != 1 || turns[0].Terminal == nil || turns[0].Terminal.Usage != usage || turns[0].Opening == nil || !turns[0].Opening.Resumed || turns[0].AcceptedAt == nil {
		t.Fatalf("persisted accounting %+v: %v", turns, err)
	}
}

func TestSchedulerHoldsAccountingFailureWithoutReplayingSuccessfulWork(t *testing.T) {
	lp, _, task := loopApp(t, &scriptedRunner{}, "")
	ctx := context.Background()
	calls, writes := 0, 0
	usage := session.Usage{Usage: harness.Usage{Known: true, Input: 22, Output: 5}, Final: true}
	lp.runner = accountingRunner(func(ctx context.Context, spec roles.Spec) (roles.Result, error) {
		calls++
		if err := spec.Opening(session.Opened{}, session.Ref{ID: "thread"}); err != nil {
			return roles.Result{}, err
		}
		if err := spec.Accepted(); err != nil {
			return roles.Result{}, err
		}
		return roles.Result{Text: "finished", Provider: session.Result{Status: "completed", Usage: usage}, CleanupConfirmed: true}, nil
	})
	lp.finishTeamTurn = func(context.Context, string, core.TeamTurnTerminal) error {
		writes++
		return errors.New("accounting table unavailable")
	}
	step(t, lp)
	for i := 0; i < 3; i++ {
		step(t, lp)
	}
	held := taskByID(t, lp, task.ID)
	if calls != 1 || writes != 3 || len(held.Claims) != 1 || held.Claims[0].Held == "" || held.Failures != 0 || !held.RetryAt.IsZero() {
		t.Fatalf("calls %d writes %d task %+v", calls, writes, held)
	}
	turns, err := lp.Core.TeamTurns(ctx, core.TeamTurnFilter{TaskID: task.ID})
	if err != nil || len(turns) != 1 || turns[0].Terminal != nil || turns[0].AcceptedAt == nil {
		t.Fatalf("admission %+v: %v", turns, err)
	}
}

func TestSchedulerHoldsAccountingFailureEvenWhenCallerConsumesError(t *testing.T) {
	lp, p, _, _ := pmTeam(t, &scriptedRunner{})
	ctx := context.Background()
	claim, seat, ok, err := lp.Core.ClaimPM(ctx, p.ID, func(core.Role) string { return "" })
	if err != nil || !ok {
		t.Fatalf("claim %v: %v", ok, err)
	}
	calls := 0
	lp.runner = accountingRunner(func(context.Context, roles.Spec) (roles.Result, error) {
		calls++
		return roles.Result{Text: "finished"}, nil
	})
	lp.finishTeamTurn = func(context.Context, string, core.TeamTurnTerminal) error {
		return errors.New("accounting write unavailable")
	}
	state := &accountingFailureState{}
	done := lp.run(ctx, claimed{project: p.ID, token: claim.Token}, true, func(ctx context.Context) error {
		result, err := lp.runRole(core.FencedProject(ctx, p.ID, claim.Token), roles.Spec{ProjectID: p.ID, Role: core.RolePM, Seat: seat.Name, Engine: seat.Engine})
		if result.Text != "finished" || err == nil {
			t.Errorf("lost result %+v: %v", result, err)
		}
		state.failure = ctx.Value(accountingFailureKey{}).(*accountingFailureState).failure
		// Continuing the step after consuming the error must not launch
		// another role invocation either.
		again, againErr := lp.runRole(ctx, roles.Spec{ProjectID: p.ID, Role: core.RolePM, Seat: seat.Name, Engine: seat.Engine})
		if again.Text != result.Text || againErr != err {
			t.Errorf("continued step lost accounting failure: %+v %v", again, againErr)
		}
		return nil // PM and message paths can consume errors locally.
	}, func(ctx context.Context) error { return lp.Core.ReleaseProjectClaim(ctx, p.ID, claim.Token) })
	for panicValue := range done {
		t.Fatalf("scheduled step panicked: %v", panicValue)
	}
	if state.failure == nil || state.failure.result.Text != "finished" {
		t.Fatal("accounting failure lost successful provider result")
	}
	snap, err := lp.Core.Snapshot(ctx)
	project, _ := findProject(snap, p.ID)
	if err != nil || calls != 1 || len(project.Claims) != 1 || project.Claims[0].Held == "" {
		t.Fatalf("project %+v calls %d: %v", project, calls, err)
	}
}

func TestResumeReconcilesSharedLaunchBeforeClaimReuse(t *testing.T) {
	lp, p, task := loopApp(t, &scriptedRunner{}, "")
	ctx := context.Background()
	claim, err := lp.Core.ClaimTask(ctx, task.ID, core.TaskWriting)
	if err != nil {
		t.Fatal(err)
	}
	dir := lp.launchDir(claim.Token)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"completed", "crashed"} {
		turn := core.TeamTurn{ID: id, ProjectID: p.ID, TaskID: task.ID, ClaimToken: claim.Token, LaunchDir: dir, Role: core.RoleImplementer, Seat: "Writer", Engine: "claude", AdmittedAt: time.Now()}
		if id == "crashed" {
			turn.PreviousID, turn.RetryCause = "completed", "malformed_reply"
		}
		if err := lp.Core.AdmitTeamTurn(ctx, turn); err != nil {
			t.Fatal(err)
		}
		if id == "completed" {
			if err := lp.Core.FinishTeamTurn(ctx, id, core.TeamTurnTerminal{At: time.Now(), Outcome: "completed", Usage: session.Usage{Usage: harness.Usage{Known: true, Input: 33}, Final: true}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	again := New(lp.Core, lp.Config, false)
	confirmed := false
	again.reclaim = func(context.Context, string) (session.Reclamation, error) {
		return session.Reclamation{Confirmed: confirmed}, nil
	}
	for i := 0; i < 2; i++ {
		if err := again.resume(ctx); err != nil {
			t.Fatal(err)
		}
		held := taskByID(t, again, task.ID)
		if len(held.Claims) != 1 || held.Claims[0].Held == "" {
			t.Fatalf("uncertain claim released %+v", held.Claims)
		}
		if _, err := again.Core.ClaimTask(ctx, task.ID, core.TaskWriting); !errors.Is(err, core.ErrConflict) {
			t.Fatalf("held claim reused: %v", err)
		}
	}
	confirmed = true
	if err := again.resume(ctx); err != nil {
		t.Fatal(err)
	}
	if err := again.resume(ctx); err != nil {
		t.Fatal(err)
	}
	settled := taskByID(t, again, task.ID)
	if len(settled.Claims) != 0 {
		t.Fatalf("confirmed claim held %+v", settled.Claims)
	}
	turns, err := again.Core.TeamTurns(ctx, core.TeamTurnFilter{TaskID: task.ID})
	if err != nil || len(turns) != 2 || turns[0].Terminal.Outcome != "completed" || turns[0].Terminal.Usage.Input != 33 || turns[1].Terminal.Outcome != "interrupted" || turns[1].Terminal.Usage.Known {
		t.Fatalf("recovery %+v: %v", turns, err)
	}
	if _, err := again.Core.ClaimTask(ctx, task.ID, core.TaskWriting); err != nil {
		t.Fatalf("confirmed claim cannot be reused: %v", err)
	}
}

func TestResumeHoldsUntrackedProjectAndTaskTurnsWithMissingMarkers(t *testing.T) {
	for _, projectOnly := range []bool{false, true} {
		t.Run(fmt.Sprint(projectOnly), func(t *testing.T) {
			lp, p, task, _ := pmTeam(t, &scriptedRunner{})
			ctx := context.Background()
			var claim core.Claim
			var err error
			spec := roles.Spec{ProjectID: p.ID, TaskID: task.ID, Role: core.RolePM, Seat: "PM", Engine: "claude"}
			if projectOnly {
				claim, _, _, err = lp.Core.ClaimPM(ctx, p.ID, func(core.Role) string { return "" })
				spec.TaskID, spec.Role = "", core.RoleQA // release QA is project-only, also without tools
				ctx = core.FencedProject(ctx, p.ID, claim.Token)
			} else {
				claim, err = lp.Core.ClaimTask(ctx, task.ID, core.TaskWriting)
				ctx = core.Fenced(ctx, task.ID, claim.Token)
			}
			if err != nil {
				t.Fatal(err)
			}
			lp.runner = accountingRunner(func(context.Context, roles.Spec) (roles.Result, error) {
				panic("simulated crash before terminal persistence")
			})
			func() {
				defer func() {
					if recover() == nil {
						t.Error("expected simulated crash")
					}
				}()
				_, _ = lp.runRole(context.WithValue(ctx, slotKey{}, "claude"), spec)
			}()
			// The real harness considers a missing marker confirmed absence.
			// That must not settle an invocation which never had launch tracking.
			again := New(lp.Core, lp.Config, false)
			for i := 0; i < 2; i++ {
				if err := again.resume(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			turns, err := again.Core.TeamTurns(context.Background(), core.TeamTurnFilter{ClaimToken: claim.Token})
			if err != nil || len(turns) != 1 || !turns[0].UntrackedLaunch || !turns[0].Held || turns[0].Terminal != nil {
				t.Fatalf("untracked accounting %+v: %v", turns, err)
			}
			snap, _ := again.Core.Snapshot(context.Background())
			claims := taskByID(t, again, task.ID).Claims
			if projectOnly {
				project, _ := findProject(snap, p.ID)
				claims = project.Claims
			}
			if len(claims) != 1 || claims[0].Held == "" {
				t.Fatalf("untracked claim released: %+v", claims)
			}
		})
	}
}

func TestScreenshotActivityUsesDurableAttemptIDsAcrossBrowserFallback(t *testing.T) {
	lp, p, task := loopApp(t, &scriptedRunner{}, "")
	ctx := context.WithValue(context.Background(), slotKey{}, "claude")
	lp.runner = accountingRunner(func(ctx context.Context, spec roles.Spec) (roles.Result, error) {
		spec.Observer.Started()
		defer spec.Observer.Ended()
		spec.Observer.Asked("check")
		spec.Observer.Saw(session.Event{Kind: "tool_completed", ItemID: "shot", Tool: "screenshot", Images: []session.Image{{MediaType: "image/png", Data: []byte("synthetic")}}})
		if spec.Browser {
			return roles.Result{}, &session.CapabilityError{Code: session.CapabilityBrowserToolsMissing}
		}
		return roles.Result{Text: "done"}, nil
	})
	seat := core.Role{Name: "QA", Engine: "claude"}
	shots := &screenshots{next: lp.watchTurn(task, core.RoleQA, seat, "", false)}
	if _, err := lp.runRole(ctx, roles.Spec{ProjectID: p.ID, TaskID: task.ID, Role: core.RoleQA, Seat: seat.Name, Engine: seat.Engine, Browser: true, Observer: shots}); err != nil {
		t.Fatal(err)
	}
	turns, err := lp.Core.TeamTurns(ctx, core.TeamTurnFilter{TaskID: task.ID})
	if err != nil || len(turns) != 2 {
		t.Fatalf("turns %+v: %v", turns, err)
	}
	steps, err := lp.Core.TurnSteps(ctx, p.ID, task.ID, seat.Name)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, step := range steps {
		if step.Turn != turns[0].ID && step.Turn != turns[1].ID {
			t.Fatalf("unrelated activity identity %+v", step)
		}
		seen[step.Turn] = true
	}
	if len(seen) != 2 {
		t.Fatalf("missing attempt activity %+v", steps)
	}
	images, _ := shots.taken()
	if len(images) != 2 {
		t.Fatalf("lost screenshots %+v", images)
	}
}

func TestResumeAfterTerminalAccountingBeforeHandoff(t *testing.T) {
	lp, p, task := loopApp(t, &scriptedRunner{}, "")
	ctx := context.Background()
	claim, err := lp.Core.ClaimTask(ctx, task.ID, core.TaskWriting)
	if err != nil {
		t.Fatal(err)
	}
	lp.runner = accountingRunner(func(context.Context, roles.Spec) (roles.Result, error) {
		return roles.Result{Text: "finished", CleanupConfirmed: true, Provider: session.Result{Status: "completed", Usage: session.Usage{Usage: harness.Usage{Known: true, Input: 9}, Final: true}}}, nil
	})
	result, err := lp.runRole(context.WithValue(core.Fenced(ctx, task.ID, claim.Token), slotKey{}, "claude"), roles.Spec{ProjectID: p.ID, TaskID: task.ID, Role: core.RoleImplementer, Seat: "Writer", Engine: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := lp.Core.TeamTurns(ctx, core.TeamTurnFilter{TaskID: task.ID})
	if err != nil || len(before) != 1 || before[0].ID != result.AttemptID {
		t.Fatalf("turn %+v: %v", before, err)
	}
	// No task handoff or claim release occurred before the simulated restart.
	again := New(lp.Core, lp.Config, false)
	for i := 0; i < 2; i++ {
		if err := again.resume(ctx); err != nil {
			t.Fatal(err)
		}
	}
	after, err := again.Core.TeamTurns(ctx, core.TeamTurnFilter{TaskID: task.ID})
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("completed accounting changed: %+v: %v", after, err)
	}
	if got := taskByID(t, again, task.ID); len(got.Claims) != 0 {
		t.Fatalf("completed claim retained %+v", got.Claims)
	}
}

func assertTaskPMAccounting(t *testing.T, lp *Loop, task core.Task) {
	t.Helper()
	turns, err := lp.Core.TeamTurns(context.Background(), core.TeamTurnFilter{TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := lp.Core.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p, _ := findProject(snap, task.ProjectID)
	seat, _ := p.PMSeat()
	count := 0
	for _, turn := range turns {
		if turn.Role != core.RolePM {
			continue
		}
		count++
		if turn.ProjectID != p.ID || turn.TaskID != task.ID || turn.Seat != seat.Name || turn.MemberID != seat.Member || turn.Engine != seat.Engine || turn.Model != seat.Model || turn.Terminal == nil {
			t.Fatalf("task PM attribution %+v", turn)
		}
	}
	if count == 0 {
		t.Fatal("task-related PM turn has no durable task accounting")
	}
}

func TestTeamOpeningPersistsUnavailableToolsWithFreshReason(t *testing.T) {
	lp, p, task := loopApp(t, &scriptedRunner{}, "")
	want := []core.TeamTurnTool{{Name: "read_file", Reason: "file tools off"}, {Name: "search_files", Reason: "file tools off"}, {Name: "edit_file", Reason: "file tools off"}}
	lp.runner = accountingRunner(func(ctx context.Context, spec roles.Spec) (roles.Result, error) {
		var tools []session.WorkbenchTool
		for _, tool := range want {
			tools = append(tools, session.WorkbenchTool{Name: tool.Name, Capability: harness.Capability{Availability: harness.Unsupported, Reason: tool.Reason}})
		}
		spec.ToolReport(tools)
		for i := 0; i < 2; i++ {
			if err := spec.Opening(session.Opened{Fresh: session.FreshIncompatible}, session.Ref{ID: "fresh-api"}); err != nil {
				return roles.Result{}, err
			}
		}
		return roles.Result{CleanupConfirmed: true}, nil
	})
	result, err := lp.runAttempt(context.Background(), roles.Spec{ProjectID: p.ID, TaskID: task.ID, Role: core.RoleReviewer, Seat: "reviewer", Engine: "openai-compatible"})
	if err != nil {
		t.Fatal(err)
	}
	turns, err := lp.Core.TeamTurns(context.Background(), core.TeamTurnFilter{TaskID: task.ID})
	if err != nil || len(turns) != 1 || turns[0].ID != result.AttemptID || turns[0].Opening.FreshReason != core.FreshHarnessIncompatible || !reflect.DeepEqual(turns[0].Opening.UnavailableTools, want) {
		t.Fatalf("%+v %v", turns, err)
	}
}
