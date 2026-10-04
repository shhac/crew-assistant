package core

import (
	"encoding/json"
	"testing"

	"bytes"
	"github.com/shhac/crew-assistant/internal/autopilot"
)

func TestAutopilotAssistantNamedOwnerHasNoOwnerAuthority(t *testing.T) {
	s, cfg := fixture(t)
	cfg.Assistants[0].ID = "owner"
	cfg.Assistant.Seat = "owner"
	cfg.Autopilot.Modes = map[string]autopilot.Mode{"authorised-research": autopilot.Act}
	if err := s.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	p := newProject(t, s)
	d, err := s.CreateDecision(testContext, DecisionInput{ProjectID: p.ID, Title: "Synthetic", Context: "Test", Recommendation: "One", Choices: []string{"One", "Two"}})
	if err != nil {
		t.Fatal(err)
	}
	c := NewAutopilotCoordinator(s, nil)
	f, err := c.Register("authorised-research", "v1", []string{"resolve-choice"}, func(Snapshot, ConcreteAction) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	v, _ := s.Snapshot(testContext)
	digest, _ := DecisionActionDigest(v, d.ID)
	args, _ := json.Marshal(ResolveChoiceArgs{d.ID, "One"})
	a, err := f.Submit(testContext, "collision", "reason", ConcreteAction{Kind: "resolve-choice", ProjectID: p.ID, TargetDigest: digest, Args: args})
	if err != nil || a.Status != "performed" || a.ExecutorKind != AutopilotAssistant || a.Executor != "owner" {
		t.Fatalf("execution: %+v %v", a, err)
	}
	v, _ = s.Snapshot(testContext)
	if v.Decisions[0].AnsweredBy != FromAssistant {
		t.Fatal("profile identity acquired owner authority")
	}
	history, err := c.History(testContext, AutopilotHistoryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range history.Entries {
		if entry.ActorKind != AutopilotAssistant {
			t.Fatalf("ambiguous attribution: %+v", entry)
		}
	}
}

func TestAutopilotChoiceUsesCheckedResolutionAndOwnerPrecedence(t *testing.T) {
	for _, ownerFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "approval", true: "owner-first"}[ownerFirst], func(t *testing.T) {
			s, _ := fixture(t)
			p := newProject(t, s)
			c := NewAutopilotCoordinator(s, nil)
			notifications := 0
			c.OnPerformed(func(a AutopilotAction) { notifications++; _ = c.Catalog() })
			f, err := c.Register("authorised-research", "v1", []string{"resolve-choice"}, func(Snapshot, ConcreteAction) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			d, err := s.CreateDecision(testContext, DecisionInput{ProjectID: p.ID, Title: "Synthetic choice", Context: "Test", Recommendation: "One", Choices: []string{"One", "Two"}})
			if err != nil {
				t.Fatal(err)
			}
			v, _ := s.Snapshot(testContext)
			digest, _ := DecisionActionDigest(v, d.ID)
			args, _ := json.Marshal(ResolveChoiceArgs{d.ID, "One"})
			a, err := f.Submit(testContext, "source", "reason", ConcreteAction{Kind: "resolve-choice", ProjectID: p.ID, TargetDigest: digest, Args: args})
			if err != nil {
				t.Fatal(err)
			}
			if ownerFirst {
				s.ChooseDecision(testContext, d.ID, "Two", FromOwner)
			} else {
				// Numbering another task and taking a PM claim do not change
				// the proposed decision or the brief that authorizes it.
				if err := s.store.update(testContext, func(v *Snapshot) error {
					p := project(v, p.ID)
					p.NextTask++
					p.Attempt++
					p.Claims = append(p.Claims, Claim{Token: "synthetic"})
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			out, err := c.OwnerAction(testContext, a.ID, 1, "approve", nil)
			if err != nil {
				t.Fatal(err)
			}
			v, _ = s.Snapshot(testContext)
			closed := v.Decisions[0]
			if ownerFirst {
				if notifications != 0 {
					t.Fatal("refusal notified work")
				}
				if out.Status != "refused" || closed.Answer != "Two" {
					t.Fatal("autopilot replaced owner answer")
				}
			} else {
				if notifications != 1 {
					t.Fatal("committed choice did not notify work")
				}
				if _, err := c.OwnerAction(testContext, a.ID, 1, "approve", nil); err != nil {
					t.Fatal(err)
				}
				if notifications != 1 {
					t.Fatal("replayed approval notified twice")
				}
				if out.Status != "performed" || closed.Answer != "One" || closed.AnsweredBy != FromOwner {
					t.Fatal("checked choice did not resolve")
				}
				var evidence bytes.Buffer
				if err := s.ExportDecisionEvaluations(testContext, &evidence); err != nil || !bytes.Contains(evidence.Bytes(), []byte(d.ID)) {
					t.Fatal("resolved decision evaluation was not retained atomically")
				}
			}

		})
	}
}

func TestAutopilotRollbackDoesNotNotifyWork(t *testing.T) {
	s, c, f, p := autopilotFixture(t, autopilot.Suggest)
	notifications := 0
	c.OnPerformed(func(AutopilotAction) { notifications++ })
	a, err := f.Submit(testContext, "rollback-notify", "reason", renameProposal(p, "Changed"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.db.Exec(`CREATE TRIGGER reject_notification_audit BEFORE INSERT ON autopilot_audit BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := c.OwnerAction(testContext, a.ID, 1, "approve", nil); err == nil {
		t.Fatal("injected transaction succeeded")
	}
	if notifications != 0 {
		t.Fatal("rolled-back effect notified work")
	}
	if _, err := s.store.db.Exec("DROP TRIGGER reject_notification_audit"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.OwnerAction(testContext, a.ID, 1, "approve", nil); err != nil {
		t.Fatal(err)
	}
	if notifications != 1 {
		t.Fatal("committed retry did not notify")
	}
}

func TestAutopilotCannotUseOrdinaryChoiceForReleaseAuthority(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	d, err := s.CreateDecision(testContext, DecisionInput{ProjectID: p.ID, Title: "Synthetic", Context: "Test", Recommendation: "One", Choices: []string{"One", "Two"}})
	if err != nil {
		t.Fatal(err)
	}
	s.store.update(testContext, func(v *Snapshot) error { decision(v, d.ID).Kind = DecisionRelease; return nil })
	c := NewAutopilotCoordinator(s, nil)
	f, _ := c.Register("authorised-research", "v1", []string{"resolve-choice"}, func(Snapshot, ConcreteAction) error { return nil })
	v, _ := s.Snapshot(testContext)
	digest, _ := DecisionActionDigest(v, d.ID)
	args, _ := json.Marshal(ResolveChoiceArgs{d.ID, "One"})
	out, err := f.Submit(testContext, "source", "reason", ConcreteAction{Kind: "resolve-choice", ProjectID: p.ID, TargetDigest: digest, Args: args})
	if err != nil || out.Status != "refused" {
		t.Fatal("ordinary function acquired release authority")
	}
}

func TestAutopilotCancelledChoiceLeavesDecisionOpen(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	d, err := s.CreateDecision(testContext, DecisionInput{ProjectID: p.ID, Title: "Synthetic choice", Context: "Test", Recommendation: "One", Choices: []string{"One", "Two"}})
	if err != nil {
		t.Fatal(err)
	}
	c := NewAutopilotCoordinator(s, nil)
	f, _ := c.Register("authorised-research", "v1", []string{"resolve-choice"}, func(Snapshot, ConcreteAction) error { return nil })
	v, _ := s.Snapshot(testContext)
	digest, _ := DecisionActionDigest(v, d.ID)
	args, _ := json.Marshal(ResolveChoiceArgs{d.ID, "One"})
	a, err := f.Submit(testContext, "source", "reason", ConcreteAction{Kind: "resolve-choice", ProjectID: p.ID, TargetDigest: digest, Args: args})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.OwnerAction(testContext, a.ID, 1, "cancel", nil); err != nil {
		t.Fatal(err)
	}
	v, _ = s.Snapshot(testContext)
	if v.Decisions[0].Status != DecisionOpen {
		t.Fatal("cancel answered underlying decision")
	}
}

func TestTaskChoiceFreshnessIgnoresDerivedTaskFields(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	target, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Synthetic", Criteria: []string{"Test"}})
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.CreateDecision(testContext, DecisionInput{ProjectID: p.ID, Title: "Synthetic choice", Context: "Test", Recommendation: "One", Choices: []string{"One", "Two"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.update(testContext, func(v *Snapshot) error {
		decision(v, d.ID).TaskID = target.ID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	c := NewAutopilotCoordinator(s, nil)
	f, err := c.Register("authorised-research", "v1", []string{"resolve-choice"}, func(Snapshot, ConcreteAction) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	v, _ := s.Snapshot(testContext)
	digest, _ := DecisionActionDigest(v, d.ID)
	args, _ := json.Marshal(ResolveChoiceArgs{d.ID, "One"})
	a, err := f.Submit(testContext, "task-choice", "reason", ConcreteAction{Kind: "resolve-choice", ProjectID: p.ID, TaskID: target.ID, TargetDigest: digest, Args: args})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Another task", Criteria: []string{"Test"}, DependsOn: []string{target.ID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetProjectPrefix(testContext, p.ID, "OTHER"); err != nil {
		t.Fatal(err)
	}
	v, err = s.Snapshot(testContext)
	if err != nil {
		t.Fatal(err)
	}
	task(&v, target.ID).Ref = "CHANGED-1"
	task(&v, target.ID).Blocks = []string{"another-task"}
	task(&v, target.ID).WaitingOn = []WaitOn{{}}
	unchanged, err := DecisionActionDigest(v, d.ID)
	if err != nil || unchanged != digest {
		t.Fatal("derived task fields changed authority", err)
	}
	task(&v, target.ID).Objective = "Changed authority"
	changed, _ := DecisionActionDigest(v, d.ID)
	if changed == digest {
		t.Fatal("task text did not change authority")
	}
	out, err := c.OwnerAction(testContext, a.ID, 1, "approve", nil)
	if err != nil || out.Status != "performed" {
		t.Fatal(out, err)
	}
}

func TestAutopilotChoiceRefusesReplacedActiveDecision(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	target, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Synthetic", Criteria: []string{"Test"}})
	if err != nil {
		t.Fatal(err)
	}
	input := DecisionInput{Title: "Choice", Context: "Test", Recommendation: "One", Choices: []string{"One", "Two"}}
	d, err := s.OpenTaskDecision(testContext, target.ID, DecisionChoice, input)
	if err != nil {
		t.Fatal(err)
	}
	c := NewAutopilotCoordinator(s, nil)
	f, err := c.Register("authorised-research", "v1", []string{"resolve-choice"}, func(Snapshot, ConcreteAction) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	v, _ := s.Snapshot(testContext)
	digest, _ := DecisionActionDigest(v, d.ID)
	args, _ := json.Marshal(ResolveChoiceArgs{d.ID, "One"})
	a, err := f.Submit(testContext, "replaced-choice", "reason", ConcreteAction{Kind: "resolve-choice", ProjectID: p.ID, TaskID: target.ID, TargetDigest: digest, Args: args})
	if err != nil || a.Status != "proposed" {
		t.Fatalf("proposal: %+v %v", a, err)
	}
	replacement, err := s.OpenTaskDecision(testContext, target.ID, DecisionChoice, input)
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.OwnerAction(testContext, a.ID, 1, "approve", nil)
	if err != nil || out.Status != "refused" {
		t.Fatalf("stale approval: %+v %v", out, err)
	}
	v, err = s.Snapshot(testContext)
	if err != nil {
		t.Fatal(err)
	}
	if decision(&v, d.ID).Status != DecisionOpen || decision(&v, replacement.ID).Status != DecisionOpen || task(&v, target.ID).DecisionID != replacement.ID {
		t.Fatal("stale approval changed decisions")
	}
}
