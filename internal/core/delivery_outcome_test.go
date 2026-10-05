package core

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDeliveryEvidenceSurvivesRevocationButNotAnotherIntent(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	task := queueAll(t, s, p, "Outward outcome")[0]
	intent := Delivering{Revision: 1, At: time.Now().UTC()}
	_, err := s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
		t.Status = TaskLanding
		t.Delivering = &intent
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimTask(testContext, task.ID, TaskLanding)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) { t.Status = TaskStopped; return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(Fenced(testContext, task.ID, claim.Token))
	cancel()
	if err := s.RecordDeliveryOutcome(context.WithoutCancel(ctx), task.ID, intent, "permission refused", true); err != nil {
		t.Fatal(err)
	}
	now := taskNow(t, s, task.ID)
	if now.Status != TaskStopped || len(now.Claims) != 0 || !now.Delivering.Refused {
		t.Fatal(now)
	}
	old := intent
	intent.At = intent.At.Add(time.Second)
	_, err = s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) { t.Delivering = &intent; return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordDeliveryOutcome(testContext, task.ID, old, "old result", true); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
	if taskNow(t, s, task.ID).Delivering.Refused {
		t.Fatal("old refusal poisoned new intent")
	}
}

func TestCleanupReleasesOnlyItsOwnHeldObligation(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	task := queueAll(t, s, p, "Held work")[0]
	c, err := s.ClaimTask(testContext, task.ID, TaskWriting)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.HoldClaim(testContext, task.ID, p.ID, c.Token, "accounting"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseHeldClaim(testContext, task.ID, c.Token, "command cleanup"); err != nil {
		t.Fatal(err)
	}
	if len(taskNow(t, s, task.ID).Claims) != 1 {
		t.Fatal("released accounting obligation")
	}
	if err := s.ReleaseHeldClaim(testContext, task.ID, c.Token, "accounting"); err != nil {
		t.Fatal(err)
	}
	if len(taskNow(t, s, task.ID).Claims) != 0 {
		t.Fatal("matching obligation retained")
	}
}
