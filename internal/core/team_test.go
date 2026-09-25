package core

import (
	"errors"
	"strings"
	"testing"
)

func startedTask(t *testing.T, s *Service) (Project, Task) {
	t.Helper()
	p := newProject(t, s)
	if _, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "A draft"}); err != nil {
		t.Fatal(err)
	}
	task, _, err := s.NextTask(testContext)
	if err != nil {
		t.Fatal(err)
	}
	return p, task
}

func TestAMessageReachesOnlySomeoneOnTheTeam(t *testing.T) {
	s, _ := fixture(t)
	p, task := startedTask(t, s)
	if _, err := s.SendTeamMessage(testContext, p.ID, task.ID, "Designer", FromOwner, "hello"); err == nil || !strings.Contains(err.Error(), "Writer") {
		t.Fatalf("a stranger was messaged: %v", err)
	}
	if _, err := s.SendTeamMessage(testContext, p.ID, task.ID, "reviewer", FromOwner, "check it"); !errors.Is(err, ErrConflict) {
		t.Fatalf("a reviewer was asked to check nothing: %v", err)
	}
	m, err := s.SendTeamMessage(testContext, p.ID, task.ID, "implementer", FromAssistant, "Keep it short")
	if err != nil || m.To != "Writer" || m.Status != MessageWaiting {
		t.Fatalf("message %+v %v", m, err)
	}
	snap, _ := s.Snapshot(testContext)
	if got := snap.Tasks[0]; got.Direction[0] != "Keep it short" || got.DirectionPending != 1 || got.Status != TaskWriting {
		t.Fatalf("the writer's direction %+v", got)
	}

	two := []Role{{Name: "Tone", Kinds: []string{RoleReviewer}}, {Name: "Facts", Kinds: []string{RoleReviewer}}}
	if _, err := addressee(two, "reviewer"); err == nil || !strings.Contains(err.Error(), "Tone, Facts") {
		t.Fatalf("an ambiguous kind was accepted: %v", err)
	}
	if r, err := addressee(two, "facts"); err != nil || r.Name != "Facts" {
		t.Fatalf("a name was not found: %+v %v", r, err)
	}
}

func TestAMessageToTheImplementerAnswersWhatTheTaskWaitsOn(t *testing.T) {
	s, _ := fixture(t)
	p, task := startedTask(t, s)
	// A delivery comes after a first draft.
	s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
		t.Revisions = append(t.Revisions, Revision{N: 1})
		return "", nil
	})
	d, err := s.OpenTaskDecision(testContext, task.ID, "delivery", DecisionInput{Title: "Ready", Context: "c", Recommendation: "Approve", Choices: []string{"Approve", "Request changes"}})
	if err != nil {
		t.Fatal(err)
	}
	// Text that names a choice is still only direction.
	if _, err = s.SendTeamMessage(testContext, p.ID, task.ID, "Writer", FromOwner, "Approve"); err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(testContext)
	got := snap.Tasks[0]
	if got.Status != TaskWriting || got.Round != 2 || got.DecisionID != "" || got.Direction[0] != "Approve" {
		t.Fatalf("the task did not go back to the writer: %+v", got)
	}
	for _, closed := range snap.Decisions {
		if closed.ID == d.ID && (closed.Status != "resolved" || closed.Disposition != "custom") {
			t.Fatalf("the approval was not answered with the message: %+v", closed)
		}
	}

	// A failed landing retried after a message goes back to the writer, not
	// straight to landing without it.
	s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
		t.ResumeStatus = TaskLanding
		return "", nil
	})
	if _, err = s.OpenTaskDecision(testContext, task.ID, "failure", DecisionInput{Title: "Couldn't land", Context: "c", Recommendation: "Try again", Choices: []string{"Try again", "Stop"}}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SendTeamMessage(testContext, p.ID, task.ID, "Writer", FromOwner, "Also fix the title"); err != nil {
		t.Fatal(err)
	}
	snap, _ = s.Snapshot(testContext)
	if got = snap.Tasks[0]; got.Status != TaskWaiting || got.ResumeStatus != TaskWriting || got.Round != 3 {
		t.Fatalf("the retry would still go straight to landing: %+v", got)
	}

	s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
		t.Status = TaskLanding
		return "", nil
	})
	if _, err = s.SendTeamMessage(testContext, p.ID, task.ID, "Writer", FromOwner, "wait"); !errors.Is(err, ErrConflict) {
		t.Fatalf("a message reached a task mid-landing: %v", err)
	}
	s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
		t.Status = TaskAwaiting
		return "", nil
	})
	if _, err = s.SendTeamMessage(testContext, p.ID, task.ID, "Writer", FromOwner, "One more change"); err != nil {
		t.Fatal(err)
	}
	snap, _ = s.Snapshot(testContext)
	if got = snap.Tasks[0]; got.Status != TaskWriting || got.Round != 4 {
		t.Fatalf("a task waiting on its pull request did not go back to the writer: %+v", got)
	}
}

func TestOpenMessagesCloseWhenTheTaskFinishes(t *testing.T) {
	s, _ := fixture(t)
	p, task := startedTask(t, s)
	if _, err := s.SendTeamMessage(testContext, p.ID, task.ID, "Writer", FromOwner, "note"); err != nil {
		t.Fatal(err)
	}
	stopped, err := s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
		t.Status = TaskStopped
		return "", nil
	})
	if err != nil || stopped.Messages[0].Status != MessageClosed {
		t.Fatalf("message %+v %v", stopped.Messages, err)
	}
	if _, err = s.SendTeamMessage(testContext, p.ID, task.ID, "Writer", FromOwner, "late"); !errors.Is(err, ErrConflict) {
		t.Fatalf("a finished task took a message: %v", err)
	}
}
