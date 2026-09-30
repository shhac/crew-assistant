package core

import "testing"

// A step proposed for what is already the owner's step, reworded, joins no
// second copy of it.
func TestAnOwnerStepProposedAgainIsKeptOnce(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	task, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Draw an icon", Criteria: []string{"A live run attaches an image"}})
	if err != nil {
		t.Fatal(err)
	}
	propose := func(criterion, step string) Task {
		t.Helper()
		d, err := s.OpenTaskDecision(testContext, task.ID, DecisionEscalation, DecisionInput{Title: "Owner step?", Context: "c", Recommendation: "r", Choices: []string{"Make it an owner step", "Stop"}, OwnerStep: &OwnerStep{Criterion: criterion, Step: step}})
		if err != nil {
			t.Fatal(err)
		}
		out, err := s.MakeOwnerStep(testContext, task.ID, d.ID)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	propose("A live run attaches an image", "After it lands, generate an image and check it is attached")
	again := propose("After it lands, generate an image and check it is attached", "On a real task, generate an image and check it is attached")
	if len(again.OwnerSteps) != 1 || again.OwnerSteps[0] != "After it lands, generate an image and check it is attached" {
		t.Fatalf("owner steps: %q", again.OwnerSteps)
	}
}
