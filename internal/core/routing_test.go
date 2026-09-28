package core

import (
	"errors"
	"slices"
	"testing"
)

// checkedTask is a task of a researched project with draft 1 checked by
// the Reviewer, which asked for research, on branch b.
func checkedTask(t *testing.T, s *Service) Task {
	t.Helper()
	p := plannedProject(t, s)
	queued, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "A draft"})
	s.NextTask(testContext)
	task, err := s.UpdateTask(testContext, queued.ID, func(t *Task, p *Project) (string, error) {
		t.Status, t.Round, t.Branch, t.Base = TaskDeciding, 1, "b", "base"
		t.Plan = &Plan{Summary: "First plan", Role: "Researcher"}
		t.Revisions = []Revision{{N: 1, BriefVersion: p.Brief.Version}}
		t.Verdicts = []Verdict{{Revision: 1, Role: "Reviewer", BriefVersion: p.Brief.Version, Outcome: VerdictResearch, Question: "Which API?"}}
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return task
}

var researchOwner = DecisionInput{Title: "More research?", Context: "Which API?", Recommendation: "Answer it", Choices: []string{"Use your judgment", "Stop"}}

func TestResearchAskedForInReviewGoesBackToTheCheckerWithoutTouchingTheDraft(t *testing.T) {
	s, _ := fixture(t)
	task := checkedTask(t, s)
	ask := ResearchAsk{From: "Reviewer", Revision: 1, Question: "Which API?", Owner: researchOwner}
	held, err := s.AskResearch(testContext, task.ID, ask)
	if err != nil || held.Status != TaskResearching || held.Stage != StageResearching || held.OpenResearch() == nil {
		t.Fatalf("with the researcher: %s %s %+v %v", held.Status, held.Stage, held.Research, err)
	}
	// The researcher can't make a task whose work has begun wait, and its
	// plan sends the task back to the checker that asked, on its branch.
	other, _ := s.QueueTask(testContext, task.ProjectID, TaskInput{Objective: "Other"})
	back, err := s.RecordPlan(testContext, task.ID, Plan{Summary: "Use v2", Role: "Researcher"}, []string{other.ID})
	if err != nil || back.Status != TaskReviewing || back.Plan.Summary != "Use v2" || back.Branch != "b" || back.Base != "base" || len(back.DependsOn) != 0 {
		t.Fatalf("back to the checker: %s %+v branch %q deps %v %v", back.Status, back.Plan, back.Branch, back.DependsOn, err)
	}
	if r := back.Research[0]; r.Open() || r.Researcher != "Researcher" || r.From != "Reviewer" || r.Revision != 1 {
		t.Fatalf("request %+v", r)
	}
	if back.Judged("Reviewer", 1, back.Verdicts[0].BriefVersion) || back.Checking != "Reviewer" {
		t.Fatalf("the checker that asked should judge draft 1 again: checking %q", back.Checking)
	}
	// Past the limit in a round, the question goes to the owner, and their
	// answer comes back to the checker.
	s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
		t.Status = TaskDeciding
		t.Research = append(t.Research, ResearchRequest{Round: 1, From: "Reviewer"})
		return "", nil
	})
	waiting, err := s.AskResearch(testContext, task.ID, ask)
	if err != nil || waiting.Status != TaskWaiting || waiting.Asker == nil || waiting.Asker.Step != TaskReviewing || waiting.Research[2].Decision != waiting.DecisionID || waiting.Stage != StageReviewing {
		t.Fatalf("past the limit: %s %+v %s %v", waiting.Status, waiting.Asker, waiting.Stage, err)
	}
	if _, err := s.AskResearch(testContext, task.ID, ask); !errors.Is(err, ErrConflict) {
		t.Fatalf("a task no longer being checked was sent for research: %v", err)
	}
}

func TestAnAnswerGoesBackToWhoeverAskedTheQuestion(t *testing.T) {
	s, _ := fixture(t)
	task := checkedTask(t, s)
	d, err := s.AskQuestion(testContext, task.ID, Asker{From: "Reviewer", Step: TaskReviewing, Revision: 1}, researchOwner)
	if err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(testContext)
	waiting, _ := findSnapshotTask(snap, task.ID)
	if waiting.Status != TaskWaiting || waiting.Asker.Decision != d.ID || waiting.Stage != StageReviewing {
		t.Fatalf("waiting: %s %+v %s", waiting.Status, waiting.Asker, waiting.Stage)
	}
	if waiting.BackToAsker("another") != nil || waiting.Status != TaskWaiting {
		t.Fatal("another decision's answer went to the asker")
	}
	if a := waiting.BackToAsker(d.ID); a == nil || waiting.Status != TaskReviewing || waiting.Asker != nil || !waiting.Verdicts[0].Answered || waiting.Round != 1 {
		t.Fatalf("back to the checker in the same round: %s %+v", waiting.Status, waiting.Verdicts)
	}
	researcher := Task{Status: TaskWaiting, DecisionID: "d", Plan: &Plan{Questions: []string{"Which?"}}, Asker: &Asker{Decision: "d", From: "Researcher", Step: TaskResearching}}
	if a := researcher.BackToAsker("d"); a == nil || researcher.Status != TaskResearching || !researcher.Plan.Answered {
		t.Fatalf("back to the researcher to plan again: %s %+v", researcher.Status, researcher.Plan)
	}
}

func TestARecommendationDisagreesOnlyWhenItLeadsElsewhere(t *testing.T) {
	for _, c := range []struct {
		v    Verdict
		want bool
	}{
		{Verdict{Outcome: VerdictPass}, false},
		{Verdict{Outcome: VerdictPass, Next: NextLand}, false},
		{Verdict{Outcome: VerdictPass, Next: NextRevise}, true},
		{Verdict{Outcome: VerdictRevise, Next: NextResearch}, true},
		{Verdict{Outcome: VerdictResearch, Next: NextResearch}, false},
	} {
		if got := c.v.Disagrees(); got != c.want {
			t.Errorf("%+v: %v", c.v, got)
		}
	}
}

func TestTheTeamEditsATaskWithinItsRoleAndTheOwnerCanUndoIt(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	task, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Messy title", Criteria: []string{"Owner's words"}})
	if _, err := s.EditTask(testContext, EditInput{Project: p.ID, Task: task.ID, By: "Rev", Kind: RoleReviewer, Objective: "New"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a reviewer reworded the title: %v", err)
	}
	if _, err := s.EditTask(testContext, EditInput{Project: p.ID, Task: task.ID, By: "QA", Kind: RoleQA, Criteria: []string{}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("QA removed requirements: %v", err)
	}
	added, err := s.EditTask(testContext, EditInput{Project: p.ID, Task: task.ID, By: "QA", Kind: RoleQA, Add: []string{" Tests pass "}})
	if err != nil || !slices.Equal(added.Criteria, []string{"Owner's words", "Tests pass"}) {
		t.Fatalf("QA adds a requirement: %v %v", added.Criteria, err)
	}
	if _, err := s.EditTask(testContext, EditInput{Project: p.ID, Task: task.ID, By: "QA", Kind: RoleQA, Add: []string{"Tests pass"}}); err == nil {
		t.Fatal("an edit that changes nothing was kept")
	}
	if _, err := s.EditTask(testContext, EditInput{Project: "elsewhere", Task: task.ID, By: "Pim", Kind: RolePM, Objective: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("edited another project's task: %v", err)
	}
	rewritten, err := s.EditTask(testContext, EditInput{Project: p.ID, Task: task.ID, By: "Rhea", Kind: RoleResearcher, Objective: "Clear title", Criteria: []string{"Reworded"}})
	if err != nil || rewritten.Objective != "Clear title" || !slices.Equal(rewritten.Criteria, []string{"Reworded"}) || len(rewritten.Edits) != 2 {
		t.Fatalf("the researcher rewrites: %+v %v", rewritten, err)
	}
	e := rewritten.Edits[1]
	if e.By != "Rhea" || e.Kind != RoleResearcher || e.Before.Objective != "Messy title" || !slices.Equal(e.Before.Criteria, []string{"Owner's words", "Tests pass"}) || e.After.Objective != "Clear title" {
		t.Fatalf("the edit keeps what it replaced: %+v", e)
	}
	undone, err := s.UndoTaskEdit(testContext, p.ID, task.ID, e.ID)
	if err != nil || undone.Objective != "Messy title" || !slices.Equal(undone.Criteria, []string{"Owner's words", "Tests pass"}) || len(undone.Edits) != 3 || undone.Edits[2].Undoes != e.ID || undone.Edits[2].By != FromOwner {
		t.Fatalf("the owner undoes it: %+v %v", undone, err)
	}
	snap, _ := s.Snapshot(testContext)
	if !slices.ContainsFunc(snap.Activity, func(a Activity) bool { return a.Kind == "task.edited" && a.TaskID == task.ID }) {
		t.Fatal("no activity for the edit")
	}
	s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) { t.Status = TaskStopped; return "", nil })
	if _, err := s.EditTask(testContext, EditInput{Project: p.ID, Task: task.ID, By: "Pim", Kind: RolePM, Objective: "Late"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a finished task was edited: %v", err)
	}
}

// A check counts only against the requirements it judged: an edit, or the
// owner undoing one, asks every checker again.
func TestAnEditSetsAsideTheChecksMadeBeforeIt(t *testing.T) {
	s, _ := fixture(t)
	task := checkedTask(t, s)
	brief := task.Verdicts[0].BriefVersion
	if !task.Judged("Reviewer", 1, brief) {
		t.Fatal("the check should count before any edit")
	}
	edited, err := s.EditTask(testContext, EditInput{Project: task.ProjectID, Task: task.ID, By: "QA", Kind: RoleQA, Add: []string{"Handles errors"}})
	if err != nil || edited.TextVersion != 1 || edited.Judged("Reviewer", 1, brief) || edited.Checking != "Reviewer" {
		t.Fatalf("after the edit: version %d, checking %q, %v", edited.TextVersion, edited.Checking, err)
	}
	undone, err := s.UndoTaskEdit(testContext, task.ProjectID, task.ID, edited.Edits[0].ID)
	if err != nil || undone.TextVersion != 2 || undone.Judged("Reviewer", 1, brief) {
		t.Fatalf("an undo is a change too: version %d, %v", undone.TextVersion, err)
	}
}

func TestNotesAreLeftOnATaskForEveryoneToRead(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	task, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "A draft"})
	if _, err := s.AddNote(testContext, NoteInput{Project: p.ID, Task: task.ID, By: "Rev", Kind: RoleReviewer, Text: "  "}); err == nil {
		t.Fatal("an empty note was kept")
	}
	if _, err := s.AddNote(testContext, NoteInput{Project: p.ID, Task: task.ID, By: "Rev", Kind: RoleReviewer, While: TaskReviewing, Text: "Late"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a turn whose task moved on left a note: %v", err)
	}
	n, err := s.AddNote(testContext, NoteInput{Project: p.ID, Task: task.ID, By: "Rev", Kind: RoleReviewer, Text: "Watch the dates"})
	if err != nil || n.By != "Rev" || n.Text != "Watch the dates" {
		t.Fatalf("note %+v %v", n, err)
	}
	s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) { t.Status = TaskStopped; return "", nil })
	if _, err := s.AddNote(testContext, NoteInput{Project: p.ID, Task: task.ID, By: "Rev", Kind: RoleReviewer, Text: "More"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("the team left a note on a finished task: %v", err)
	}
	if _, err := s.AddNote(testContext, NoteInput{Project: p.ID, Task: task.ID, By: FromOwner, Kind: FromOwner, Text: "For next time"}); err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(testContext)
	if found, _ := findSnapshotTask(snap, task.ID); len(found.Notes) != 2 || found.Notes[1].Kind != FromOwner {
		t.Fatalf("notes %+v", found.Notes)
	}
}
