package core

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestOwnerAndOutsideWaitsCountTowardsColumnCapacity(t *testing.T) {
	for _, status := range []string{TaskWaiting, TaskAwaiting} {
		for _, capacity := range []int{1, 10} {
			t.Run(fmt.Sprintf("%s/capacity-%d", status, capacity), func(t *testing.T) {
				s, _ := fixture(t)
				var limits map[string]int
				if capacity == 1 {
					limits = map[string]int{StageReviewing: capacity}
				}
				p := staged(t, s, newProject(t, s), 0, limits)
				var names []string
				for i := 0; i <= capacity; i++ {
					names = append(names, fmt.Sprintf("Request %d", i))
				}
				tasks := queueAll(t, s, p, names...)
				claimed(t, s)
				for _, task := range tasks[:capacity] {
					finish(t, s, task.ID, TaskReviewing)
					claimed(t, s)
					finish(t, s, task.ID, status)
					claimed(t, s)
				}
				last := tasks[capacity]
				finish(t, s, last.ID, TaskReviewing)
				claimed(t, s)
				want := Wait{Kind: WaitStage, Stage: StageReviewing, From: StageImplementing, Count: capacity, Limit: capacity}
				if w := waiting(t, s, last.ID); w == nil || *w != want {
					t.Fatalf("full column: %+v, want %+v", w, want)
				}
				// Stopping an owner/outside wait frees room just like completed work.
				finish(t, s, tasks[0].ID, TaskStopped)
				claimed(t, s)
				if got := onBoard(t, s, last.ID); got.Place != StageReviewing || got.Waiting != nil {
					t.Fatalf("freed room: %+v", got)
				}
			})
		}
	}
}

func TestExplicitReadyLimitCountsOwnerApproval(t *testing.T) {
	s, _ := fixture(t)
	p := staged(t, s, newProject(t, s), 0, map[string]int{StageReady: 1})
	tasks := queueAll(t, s, p, "A", "B")
	claimed(t, s)
	for _, task := range tasks {
		finish(t, s, task.ID, TaskReviewing)
		claimed(t, s)
		judge(t, s, task.ID, "Reviewer", VerdictPass)
		judge(t, s, task.ID, "QA", VerdictPass)
		finish(t, s, task.ID, TaskDeciding)
		claimed(t, s)
		if task.ID == tasks[0].ID {
			if got := onBoard(t, s, task.ID); got.Place != StageReady {
				t.Fatalf("A did not reach Ready: %+v", got)
			}
			finish(t, s, task.ID, TaskWaiting)
			if err := s.store.update(testContext, func(v *Snapshot) error {
				v.Decisions = append(v.Decisions, Decision{ID: "approval", ProjectID: p.ID, Kind: DecisionDelivery})
				for i := range v.Tasks {
					if v.Tasks[i].ID == task.ID {
						v.Tasks[i].DecisionID = "approval"
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if w := waiting(t, s, tasks[1].ID); w == nil || w.Kind != WaitStage || w.Stage != StageReady || w.Count != 1 || w.Limit != 1 {
		t.Fatalf("owner approval did not hold Ready: %+v", w)
	}
	finish(t, s, tasks[0].ID, TaskStopped)
	claimed(t, s)
	if got := onBoard(t, s, tasks[1].ID); got.Place != StageReady || got.Waiting != nil {
		t.Fatalf("stopping did not free Ready: %+v", got)
	}
}

func TestBackwardHandoffsDisplayBeforeScheduling(t *testing.T) {
	for _, c := range []struct{ from, status, want string }{
		{StageReviewing, TaskResearching, StageResearching},
		{StageDesigning, TaskResearching, StageResearching},
		{StageQA, TaskWriting, StageImplementing},
	} {
		t.Run(c.from+"/"+c.status, func(t *testing.T) {
			s, _ := fixture(t)
			p := newProject(t, s)
			task := queueAll(t, s, p, "A")[0]
			if _, err := s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
				t.Status, t.Place = c.status, c.from
				return "", nil
			}); err != nil {
				t.Fatal(err)
			}
			if got := onBoard(t, s, task.ID); got.Stage != c.want {
				t.Fatalf("handoff displays %s, want %s", got.Stage, c.want)
			}
		})
	}
}

func TestColumnDefaultsAreIndependentOfSeats(t *testing.T) {
	for _, roles := range [][]Role{nil, Templates["draft"].Roles, append(slices.Clone(Templates["draft"].Roles), Templates["draft"].Roles...)} {
		p := Playbook{Roles: roles}
		for _, stage := range []string{StageResearching, StageDesigning, StageImplementing, StageReviewing, StageQA} {
			if got := p.StageLimit(stage); got != 10 {
				t.Fatalf("%s: %d", stage, got)
			}
		}
		for _, stage := range []string{StageTodo, StageTriage, StageReady, StagePROpening, StagePROpen, "unknown"} {
			if p.StageLimit(stage) != 0 {
				t.Fatalf("%s limited", stage)
			}
		}
	}
	p := Templates["draft"]
	p.StageLimits = map[string]int{StageTodo: 500, StageQA: 4}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if p.StageLimit(StageTodo) != 500 || p.StageLimit(StageQA) != 4 || p.StageLimit(StageReady) != 0 {
		t.Fatal("overrides")
	}
	for _, stage := range []string{StagePROpening, StagePROpen, StageReady} {
		p.StageLimits = map[string]int{stage: 4}
		if p.StageLimit(stage) != 4 {
			t.Fatalf("%s explicit capacity ignored", stage)
		}
		p.StageLimits[stage] = 0
		if p.StageLimit(stage) != 0 {
			t.Fatalf("%s zero capacity limited", stage)
		}
	}
	p.MaxActive = 11
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "0 for no overall limit") {
		t.Fatalf("validation: %v", err)
	}
}

func TestStoppingQAFreesItsLimitedPlaceAfterRestart(t *testing.T) {
	s, tasks := bottleneck(t, map[string]int{StageImplementing: 1, StageReviewing: 1, StageQA: 1})
	claimed(t, s)
	if b := onBoard(t, s, tasks[1].ID); b.Place != StageReviewing || b.Waiting == nil || b.Waiting.Stage != StageQA {
		t.Fatalf("passed review should wait for QA: %+v", b)
	}
	if err := s.RecoverClaims(testContext, nil); err != nil {
		t.Fatal(err)
	}
	claimed(t, s)
	finish(t, s, tasks[0].ID, TaskStopped)
	got := claimed(t, s)
	if !slices.Contains(got, "B: reviewing by QA") {
		t.Fatalf("stopping did not free QA: %v", got)
	}
	if stopped := onBoard(t, s, tasks[0].ID); stopped.Place != "" || len(stopped.Claims) != 0 {
		t.Fatalf("stopped holds place or seat: %+v", stopped)
	}
	if b := onBoard(t, s, tasks[1].ID); b.Place != StageQA || b.Waiting != nil {
		t.Fatalf("B did not enter QA: %+v", b)
	}
}

// Columns buffer finished work independently of available teammates.
func TestFreeImplementersFillColumnsBehindASlowReviewer(t *testing.T) {
	s, _ := fixture(t)
	p := staged(t, s, newProject(t, s), 0, map[string]int{StageReviewing: 2},
		Role{Name: "Writer #2", Kinds: []string{RoleImplementer}, Engine: "claude"},
		Role{Name: "Writer #3", Kinds: []string{RoleImplementer}, Engine: "claude"})
	var names []string
	for i := 0; i < 16; i++ {
		names = append(names, string(rune('A'+i)))
	}
	tasks := queueAll(t, s, p, names...)
	if got := claimed(t, s); len(got) != 3 {
		t.Fatalf("writers: %v", got)
	}
	finish(t, s, tasks[0].ID, TaskReviewing)
	claimed(t, s)
	finish(t, s, tasks[1].ID, TaskReviewing)
	got := claimed(t, s)
	if !slices.Contains(got, "E: writing by Writer #2") {
		t.Fatalf("free writer did not start: %v", got)
	}
	if b := onBoard(t, s, tasks[1].ID); b.Place != StageReviewing || b.Waiting == nil || b.Waiting.Kind != WaitMember {
		t.Fatalf("buffered review: %+v", b)
	}
	// Finish each new implementation; Reviewing stays full, Implementing
	// fills to ten while each free writer keeps taking work.
	for i := 2; i < 12; i++ {
		finish(t, s, tasks[i].ID, TaskReviewing)
		claimed(t, s)
	}
	want := Wait{Kind: WaitStage, Stage: StageReviewing, From: StageImplementing, Count: 2, Limit: 2}
	if w := waiting(t, s, tasks[2].ID); w == nil || *w != want {
		t.Fatalf("finished draft: %+v", w)
	}
	if w := waiting(t, s, tasks[12].ID); w == nil || *w != (Wait{Kind: WaitStage, Stage: StageImplementing, Count: 10, Limit: 10}) {
		t.Fatalf("eleventh implementation: %+v", w)
	}
}

// staged gives p's team QA, the seats extra, cap tasks under way at once
// and limits on its stages.
func staged(t *testing.T, s *Service, p Project, cap int, limits map[string]int, extra ...Role) Project {
	t.Helper()
	playbook := *p.Playbook
	playbook.Roles = append(slices.Clone(playbook.Roles), extra...)
	if !slices.ContainsFunc(playbook.Roles, func(r Role) bool { return r.Holds(RoleQA) }) {
		playbook.Roles = append(playbook.Roles, Role{Name: "QA", Kinds: []string{RoleQA}, Engine: "codex"})
	}
	playbook.Check, playbook.MaxActive, playbook.StageLimits = "make check", cap, limits
	p, err := s.SetPlaybook(testContext, p.ID, playbook)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// judge records seat's verdict on a task's latest draft and ends its check.
func judge(t *testing.T, s *Service, id, seat, outcome string) {
	t.Helper()
	if _, err := s.UpdateTask(testContext, id, func(t *Task, p *Project) (string, error) {
		t.Claims = slices.DeleteFunc(t.Claims, func(c Claim) bool { return c.Seat == seat })
		t.Verdicts = append(t.Verdicts, Verdict{Role: seat, Revision: t.Revisions[len(t.Revisions)-1].N, BriefVersion: p.Brief.Version, TextVersion: t.TextVersion, Outcome: outcome})
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
}

// onBoard is a task as the board shows it.
func onBoard(t *testing.T, s *Service, id string) Task {
	t.Helper()
	snap, _ := s.Snapshot(testContext)
	task, _ := snap.FindTask(id)
	return task
}

// bottleneck runs four tasks into a project with room for one task in each
// of implementing, review and QA, up to the point where QA is full with A,
// B has passed review, C has been written and D is next on the list.
func bottleneck(t *testing.T, limits map[string]int) (*Service, []Task) {
	t.Helper()
	s, _ := fixture(t)
	p := staged(t, s, newProject(t, s), 10, limits)
	tasks := queueAll(t, s, p, "A", "B", "C", "D")
	if got := claimed(t, s); !slices.Equal(got, []string{"A: writing by Writer"}) {
		t.Fatalf("first look: %v", got)
	}
	finish(t, s, tasks[0].ID, TaskReviewing)
	if got := claimed(t, s); !slices.Equal(got, []string{"A: reviewing by Reviewer", "A: reviewing by QA", "B: writing by Writer"}) {
		t.Fatalf("with A written: %v", got)
	}
	// A passes review while QA's check of it runs on: A is in QA.
	judge(t, s, tasks[0].ID, "Reviewer", VerdictPass)
	if got := claimed(t, s); len(got) != 0 {
		t.Fatalf("with A in QA: %v", got)
	}
	finish(t, s, tasks[1].ID, TaskReviewing)
	if got := claimed(t, s); !slices.Equal(got, []string{"B: reviewing by Reviewer", "C: writing by Writer"}) {
		t.Fatalf("with B written: %v", got)
	}
	judge(t, s, tasks[1].ID, "Reviewer", VerdictPass)
	claimed(t, s)
	finish(t, s, tasks[2].ID, TaskReviewing)
	return s, tasks
}

// A full QA holds a finished review in review, a full review holds a
// finished draft in implementing, and a full implementing stage starts
// nothing from To do, though the writer is free and the cap allows. Each
// says which stage it waits for room in, and shows where it waits.
func TestAFullStagePushesBackUpThePipeline(t *testing.T) {
	s, tasks := bottleneck(t, map[string]int{StageImplementing: 1, StageReviewing: 1, StageQA: 1})
	if got := claimed(t, s); len(got) != 0 {
		t.Fatalf("work moved into a full stage: %v", got)
	}
	for i, want := range []struct {
		stage string
		wait  Wait
	}{
		{StageQA, Wait{}},
		{StageReviewing, Wait{Kind: WaitStage, Stage: StageQA, From: StageReviewing, Count: 1, Limit: 1}},
		{StageImplementing, Wait{Kind: WaitStage, Stage: StageReviewing, From: StageImplementing, Count: 1, Limit: 1}},
		{StageTodo, Wait{Kind: WaitStage, Stage: StageImplementing, Count: 1, Limit: 1}},
	} {
		got := onBoard(t, s, tasks[i].ID)
		if got.Stage != want.stage {
			t.Errorf("%s shows in %s, want %s", got.Objective, got.Stage, want.stage)
		}
		if w := got.Waiting; (w == nil) != (want.wait == Wait{}) || (w != nil && *w != want.wait) {
			t.Errorf("%s waits for %+v, want %+v", got.Objective, w, want.wait)
		}
	}
	// Held tasks are still under way, and count towards the project's cap.
	if d := onBoard(t, s, tasks[3].ID); d.Status != TaskQueued {
		t.Fatalf("D started: %+v", d)
	}
}

// Once QA passes A, the room it leaves is taken on the same look, all the
// way back to To do.
func TestAHeldTaskEntersAsSoonAsRoomFrees(t *testing.T) {
	s, tasks := bottleneck(t, map[string]int{StageImplementing: 1, StageReviewing: 1, StageQA: 1})
	claimed(t, s)
	judge(t, s, tasks[0].ID, "QA", VerdictPass)
	want := []string{"A: reviewing by ", "B: reviewing by QA", "C: reviewing by Reviewer", "D: writing by Writer"}
	if got := claimed(t, s); !slices.Equal(got, want) {
		t.Fatalf("once A passed QA: %v, want %v", got, want)
	}
	for i, stage := range []string{StageReady, StageQA, StageReviewing, StageImplementing} {
		if got := onBoard(t, s, tasks[i].ID); got.Place != stage || got.Waiting != nil {
			t.Errorf("%s: place %s, waiting %+v; want %s", got.Objective, got.Place, got.Waiting, stage)
		}
		// Where stages are limited, each shows in the stage it holds: A in
		// Ready while it is decided.
		if got := onBoard(t, s, tasks[i].ID); got.Stage != stage {
			t.Errorf("%s shows in %s, want %s", got.Objective, got.Stage, stage)
		}
	}
}

// Several tasks may wait in a column even while its teammate is busy.
func TestLargeCapacitiesBufferWorkIndependentlyOfSeats(t *testing.T) {
	s, tasks := bottleneck(t, map[string]int{StageImplementing: 10, StageReviewing: 10, StageQA: 10})
	if got := claimed(t, s); !slices.Equal(got, []string{"C: reviewing by Reviewer", "D: writing by Writer"}) {
		t.Fatalf("with explicit limits: %v", got)
	}
	snap, _ := s.Snapshot(testContext)
	for i, stage := range []string{StageQA, StageQA, StageReviewing, StageImplementing} {
		got, _ := snap.FindTask(tasks[i].ID)
		if got.Stage != stage {
			t.Errorf("%s shows in %s, want %s", got.Objective, got.Stage, stage)
		}
		if got.Waiting != nil && got.Waiting.Kind == WaitStage {
			t.Errorf("%s waits for room: %+v", got.Objective, got.Waiting)
		}
	}
}

// Starting work needs a free person, room and an available overall slot.
// Moving finished work into a column needs only room.
func TestStageLimitsCombineWithPeopleAndTheCap(t *testing.T) {
	writer2 := Role{Name: "Writer #2", Kinds: []string{RoleImplementer}, Engine: "claude"}
	for name, c := range map[string]struct {
		cap    int
		limits map[string]int
		extra  []Role
		want   Wait
	}{
		"room, but the one writer is busy": {cap: 3, limits: map[string]int{StageImplementing: 2}, want: Wait{Kind: WaitMember, Seat: "Writer"}},
		"room, but the cap is reached":     {cap: 1, limits: map[string]int{StageImplementing: 2}, extra: []Role{writer2}, want: Wait{Kind: WaitProjectCap, Active: 1, Cap: 1}},
		"a free writer, but no room":       {cap: 3, limits: map[string]int{StageImplementing: 1}, extra: []Role{writer2}, want: Wait{Kind: WaitStage, Stage: StageImplementing, Count: 1, Limit: 1}},
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := fixture(t)
			p := staged(t, s, newProject(t, s), c.cap, c.limits, c.extra...)
			tasks := queueAll(t, s, p, "A", "B")
			if got := claimed(t, s); !slices.Equal(got, []string{"A: writing by Writer"}) {
				t.Fatalf("first look: %v", got)
			}
			w := waiting(t, s, tasks[1].ID)
			if w != nil {
				w.On = ""
			}
			if w == nil || *w != c.want {
				t.Fatalf("B waits for %+v, want %+v", w, c.want)
			}
		})
	}
	// Room in review admits a finished draft even while the reviewer is busy.
	s, _ := fixture(t)
	p := staged(t, s, newProject(t, s), 3, map[string]int{StageReviewing: 2}, writer2)
	tasks := queueAll(t, s, p, "A", "B")
	claimed(t, s)
	finish(t, s, tasks[0].ID, TaskReviewing)
	finish(t, s, tasks[1].ID, TaskReviewing)
	if got := claimed(t, s); !slices.Equal(got, []string{"A: reviewing by Reviewer", "A: reviewing by QA"}) {
		t.Fatalf("with A and B written: %v", got)
	}
	if b := onBoard(t, s, tasks[1].ID); b.Stage != StageReviewing || b.Place != StageReviewing || b.Waiting == nil || b.Waiting.Kind != WaitMember || b.Waiting.Seat != "Reviewer" {
		t.Fatalf("B should wait in reviewing for the reviewer: %s %s %+v", b.Stage, b.Place, b.Waiting)
	}
	judge(t, s, tasks[0].ID, "Reviewer", VerdictPass)
	if got := claimed(t, s); !slices.Equal(got, []string{"B: reviewing by Reviewer"}) {
		t.Fatalf("once the reviewer is free: %v", got)
	}
	if b := onBoard(t, s, tasks[1].ID); b.Stage != StageReviewing || b.Place != StageReviewing {
		t.Fatalf("B should be in review once the reviewer takes it: %s %s", b.Stage, b.Place)
	}
}

// The reviewer and QA check a draft side by side whatever QA holds: the
// task is in review until the reviewer passes it, and a draft QA has
// already passed goes on towards Ready without waiting for room in QA.
func TestChecksRunSideBySideWhateverQAHolds(t *testing.T) {
	s, _ := fixture(t)
	quinn := Role{Name: "Quinn", Kinds: []string{RoleQA}, Engine: "codex", Member: "quinn"}
	quinn2 := quinn
	quinn2.Name = "Quinn #2"
	p := staged(t, s, newProject(t, s), 3, map[string]int{StageQA: 1, StageReady: 1}, quinn, quinn2)
	tasks := queueAll(t, s, p, "A", "B")
	claimed(t, s)
	finish(t, s, tasks[0].ID, TaskReviewing)
	claimed(t, s)
	judge(t, s, tasks[0].ID, "Reviewer", VerdictPass)
	claimed(t, s)
	if a := onBoard(t, s, tasks[0].ID); a.Place != StageQA {
		t.Fatalf("A should be in QA: %+v", a.Place)
	}
	finish(t, s, tasks[1].ID, TaskReviewing)
	if got := claimed(t, s); !slices.Equal(got, []string{"B: reviewing by Reviewer", "B: reviewing by Quinn #2"}) {
		t.Fatalf("B's checks should run side by side while QA is full: %v", got)
	}
	if b := onBoard(t, s, tasks[1].ID); b.Place != StageReviewing {
		t.Fatalf("B should count in review: %s", b.Place)
	}
	judge(t, s, tasks[1].ID, "Quinn #2", VerdictPass)
	judge(t, s, tasks[1].ID, "Reviewer", VerdictPass)
	if got := claimed(t, s); !slices.Equal(got, []string{"B: reviewing by "}) {
		t.Fatalf("B, passed by both, should go on: %v", got)
	}
	if b := onBoard(t, s, tasks[1].ID); b.Place != StageReady {
		t.Fatalf("B should be in Ready, past a full QA: %s", b.Place)
	}
	// Ready is now full: A, once QA passes it, waits in QA.
	judge(t, s, tasks[0].ID, "Quinn", VerdictPass)
	claimed(t, s)
	if a := onBoard(t, s, tasks[0].ID); a.Stage != StageQA || a.Waiting == nil || *a.Waiting != (Wait{Kind: WaitStage, Stage: StageReady, From: StageQA, Count: 1, Limit: 1}) {
		t.Fatalf("A should wait in QA for room in Ready: %s %+v", a.Stage, a.Waiting)
	}
}

// A draft the reviewer turned down stays in review, however full QA is,
// and goes back without ever waiting for room.
func TestADraftTurnedDownNeverWaitsForRoom(t *testing.T) {
	s, tasks := bottleneck(t, map[string]int{StageQA: 1, StageReviewing: 3})
	claimed(t, s)
	judge(t, s, tasks[2].ID, "Reviewer", VerdictRevise)
	claimed(t, s)
	if c := onBoard(t, s, tasks[2].ID); c.Place != StageReviewing || c.Waiting != nil && c.Waiting.Kind == WaitStage {
		t.Fatalf("C should stay in review: %s %+v", c.Place, c.Waiting)
	}
	finish(t, s, tasks[2].ID, TaskWriting)
	claimed(t, s)
	if c := onBoard(t, s, tasks[2].ID); c.Place != StageImplementing {
		t.Fatalf("C should be back in implementing: %s", c.Place)
	}
}

// A restart forgets what tasks waited for but not the stage each holds, so
// the first look after it holds them back again, just as before.
func TestStageHoldsSurviveARestart(t *testing.T) {
	s, tasks := bottleneck(t, map[string]int{StageImplementing: 1, StageReviewing: 1, StageQA: 1})
	claimed(t, s)
	before := make([]Task, len(tasks))
	for i := range tasks {
		before[i] = onBoard(t, s, tasks[i].ID)
	}
	if err := s.RecoverClaims(testContext, nil); err != nil {
		t.Fatal(err)
	}
	for i := range tasks {
		if got := onBoard(t, s, tasks[i].ID); got.Place != before[i].Place || got.Waiting != nil {
			t.Errorf("%s after the restart: place %s, waiting %+v", got.Objective, got.Place, got.Waiting)
		}
	}
	// A's QA check is claimed again; nothing else moves.
	if got := claimed(t, s); !slices.Equal(got, []string{"A: reviewing by QA"}) {
		t.Fatalf("first look after the restart: %v", got)
	}
	for i := range tasks {
		got := onBoard(t, s, tasks[i].ID)
		if got.Stage != before[i].Stage || (got.Waiting == nil) != (before[i].Waiting == nil) || got.Waiting != nil && *got.Waiting != *before[i].Waiting {
			t.Errorf("%s: %s %+v, before the restart %s %+v", got.Objective, got.Stage, got.Waiting, before[i].Stage, before[i].Waiting)
		}
	}
}

// Working columns and To do accept bounded owner-set capacities.
func TestStageLimitsAreForWorkingStages(t *testing.T) {
	playbook := Templates["draft"]
	playbook.StageLimits = map[string]int{StageImplementing: 2, StageReady: 1, StageQA: 0}
	if err := playbook.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, limits := range map[string]map[string]int{
		"triage":       {StageTriage: 1},
		"done":         {StageDone: 1},
		"unknown":      {"testing": 1},
		"negative":     {StageQA: -1},
		"past the cap": {StageQA: maxCapacity + 1},
	} {
		playbook.StageLimits = limits
		if playbook.Validate() == nil {
			t.Errorf("%s was taken", name)
		}
	}
}

// Opening a pull request needs room among the open ones; one GitHub says is
// ready moves on to Ready by itself and makes room.
func TestALimitOnOpenPullRequestsHoldsTheNextOpening(t *testing.T) {
	s, _ := fixture(t)
	p := staged(t, s, newProject(t, s), 0, map[string]int{StagePROpen: 1})
	tasks := queueAll(t, s, p, "A", "B")
	claimed(t, s)
	for i, task := range tasks {
		if _, err := s.UpdateTask(testContext, task.ID, func(t *Task, p *Project) (string, error) {
			pinned := *p.Playbook
			pinned.Land = LandPolicy{PullRequests: true, Target: "main", GitHub: "o/r"}
			t.Playbook = &pinned
			t.Claims, t.Revisions, t.Place = nil, []Revision{{N: 1}}, StagePROpening
			t.Status = TaskLanding
			if i == 0 {
				t.Status, t.Proposal = TaskAwaiting, &Proposal{Number: 7, Observed: &Observed{Checks: "PENDING"}}
			}
			return "", nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	claimed(t, s)
	if got := onBoard(t, s, tasks[0].ID); got.Place != StagePROpen || got.Stage != StagePROpen {
		t.Fatalf("A's open pull request is not in PR open: %+v", got)
	}
	if w := waiting(t, s, tasks[1].ID); w == nil || w.Kind != WaitStage || w.Stage != StagePROpen || w.Limit != 1 {
		t.Fatalf("B opened a pull request past the limit: %+v", w)
	}
	if got := onBoard(t, s, tasks[1].ID); got.Stage != StagePROpening {
		t.Fatalf("B waits in %q", got.Stage)
	}
	if _, err := s.UpdateTask(testContext, tasks[0].ID, func(t *Task, _ *Project) (string, error) {
		t.Proposal.Observed = &Observed{Checks: "SUCCESS", Ready: true}
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := claimed(t, s); !slices.Contains(got, "B: landing by ") {
		t.Fatalf("B did not go on to open its pull request: %v", got)
	}
	if got := onBoard(t, s, tasks[0].ID); got.Stage != StageReady {
		t.Fatalf("A is in %q, not Ready", got.Stage)
	}
}
