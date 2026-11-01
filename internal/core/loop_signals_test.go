package core

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func loopFixture() Snapshot {
	at := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	return Snapshot{Projects: []Project{{ID: "p", Brief: Brief{Version: 1}}}, Tasks: []Task{{
		ID: "t", ProjectID: "p", Status: TaskReviewing, Round: 4, MaxRounds: 4, Attempt: 32, UpdatedAt: at,
		Criteria: []string{"Identity"}, Revisions: []Revision{{N: 1, Files: []string{"art.png"}, At: at}, {N: 2, Files: []string{"art.png", "evidence.png"}, At: at.Add(time.Hour)}},
	}}}
}
func signal(r LoopReport, kind string) LoopSignal {
	for _, s := range r.Signals {
		if s.Kind == kind {
			return s
		}
	}
	return LoopSignal{}
}
func TestLoopSignalRoundLimitAndAttemptsAloneAreNotLoops(t *testing.T) {
	v := loopFixture()
	r := LoopSignals(v, "t")
	if r.Looping() || r.Rounds != 4 || r.Limit != 4 || r.Attempts != 32 {
		t.Fatal(r)
	}
	for _, k := range []string{"rounds", "attempts"} {
		s := signal(r, k)
		if len(s.Sources) != 1 || s.Sources[0].At.IsZero() {
			t.Fatal(s)
		}
	}
	if LoopSignals(v, "missing").Looping() {
		t.Fatal("missing task")
	}
}
func TestLoopSignalConsecutiveEscalationsHaveDatedEvidence(t *testing.T) {
	v := loopFixture()
	at := v.Tasks[0].UpdatedAt
	for i := 1; i <= 4; i++ {
		v.Decisions = append(v.Decisions, Decision{ID: fmt.Sprint(i), TaskID: "t", Kind: DecisionEscalation, Title: fmt.Sprintf("Identity still has review points after %d rounds", i), CreatedAt: at.Add(time.Duration(i) * time.Hour)})
	}
	r := LoopSignals(v, "t")
	s := signal(r, "identical_decisions")
	if !s.Tripped || s.Count != 4 || len(s.Sources) != 4 || s.Sources[0].ID != "4" || !strings.Contains(r.Explain(), "4th identical escalation") {
		t.Fatalf("%+v\n%s", s, r.Explain())
	}
	if signal(r, "decision_kind").Count != 4 {
		t.Fatal(r)
	}
	v.Decisions = append(v.Decisions, Decision{ID: "different", TaskID: "t", Kind: DecisionFailure, Title: "Different", CreatedAt: at.Add(10 * time.Hour)})
	if signal(LoopSignals(v, "t"), "identical_decisions").Tripped {
		t.Fatal("nonconsecutive")
	}
}
func TestLoopSignalOrdinaryChoicesDeliveryAndUnrelatedQuestionsDoNotTrip(t *testing.T) {
	for _, kind := range []string{DecisionChoice, DecisionDelivery, DecisionUpdate, DecisionQuestion} {
		v := loopFixture()
		for i := range 4 {
			context := "same"
			if kind == DecisionQuestion {
				context = fmt.Sprint(i)
			}
			v.Decisions = append(v.Decisions, Decision{ID: fmt.Sprint(i), TaskID: "t", Kind: kind, Title: "Which audience?", Context: context})
		}
		if LoopSignals(v, "t").Looping() {
			t.Fatal(kind)
		}
	}
	v := loopFixture()
	for i := range 3 {
		v.Decisions = append(v.Decisions, Decision{ID: fmt.Sprint(i), TaskID: "t", Kind: DecisionQuestion, Title: "Which audience?", Context: "The same unresolved choice", CreatedAt: v.Tasks[0].UpdatedAt})
	}
	s := signal(LoopSignals(v, "t"), "owner_questions")
	if !s.Tripped || len(s.Sources) != 3 {
		t.Fatal(s)
	}
}
func repeatedFindings(v *Snapshot) {
	at := v.Tasks[0].UpdatedAt
	v.Tasks[0].Verdicts = []Verdict{
		{ID: "v1", Revision: 1, Outcome: VerdictRevise, Findings: []Finding{{Criterion: "Identity", Note: "Round 1: face wrong"}, {Criterion: "Identity", Note: "Round 1: face wrong"}}, At: at},
		{ID: "v2", Revision: 2, Outcome: VerdictRevise, Findings: []Finding{{Criterion: "Identity", Note: "ROUND 2:  face wrong"}}, At: at.Add(time.Hour)},
	}
}
func TestLoopSignalFindingsAcrossDistinctDraftsAndEvidenceGrowth(t *testing.T) {
	v := loopFixture()
	repeatedFindings(&v)
	at := v.Tasks[0].UpdatedAt
	v.Tasks[0].Attachments = []Attachment{{ID: "a1", Verdict: "v2", At: at.Add(time.Hour), Name: "evidence.png"}}
	r := LoopSignals(v, "t")
	s := signal(r, "findings")
	if !s.Tripped || s.Count != 2 || len(s.Sources) != 2 || s.Sources[0].ID != "v1" {
		t.Fatal(s)
	}
	g := signal(r, "growth")
	if !g.Tripped || g.Count != 2 || len(g.Sources) != 3 || g.Sources[2].ID != "a1" {
		t.Fatal(g)
	}
	if !strings.Contains(r.Recommend(v, "p"), "change_approach") && !strings.Contains(r.Recommend(v, "p"), "rescope") {
		t.Fatal(r)
	}
	// Two copies within one review are not two failed approaches.
	v.Tasks[0].Verdicts = v.Tasks[0].Verdicts[:1]
	if signal(LoopSignals(v, "t"), "findings").Tripped {
		t.Fatal("duplicate within draft")
	}
	// Growth with no repeated unresolved core finding is not a loop.
	v = loopFixture()
	repeatedFindings(&v)
	v.Tasks[0].Verdicts[1] = Verdict{ID: "pass", Revision: 2, Outcome: VerdictPass, At: at}
	if LoopSignals(v, "t").Looping() {
		t.Fatal("resolved findings")
	}
}
func TestLoopSignalCatchUpsConsiderAcceptedCheckedLanding(t *testing.T) {
	v := loopFixture()
	t0 := &v.Tasks[0]
	at := t0.UpdatedAt
	t0.Revisions = []Revision{{N: 1, At: at}, {N: 2, CleanMergeOf: 1, At: at.Add(time.Hour)}, {N: 3, CleanMergeOf: 2, At: at.Add(2 * time.Hour)}}
	s := signal(LoopSignals(v, "t"), "catch_ups")
	if !s.Tripped || s.Count != 2 || len(s.Sources) != 2 || s.Sources[0].At.IsZero() {
		t.Fatal(s)
	}
	t0.Status = TaskLanding
	t0.Acceptance = &DraftAcceptance{Revision: 1, BriefVersion: 1}
	t0.Approved = 1
	if LoopSignals(v, "t").Looping() {
		t.Fatal("accepted landing counted as failed progress")
	}
	t0.Acceptance = nil
	t0.Revisions = append(t0.Revisions, Revision{N: 4, At: at.Add(3 * time.Hour)})
	if signal(LoopSignals(v, "t"), "catch_ups").Count != 0 {
		t.Fatal("did not reset after substantive draft")
	}
}
func TestLoopSignalPrerequisiteHistoryAndOffRecommendations(t *testing.T) {
	v := loopFixture()
	at := v.Tasks[0].UpdatedAt
	v.Tasks[0].Blockers = []Blocker{{ID: "b", Kind: BlockerPrerequisite, Description: "Need approval", Settlements: []PrerequisiteSettlement{{Answer: "Already approved", At: at}, {Answer: "Still approved", At: at.Add(time.Hour)}}}}
	s := signal(LoopSignals(v, "t"), "prerequisites")
	if !s.Tripped || s.Count != 2 || s.Sources[1].At != at.Add(time.Hour) || !strings.Contains(s.Sources[1].Text, "Still approved") {
		t.Fatal(s)
	}
	if !strings.Contains(LoopSignals(v, "t").Recommend(v, "p"), "mechanical") {
		t.Fatal("mechanical recommendation")
	}
	v = loopFixture()
	repeatedFindings(&v)
	v.Projects[0].Settings = &Playbook{PMPolicies: map[string]bool{"change_approach": false, "rescope": false}}
	r := LoopSignals(v, "t")
	if rec := r.Recommend(v, "p"); !strings.Contains(rec, "off") || !strings.Contains(rec, "Not executed") {
		t.Fatal(rec)
	}
	v.Tasks[0].Verdicts[1].Findings[0].Note += " P1 security data loss"
	if rec := LoopSignals(v, "t").Recommend(v, "p"); !strings.Contains(rec, "this stays with you") {
		t.Fatal(rec)
	}
}
func TestLoopDecisionsExplainOnlyRelevantKindsAndRemainOwnerControlled(t *testing.T) {
	s, p, a, _ := duplicateFixture(t)
	for range 4 {
		if err := s.store.update(testContext, func(v *Snapshot) error { q := task(v, a.ID); q.Status = TaskDeciding; q.DecisionID = ""; return nil }); err != nil {
			t.Fatal(err)
		}
		_, err := s.OpenTaskDecision(testContext, a.ID, DecisionEscalation, DecisionInput{Title: "Same escalation", Context: "Core still unresolved", Recommendation: "Try again", Choices: []string{"Retry", "Stop"}})
		if err != nil {
			t.Fatal(err)
		}
	}
	snap, _ := s.Snapshot(testContext)
	d := decision(&snap, task(&snap, a.ID).DecisionID)
	if !strings.Contains(d.Context, "4th identical escalation") || !strings.Contains(d.Recommendation, "No automatic action was taken") {
		t.Fatal(d)
	}
	if PMPolicyOn(snap, p.ID, "unknown") {
		t.Fatal("unknown policy")
	}
}

func TestLoopResearchAndCatchUpAttemptHistory(t *testing.T) {
	v := loopFixture()
	at := v.Tasks[0].UpdatedAt
	for i := range 3 {
		v.Tasks[0].Research = append(v.Tasks[0].Research, ResearchRequest{ID: fmt.Sprint(i), Question: fmt.Sprintf("Round %d: same investigation", i), At: at.Add(time.Duration(i) * time.Hour)})
	}
	s := signal(LoopSignals(v, "t"), "research")
	if !s.Tripped || s.Count != 3 || len(s.Sources) != 3 || s.Sources[2].At != at.Add(2*time.Hour) {
		t.Fatal(s)
	}
	v = loopFixture()
	v.Tasks[0].CatchUps = 5
	s = signal(LoopSignals(v, "t"), "catch_up_attempts")
	if !s.Tripped || s.Count != 5 || s.Sources[0].ID != "t" {
		t.Fatal(s)
	}
}
func TestLoopLatestPassRetiresEarlierFindingOnSameDraft(t *testing.T) {
	v := loopFixture()
	repeatedFindings(&v)
	v.Tasks[0].Verdicts = append(v.Tasks[0].Verdicts, Verdict{ID: "pass", Revision: 2, Outcome: VerdictPass, At: v.Tasks[0].UpdatedAt.Add(2 * time.Hour)})
	if LoopSignals(v, "t").Looping() {
		t.Fatal("superseded finding kept alive")
	}
}
func TestRepeatedOpenLoopDecisionIsIdempotent(t *testing.T) {
	s, _, a, _ := duplicateFixture(t)
	in := DecisionInput{Title: "Repeated escalation", Context: "Unresolved core", Recommendation: "Retry", Choices: []string{"Retry", "Stop"}}
	first, err := s.OpenTaskDecision(testContext, a.ID, DecisionEscalation, in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.OpenTaskDecision(testContext, a.ID, DecisionEscalation, in)
	if err != nil || first.ID != second.ID {
		t.Fatal(first, second, err)
	}
}

func TestEachUnimplementedPMResponseExplainsOwnerDisablement(t *testing.T) {
	v := loopFixture()
	repeatedFindings(&v)
	v.Projects[0].Settings = &Playbook{PMPolicies: map[string]bool{}}
	for _, key := range []string{"rescope", "accept_leftovers", "pause_loop", "change_approach"} {
		v.Projects[0].Settings.PMPolicies[key] = false
	}
	r := LoopSignals(v, "t")
	recommendation := r.Recommend(v, "p")
	for _, key := range []string{"rescope", "accept_leftovers", "pause_loop", "change_approach"} {
		if !strings.Contains(recommendation, key+" (Off)") {
			t.Fatal(recommendation)
		}
	}
	if !strings.Contains(recommendation, "Accept this draft") || !strings.Contains(recommendation, "core purpose") {
		t.Fatal(recommendation)
	}
}

func TestResearchLimitDecisionIncludesTheRequestThatReachedIt(t *testing.T) {
	s, _, a, _ := duplicateFixture(t)
	for range 3 {
		_, err := s.UpdateTask(testContext, a.ID, func(t *Task, _ *Project) (string, error) {
			t.Status = TaskReviewing
			t.Round = 1
			t.Roles = []Role{{Name: "Researcher", Kinds: []string{RoleResearcher}}}
			return "", nil
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.AskResearch(testContext, a.ID, ResearchAsk{From: "Reviewer", Revision: 1, Question: "Same investigation", Owner: DecisionInput{Title: "Research limit", Context: "Same investigation", Recommendation: "Choose an approach", Choices: []string{"Retry", "Stop"}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	snap, _ := s.Snapshot(testContext)
	task := task(&snap, a.ID)
	d := decision(&snap, task.DecisionID)
	if d == nil || !strings.Contains(d.Context, "same research 3 times") || d.LoopReport == nil || signal(*d.LoopReport, "research").Count != 3 || !slices.ContainsFunc(signal(*d.LoopReport, "research").Sources, func(source LoopSource) bool { return source.ID == task.Research[2].ID }) {
		t.Fatalf("%+v", d)
	}
}

func TestUnrelatedOwnerQuestionKeepsItsContextDespiteOtherLoops(t *testing.T) {
	s, _, a, _ := duplicateFixture(t)
	err := s.store.update(testContext, func(v *Snapshot) error {
		q := task(v, a.ID)
		q.Blockers = []Blocker{{ID: "b", Kind: BlockerPrerequisite, Settlements: []PrerequisiteSettlement{{Answer: "Not needed", At: q.CreatedAt}, {Answer: "Still not needed", At: q.CreatedAt}}}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	in := DecisionInput{Title: "Which format?", Context: "JSON or text?", Recommendation: "JSON", Choices: []string{"JSON", "Text"}}
	d, err := s.OpenTaskDecision(testContext, a.ID, DecisionQuestion, in)
	if err != nil {
		t.Fatal(err)
	}
	if d.Context != in.Context || d.Recommendation != in.Recommendation {
		t.Fatal("unrelated question changed", d)
	}
	_, err = s.UpdateTask(testContext, a.ID, func(q *Task, _ *Project) (string, error) { q.Status = TaskDeciding; q.DecisionID = ""; return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	d, err = s.OpenTaskDecision(testContext, a.ID, DecisionQuestion, in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d.Context, "same question 2 times") {
		t.Fatal("repeated question not explained", d)
	}
}

func TestLoopSummaryIncludesOnlyTrippedSignalsAndLatestSources(t *testing.T) {
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	report := LoopReport{Rounds: 4, Limit: 4, Attempts: 9, Signals: []LoopSignal{
		{Kind: "decision_kind", Count: 100, Summary: "100 ordinary choices", Sources: []LoopSource{{ID: "ordinary", At: at}}},
		{Kind: "identical_decisions", Count: 3, Threshold: 2, Tripped: true, Summary: "3rd identical escalation " + strings.Repeat("x", 1000), Sources: []LoopSource{
			{Kind: "decision", ID: "old", At: at.Add(-time.Hour)},
			{Kind: "decision", ID: "latest", At: at, Text: strings.Repeat("evidence ", 500)},
			{Kind: "decision", ID: "middle", At: at.Add(-time.Minute)},
		}},
	}}
	brief := report.Summary()
	for _, want := range []string{"4 rounds used versus limit 4", "9 attempts", "count 3; threshold 2", "decision latest", at.Format(time.RFC3339)} {
		if !strings.Contains(brief, want) {
			t.Fatal(brief)
		}
	}
	for _, excluded := range []string{"ordinary", "100 ordinary choices", "decision old", "decision middle"} {
		if strings.Contains(brief, excluded) {
			t.Fatal(brief)
		}
	}
	if len(brief) > 600 {
		t.Fatal("queue summary is not bounded", len(brief))
	}
	if full := report.Explain(); !strings.Contains(full, "ordinary") || !strings.Contains(full, "decision old") {
		t.Fatal("full history lost", full)
	}
	if (LoopReport{}).Summary() != "" {
		t.Fatal("non-looping summary")
	}
}

func TestSameDecisionTitleDoesNotDiscardNewContextOrChoices(t *testing.T) {
	for _, kind := range []string{DecisionEscalation, DecisionFailure, DecisionQuestion} {
		t.Run(kind, func(t *testing.T) {
			s, _, a, _ := duplicateFixture(t)
			in := DecisionInput{Title: "Generic title", Context: "First batch", Recommendation: "Choose", Choices: []string{"One", "Two"}}
			first, err := s.OpenTaskDecision(testContext, a.ID, kind, in)
			if err != nil {
				t.Fatal(err)
			}
			in.Context = "A different batch"
			second, err := s.OpenTaskDecision(testContext, a.ID, kind, in)
			if err != nil {
				t.Fatal(err)
			}
			if second.ID == first.ID || originalDecisionContext(second) != in.Context {
				t.Fatal("new context discarded", second)
			}
			same, err := s.OpenTaskDecision(testContext, a.ID, kind, in)
			if err != nil || same.ID != second.ID {
				t.Fatal("identical retry duplicated", same, err)
			}
			in.Choices = []string{"One", "Three"}
			third, err := s.OpenTaskDecision(testContext, a.ID, kind, in)
			if err != nil {
				t.Fatal(err)
			}
			if third.ID == second.ID || !slices.Equal(third.Choices, in.Choices) {
				t.Fatal("new choices discarded", third)
			}
			in.Context = third.Context
			identical, err := s.OpenTaskDecision(testContext, a.ID, kind, in)
			if err != nil || identical.ID != third.ID {
				t.Fatal("verbatim annotated context duplicated", identical, err)
			}

		})
	}
}

func TestAcceptedCatchUpsStayOrdinaryWhenOpeningOwnerDecision(t *testing.T) {
	for _, status := range []string{TaskLanding, TaskAwaiting} {
		for _, resume := range []string{"", TaskLanding, TaskAwaiting} {
			t.Run(status+"/resume="+resume, func(t *testing.T) {
				s, p, a, _ := duplicateFixture(t)
				_, err := s.UpdateTask(testContext, a.ID, func(q *Task, _ *Project) (string, error) {
					q.Status, q.ResumeStatus = status, resume
					q.Revisions = []Revision{{N: 1, At: q.CreatedAt}, {N: 2, CleanMergeOf: 1, At: q.CreatedAt.Add(time.Hour)}, {N: 3, CleanMergeOf: 2, At: q.CreatedAt.Add(2 * time.Hour)}}
					q.Acceptance = &DraftAcceptance{Revision: 1, BriefVersion: p.Brief.Version}
					q.Approved = 1
					q.CatchUps = 2
					return "", nil
				})
				if err != nil {
					t.Fatal(err)
				}
				in := DecisionInput{Title: "Cannot finish delivery", Context: "The workspace check could not start.", Recommendation: "Try again once it is available", Choices: []string{"Retry", "Stop"}}
				for i := range 2 {
					if i > 0 {
						if _, err = s.UpdateTask(testContext, a.ID, func(q *Task, _ *Project) (string, error) { q.Status = status; return "", nil }); err != nil {
							t.Fatal(err)
						}
					}
					d, err := s.OpenTaskDecision(testContext, a.ID, DecisionFailure, in)
					if err != nil {
						t.Fatal(err)
					}
					if i == 0 {
						if d.LoopReport != nil || d.Context != in.Context || d.Recommendation != in.Recommendation {
							t.Fatal("ordinary accepted catch-ups became a loop", d)
						}
					} else {
						if d.LoopReport == nil {
							t.Fatal("real repeated failure lost")
						}
						catchups := signal(*d.LoopReport, "catch_ups")
						if catchups.Tripped || catchups.Count != 2 || signal(*d.LoopReport, "catch_up_attempts").Tripped {
							t.Fatal("decision lost accepted landing state", d.LoopReport)
						}
					}
					snap, _ := s.Snapshot(testContext)
					report := LoopSignals(snap, a.ID)
					if signal(report, "catch_ups").Tripped || signal(report, "catch_up_attempts").Tripped {
						t.Fatal("owner hold counted as failed progress", report)
					}
				}
			})
		}
	}
}
func TestOwnerLoopCopyIsPlainWhileEvidenceRemainsAvailable(t *testing.T) {
	s, p, a, _ := duplicateFixture(t)
	off := false
	if err := s.SetPMPolicy(testContext, "project", p.ID, "pause_loop", &off, 0, FromOwner); err != nil {
		t.Fatal(err)
	}
	var d Decision
	for range 4 {
		_, err := s.UpdateTask(testContext, a.ID, func(q *Task, _ *Project) (string, error) { q.Status = TaskDeciding; q.DecisionID = ""; return "", nil })
		if err != nil {
			t.Fatal(err)
		}
		d, err = s.OpenTaskDecision(testContext, a.ID, DecisionEscalation, DecisionInput{Title: "Needs a decision", Context: "The same requirement is unresolved.", Recommendation: "Try again", Choices: []string{"Retry", "Stop"}})
		if err != nil {
			t.Fatal(err)
		}
	}
	if !strings.HasPrefix(d.Context, "This is the 4th identical escalation.") || !strings.Contains(d.Recommendation, "is off; no action was taken.") {
		t.Fatal(d)
	}
	for _, internal := range []string{"rounds", "handoff", "revision", "verdict", "decision_kind", "pause_loop", "Not executed in this part", "later part", a.ID, d.ID, "T12:00:"} {
		if strings.Contains(d.Context+" "+d.Recommendation, internal) {
			t.Fatal("internal evidence in owner copy", internal, d)
		}
	}
	if strings.Contains(d.Recommendation, "\n") {
		t.Fatal("more than one recommendation", d.Recommendation)
	}
	if d.LoopReport == nil {
		t.Fatal("drill-down evidence missing")
	}
	full := d.LoopReport.Explain()
	if !strings.Contains(full, d.ID) || !strings.Contains(full, "rounds used versus limit") {
		t.Fatal("source history lost", full)
	}
	again, err := s.OpenTaskDecision(testContext, a.ID, DecisionEscalation, DecisionInput{Title: d.Title, Context: "The same requirement is unresolved.", Recommendation: "Try again", Choices: d.Choices})
	if err != nil || again.ID != d.ID {
		t.Fatal("plain annotation broke replay", again, err)
	}
	snap, _ := s.Snapshot(testContext)
	report := LoopSignals(snap, a.ID)
	if signal(report, "identical_decisions").Count != 4 {
		t.Fatal("stored evidence became detector history", report)
	}
}
func TestOwnerRecommendationsKeepRiskWithOwnerAndNoPolicyCatalogue(t *testing.T) {
	v := loopFixture()
	repeatedFindings(&v)
	r := LoopSignals(v, "t")
	v.Projects[0].Settings = &Playbook{PMPolicies: map[string]bool{"rescope": false, "change_approach": false}}
	rec := r.OwnerRecommendation(v, "p")
	if !strings.Contains(rec, "is off") || strings.Contains(rec, "change_approach") || strings.Contains(rec, "rescope") || strings.Contains(rec, "later part") {
		t.Fatal(rec)
	}
	r.Risk = true
	if rec = r.OwnerRecommendation(v, "p"); !strings.HasSuffix(rec, "this stays with you.") {
		t.Fatal(rec)
	}
}
func TestWaitingAcceptanceDoesNotHideExpiredApprovalOrAReworkHold(t *testing.T) {
	v := loopFixture()
	q := &v.Tasks[0]
	q.Revisions = []Revision{{N: 1}, {N: 2, CleanMergeOf: 1}, {N: 3, CleanMergeOf: 2}}
	q.Status, q.ResumeStatus = TaskWaiting, TaskLanding
	q.Acceptance = &DraftAcceptance{Revision: 1, BriefVersion: 1}
	q.Approved = 1
	if signal(LoopSignals(v, "t"), "catch_ups").Tripped {
		t.Fatal("accepted hold became a loop")
	}
	q.ResumeStatus = TaskWriting
	if !signal(LoopSignals(v, "t"), "catch_ups").Tripped {
		t.Fatal("rework hold suppressed")
	}
	q.ResumeStatus = TaskLanding
	q.Acceptance.BriefVersion = 2
	if !signal(LoopSignals(v, "t"), "catch_ups").Tripped {
		t.Fatal("expired acceptance suppressed")
	}
}

func TestAcceptedMergeValidationFailureDoesNotReclassifyOrdinaryCatchUps(t *testing.T) {
	s, p, a, _ := duplicateFixture(t)
	_, err := s.UpdateTask(testContext, a.ID, func(q *Task, _ *Project) (string, error) {
		q.Status = TaskReviewing
		q.Revisions = []Revision{{N: 1, Ref: "original"}, {N: 2, CleanMergeOf: 1, Ref: "merged-once"}, {N: 3, CleanMergeOf: 2, Ref: "merged-twice"}}
		q.Acceptance = &DraftAcceptance{Revision: 1, BriefVersion: p.Brief.Version}
		q.Approved = 1
		q.MergeValidation = &MergeValidation{Revision: 3, Ref: "merged-twice", BriefVersion: p.Brief.Version}
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	in := DecisionInput{Title: "The merged deliverable needs a check", Context: "The merged check failed.", Recommendation: "Fix the check", Choices: []string{"Retry", "Stop"}}
	d, err := s.FailAcceptedCatchUp(testContext, a.ID, 3, p.Brief.Version, 0, false, in)
	if err != nil {
		t.Fatal(err)
	}
	if d.LoopReport != nil || d.Context != in.Context {
		t.Fatal("one failed check was called a catch-up loop", d)
	}
	snap, _ := s.Snapshot(testContext)
	report := LoopSignals(snap, a.ID)
	if signal(report, "catch_ups").Tripped {
		t.Fatal(report)
	}
	q := task(&snap, a.ID)
	if q.MergeValidation.Failure != in.Context || q.ResumeStatus != TaskReviewing || q.Status != TaskWaiting {
		t.Fatal("validation failure or continuation lost", q)
	}
	q.MergeValidation.Revision = 2
	if !signal(LoopSignals(snap, a.ID), "catch_ups").Tripped {
		t.Fatal("stale check used to suppress loop")
	}
}

func TestAcceptedCatchUpLimitIsMechanicalNotFailedProgress(t *testing.T) {
	v := loopFixture()
	repeatedFindings(&v)
	q := &v.Tasks[0]
	q.Status = TaskLanding
	// Only the last clean merge carries acceptance. Earlier real findings stay
	// visible, but must not turn accepted landing's evidence growth into failure.
	q.Revisions = append(q.Revisions, Revision{N: 3, CleanMergeOf: 2, At: q.UpdatedAt.Add(2 * time.Hour)})
	q.Acceptance = &DraftAcceptance{Revision: 2, BriefVersion: 1}
	q.Approved = 2
	q.Verdicts = append(q.Verdicts, Verdict{ID: "v3", Revision: 3, Outcome: VerdictRevise, Findings: []Finding{{Criterion: "Identity", Note: "Round 3: face wrong"}}, At: q.UpdatedAt.Add(2 * time.Hour)})
	q.CatchUps = 5
	r := LoopSignals(v, q.ID)
	if signal(r, "catch_ups").Tripped || signal(r, "growth").Tripped {
		t.Fatal("accepted landing counted as failed progress", r)
	}
	mechanical := signal(r, "catch_up_attempts")
	if !mechanical.Tripped || mechanical.Count != 5 || len(mechanical.Sources) != 2 || mechanical.Sources[1].ID != "t/draft/3" || !strings.Contains(r.Recommend(v, "p"), "preserve accepted-draft checked landing") {
		t.Fatal(r)
	}
}
