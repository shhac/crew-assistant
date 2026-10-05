package core

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
)

func acceptanceFixture(t *testing.T) (*Service, Project, Task, Decision) {
	t.Helper()
	s, _ := fixture(t)
	p := newProject(t, s)
	task, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Accepted work"})
	if err != nil {
		t.Fatal(err)
	}
	task, err = s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
		t.Status = TaskDeciding
		t.Revisions = []Revision{{N: 1, Ref: "draft"}}
		t.Roles = []Role{{Name: "Review", Kinds: []string{RoleReviewer}}, {Name: "QA", Kinds: []string{RoleQA}}, {Name: "QA2", Kinds: []string{RoleQA}}}
		t.Approved = 1
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.OpenTaskDecision(testContext, task.ID, DecisionEscalation, DecisionInput{
		Title: "Accept?", Context: "Review findings remain", Recommendation: "Accept and follow up",
		Choices: []string{"Accept and follow up", "Accept this draft", "Stop"}, FollowUp: &TaskInput{Objective: "Remaining work", Criteria: []string{"One thing"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	d, err = s.ChooseDecision(testContext, d.ID, "Accept and follow up", FromOwner)
	if err != nil {
		t.Fatal(err)
	}
	return s, p, task, d
}
func acceptForTest(t *Task) string {
	t.Approved = t.Revisions[len(t.Revisions)-1].N
	t.Status, t.DecisionID = TaskLanding, ""
	return "Accepted"
}

func TestAcceptanceKeepsFullFollowUpCriteria(t *testing.T) {
	s, _, source, d := acceptanceFixture(t)
	criteria := []string{strings.Repeat("a", 499) + "界" + strings.Repeat("é", 700), "Short criterion"}
	if err := s.store.update(testContext, func(v *Snapshot) error {
		decision(v, d.ID).FollowUp.Criteria = slices.Clone(criteria)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, follow, err := s.AcceptWithFollowUp(testContext, source.ID, d.ID, acceptForTest)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(follow.Criteria, criteria) {
		t.Fatal("returned follow-up changed criteria")
	}
	snap, err := s.Snapshot(testContext)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(task(&snap, follow.ID).Criteria, criteria) || !slices.Equal(decision(&snap, d.ID).FollowUp.Criteria, criteria) {
		t.Fatal("persisted follow-up changed criteria")
	}
	_, replay, err := s.AcceptWithFollowUp(testContext, source.ID, d.ID, func(*Task) string { panic("replay applied acceptance") })
	if err != nil || replay.ID != follow.ID || !slices.Equal(replay.Criteria, criteria) {
		t.Fatalf("replay changed follow-up: %v", err)
	}
}

func TestAcceptanceCleansFollowUpCriteriaWithoutRemovingDuplicates(t *testing.T) {
	s, _, source, d := acceptanceFixture(t)
	criterion := strings.Repeat("Full criterion café. ", 40) + "End."
	proposed := []string{" \n" + criterion + "\t ", "", " \n\t", criterion}
	want := []string{criterion, criterion}
	if err := s.store.update(testContext, func(v *Snapshot) error {
		decision(v, d.ID).FollowUp.Criteria = slices.Clone(proposed)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, follow, err := s.AcceptWithFollowUp(testContext, source.ID, d.ID, acceptForTest)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(follow.Criteria, want) {
		t.Fatal("follow-up should only trim whitespace and drop empty criteria")
	}
	snap, err := s.Snapshot(testContext)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(task(&snap, follow.ID).Criteria, want) || !slices.Equal(decision(&snap, d.ID).FollowUp.Criteria, proposed) {
		t.Fatal("cleanup changed the decision or persisted criteria")
	}
}

func TestFollowUpStoredCriteriaRemainEditable(t *testing.T) {
	for _, criterion := range []string{strings.Repeat("é", 700), strings.Repeat("a", 500) + "…"} {
		s, p, source, d := acceptanceFixture(t)
		_, follow, err := s.AcceptWithFollowUp(testContext, source.ID, d.ID, acceptForTest)
		if err != nil {
			t.Fatal(err)
		}
		follow, err = s.UpdateTask(testContext, follow.ID, func(t *Task, _ *Project) (string, error) {
			t.Criteria = []string{criterion, "Short criterion"}
			return "", nil
		})
		if err != nil {
			t.Fatal(err)
		}
		requirements := slices.Clone(follow.Criteria)
		slices.Reverse(requirements)
		replay, err := s.EditTask(testContext, EditInput{Project: p.ID, Task: follow.ID, Kind: RolePM, Criteria: requirements})
		if err != nil || !slices.Equal(replay.Criteria, requirements) {
			t.Fatalf("requirements replay changed stored criteria: %v", err)
		}
		added, err := s.EditTask(testContext, EditInput{Project: p.ID, Task: follow.ID, Kind: RoleReviewer, Add: []string{"New requirement"}})
		if err != nil || !slices.Equal(added.Criteria, []string{"Short criterion", criterion, "New requirement"}) {
			t.Fatalf("adding changed stored criteria: %v", err)
		}
		moved, err := s.EditTask(testContext, EditInput{Project: p.ID, Task: follow.ID, Kind: RolePM, OwnerChecks: []string{criterion}})
		if err != nil || !slices.Equal(moved.OwnerChecks, []string{criterion}) || !slices.Equal(moved.Criteria, []string{"Short criterion", "New requirement"}) {
			t.Fatalf("owner transfer changed stored criteria: %v", err)
		}
	}
}

func TestAcceptanceIsAtomicAndReplaySafe(t *testing.T) {
	s, _, task, d := acceptanceFixture(t)
	var wg sync.WaitGroup
	ids := make(chan string, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, follow, err := s.AcceptWithFollowUp(testContext, task.ID, d.ID, acceptForTest)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- follow.ID
		}()
	}
	wg.Wait()
	close(ids)
	id := ""
	for next := range ids {
		if id == "" {
			id = next
		}
		if id != next {
			t.Error("duplicate follow-up")
		}
	}
	snap, err := s.Snapshot(testContext)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Tasks) != 2 || snap.Tasks[1].ID != id || !snap.Tasks[1].HeldByOwner(RelationDependsOn, task.ID) {
		t.Fatalf("follow-up changed: %+v", snap.Tasks)
	}
	accepted := snap.Tasks[0]
	if accepted.Acceptance == nil || !accepted.AcceptanceStands(snap.Projects[0].Brief.Version) {
		t.Fatal("approval and acceptance not committed together")
	}
	if _, err = s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) { t.Status = TaskWriting; return "", nil }); err != nil {
		t.Fatal(err)
	}
	out, follow, err := s.AcceptWithFollowUp(testContext, task.ID, d.ID, func(t *Task) string { panic("replay must not apply acceptance") })
	if err != nil || out.Status != TaskWriting || follow.ID != id {
		t.Fatalf("replay reset moved task: %+v %v", out, err)
	}
}

func TestAcceptanceRejectsStaleOrWrongDecisions(t *testing.T) {
	for _, change := range []string{"revision", "brief", "text", "decision", "disposition", "choice"} {
		t.Run(change, func(t *testing.T) {
			s, _, sourceTask, d := acceptanceFixture(t)
			err := s.store.update(testContext, func(v *Snapshot) error {
				task := task(v, sourceTask.ID)
				decision := decision(v, d.ID)
				switch change {
				case "revision":
					task.Revisions = append(task.Revisions, Revision{N: 2})
				case "brief":
					project(v, task.ProjectID).Brief.Version++
				case "text":
					task.TextVersion++
				case "decision":
					task.DecisionID = "another"
				case "disposition":
					decision.Disposition = DispositionCustom
				case "choice":
					decision.Answer = "Accept this draft"
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			out, _, err := s.AcceptWithFollowUp(testContext, sourceTask.ID, d.ID, acceptForTest)
			if change == "revision" || change == "brief" || change == "text" {
				if err != nil || out.Status != TaskReviewing || out.DecisionID != "" {
					t.Fatalf("stale continuation retained: %+v %v", out, err)
				}
			} else if !errors.Is(err, ErrConflict) {
				t.Fatalf("wrong acceptance allowed: %v", err)
			}
			snap, _ := s.Snapshot(testContext)
			if len(snap.Tasks) != 1 || snap.Tasks[0].Acceptance != nil {
				t.Fatal("partial acceptance")
			}
		})
	}
}

func TestAcceptedMergeSchedulingIsExclusiveThenOnlyQA(t *testing.T) {
	s, p, task, d := acceptanceFixture(t)
	if _, _, err := s.AcceptWithFollowUp(testContext, task.ID, d.ID, acceptForTest); err != nil {
		t.Fatal(err)
	}
	task, err := s.UpdateTask(testContext, task.ID, func(t *Task, p *Project) (string, error) {
		t.Revisions = append(t.Revisions, Revision{N: 2, Ref: "merge", CleanMergeOf: 1})
		t.MergeValidation = &MergeValidation{Revision: 2, Ref: "merge", BriefVersion: p.Brief.Version, TextVersion: t.TextVersion}
		t.Status = TaskReviewing
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	claims := make(chan Scheduled, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := s.Schedule(testContext, anyone)
			if err != nil {
				t.Error(err)
			}
			for _, c := range out {
				claims <- c
			}
		}()
	}
	wg.Wait()
	close(claims)
	var taken []Scheduled
	for c := range claims {
		taken = append(taken, c)
	}
	if len(taken) != 1 || taken[0].Claim.Step != StepValidateMerge || taken[0].Claim.Shared || taken[0].Seat.Name != "" {
		t.Fatalf("command not exclusive: %+v", taken)
	}
	if _, err = s.UpdateTask(Fenced(testContext, task.ID, taken[0].Claim.Token), task.ID, func(t *Task, _ *Project) (string, error) { t.MergeValidation.Checked = true; return "", nil }); err != nil {
		t.Fatal(err)
	}
	if err = s.ReleaseClaim(testContext, task.ID, taken[0].Claim.Token); err != nil {
		t.Fatal(err)
	}
	out, err := s.Schedule(testContext, anyone)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || !slices.ContainsFunc(out, func(c Scheduled) bool { return c.Seat.Name == "QA" }) || !slices.ContainsFunc(out, func(c Scheduled) bool { return c.Seat.Name == "QA2" }) {
		t.Fatalf("required QA groups: %+v", out)
	}
	if next, err := s.Schedule(testContext, anyone); err != nil || len(next) != 0 {
		t.Fatal("duplicate QA", next, err)
	}
	if task.AcceptedMergeReady(p.Brief.Version) {
		t.Fatal("unchecked result authorized delivery")
	}
}

func TestAcceptanceDoesNotAuthorizeChangedWork(t *testing.T) {
	original := Task{Approved: 1, Revisions: []Revision{{N: 1}, {N: 2, CleanMergeOf: 1}, {N: 3, CleanMergeOf: 2}}, Acceptance: &DraftAcceptance{Revision: 1, BriefVersion: 2, TextVersion: 4}, TextVersion: 4}
	if !original.AcceptanceStands(2) {
		t.Fatal("two clean catch-ups lost acceptance")
	}
	for _, change := range []string{"brief", "text", "direction", "foreign", "new draft", "stopped", "wrong approval"} {
		t.Run(change, func(t *testing.T) {
			task := original
			task.Revisions = slices.Clone(original.Revisions)
			brief := 2
			switch change {
			case "brief":
				brief++
			case "text":
				task.TextVersion++
			case "direction":
				task.DirectionPending = 1
			case "foreign":
				task.Revisions[1].CleanMergeOf = 0
			case "new draft":
				task.Revisions = append(task.Revisions, Revision{N: 4})
			case "stopped":
				task.Status = TaskStopped
			case "wrong approval":
				task.Approved = 2
			}
			if task.AcceptanceStands(brief) {
				t.Fatal("changed work reused acceptance")
			}
			if len(task.AcceptanceCheckers(brief)) != len(task.Checkers()) {
				t.Fatal("ordinary checks skipped")
			}
		})
	}
}

func TestAcceptanceAndFailureTransactionsRollBackTogether(t *testing.T) {
	s, p, task, d := acceptanceFixture(t)
	refuse := func() {
		t.Helper()
		if _, err := s.store.db.Exec("CREATE TRIGGER refuse_acceptance BEFORE UPDATE ON state BEGIN SELECT RAISE(ABORT, 'injected'); END"); err != nil {
			t.Fatal(err)
		}
	}
	allow := func() {
		t.Helper()
		if _, err := s.store.db.Exec("DROP TRIGGER refuse_acceptance"); err != nil {
			t.Fatal(err)
		}
	}
	refuse()
	if _, _, err := s.AcceptWithFollowUp(testContext, task.ID, d.ID, acceptForTest); err == nil {
		t.Fatal("injected commit failure ignored")
	}
	snap, _ := s.Snapshot(testContext)
	if len(snap.Tasks) != 1 || snap.Tasks[0].Acceptance != nil || snap.Decisions[0].Applied {
		t.Fatal("partial acceptance persisted")
	}
	allow()
	accepted, follow, err := s.AcceptWithFollowUp(testContext, task.ID, d.ID, acceptForTest)
	if err != nil || follow.ID == "" {
		t.Fatal(err)
	}
	in := DecisionInput{Title: "Conflicting catch-up", Context: "Main conflicts with this draft", Recommendation: "Resolve", Choices: []string{"Resolve", "Stop"}}
	refuse()
	if _, err := s.FailAcceptedCatchUp(testContext, task.ID, 1, p.Brief.Version, accepted.TextVersion, true, in); err == nil {
		t.Fatal("injected decision failure ignored")
	}
	snap, _ = s.Snapshot(testContext)
	if !snap.Tasks[0].AcceptanceStands(p.Brief.Version) || snap.Tasks[0].Status != TaskLanding || len(snap.Decisions) != 1 {
		t.Fatal("conflict lost acceptance without a decision")
	}
	allow()
	failure, err := s.FailAcceptedCatchUp(testContext, task.ID, 1, p.Brief.Version, accepted.TextVersion, true, in)
	if err != nil {
		t.Fatal(err)
	}
	snap, _ = s.Snapshot(testContext)
	if snap.Tasks[0].Acceptance != nil || snap.Tasks[0].Approved != 0 || snap.Tasks[0].Status != TaskWaiting || snap.Tasks[0].DecisionID != failure.ID || len(snap.Tasks) != 2 {
		t.Fatal("conflict transition incomplete")
	}
}

func TestAcceptedMergeEvidenceMustMatchTheWork(t *testing.T) {
	original := Task{Approved: 1, TextVersion: 3, Revisions: []Revision{{N: 1, Ref: "draft"}, {N: 2, Ref: "merge", CleanMergeOf: 1}},
		Acceptance:      &DraftAcceptance{Revision: 1, BriefVersion: 4, TextVersion: 3},
		MergeValidation: &MergeValidation{Revision: 2, Ref: "merge", BriefVersion: 4, TextVersion: 3, Checked: true}}
	if !original.AcceptedMergeReady(4) {
		t.Fatal("matching evidence should pass")
	}
	for _, change := range []string{"revision", "commit", "brief", "text", "unfinished", "failed"} {
		t.Run(change, func(t *testing.T) {
			task := original
			validation := *original.MergeValidation
			task.MergeValidation = &validation
			switch change {
			case "revision":
				validation.Revision = 1
			case "commit":
				validation.Ref = "another"
			case "brief":
				validation.BriefVersion = 3
			case "text":
				validation.TextVersion = 2
			case "unfinished":
				validation.Checked = false
			case "failed":
				validation.Failure = "check failed"
			}
			if task.AcceptedMergeReady(4) {
				t.Fatal("unmatched evidence authorized landing")
			}
		})
	}
}

func TestAcceptedMergeFailureRecoveryCannotScheduleReviews(t *testing.T) {
	s, p, task, d := acceptanceFixture(t)
	if _, _, err := s.AcceptWithFollowUp(testContext, task.ID, d.ID, acceptForTest); err != nil {
		t.Fatal(err)
	}
	task, err := s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
		t.Status = TaskReviewing
		t.Revisions = append(t.Revisions, Revision{N: 2, Ref: "merge", CleanMergeOf: 1})
		t.MergeValidation = &MergeValidation{Revision: 2, Ref: "merge", BriefVersion: p.Brief.Version, TextVersion: t.TextVersion, Checked: true, Failure: "exit 2"}
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.db.Exec("CREATE TRIGGER refuse_failure BEFORE UPDATE ON state BEGIN SELECT RAISE(ABORT, 'injected'); END"); err != nil {
		t.Fatal(err)
	}
	in := DecisionInput{Title: "Merged check failed", Context: "exit 2", Recommendation: "Retry", Choices: []string{"Retry", "Stop"}}
	if _, err := s.FailAcceptedCatchUp(testContext, task.ID, 2, p.Brief.Version, task.TextVersion, false, in); err == nil {
		t.Fatal("failed decision persistence ignored")
	}
	if _, err := s.store.db.Exec("DROP TRIGGER refuse_failure"); err != nil {
		t.Fatal(err)
	}
	scheduled, err := s.Schedule(testContext, anyone)
	if err != nil || len(scheduled) != 1 || scheduled[0].Claim.Step != StepValidateMerge {
		t.Fatalf("failure recovery offered reviews: %+v %v", scheduled, err)
	}
	failure, err := s.FailAcceptedCatchUp(Fenced(testContext, task.ID, scheduled[0].Claim.Token), task.ID, 2, p.Brief.Version, task.TextVersion, false, in)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := s.FailAcceptedCatchUp(testContext, task.ID, 2, p.Brief.Version, task.TextVersion, false, in)
	if err != nil || repeated.ID != failure.ID {
		t.Fatal("duplicate failure decision", err)
	}
	snap, _ := s.Snapshot(testContext)
	if len(snap.Decisions) != 2 || len(snap.Tasks) != 2 || snap.Tasks[0].ResumeStatus != TaskReviewing {
		t.Fatal("failure recovery lost history")
	}
}

func TestChangedAcceptedWorkReturnsToChecksEvenWithoutAnApprovalGate(t *testing.T) {
	for _, change := range []string{"text", "brief", "direction", "revision"} {
		t.Run(change, func(t *testing.T) {
			s, _, task, d := acceptanceFixture(t)
			if _, _, err := s.AcceptWithFollowUp(testContext, task.ID, d.ID, acceptForTest); err != nil {
				t.Fatal(err)
			}
			if _, err := s.UpdateTask(testContext, task.ID, func(t *Task, p *Project) (string, error) {
				book := *p.Playbook
				book.Land.Approve = ApproveNone
				t.Playbook = &book
				switch change {
				case "text":
					t.TextVersion++
				case "brief":
					p.Brief.Version++
				case "direction":
					t.DirectionPending = 1
				case "revision":
					t.Revisions = append(t.Revisions, Revision{N: 2, Ref: "new work"})
				}
				return "", nil
			}); err != nil {
				t.Fatal(err)
			}
			steps, err := s.Schedule(testContext, anyone)
			if err != nil {
				t.Fatal(err)
			}
			snap, _ := s.Snapshot(testContext)
			now := snap.Tasks[0]
			if now.Status != TaskReviewing || now.Acceptance != nil || now.Approved != 0 {
				t.Fatalf("changed accepted work retained landing: %+v", now)
			}
			for _, step := range steps {
				if step.Claim.Step == TaskLanding || step.Claim.Step == StepValidateMerge {
					t.Fatal("changed work bypassed ordinary checks")
				}
			}
		})
	}
}

func TestUnstampedOwnerDecisionsStillApply(t *testing.T) {
	for _, choice := range []string{ChoiceAcceptDraft, ChoiceAcceptFollowUp} {
		t.Run(choice, func(t *testing.T) {
			s, _, task, d := acceptanceFixture(t)
			if err := s.store.update(testContext, func(v *Snapshot) error {
				old := decision(v, d.ID)
				old.Status, old.Answer = DecisionOpen, ""
				old.Revision, old.BriefVersion, old.TextVersion = 0, 0, 0
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Schedule(testContext, anyone); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ChooseDecision(testContext, d.ID, choice, FromOwner); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				accepted, _, err := s.AcceptDraft(testContext, task.ID, d.ID, choice == ChoiceAcceptFollowUp, acceptForTest)
				if err != nil || accepted.Acceptance == nil {
					t.Fatalf("legacy acceptance rejected: %+v %v", accepted, err)
				}
			}
			snap, _ := s.Snapshot(testContext)
			want := 1
			if choice == ChoiceAcceptFollowUp {
				want = 2
			}
			if len(snap.Tasks) != want {
				t.Fatal("duplicate or missing follow-up", len(snap.Tasks))
			}
		})
	}
	for _, kind := range []string{DecisionDelivery, DecisionUpdate} {
		t.Run(kind, func(t *testing.T) {
			s, _, item, _ := acceptanceFixture(t)
			if err := s.store.update(testContext, func(v *Snapshot) error {
				v.Decisions = nil
				current := task(v, item.ID)
				current.DecisionID, current.Status = "", TaskDeciding
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			d, err := s.OpenTaskDecision(testContext, item.ID, kind, DecisionInput{Title: "Approve", Context: "Ready", Recommendation: "Approve", Choices: []string{"Approve", "Request changes"}})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.store.update(testContext, func(v *Snapshot) error {
				old := decision(v, d.ID)
				old.Revision, old.BriefVersion, old.TextVersion = 0, 0, 0
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Schedule(testContext, anyone); err != nil {
				t.Fatal(err)
			}
			snap, _ := s.Snapshot(testContext)
			if decision(&snap, d.ID).Status != DecisionOpen {
				t.Fatal("legacy approval retired")
			}
			if _, err := s.ChooseDecision(testContext, d.ID, "Approve", FromOwner); err != nil {
				t.Fatal(err)
			}
			if err := s.ApplyDeliveryApproval(testContext, item.ID, d.ID, acceptForTest); err != nil {
				t.Fatal(err)
			}
			if taskNow(t, s, item.ID).Status != TaskLanding {
				t.Fatal("legacy approval did not apply")
			}
		})
	}
}

func TestDecisionVersionMatchingOnlyExemptsWhollyUnstampedDecisions(t *testing.T) {
	current := Task{TextVersion: 3, Revisions: []Revision{{N: 1}}}
	for _, d := range []Decision{{Revision: 1}, {BriefVersion: 2}, {TextVersion: 3}} {
		if d.matchesWork(&current, 2) {
			t.Fatalf("partial stamps treated as legacy: %+v", d)
		}
	}
	if !(Decision{Revision: 1, BriefVersion: 2, TextVersion: 3}).matchesWork(&current, 2) {
		t.Fatal("current stamps rejected")
	}
}
