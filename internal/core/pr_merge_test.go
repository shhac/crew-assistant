package core

import (
	"testing"
)

func prTask(t *testing.T, s *Service, p Project, status string) Task {
	t.Helper()
	task := queueAll(t, s, p, "A")[0]
	updated, err := s.UpdateTask(testContext, task.ID, func(t *Task, p *Project) (string, error) {
		pinned := *p.Playbook
		pinned.Land = LandPolicy{PullRequests: true, Target: "main", GitHub: "o/r"}
		t.Playbook, t.Status = &pinned, status
		t.Revisions, t.Approved = []Revision{{N: 1, Ref: "abc"}}, 1
		t.Proposal = &Proposal{Number: 7, Pushed: "abc"}
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

// Only a decision about merging is withdrawn when the pull request changes:
// a question the owner was asked stays.
func TestAChangedPullRequestWithdrawsOnlyAMergeDecision(t *testing.T) {
	for kind, withdrawn := range map[string]bool{DecisionDelivery: true, DecisionQuestion: false, DecisionUpdate: false} {
		s, _ := fixture(t)
		task := prTask(t, s, newProject(t, s), TaskAwaiting)
		d, err := s.OpenTaskDecision(testContext, task.ID, kind, DecisionInput{Title: "t", Context: "c", Recommendation: "r", Choices: []string{"Approve", "Stop"}})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.ReconsiderMerge(testContext, task.ID, "it changed"); err != nil {
			t.Fatal(err)
		}
		snap, _ := s.Snapshot(testContext)
		got, _ := snap.FindTask(task.ID)
		dec := *decision(&snap, d.ID)
		if (dec.Status == DecisionDismissed) != withdrawn || (got.Status == TaskLanding) != withdrawn {
			t.Errorf("%s: decision %s, task %s", kind, dec.Status, got.Status)
		}
	}
}

// The owner merging ahead of the PM approves the merge, not the opening
// again.
func TestTheOwnerMergesAheadOfThePMWithoutApprovingTheOpeningAgain(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	playbook := *p.Playbook
	playbook.Roles = append(playbook.Roles, Role{Name: "QA", Kinds: []string{RoleQA}, Engine: "claude"}, Role{Name: "Pim", Kinds: []string{RolePM}, Engine: "claude"})
	playbook.Land = LandPolicy{PullRequests: true, Target: "main", GitHub: "o/r", Approve: ApprovePM}
	playbook.Medium = MediumGit
	p.Playbook = &playbook
	if err := s.store.update(testContext, func(v *Snapshot) error {
		project(v, p.ID).Settings = &playbook
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	task := prTask(t, s, p, TaskDeciding)
	if _, err := s.UpdateTask(testContext, task.ID, func(t *Task, p *Project) (string, error) {
		t.Playbook.Land.Approve, t.Approved = ApprovePM, 0
		t.Roles = playbook.Roles
		for _, r := range t.Checkers() {
			t.Verdicts = append(t.Verdicts, Verdict{Role: r.Name, Revision: 1, BriefVersion: p.Brief.Version, TextVersion: t.TextVersion, Outcome: VerdictPass})
		}
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.LandAheadOfPM(testContext, p.ID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Proposal.MergeApproved != 1 || got.Approved != 0 || got.Status != TaskLanding {
		t.Fatalf("merge approved %d, approved %d, %s", got.Proposal.MergeApproved, got.Approved, got.Status)
	}
}

// Resuming landing looks again only at this project's pull requests that
// were waiting on it, not one waiting on the owner or another project's.
func TestResumingLandingWakesOnlyThisProjectsWaitingPullRequests(t *testing.T) {
	s, _ := fixture(t)
	p, other := newProject(t, s), newProject(t, s)
	held := prTask(t, s, p, TaskAwaiting)
	asking := prTask(t, s, p, TaskWaiting)
	elsewhere := prTask(t, s, other, TaskAwaiting)
	for _, id := range []string{p.ID, other.ID} {
		if _, err := s.SetLandingPaused(testContext, id, true, "freeze"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SetLandingPaused(testContext, p.ID, false, ""); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{held.ID: TaskLanding, asking.ID: TaskWaiting, elsewhere.ID: TaskAwaiting} {
		if got := onBoard(t, s, id); got.Status != want {
			t.Errorf("%s: %s, want %s", got.Objective, got.Status, want)
		}
	}
}
