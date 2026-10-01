package core

import (
	"testing"
	"time"
)

// Moving a task off pull requests decides it again only when it was past its
// checks, and never withdraws a decision that isn't about it going out.
func TestMovingATaskOffPullRequestsDecidesAgainOnlyWhatWasPastItsChecks(t *testing.T) {
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	prs := Playbook{Land: LandPolicy{PullRequests: true, Target: "main", GitHub: "o/r"}}
	open := &Proposal{Number: 7, Pushed: "abc"}
	for name, tc := range map[string]struct {
		task         Task
		decision     string
		wantStatus   string
		dismissed    bool
		closes       bool
		keepProposal bool
	}{
		"implementing, no pull request":  {Task{Status: TaskWriting}, "", TaskWriting, false, false, false},
		"awaiting its pull request":      {Task{Status: TaskAwaiting, Proposal: open}, "", TaskDeciding, false, true, false},
		"waiting on an update to push":   {Task{Status: TaskWaiting, Proposal: open}, DecisionUpdate, TaskDeciding, true, true, false},
		"waiting on a reviewer question": {Task{Status: TaskWaiting, Proposal: open}, DecisionQuestion, TaskWaiting, false, true, false},
	} {
		t.Run(name, func(t *testing.T) {
			pinned := prs
			task := tc.task
			task.ID, task.ProjectID, task.Playbook, task.Approved = "t", "p", &pinned, 1
			task.Revisions = []Revision{{N: 1, Ref: "abc"}}
			task.LandDecision = &LandDecision{Land: true, Revision: 1}
			v := &Snapshot{Projects: []Project{{ID: "p", Playbook: &Playbook{Land: LandPolicy{Via: LandPush, Target: "main"}}, PRChoices: []string{"t"}}}, Tasks: []Task{task}}
			if tc.decision != "" {
				v.Decisions = []Decision{{ID: "d", TaskID: "t", Kind: tc.decision, Status: DecisionOpen}}
				v.Tasks[0].DecisionID = "d"
			}
			choosePRFlow(v, "t", false, "The PM", now)
			got := v.Tasks[0]
			if got.UsesPRs() || got.Playbook.Land.Way() != LandPush || got.Approved != 0 || got.LandDecision != nil || got.Proposal != nil {
				t.Fatalf("not moved off: %+v %+v approved %d", got.Playbook.Land, got.Proposal, got.Approved)
			}
			if got.Status != tc.wantStatus || (got.ClosePR != nil) != tc.closes || len(v.Projects[0].PRChoices) != 0 {
				t.Fatalf("status %s close %+v choices %v", got.Status, got.ClosePR, v.Projects[0].PRChoices)
			}
			if tc.decision != "" && (v.Decisions[0].Status == DecisionDismissed) != tc.dismissed {
				t.Fatalf("decision %s", v.Decisions[0].Status)
			}
		})
	}
}

// Choosing for a task once pull requests are back on changes nothing, but
// the choice is no longer asked for.
func TestAChoiceOncePullRequestsAreBackOnChangesNothing(t *testing.T) {
	pinned := Playbook{Land: LandPolicy{PullRequests: true, Target: "main", GitHub: "o/r"}}
	v := &Snapshot{Projects: []Project{{ID: "p", Playbook: &pinned, PRChoices: []string{"t"}}}, Tasks: []Task{{ID: "t", ProjectID: "p", Status: TaskAwaiting, Playbook: &pinned, Proposal: &Proposal{Number: 7}}}}
	choosePRFlow(v, "t", false, "The PM", time.Now())
	if !v.Tasks[0].UsesPRs() || v.Tasks[0].Proposal == nil || len(v.Projects[0].PRChoices) != 0 {
		t.Fatalf("%+v choices %v", v.Tasks[0].Playbook.Land, v.Projects[0].PRChoices)
	}
}

// Turning pull requests off twice asks the owner about each task once.
func TestEndingPullRequestsTwiceAsksOnce(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	task := queueAll(t, s, p, "A")[0]
	if _, err := s.UpdateTask(testContext, task.ID, func(t *Task, p *Project) (string, error) {
		pinned := *p.Playbook
		pinned.Land = LandPolicy{PullRequests: true, Target: "main", GitHub: "o/r"}
		t.Playbook, t.Status = &pinned, TaskAwaiting
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.EndPullRequests(testContext, p.ID); err != nil {
			t.Fatal(err)
		}
	}
	snap, _ := s.Snapshot(testContext)
	asked := 0
	for _, d := range snap.Decisions {
		if d.TaskID == task.ID && d.Kind == DecisionPRFlow {
			asked++
		}
	}
	if asked != 1 {
		t.Fatalf("asked %d times", asked)
	}
}
