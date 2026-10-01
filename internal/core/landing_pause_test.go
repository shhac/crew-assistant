package core

import (
	"strings"
	"testing"
)

// A landing pause holds every landing without pull requests; with them it
// holds only merging, so pull requests still open and are still answered.
func TestALandingPauseHoldsLandingButNotOpeningAPullRequest(t *testing.T) {
	paused := &Project{LandingPaused: &LandingPause{Reason: "release freeze until Friday"}}
	prs := &Playbook{Land: LandPolicy{PullRequests: true, Target: "main", GitHub: "o/r"}}
	push := &Playbook{Land: LandPolicy{Via: LandPush, Target: "main"}}
	for name, tc := range map[string]struct {
		task       Task
		held, step bool
	}{
		"push":           {Task{Playbook: push}, true, true},
		"PR to open":     {Task{Playbook: prs}, false, false},
		"PR open":        {Task{Playbook: prs, Proposal: &Proposal{Number: 7}}, true, false},
		"blocked PR":     {Task{Playbook: prs, Blockers: []Blocker{{Description: "a build"}}}, true, true},
		"push, unpaused": {Task{Playbook: push}, false, false},
	} {
		p := paused
		if strings.HasSuffix(name, "unpaused") {
			p = &Project{}
		}
		held, step := LandingHeld(p, tc.task), landingStepHeld(p, tc.task)
		if (len(held) > 0) != tc.held || (len(step) > 0) != tc.step {
			t.Errorf("%s: held %v step %v", name, held, step)
		}
		if tc.held && name != "blocked PR" && !strings.Contains(strings.Join(held, ";"), "release freeze until Friday") {
			t.Errorf("%s: why %v", name, held)
		}
	}
}

// Resuming lands what the pause held: a pull request waiting only on it is
// looked at again.
func TestResumingLandingLooksAgainAtHeldPullRequests(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	task := queueAll(t, s, p, "A")[0]
	if _, err := s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
		t.Playbook = &Playbook{Land: LandPolicy{PullRequests: true, Target: "main", GitHub: "o/r"}}
		t.Status, t.Proposal = TaskAwaiting, &Proposal{Number: 7}
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetLandingPaused(testContext, p.ID, true, "freeze\nnow"); err == nil {
		t.Fatal("a reason over more than one line was accepted")
	}
	got, err := s.SetLandingPaused(testContext, p.ID, true, " freeze ")
	if err != nil || got.LandingPaused == nil || got.LandingPaused.Reason != "freeze" {
		t.Fatalf("%+v %v", got.LandingPaused, err)
	}
	if b := onBoard(t, s, task.ID); b.Status != TaskAwaiting {
		t.Fatalf("pausing moved the task: %s", b.Status)
	}
	if got, err = s.SetLandingPaused(testContext, p.ID, false, ""); err != nil || got.LandingPaused != nil {
		t.Fatalf("%+v %v", got.LandingPaused, err)
	}
	if b := onBoard(t, s, task.ID); b.Status != TaskLanding {
		t.Fatalf("resuming left the pull request waiting: %s", b.Status)
	}
}
