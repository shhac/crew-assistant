package core

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
)

func TestProjectPauseIsIdempotentAndDurable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewService(st, config.Default())
	p := newProject(t, s)
	for _, paused := range []bool{true, true, false, false, true} {
		got, err := s.SetProjectPaused(testContext, p.ID, paused)
		if err != nil || got.Paused != paused {
			t.Fatalf("pause: %+v %v", got, err)
		}
	}
	if _, err := s.SetProjectPaused(testContext, "missing", true); !errors.Is(err, ErrNotFound) {
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
	snap, err := NewService(st, config.Default()).Snapshot(testContext)
	if err != nil || !snap.ProjectPaused(p.ID) {
		t.Fatalf("restart: %+v %v", snap, err)
	}
	n := 0
	for _, a := range snap.Activity {
		if a.Kind == "project.paused" {
			n++
			if a.ProjectID != p.ID {
				t.Fatal(a)
			}
		}
	}
	if n != 3 {
		t.Fatalf("pause activities: %d", n)
	}
}

func TestProjectPauseClaimsOnlyOtherProjectsThenResumes(t *testing.T) {
	s, _ := fixture(t)
	a, b := newProject(t, s), newProject(t, s)
	ta, _ := s.QueueTask(testContext, a.ID, TaskInput{Objective: "A"})
	tb, _ := s.QueueTask(testContext, b.ID, TaskInput{Objective: "B"})
	if _, err := s.SetProjectPaused(testContext, a.ID, true); err != nil {
		t.Fatal(err)
	}
	got, err := s.Schedule(testContext, anyone)
	if err != nil || len(got) != 1 || got[0].Task.ID != tb.ID {
		t.Fatalf("scheduled: %+v %v", got, err)
	}
	if _, err := s.SetProjectPaused(testContext, a.ID, false); err != nil {
		t.Fatal(err)
	}
	got, err = s.Schedule(testContext, anyone)
	if err != nil || len(got) != 1 || got[0].Task.ID != ta.ID {
		t.Fatalf("resumed: %+v %v", got, err)
	}
	// Active work is also held, without dropping its already committed claim.
	if _, err := s.SetProjectPaused(testContext, a.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseClaim(testContext, ta.ID, got[0].Claim.Token); err != nil {
		t.Fatal(err)
	}
	got, err = s.Schedule(testContext, anyone)
	if err != nil || len(got) != 0 {
		t.Fatalf("active paused task claimed: %+v %v", got, err)
	}
}

func TestProjectPauseHoldsPMLookButNotAssistantQuestion(t *testing.T) {
	s, p, _ := pmWriter(t)
	if _, err := s.SetProjectPaused(testContext, p.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err := s.ClaimPM(testContext, p.ID, anyone); err != nil || ok {
		t.Fatalf("PM claimed: %v %v", ok, err)
	}
	if _, _, ok, err := s.ClaimPMQuestion(testContext, p.ID, anyone); err != nil || !ok {
		t.Fatalf("question held: %v %v", ok, err)
	}
}

func TestProjectPauseHoldsMessageClaimUntilResume(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	a := queueAll(t, s, p, "A")[0]
	claimed(t, s)
	finish(t, s, a.ID, TaskReviewing)
	m, err := s.SendTeamMessage(testContext, p.ID, a.ID, "Reviewer", FromOwner, "Check again")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetProjectPaused(testContext, p.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.ClaimMessage(testContext, a.ID, m.ID, anyone); err != nil || ok {
		t.Fatalf("message claimed: %v %v", ok, err)
	}
	if _, err := s.SetProjectPaused(testContext, p.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.ClaimMessage(testContext, a.ID, m.ID, anyone); err != nil || !ok {
		t.Fatalf("message held after resume: %v %v", ok, err)
	}
}
