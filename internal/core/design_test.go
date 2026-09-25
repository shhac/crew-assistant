package core

import (
	"errors"
	"testing"
)

func TestADesignHandOffIsRecordedOnceAndGoesBackToWhoeverAsked(t *testing.T) {
	s, _ := fixture(t)
	p := plannedProject(t, s)
	playbook := *p.Playbook
	playbook.Roles = append(playbook.Roles, Role{Name: "Dee", Kinds: []string{RoleDesigner}, Engine: "claude"})
	if _, err := s.SetPlaybook(testContext, p.ID, playbook); err != nil {
		t.Fatal(err)
	}
	queued, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "A draft"})
	s.NextTask(testContext)
	owner := DecisionInput{Title: "More design input?", Context: "Tabs or a sidebar?", Recommendation: "Answer it", Choices: []string{"Use your judgment", "Stop"}}
	ask := DesignAsk{From: "Researcher", Question: "Tabs or a sidebar?", Owner: owner}
	held, err := s.AskDesign(testContext, queued.ID, ask)
	if err != nil || held.Status != TaskDesigning || held.Stage != StageResearching || !held.WithDesigner || held.Checking != "Dee" {
		t.Fatalf("with the designer: %s %s %v %q %v", held.Status, held.Stage, held.WithDesigner, held.Checking, err)
	}
	if _, err := s.AskDesign(testContext, queued.ID, ask); !errors.Is(err, ErrConflict) {
		t.Fatalf("a task already with the designer asked again: %v", err)
	}
	if _, err := s.RecordDesign(testContext, queued.ID, "another", "Dee", "Tabs", nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("input for a request that isn't open was recorded: %v", err)
	}
	back, err := s.RecordDesign(testContext, queued.ID, held.Design[0].ID, "Dee", "A sidebar", nil)
	if err != nil || back.Status != TaskResearching || back.WithDesigner || back.Design[0].Input != "A sidebar" || back.Design[0].Open() {
		t.Fatalf("back to the researcher: %+v %v", back, err)
	}
	if _, err := s.RecordDesign(testContext, queued.ID, held.Design[0].ID, "Dee", "Again", nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("the same input was recorded twice: %v", err)
	}
	// Past the limit the question goes to the owner, and the task waits in
	// the stage that asked.
	s.AskDesign(testContext, queued.ID, ask)
	held, _ = s.RecordDesign(testContext, queued.ID, lastDesign(t, s, queued.ID).ID, "Dee", "Tabs", nil)
	if held.DesignsAt(TaskResearching) != DesignLimit {
		t.Fatalf("%d hand-offs", held.DesignsAt(TaskResearching))
	}
	waiting, err := s.AskDesign(testContext, queued.ID, ask)
	if err != nil || waiting.Status != TaskWaiting || waiting.Stage != StageResearching || waiting.Design[2].Decision != waiting.DecisionID {
		t.Fatalf("past the limit: %s %s %+v %v", waiting.Status, waiting.Stage, waiting.Design, err)
	}
}

func lastDesign(t *testing.T, s *Service, id string) DesignRequest {
	t.Helper()
	snap, _ := s.Snapshot(testContext)
	found, _ := findSnapshotTask(snap, id)
	return found.Design[len(found.Design)-1]
}
