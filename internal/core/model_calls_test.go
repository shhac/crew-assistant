package core

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
)

func stateVersion(t *testing.T, s *Service) int {
	t.Helper()
	var version int
	if err := s.store.db.QueryRowContext(testContext, "SELECT version FROM state WHERE id=1").Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func modelCalls(t *testing.T, s *Service) map[string]int {
	t.Helper()
	snap, err := s.store.Snapshot(testContext)
	if err != nil {
		t.Fatal(err)
	}
	return snap.ModelCalls
}

func TestTheModelCallAllowanceResetsAtUTCMidnight(t *testing.T) {
	s, _ := fixture(t)
	lastMinute := time.Date(2026, 9, 14, 23, 59, 0, 0, time.UTC)
	s.now = func() time.Time { return lastMinute }
	for range 3 {
		if err := s.ReserveModelCall(testContext, 3); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ReserveModelCall(testContext, 3); err == nil {
		t.Fatal("a fourth call within the day was allowed past a limit of three")
	}
	if got := modelCalls(t, s); len(got) != 1 || got["2026-09-14"] != 3 {
		t.Fatalf("a refused call changed the count: %v", got)
	}

	s.now = func() time.Time { return lastMinute.Add(time.Minute) }
	if err := s.ReserveModelCall(testContext, 3); err != nil {
		t.Fatalf("the first call of a new UTC day was refused: %v", err)
	}
	got := modelCalls(t, s)
	if _, kept := got["2026-09-14"]; kept {
		t.Fatalf("the previous day's count was not pruned: %v", got)
	}
	if len(got) != 1 || got["2026-09-15"] != 1 {
		t.Fatalf("the new day's count = %v, want only 2026-09-15: 1", got)
	}
}

func TestTheModelCallDayFollowsUTCNotLocalTime(t *testing.T) {
	s, _ := fixture(t)
	ahead := time.FixedZone("ahead", 10*60*60)
	s.now = func() time.Time { return time.Date(2026, 9, 15, 8, 0, 0, 0, ahead) }
	if err := s.ReserveModelCall(testContext, 1); err != nil {
		t.Fatal(err)
	}
	if got := modelCalls(t, s); got["2026-09-14"] != 1 {
		t.Fatalf("a call at 08:00 +10:00 was counted as %v, want the UTC day 2026-09-14", got)
	}
}

func TestTheModelCallCountSurvivesARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	clock := func() time.Time { return time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC) }
	open := func() (*Service, *Store) {
		st, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		s := NewService(st, config.Default())
		s.now = clock
		return s, st
	}

	s, st := open()
	for range 2 {
		if err := s.ReserveModelCall(testContext, 2); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	s, st = open()
	t.Cleanup(func() { st.Close() })
	if got := modelCalls(t, s); got["2026-09-14"] != 2 {
		t.Fatalf("after reopening the count is %v, want 2", got)
	}
	if err := s.ReserveModelCall(testContext, 2); err == nil {
		t.Fatal("a restart handed back an exhausted day's allowance")
	}
}

func TestANonPositiveModelCallLimitIsRefusedWithoutWriting(t *testing.T) {
	for _, limit := range []int{0, -1} {
		s, _ := fixture(t)
		before := stateVersion(t, s)
		if err := s.ReserveModelCall(testContext, limit); err == nil {
			t.Fatalf("limit %d was accepted", limit)
		}
		if after := stateVersion(t, s); after != before {
			t.Fatalf("limit %d wrote the state: version %d -> %d", limit, before, after)
		}
		if got := modelCalls(t, s); len(got) != 0 {
			t.Fatalf("limit %d counted a call: %v", limit, got)
		}
	}
}
