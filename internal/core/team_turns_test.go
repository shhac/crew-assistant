package core

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/session"
)

func admittedTurn(id string) TeamTurn {
	return TeamTurn{ID: id, ProjectID: "project", TaskID: "task", Role: RoleImplementer, Seat: "Writer", MemberID: "member", MemberName: "Writer", Engine: "codex", ClaimToken: "claim", AdmittedAt: time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)}
}

func TestTeamTurnsRecoverCleanupWithoutRewritingTerminalAccounting(t *testing.T) {
	s, cfg := fixture(t)
	turn := admittedTurn("finalized")
	if err := s.AdmitTeamTurn(testContext, turn); err != nil {
		t.Fatal(err)
	}
	terminal := TeamTurnTerminal{At: turn.AdmittedAt.Add(time.Second), Outcome: "failed", FailureStage: "release", ProviderStatus: "completed", Usage: session.Usage{Usage: harness.Usage{Known: true, CacheKnown: true, Input: 20, CacheRead: 8, Output: 3}, Final: true}}
	if err := s.FinishTeamTurn(testContext, turn.ID, terminal); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.RecoverTeamTurn(testContext, turn.ID, false, terminal.At.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.TeamTurns(testContext, TeamTurnFilter{NeedsRecovery: true})
	if err != nil || len(got) != 1 || !got[0].Held || !reflect.DeepEqual(*got[0].Terminal, terminal) {
		t.Fatalf("lost terminal: %+v %v", got, err)
	}
	if err := s.RecoverTeamTurn(testContext, turn.ID, true, turn.AdmittedAt); !errors.Is(err, ErrConflict) {
		t.Fatalf("invalid recovery time: %v", err)
	}
	confirmedAt := terminal.At.Add(2 * time.Second)
	if err := s.RecoverTeamTurn(testContext, turn.ID, true, confirmedAt); err != nil {
		t.Fatal(err)
	}
	// Late duplicate writes remain idempotent against the original terminal.
	if err := s.FinishTeamTurn(testContext, turn.ID, terminal); err != nil {
		t.Fatal(err)
	}
	if err := s.AdmitTeamTurn(testContext, turn); err != nil {
		t.Fatal(err)
	}
	if err := s.RecoverTeamTurn(testContext, turn.ID, false, confirmedAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got, err = s.TeamTurns(testContext, TeamTurnFilter{NeedsRecovery: true})
	if err != nil || len(got) != 0 {
		t.Fatalf("settled cleanup still pending: %+v %v", got, err)
	}
	// Reopen the database to prove reclamation survives daemon restarts.
	path := filepath.Join(s.store.stateDirectory, "state.db")
	if err := s.store.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s = NewService(st, cfg)
	got, err = s.TeamTurns(testContext, TeamTurnFilter{})
	if err != nil || len(got) != 1 || got[0].Held || got[0].CleanupConfirmedAt == nil || !got[0].CleanupConfirmedAt.Equal(confirmedAt) || !reflect.DeepEqual(*got[0].Terminal, terminal) {
		t.Fatalf("reclamation lost: %+v %v", got, err)
	}
}

func TestTeamTurnReplayUsesInstantsAndImmutableAdmission(t *testing.T) {
	s, _ := fixture(t)
	turn := admittedTurn("clock")
	turn.AdmittedAt = time.Now()
	opening := TeamTurnOpening{At: turn.AdmittedAt.Add(time.Second), Resumed: true}
	accepted := opening.At.Add(time.Second)
	terminal := TeamTurnTerminal{At: accepted.Add(time.Second), Outcome: "completed"}
	replay := func() {
		t.Helper()
		if err := s.AdmitTeamTurn(testContext, turn); err != nil {
			t.Fatal(err)
		}
		zoned := turn
		zoned.AdmittedAt = turn.AdmittedAt.In(time.FixedZone("other", 3600))
		if err := s.AdmitTeamTurn(testContext, zoned); err != nil {
			t.Fatal(err)
		}
	}
	replay()
	if err := s.OpenTeamTurn(testContext, turn.ID, opening); err != nil {
		t.Fatal(err)
	}
	replay()
	if err := s.OpenTeamTurn(testContext, turn.ID, opening); err != nil {
		t.Fatal(err)
	}
	opening.At = opening.At.In(time.FixedZone("other", -3600))
	if err := s.OpenTeamTurn(testContext, turn.ID, opening); err != nil {
		t.Fatal(err)
	}
	if err := s.AcceptTeamTurn(testContext, turn.ID, accepted); err != nil {
		t.Fatal(err)
	}
	replay()
	if err := s.RecoverTeamTurn(testContext, turn.ID, false, accepted); err != nil {
		t.Fatal(err)
	}
	replay()
	if err := s.FinishTeamTurn(testContext, turn.ID, terminal); err != nil {
		t.Fatal(err)
	}
	replay()
	if err := s.FinishTeamTurn(testContext, turn.ID, terminal); err != nil {
		t.Fatal(err)
	}
	terminal.At = terminal.At.In(time.FixedZone("other", 7200))
	if err := s.FinishTeamTurn(testContext, turn.ID, terminal); err != nil {
		t.Fatal(err)
	}
}

func TestTeamTurnsLifecycleAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewService(st, config.Default())
	turn := admittedTurn("attempt")
	if err := s.AdmitTeamTurn(testContext, turn); err != nil {
		t.Fatal(err)
	}
	if err := s.AdmitTeamTurn(testContext, turn); err != nil {
		t.Fatal(err)
	}
	opening := TeamTurnOpening{At: turn.AdmittedAt.Add(time.Second), FreshReason: FreshNoThread, SessionID: "conversation"}
	if err := s.OpenTeamTurn(testContext, turn.ID, opening); err != nil {
		t.Fatal(err)
	}
	if err := s.OpenTeamTurn(testContext, turn.ID, opening); err != nil {
		t.Fatal(err)
	}
	accepted := opening.At.Add(time.Second)
	if err := s.AcceptTeamTurn(testContext, turn.ID, accepted); err != nil {
		t.Fatal(err)
	}
	if err := s.AcceptTeamTurn(testContext, turn.ID, accepted); err != nil {
		t.Fatal(err)
	}
	terminal := TeamTurnTerminal{At: accepted.Add(time.Second), Outcome: "failed", FailureStage: "wait", ProviderStatus: "failed", Usage: session.Usage{Usage: harness.Usage{Known: true, CacheKnown: true, Input: 50, CacheRead: 20, CacheWrite: 10, Output: 8}, Final: true}, Observed: session.Usage{Usage: harness.Usage{Known: true, Input: 90, Output: 10}}, CompactionUsage: session.Usage{Usage: harness.Usage{Known: true, Input: 15, Output: 2}, Final: true}}
	if err := s.FinishTeamTurn(testContext, turn.ID, terminal); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishTeamTurn(testContext, turn.ID, terminal); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s = NewService(st, config.Default())
	got, err := s.TeamTurns(testContext, TeamTurnFilter{TaskID: turn.TaskID})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AdmitTeamTurn(testContext, turn); err != nil {
		t.Fatal("admission replay after reopening", err)
	}
	turn.Opening, turn.AcceptedAt, turn.Terminal = &opening, &accepted, &terminal
	if len(got) != 1 || !reflect.DeepEqual(got[0], turn) {
		t.Fatalf("durable record: %+v", got)
	}
	if got[0].Model != "" || got[0].Terminal.Usage.Input != 50 {
		t.Fatal("invented model or summed observations")
	}
}

func TestTeamTurnsMissingAndMeasuredZero(t *testing.T) {
	s, _ := fixture(t)
	for i, usage := range []session.Usage{{}, {Usage: harness.Usage{Known: true}, Final: true}, {Usage: harness.Usage{Known: true, CacheKnown: true}, Final: true}, {Usage: harness.Usage{Known: true, Input: 20, Output: 4}, Final: true}} {
		turn := admittedTurn(fmt.Sprint(i))
		if err := s.AdmitTeamTurn(testContext, turn); err != nil {
			t.Fatal(err)
		}
		terminal := TeamTurnTerminal{At: turn.AdmittedAt.Add(time.Second), Outcome: "failed", Usage: usage}
		if err := s.FinishTeamTurn(testContext, turn.ID, terminal); err != nil {
			t.Fatal(err)
		}
		got, err := s.TeamTurns(testContext, TeamTurnFilter{MemberID: "member"})
		if err != nil || !reflect.DeepEqual(got[i].Terminal.Usage, usage) {
			t.Fatalf("usage %d: %+v %v", i, got, err)
		}
	}
}

func TestTeamTurnsRejectConflictingTransitions(t *testing.T) {
	s, _ := fixture(t)
	turn := admittedTurn("attempt")
	if err := s.AdmitTeamTurn(testContext, turn); err != nil {
		t.Fatal(err)
	}
	changed := turn
	changed.TaskID = "different"
	if err := s.AdmitTeamTurn(testContext, changed); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err := s.AcceptTeamTurn(testContext, turn.ID, turn.AdmittedAt); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	opening := TeamTurnOpening{At: turn.AdmittedAt.Add(time.Second), Resumed: true}
	if err := s.OpenTeamTurn(testContext, turn.ID, opening); err != nil {
		t.Fatal(err)
	}
	changedOpening := opening
	changedOpening.Resumed = false
	if err := s.OpenTeamTurn(testContext, turn.ID, changedOpening); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	terminal := TeamTurnTerminal{At: opening.At.Add(time.Second), Outcome: "completed"}
	if err := s.FinishTeamTurn(testContext, turn.ID, terminal); err != nil {
		t.Fatal(err)
	}
	terminal.Usage.Known = true
	if err := s.FinishTeamTurn(testContext, turn.ID, terminal); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err := s.AcceptTeamTurn(testContext, turn.ID, opening.At); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err := s.OpenTeamTurn(testContext, "missing", opening); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestTeamTurnsRecoveryAndRetryAttribution(t *testing.T) {
	s, _ := fixture(t)
	first := admittedTurn("first")
	if err := s.AdmitTeamTurn(testContext, first); err != nil {
		t.Fatal(err)
	}
	retry := admittedTurn("retry")
	retry.AdmittedAt = first.AdmittedAt.Add(2 * time.Second)
	retry.PreviousID, retry.RetryCause = first.ID, "browser_unavailable"
	if err := s.AdmitTeamTurn(testContext, retry); !errors.Is(err, ErrConflict) {
		t.Fatal("retry before finalization", err)
	}
	if err := s.RecoverTeamTurn(testContext, first.ID, false, first.AdmittedAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got, _ := s.TeamTurns(testContext, TeamTurnFilter{Incomplete: true})
	if len(got) != 1 || !got[0].Held || got[0].Terminal != nil {
		t.Fatal(got)
	}
	if err := s.RecoverTeamTurn(testContext, first.ID, true, first.AdmittedAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := s.AdmitTeamTurn(testContext, retry); err != nil {
		t.Fatal(err)
	}
	wrong := retry
	wrong.ID, wrong.TaskID = "wrong", "different"
	if err := s.AdmitTeamTurn(testContext, wrong); !errors.Is(err, ErrConflict) {
		t.Fatal("cross-task retry", err)
	}
	terminal := TeamTurnTerminal{At: retry.AdmittedAt.Add(2 * time.Second), Outcome: "completed", Usage: session.Usage{Usage: harness.Usage{Known: true, Input: 100}, Final: true}}
	if err := s.FinishTeamTurn(testContext, retry.ID, terminal); err != nil {
		t.Fatal(err)
	}
	for _, confirmed := range []bool{false, true, true} {
		if err := s.RecoverTeamTurn(testContext, retry.ID, confirmed, terminal.At.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	got, _ = s.TeamTurns(testContext, TeamTurnFilter{ClaimToken: first.ClaimToken})
	if len(got) != 2 || got[0].Terminal.Usage.Known || got[0].Terminal.Outcome != "interrupted" || !reflect.DeepEqual(*got[1].Terminal, terminal) {
		t.Fatal(got)
	}
}

func TestTeamTurnsConcurrentAdmissionsAndOrdering(t *testing.T) {
	s, _ := fixture(t)
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			turn := admittedTurn(fmt.Sprintf("%02d", i))
			// Whole-second and fractional timestamps must sort chronologically.
			turn.AdmittedAt = turn.AdmittedAt.Add(time.Duration(i) * time.Millisecond)
			if i%2 == 0 {
				turn.TaskID = ""
				turn.ProjectID = "other-project"
				turn.Role = RolePM
			}
			if err := s.AdmitTeamTurn(testContext, turn); err != nil {
				t.Error(err)
				return
			}
			terminal := TeamTurnTerminal{At: turn.AdmittedAt.Add(time.Second), Outcome: "completed"}
			if err := s.FinishTeamTurn(testContext, turn.ID, terminal); err != nil {
				t.Error(err)
			}
			if err := s.FinishTeamTurn(testContext, turn.ID, terminal); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	got, err := s.TeamTurns(testContext, TeamTurnFilter{MemberID: "member"})
	if err != nil || len(got) != 30 {
		t.Fatalf("%d records: %v", len(got), err)
	}
	for i, turn := range got {
		if turn.ID != fmt.Sprintf("%02d", i) {
			t.Fatal("unstable ordering", got)
		}
		if i%2 == 0 && turn.TaskID != "" {
			t.Fatal("invented task", turn)
		}
	}
	got, _ = s.TeamTurns(testContext, TeamTurnFilter{TaskID: "task"})
	if len(got) != 15 {
		t.Fatal(got)
	}
	got, _ = s.TeamTurns(testContext, TeamTurnFilter{ProjectID: "other-project"})
	if len(got) != 15 {
		t.Fatal(got)
	}
}

func TestTeamTurnsAdmissionFailureAndStepIndependence(t *testing.T) {
	s, _ := fixture(t)
	ctx, cancel := context.WithCancel(testContext)
	cancel()
	turn := admittedTurn("attempt")
	if err := s.AdmitTeamTurn(ctx, turn); err == nil {
		t.Fatal("cancelled admission succeeded")
	}
	got, _ := s.TeamTurns(testContext, TeamTurnFilter{})
	if len(got) != 0 {
		t.Fatal("partial admission", got)
	}
	if err := s.AdmitTeamTurn(testContext, turn); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxTaskSteps+1; i++ {
		if err := s.RecordTurnStep(testContext, TurnStep{TaskID: turn.TaskID, Turn: turn.ID, Item: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	got, _ = s.TeamTurns(testContext, TeamTurnFilter{})
	if len(got) != 1 || got[0].Terminal != nil || got[0].Opening != nil {
		t.Fatal("pruning changed accounting", got)
	}
}

func TestTeamTurnsOpenExistingDatabaseWithoutBackfill(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewService(st, config.Default())
	newProject(t, s)
	// Emulate a database predating prospective accounting, retaining its state.
	if _, err := st.db.Exec("DROP TABLE team_turns"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s = NewService(st, config.Default())
	got, err := s.TeamTurns(testContext, TeamTurnFilter{})
	if err != nil || len(got) != 0 {
		t.Fatalf("backfilled old history: %+v %v", got, err)
	}
	snap, err := s.Snapshot(testContext)
	if err != nil || len(snap.Projects) != 1 {
		t.Fatalf("changed existing state: %+v %v", snap, err)
	}
}

func TestTeamTurnsSerializeAcrossStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	services := []*Service{NewService(first, config.Default()), NewService(second, config.Default())}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			turn := admittedTurn("same-attempt")
			if err := services[i%2].AdmitTeamTurn(testContext, turn); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	got, err := services[0].TeamTurns(testContext, TeamTurnFilter{})
	if err != nil || len(got) != 1 {
		t.Fatalf("duplicate admission: %+v %v", got, err)
	}
}
