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

// A requirement of the brief the owner took on for a task stays in the brief,
// so the team may raise it again; proposing it again adds no second step.
func TestABriefRequirementTakenOnIsKeptOnce(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	task, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Write the design"})
	if err != nil {
		t.Fatal(err)
	}
	brief := p.Brief.Criteria[0]
	for _, step := range []string{"Check CI is green on every platform", "Confirm the CI run passes everywhere"} {
		d, err := s.OpenTaskDecision(testContext, task.ID, DecisionEscalation, DecisionInput{Title: "Owner step?", Context: "c", Recommendation: "r", Choices: []string{"Make it an owner step", "Stop"}, OwnerStep: &OwnerStep{Criterion: brief, Step: step}})
		if err != nil {
			t.Fatal(err)
		}
		if task, err = s.MakeOwnerStep(testContext, task.ID, d.ID); err != nil {
			t.Fatal(err)
		}
	}
	if len(task.OwnerSteps) != 1 || len(task.OwnerTook) != 1 || task.OwnerTook[0] != brief {
		t.Fatalf("steps %q, took %q", task.OwnerSteps, task.OwnerTook)
	}
}
