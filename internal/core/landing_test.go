package core

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestLandingApprovalDefaultsToTheOwnerAndThePMDecidesOnlyForAPush(t *testing.T) {
	for _, tc := range []struct {
		land          LandPolicy
		asks, pmDecid bool
	}{
		{LandPolicy{}, true, false},
		{LandPolicy{Via: LandPush, Target: "main"}, true, false},
		{LandPolicy{Via: LandPush, Target: "main", Approve: ApproveBefore}, true, false},
		{LandPolicy{Via: LandPush, Target: "main", Approve: ApproveNone}, false, false},
		{LandPolicy{Via: LandPush, Target: "main", Approve: ApprovePM}, true, true},
		{LandPolicy{Via: LandPullRequest, Target: "main", Approve: ApprovePM}, true, false},
		{LandPolicy{Approve: ApprovePM}, true, false},
	} {
		if tc.land.AsksFirst() != tc.asks || tc.land.ByPM() != tc.pmDecid {
			t.Errorf("%+v: asks %v, by PM %v", tc.land, tc.land.AsksFirst(), tc.land.ByPM())
		}
	}
}

// pmLandingTask is a task on a push project whose PM decides what lands,
// with draft 1 checked by a reviewer and QA with these outcomes, waiting
// for the decision.
func pmLandingTask(t *testing.T, s *Service, reviewer, qa string) (Project, Task) {
	t.Helper()
	p := newProject(t, s)
	playbook := *p.Playbook
	playbook.Medium, playbook.Repo, playbook.BranchPrefix, playbook.Check = MediumGit, "/work/repo", "crew/", "make check"
	playbook.Roles = []Role{
		{Name: "Implementer", Kinds: []string{RoleImplementer}, Engine: "claude"},
		{Name: "Reviewer", Kinds: []string{RoleReviewer}, Engine: "codex"},
		{Name: "QA", Kinds: []string{RoleQA}, Engine: "codex"},
		{Name: "Pim", Kinds: []string{RolePM}, Engine: "claude"},
	}
	playbook.Land = LandPolicy{Via: LandPush, Target: "main", Approve: ApprovePM}
	p, err := s.SetPlaybook(testContext, p.ID, playbook)
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Cache"})
	if err != nil {
		t.Fatal(err)
	}
	task, err = s.UpdateTask(testContext, task.ID, func(t *Task, p *Project) (string, error) {
		pinned := *p.Playbook
		t.Playbook, t.Roles, t.Round, t.MaxRounds = &pinned, pinned.Roles, 1, pinned.MaxRounds
		t.Revisions = []Revision{{N: 1, BriefVersion: p.Brief.Version, Ref: "abc"}}
		t.Verdicts = []Verdict{
			{Revision: 1, Role: "Reviewer", BriefVersion: p.Brief.Version, Outcome: reviewer},
			{Revision: 1, Role: "QA", BriefVersion: p.Brief.Version, Outcome: qa},
		}
		t.Status = TaskDeciding
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return p, task
}

func signedOffNow(t *testing.T, s *Service, id string) []string {
	t.Helper()
	snap, _ := s.Snapshot(testContext)
	i := slices.IndexFunc(snap.Tasks, func(task Task) bool { return task.ID == id })
	return SignedOff(snap, snap.Tasks[i])
}

func TestAChangeIsSignedOffOnlyWhenEveryCheckPassedAndNothingWaits(t *testing.T) {
	s, _ := fixture(t)
	_, passed := pmLandingTask(t, s, VerdictPass, VerdictPass)
	if why := signedOffNow(t, s, passed.ID); len(why) != 0 {
		t.Fatalf("a passed change was not signed off: %v", why)
	}
	_, failedQA := pmLandingTask(t, s, VerdictPass, VerdictRevise)
	if why := signedOffNow(t, s, failedQA.ID); len(why) != 1 || !strings.Contains(why[0], "QA has not passed") {
		t.Fatalf("QA's failure: %v", why)
	}
	_, unreviewed := pmLandingTask(t, s, VerdictPass, VerdictPass)
	s.UpdateTask(testContext, unreviewed.ID, func(t *Task, _ *Project) (string, error) {
		t.Verdicts = t.Verdicts[1:]
		t.DirectionPending = 1
		return "", nil
	})
	if why := signedOffNow(t, s, unreviewed.ID); len(why) != 2 || !strings.Contains(why[0], "Reviewer has not passed") || !strings.Contains(why[1], "direction") {
		t.Fatalf("no review and direction pending: %v", why)
	}
	// Something waiting on the owner, and a dependency that hasn't landed.
	p, waiting := pmLandingTask(t, s, VerdictPass, VerdictPass)
	dep, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Schema"})
	s.UpdateTask(testContext, waiting.ID, func(t *Task, _ *Project) (string, error) {
		t.DependsOn = []string{dep.ID}
		return "", nil
	})
	if _, err := s.OpenTaskDecision(testContext, waiting.ID, DecisionQuestion, DecisionInput{Title: "Which cache?", Context: "c", Recommendation: "r", Choices: []string{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
	why := signedOffNow(t, s, waiting.ID)
	if len(why) != 2 || !strings.Contains(why[0], "Which cache?") || !strings.Contains(why[1], "“Schema”") {
		t.Fatalf("an open decision and an unlanded dependency: %v", why)
	}
	// A stopped dependency has not landed either.
	s.UpdateTask(testContext, dep.ID, func(t *Task, _ *Project) (string, error) {
		t.Status = TaskStopped
		return "", nil
	})
	if why := signedOffNow(t, s, waiting.ID); !slices.ContainsFunc(why, func(w string) bool { return strings.Contains(w, "“Schema”") }) {
		t.Fatalf("a stopped dependency counted as landed: %v", why)
	}
}

func TestThePMsDecisionIsRecordedWithItsReason(t *testing.T) {
	s, _ := fixture(t)
	_, task := pmLandingTask(t, s, VerdictPass, VerdictPass)
	landed, err := s.DecideLanding(testContext, task.ID, LandDecision{Land: true, Reason: "nothing waits on it", Revision: 1}, DecisionInput{})
	if err != nil {
		t.Fatal(err)
	}
	// Naming no way lands it as one commit, as push landings always did.
	if landed.Status != TaskLanding || landed.Approved != 1 || landed.LandDecision == nil || landed.LandDecision.By != LandByPM || landed.LandDecision.Reason != "nothing waits on it" || landed.LandDecision.Method != LandSquash {
		t.Fatalf("landing %+v", landed)
	}
	// Once decided, it is not decided again.
	if _, err = s.DecideLanding(testContext, task.ID, LandDecision{Land: true, Reason: "again", Revision: 1}, DecisionInput{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("decided twice: %v", err)
	}

	_, held := pmLandingTask(t, s, VerdictPass, VerdictPass)
	hold := DecisionInput{Title: "Pim held Cache", Context: "c", Recommendation: "r", Choices: []string{"Approve", "Request changes"}}
	got, err := s.DecideLanding(testContext, held.ID, LandDecision{Land: false, Reason: "the schema lands first", Revision: 1}, hold)
	if err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(testContext)
	d := snap.Decisions[slices.IndexFunc(snap.Decisions, func(d Decision) bool { return d.ID == got.DecisionID })]
	if got.Status != TaskWaiting || got.Approved != 0 || d.Kind != DecisionDelivery || got.LandDecision.Land {
		t.Fatalf("holding %+v %+v", got, d)
	}
	// Deciding to land is an approval: nothing has landed yet.
	for _, want := range []string{"The PM approved Cache to land as one commit: nothing waits on it", "The PM held Cache: the schema lands first"} {
		if !slices.ContainsFunc(snap.Activity, func(a Activity) bool { return a.Summary == want && a.Kind == activityPMLanding }) {
			t.Errorf("no activity %q", want)
		}
	}
	if slices.ContainsFunc(snap.Activity, func(a Activity) bool { return strings.Contains(a.Summary, "landed") }) {
		t.Error("the activity says a change landed that has only been approved")
	}
}

// Whatever stops a change being signed off while the PM decides, its land
// answer is refused when recorded, and the task is left as it was. Each case
// keeps the task deciding, so only the signed-off check can refuse it.
func TestThePMsLandIsRefusedWhenTheSignOffLapsesMeanwhile(t *testing.T) {
	for name, tc := range map[string]struct {
		lapse func(t *testing.T, s *Service, p Project, task Task)
		want  string
	}{
		"the reviewer asks for changes": {
			lapse: func(t *testing.T, s *Service, _ Project, task Task) {
				s.UpdateTask(testContext, task.ID, func(t *Task, p *Project) (string, error) {
					t.Verdicts = append(t.Verdicts, Verdict{Revision: 1, Role: "Reviewer", BriefVersion: p.Brief.Version, Outcome: VerdictRevise})
					return "", nil
				})
			},
			want: "Reviewer has not passed draft 1",
		},
		"the reviewer has not judged it": {
			lapse: func(t *testing.T, s *Service, _ Project, task Task) {
				s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
					t.Verdicts = t.Verdicts[1:]
					return "", nil
				})
			},
			want: "Reviewer has not passed draft 1",
		},
		"QA has not judged it": {
			lapse: func(t *testing.T, s *Service, _ Project, task Task) {
				s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
					t.Verdicts = t.Verdicts[:1]
					return "", nil
				})
			},
			want: "QA has not passed draft 1",
		},
		"an owner decision opens": {
			lapse: func(t *testing.T, s *Service, _ Project, task Task) {
				if _, err := s.OpenTaskDecision(testContext, task.ID, DecisionQuestion, DecisionInput{Title: "Which cache?", Context: "c", Recommendation: "r", Choices: []string{"a", "b"}}); err != nil {
					t.Fatal(err)
				}
				s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
					t.Status = TaskDeciding
					return "", nil
				})
			},
			want: "a decision for the owner is open: Which cache?",
		},
		"the owner gives direction": {
			lapse: func(t *testing.T, s *Service, _ Project, task Task) {
				s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
					t.DirectionPending = 1
					return "", nil
				})
			},
			want: "the owner's direction",
		},
		"it comes to depend on a change that hasn't landed": {
			lapse: func(t *testing.T, s *Service, p Project, task Task) {
				dep, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Schema"})
				if err != nil {
					t.Fatal(err)
				}
				s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
					t.DependsOn = []string{dep.ID}
					return "", nil
				})
			},
			want: "“Schema”, which it depends on, has not landed",
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := fixture(t)
			p, task := pmLandingTask(t, s, VerdictPass, VerdictPass)
			if why := signedOffNow(t, s, task.ID); len(why) != 0 {
				t.Fatalf("not signed off to begin with: %v", why)
			}
			tc.lapse(t, s, p, task)
			_, err := s.DecideLanding(testContext, task.ID, LandDecision{Land: true, Reason: "fine", Revision: 1}, DecisionInput{})
			if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("the land was not refused for %q: %v", tc.want, err)
			}
			snap, _ := s.Snapshot(testContext)
			got, _ := findTaskByID(snap, task.ID)
			if got.Status != TaskDeciding || got.Approved != 0 || got.LandDecision != nil {
				t.Fatalf("a refused land changed the task: %+v", got)
			}
			if slices.ContainsFunc(snap.Activity, func(a Activity) bool { return a.Kind == activityPMLanding }) {
				t.Fatal("a refused land was recorded in the activity")
			}
		})
	}
}

func TestThePMCannotLandWhatIsNotSignedOffOrNoLongerItsToDecide(t *testing.T) {
	s, _ := fixture(t)
	_, failed := pmLandingTask(t, s, VerdictPass, VerdictRevise)
	if _, err := s.DecideLanding(testContext, failed.ID, LandDecision{Land: true, Reason: "fine", Revision: 1}, DecisionInput{}); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "QA has not passed") {
		t.Fatalf("landed a change QA failed: %v", err)
	}
	// A draft other than the latest is never landed.
	_, task := pmLandingTask(t, s, VerdictPass, VerdictPass)
	if _, err := s.DecideLanding(testContext, task.ID, LandDecision{Land: true, Reason: "fine", Revision: 2}, DecisionInput{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("landed a draft that is not the latest: %v", err)
	}
	// The owner takes the decision back.
	p, task := pmLandingTask(t, s, VerdictPass, VerdictPass)
	playbook := *p.Playbook
	playbook.Land.Approve = ApproveBefore
	if _, err := s.SetPlaybook(testContext, p.ID, playbook); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DecideLanding(testContext, task.ID, LandDecision{Land: true, Reason: "fine", Revision: 1}, DecisionInput{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("the PM landed after the owner took the decision back: %v", err)
	}
	snap, _ := s.Snapshot(testContext)
	for _, got := range snap.Tasks {
		if got.Approved != 0 || got.LandDecision != nil && got.LandDecision.Reason == "fine" {
			t.Fatalf("a refused decision was recorded: %+v", got)
		}
	}
}

func TestThePMChoosesHowAChangeLands(t *testing.T) {
	s, _ := fixture(t)
	_, task := pmLandingTask(t, s, VerdictPass, VerdictPass)
	if _, err := s.DecideLanding(testContext, task.ID, LandDecision{Land: true, Method: "rebase", Reason: "fine", Revision: 1}, DecisionInput{}); err == nil {
		t.Fatal("a way to land that isn't squash or fast-forward was accepted")
	}
	kept, err := s.DecideLanding(testContext, task.ID, LandDecision{Land: true, Method: LandKeepCommits, Reason: "each commit stands alone", Revision: 1}, DecisionInput{})
	if err != nil || kept.LandDecision.Method != LandKeepCommits {
		t.Fatalf("keeping the commits: %+v %v", kept.LandDecision, err)
	}
	snap, _ := s.Snapshot(testContext)
	if !slices.ContainsFunc(snap.Activity, func(a Activity) bool {
		return a.Summary == "The PM approved Cache to land keeping its commits: each commit stands alone"
	}) {
		t.Fatal("how it landed is not in the activity")
	}
}

func TestTheOwnerCanLandASignedOffChangeAheadOfThePM(t *testing.T) {
	s, _ := fixture(t)
	p, task := pmLandingTask(t, s, VerdictPass, VerdictPass)
	snap, _ := s.Snapshot(testContext)
	if got, _ := findTaskByID(snap, task.ID); !got.PMDeciding {
		t.Fatal("a signed-off change waiting on the PM isn't marked so")
	}
	landed, err := s.LandAheadOfPM(testContext, p.ID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if landed.Status != TaskLanding || landed.Approved != 1 || landed.LandDecision != nil || landed.PMDeciding {
		t.Fatalf("landing %+v", landed)
	}
	// The PM's decision, arriving after, changes nothing.
	if _, err = s.DecideLanding(testContext, task.ID, LandDecision{Land: false, Reason: "hold", Revision: 1}, DecisionInput{Title: "t", Context: "c", Recommendation: "r", Choices: []string{"a", "b"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("the PM overrode the owner: %v", err)
	}
	// A change the checks haven't signed off is not the owner's to land this way.
	p, failed := pmLandingTask(t, s, VerdictPass, VerdictRevise)
	snap, _ = s.Snapshot(testContext)
	if got, _ := findTaskByID(snap, failed.ID); got.PMDeciding {
		t.Fatal("a change QA failed is marked as waiting on the PM")
	}
	if _, err = s.LandAheadOfPM(testContext, p.ID, failed.ID); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "QA has not passed") {
		t.Fatalf("landed a change QA failed: %v", err)
	}
}

func findTaskByID(snap Snapshot, id string) (Task, bool) {
	i := slices.IndexFunc(snap.Tasks, func(t Task) bool { return t.ID == id })
	if i < 0 {
		return Task{}, false
	}
	return snap.Tasks[i], true
}
