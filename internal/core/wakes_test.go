package core

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWakesWaitInParallelAndAreCancelledByHandle(t *testing.T) {
	s, _ := fixture(t)
	a, err := s.RegisterWake(testContext, WakeInput{Owner: WakeAssistant, On: WakeOnTime, Target: "2026-09-14T13:00:00Z", Baseline: "2026-09-14T13:00:00Z", Prompt: "check the build"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.RegisterWake(testContext, WakeInput{Owner: WakeAssistant, On: WakeOnChecks, Target: "shhac/x#12", Baseline: "PENDING"})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID || !strings.HasPrefix(a.ID, "wake-") || a.ExpiresAt.Sub(a.CreatedAt) != DefaultWakeFor {
		t.Fatalf("handles %s %s, expiry %s", a.ID, b.ID, a.ExpiresAt)
	}
	if _, err = s.CancelWake(testContext, a.ID, ""); err != nil {
		t.Fatal(err)
	}
	waiting, _ := s.Waiting(testContext)
	if len(waiting) != 1 || waiting[0].ID != b.ID {
		t.Fatalf("waiting %+v", waiting)
	}
	if _, err = s.CancelWake(testContext, a.ID, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("cancelled twice: %v", err)
	}
	if _, err = s.RegisterWake(testContext, WakeInput{Owner: WakeAssistant, On: "weather", Target: "x"}); err == nil {
		t.Fatal("accepted an unknown condition")
	}
	if _, err = s.RegisterWake(testContext, WakeInput{Owner: WakeAssistant, On: WakeOnTime, Target: "x", Timeout: MaxWakeFor + time.Hour}); err == nil {
		t.Fatal("accepted a wait longer than the maximum")
	}
	for i := 0; i < MaxWaiting-1; i++ {
		if _, err = s.RegisterWake(testContext, WakeInput{Owner: WakeAssistant, On: WakeOnTime, Target: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.RegisterWake(testContext, WakeInput{Owner: WakeAssistant, On: WakeOnTime, Target: "x"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("waited on more than the limit: %v", err)
	}
}

func TestAssistantWakesArriveTogetherWithTheirOwnTimes(t *testing.T) {
	s, _ := fixture(t)
	clock := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }
	first, _ := s.RegisterWake(testContext, WakeInput{Owner: WakeAssistant, On: WakeOnTask, Target: "t1", Match: "landed", Baseline: "landing", Prompt: "land the second feature"})
	second, _ := s.RegisterWake(testContext, WakeInput{Owner: WakeAssistant, On: WakeOnChecks, Target: "shhac/x#12", Baseline: "PENDING"})
	// The owner is mid-conversation, so the wake-up queues behind them.
	if _, err := s.EnqueueChat(testContext, "owner", "hello"); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(5 * time.Minute)
	if _, err := s.FireWake(testContext, first.ID, "landed", "the task landed on main", false); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Minute)
	if _, err := s.FireWake(testContext, second.ID, "FAILURE", "", false); err != nil {
		t.Fatal(err)
	}
	turns, _ := s.ChatTurns(testContext)
	var wakeTurns []ChatTurn
	for _, turn := range turns {
		if turn.Origin == OriginWake {
			wakeTurns = append(wakeTurns, turn)
		}
	}
	if len(wakeTurns) != 1 || len(wakeTurns[0].WakeIDs) != 2 {
		t.Fatalf("wakes did not arrive together: %+v", wakeTurns)
	}
	if _, err := s.EditChatMessage(testContext, wakeTurns[0].ID, "not a wake", wakeTurns[0].Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("a wake-up was edited as if the owner wrote it: %v", err)
	}
	if turn, _ := s.StartNextChat(testContext); turn.ID != "owner" {
		t.Fatalf("the wake-up jumped the owner's message: %+v", turn)
	}
	s.FinishChat(testContext, "owner", "completed", "hi", "")
	clock = clock.Add(10 * time.Minute)
	turn, err := s.StartNextChat(testContext)
	if err != nil || turn.Origin != OriginWake {
		t.Fatalf("turn %+v err %v", turn, err)
	}
	for _, want := range []string{first.ID, second.ID, "registered 2026-09-14T12:00:00Z", "seen 2026-09-14T12:05:00Z", "delivered 2026-09-14T12:16:00Z (11m0s after it was seen)", "your continuation: land the second feature", `before: "landing" · after: "landed"`, "not a message from the owner", "check the current state"} {
		if !strings.Contains(turn.Message, want) {
			t.Errorf("the wake-up lacks %q:\n%s", want, turn.Message)
		}
	}
	snap, _ := s.Snapshot(testContext)
	last := snap.Messages[len(snap.Messages)-1]
	if last.Origin != OriginWake {
		t.Fatalf("the conversation does not mark the wake-up: %+v", last)
	}
	for _, w := range snap.Wakes {
		if w.Status != WakeDelivered || w.DeliveredAt == nil {
			t.Fatalf("not marked delivered: %+v", w)
		}
	}
}

func TestATaskTakesItsWakesAndLosesThemWhenItEnds(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	task, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "x"})
	if err != nil {
		t.Fatal(err)
	}
	mine, _ := s.RegisterWake(testContext, WakeInput{Owner: WakeTask, TaskID: task.ID, On: WakeOnTime, Target: "x"})
	other, _ := s.RegisterWake(testContext, WakeInput{Owner: WakeTask, TaskID: task.ID, On: WakeOnTime, Target: "y"})
	if _, err = s.CancelWake(testContext, mine.ID, "another-task"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a task cancelled another task's wake: %v", err)
	}
	s.FireWake(testContext, mine.ID, "reached", "", false)
	taken, _ := s.TakeTaskWakes(testContext, task.ID, WakeTask)
	if len(taken) != 1 || taken[0].ID != mine.ID || taken[0].DeliveredAt == nil {
		t.Fatalf("taken %+v", taken)
	}
	if again, _ := s.TakeTaskWakes(testContext, task.ID, WakeTask); len(again) != 0 {
		t.Fatal("a wake was delivered twice")
	}
	s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
		t.Status = TaskStopped
		return "", nil
	})
	snap, _ := s.Snapshot(testContext)
	for _, w := range snap.Wakes {
		if w.ID == other.ID && w.Status != WakeCancelled {
			t.Fatalf("a stopped task's wake is still waiting: %+v", w)
		}
	}
}
