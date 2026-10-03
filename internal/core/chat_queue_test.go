package core

import (
	"errors"
	"testing"
	"time"
)

func queueFixture(t *testing.T, ids ...string) *Service {
	t.Helper()
	s, _ := fixture(t)
	for _, id := range ids {
		if _, err := s.EnqueueChat(testContext, id, "message "+id); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func queuedIDs(t *testing.T, s *Service) []string {
	t.Helper()
	turns, err := s.ChatTurns(testContext)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, turn := range turns {
		if turn.Status == "queued" {
			out = append(out, turn.ID)
		}
	}
	return out
}

func TestHoldBlocksItsTurnAndEverythingAfterIt(t *testing.T) {
	s := queueFixture(t, "a", "b", "c")
	if _, err := s.HoldChat(testContext, "b", "editing", time.Minute); err != nil {
		t.Fatal(err)
	}
	// The turn ahead of the hold is unaffected.
	started, err := s.StartNextChat(testContext, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if started.ID != "a" {
		t.Fatalf("started %q, want the turn ahead of the hold", started.ID)
	}
	if err := s.FinishChat(testContext, "a", "completed", "reply", ""); err != nil {
		t.Fatal(err)
	}
	// The held turn, and the one behind it, do not start.
	if _, err := s.StartNextChat(testContext, "claude"); !errors.Is(err, ErrChatHeld) {
		t.Fatalf("started a held turn: %v", err)
	}
}

func TestHoldLapsesAndTheQueueResumesOnItsOwn(t *testing.T) {
	s := queueFixture(t, "a")
	if _, err := s.HoldChat(testContext, "a", "editing", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartNextChat(testContext, "claude"); !errors.Is(err, ErrChatHeld) {
		t.Fatal("a live hold did not block the queue")
	}
	// A client that stops refreshing cannot hold the queue indefinitely.
	s.now = func() time.Time { return time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC).Add(MaxChatHold * 2) }
	started, err := s.StartNextChat(testContext, "claude")
	if err != nil {
		t.Fatalf("the queue did not resume after the lease lapsed: %v", err)
	}
	if started.ID != "a" {
		t.Fatalf("started %q", started.ID)
	}
}

// A hold naming a turn that has since started or been cancelled must not
// strand everything behind it.
func TestStaleHoldBlocksNothing(t *testing.T) {
	s := queueFixture(t, "a", "b")
	if _, err := s.HoldChat(testContext, "a", "editing", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelChat(testContext, "a"); err != nil {
		t.Fatal(err)
	}
	started, err := s.StartNextChat(testContext, "claude")
	if err != nil {
		t.Fatalf("a hold on a cancelled turn stranded the queue: %v", err)
	}
	if started.ID != "b" {
		t.Fatalf("started %q, want b", started.ID)
	}
}

func TestOneEditorHoldsTheQueueAtATime(t *testing.T) {
	s := queueFixture(t, "a", "b")
	if _, err := s.HoldChat(testContext, "a", "editing", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := s.HoldChat(testContext, "b", "editing", time.Minute); !errors.Is(err, ErrConflict) {
		t.Fatalf("a second editor took the queue: %v", err)
	}
	// Refreshing your own lease is not stealing it.
	if _, err := s.HoldChat(testContext, "a", "editing", time.Minute); err != nil {
		t.Fatalf("could not refresh an existing lease: %v", err)
	}
	if err := s.ReleaseChatHold(testContext, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.HoldChat(testContext, "b", "editing", time.Minute); err != nil {
		t.Fatalf("the queue stayed held after release: %v", err)
	}
}

func TestEditKeepsIdentityAndMovesRevision(t *testing.T) {
	s := queueFixture(t, "a")
	turns, _ := s.ChatTurns(testContext)
	before := turns[0]
	after, err := s.EditChatMessage(testContext, "a", "  corrected text  ", before.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if after.ID != before.ID {
		t.Fatal("editing changed the key that protects against duplicate delivery")
	}
	if after.Message != "corrected text" {
		t.Fatalf("message = %q", after.Message)
	}
	if after.Revision == before.Revision {
		t.Fatal("revision did not move, so stale text could still start")
	}
	// The revision the editor was opened against is now stale.
	if _, err := s.EditChatMessage(testContext, "a", "again", before.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("a stale edit was applied: %v", err)
	}
}

// The owner can open an editor at the moment the daemon starts the turn. That
// race is reported, never applied to a running turn.
func TestEditLosingTheRaceIsRefused(t *testing.T) {
	s := queueFixture(t, "a")
	turns, _ := s.ChatTurns(testContext)
	revision := turns[0].Revision
	if _, err := s.StartNextChat(testContext, "claude"); err != nil {
		t.Fatal(err)
	}
	_, err := s.EditChatMessage(testContext, "a", "too late", revision)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("edited a running turn: %v", err)
	}
	turns, _ = s.ChatTurns(testContext)
	if turns[0].Message != "message a" {
		t.Fatalf("the running turn's text changed to %q", turns[0].Message)
	}
}

func TestReorderSetsTheWholeOrderAtOnce(t *testing.T) {
	s := queueFixture(t, "a", "b", "c")
	v, _ := s.store.Snapshot(testContext)
	if _, err := s.ReorderChat(testContext, []string{"a", "c", "b"}, v.ChatQueueRevision); err != nil {
		t.Fatal(err)
	}
	if got := queuedIDs(t, s); got[0] != "a" || got[1] != "c" || got[2] != "b" {
		t.Fatalf("order = %v", got)
	}
	started, err := s.StartNextChat(testContext, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if started.ID != "a" {
		t.Fatalf("started %q", started.ID)
	}
	if err := s.FinishChat(testContext, "a", "completed", "reply", ""); err != nil {
		t.Fatal(err)
	}
	next, err := s.StartNextChat(testContext, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if next.ID != "c" {
		t.Fatalf("ran %q, not the order the owner set", next.ID)
	}
}

func TestReorderAgainstAStaleQueueIsRefused(t *testing.T) {
	s := queueFixture(t, "a", "b")
	v, _ := s.store.Snapshot(testContext)
	stale := v.ChatQueueRevision
	if _, err := s.EnqueueChat(testContext, "c", "message c"); err != nil {
		t.Fatal(err)
	}
	for _, order := range [][]string{{"b", "a"}, {"b", "a", "c"}} {
		if _, err := s.ReorderChat(testContext, order, stale); !errors.Is(err, ErrConflict) {
			t.Fatalf("a reorder decided against a stale queue was applied: %v", err)
		}
	}
	// An order that does not name the queued messages is malformed, not stale:
	// refreshing will not make it work, so it is a different refusal.
	v, _ = s.store.Snapshot(testContext)
	for _, order := range [][]string{{"a", "b", "gone"}, {"a", "a", "b"}, {"a", "b"}} {
		_, err := s.ReorderChat(testContext, order, v.ChatQueueRevision)
		if !errors.Is(err, ErrChatValidation) {
			t.Fatalf("order %v was not reported as malformed: %v", order, err)
		}
	}
	if got := queuedIDs(t, s); got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("a refused reorder still changed the queue: %v", got)
	}
}

// A turn that has already started keeps its place; only queued turns move.
func TestReorderLeavesStartedTurnsAlone(t *testing.T) {
	s := queueFixture(t, "a", "b", "c")
	if _, err := s.StartNextChat(testContext, "claude"); err != nil {
		t.Fatal(err)
	}
	v, _ := s.store.Snapshot(testContext)
	if _, err := s.ReorderChat(testContext, []string{"c", "b"}, v.ChatQueueRevision); err != nil {
		t.Fatal(err)
	}
	turns, _ := s.ChatTurns(testContext)
	if turns[0].ID != "a" || turns[0].Status != "running" {
		t.Fatalf("the running turn moved: %+v", turns[0])
	}
	if got := queuedIDs(t, s); got[0] != "c" || got[1] != "b" {
		t.Fatalf("queued order = %v", got)
	}
}

// Cancelling changes the queued set, so an order decided before it must be
// refused by the revision rather than relying on a length check to catch it.
func TestCancellingMovesTheQueueRevision(t *testing.T) {
	s := queueFixture(t, "a", "b", "c")
	v, _ := s.store.Snapshot(testContext)
	before := v.ChatQueueRevision
	if _, err := s.CancelChat(testContext, "c"); err != nil {
		t.Fatal(err)
	}
	v, _ = s.store.Snapshot(testContext)
	if v.ChatQueueRevision == before {
		t.Fatal("cancelling left the revision that guards the queue unchanged")
	}
	if _, err := s.ReorderChat(testContext, []string{"b", "a"}, before); !errors.Is(err, ErrConflict) {
		t.Fatal("an order decided before the cancellation was not refused")
	}
}

// reorderQueued is the whole permutation rule, testable without a store.
func TestReorderQueuedRebuildsAroundStartedTurns(t *testing.T) {
	turns := []ChatTurn{
		{ID: "running", Status: "running"},
		{ID: "a", Status: "queued"},
		{ID: "done", Status: "completed"},
		{ID: "b", Status: "queued"},
	}
	next, err := reorderQueued(turns, []string{"b", "a"})
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, turn := range next {
		got = append(got, turn.ID)
	}
	// Started turns keep their slots; the queued slots take the new order.
	if got[0] != "running" || got[1] != "b" || got[2] != "done" || got[3] != "a" {
		t.Fatalf("order = %v", got)
	}
	for _, bad := range [][]string{{"a"}, {"a", "a"}, {"a", "missing"}, {}} {
		if _, err := reorderQueued(turns, bad); !errors.Is(err, ErrChatValidation) {
			t.Fatalf("accepted %v", bad)
		}
	}
}

// Cancelling the message you were changing must not leave a hold standing: it
// would tell the owner the queue is paused while turns visibly run, and refuse
// every other change until it lapsed. (A held turn cannot start, so cancelling
// is the only way a held turn leaves the queue.)
func TestCancellingAHeldTurnEndsTheHold(t *testing.T) {
	s := queueFixture(t, "a", "b")
	if _, err := s.HoldChat(testContext, "a", "editing", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelChat(testContext, "a"); err != nil {
		t.Fatal(err)
	}
	hold, _, err := s.ChatQueueState(testContext)
	if err != nil {
		t.Fatal(err)
	}
	if hold != nil {
		t.Fatalf("a spent hold is still reported: %+v", hold)
	}
	// Another message can be changed rather than being locked out.
	if _, err := s.HoldChat(testContext, "b", "editing", time.Minute); err != nil {
		t.Fatalf("a spent hold still blocks other changes: %v", err)
	}
}
