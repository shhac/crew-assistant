package core

import (
	"errors"
	"testing"
)

func TestReplacementDecisionKeepsUnappliedOwnerAnswer(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	task, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Preserve the owner's answer"})
	if err != nil {
		t.Fatal(err)
	}
	in := DecisionInput{Title: "Approve draft", Context: "The draft is ready", Recommendation: "Approve", Choices: []string{"Approve", "Request changes"}}
	d, err := s.OpenTaskDecision(testContext, task.ID, DecisionDelivery, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChooseDecision(testContext, d.ID, "Request changes", FromOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenTaskDecision(testContext, task.ID, DecisionFailure, DecisionInput{Title: "Catch-up failed", Context: "Publication failed", Recommendation: "Retry", Choices: []string{"Retry", "Stop"}}); !errors.Is(err, ErrStale) {
		t.Fatal("replacement detached unapplied owner answer", err)
	}
	current := taskNow(t, s, task.ID)
	snap, err := s.Snapshot(testContext)
	if err != nil {
		t.Fatal(err)
	}
	original := decision(&snap, d.ID)
	if current.Status != TaskWaiting || current.DecisionID != d.ID || original.Status != DecisionResolved || original.Answer != "Request changes" || len(snap.Decisions) != 1 {
		t.Fatal("owner continuation changed", current)
	}
}
