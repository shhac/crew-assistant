package core

import (
	"slices"
	"strings"
	"testing"
)

func TestOutsideWaitsRespectLimitSource(t *testing.T) {
	for _, status := range []string{TaskWaiting, TaskAwaiting} {
		for _, limits := range []map[string]int{nil, {StageReviewing: 1}} {
			t.Run(status+"/"+mapLabel(limits), func(t *testing.T) {
				s, _ := fixture(t)
				p := staged(t, s, newProject(t, s), 0, limits)
				tasks := queueAll(t, s, p, "A", "B")
				claimed(t, s)
				finish(t, s, tasks[0].ID, TaskReviewing)
				claimed(t, s)
				finish(t, s, tasks[0].ID, status)
				finish(t, s, tasks[1].ID, TaskReviewing)
				got := claimed(t, s)
				if limits != nil {
					if slices.Contains(got, "B: reviewing by Reviewer") {
						t.Fatalf("outside wait stopped counting against explicit limit: %v", got)
					}
					if w := waiting(t, s, tasks[1].ID); w == nil || w.Kind != WaitStage || w.Stage != StageReviewing || w.Count != 1 {
						t.Fatalf("explicit review limit did not hold B: %+v", w)
					}
					return
				}
				if !slices.Contains(got, "B: reviewing by Reviewer") {
					t.Fatalf("outside wait blocked a free reviewer: %v", got)
				}
				if b := onBoard(t, s, tasks[1].ID); b.Place != StageReviewing || b.Waiting != nil {
					t.Fatalf("B did not enter Reviewing: %+v", b)
				}
				// Coming back to a now-full stage evicts neither task.
				finish(t, s, tasks[0].ID, TaskReviewing)
				claimed(t, s)
				for _, task := range tasks {
					if got := onBoard(t, s, task.ID); got.Place != StageReviewing {
						t.Fatalf("return evicted a task: %+v", got)
					}
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

func mapLabel(limits map[string]int) string {
	if limits == nil {
		return "seat default"
	}
	return "explicit limit"
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

func TestStageDefaultsCountSeatsOfEachRole(t *testing.T) {
	p := Templates["draft"]
	p.Roles = slices.Clone(p.Roles)
	for stage, kind := range stageRole {
		p.Roles = append(p.Roles, Role{Name: stage, Kinds: []string{kind}, Member: "same"}, Role{Name: stage + " #2", Kinds: []string{kind}, Member: "same"})
	}
	for stage, kind := range stageRole {
		want := len(rolesOf(p.Roles, kind))
		if got := p.StageLimit(stage); got != want {
			t.Errorf("%s: %d, want %d", stage, got, want)
		}
		for _, n := range []int{1, 10} {
			p.StageLimits = map[string]int{stage: n}
			if p.StageLimit(stage) != n {
				t.Errorf("%s override %d ignored", stage, n)
			}
		}
		p.StageLimits = nil
	}
	for _, stage := range []string{StageReady, StageTodo, "unknown"} {
		if p.StageLimit(stage) != 0 {
			t.Errorf("roleless stage %s limited", stage)
		}
	}
	if (Playbook{}).StageLimit(StageQA) != 0 {
		t.Fatal("unstaffed QA limited")
	}
	for n := 0; n <= 10; n++ {
		valid := Templates["draft"]
		valid.MaxActive = n
		if err := valid.Validate(); err != nil {
			t.Fatal(err)
		}
		if valid.ActiveCap() != n {
			t.Fatalf("overall limit %d ignored", n)
		}
	}
	invalid := Templates["draft"]
	invalid.MaxActive = 11
	if err := invalid.Validate(); err == nil || !strings.Contains(err.Error(), "0 for no overall limit") {
		t.Fatalf("validation: %v", err)
	}
}

func TestStoppingQAFreesItsDefaultPlaceAfterRestart(t *testing.T) {
	s, tasks := bottleneck(t, nil)
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

// Three writers and two QA seats can hold five tasks together with no
// overall cap. A passed review waiting for QA still holds Reviewing.
func TestSeatDefaultsAllowFiveTasksAcrossStages(t *testing.T) {
	s, _ := fixture(t)
	p := staged(t, s, newProject(t, s), 0, nil,
		Role{Name: "Writer #2", Kinds: []string{RoleImplementer}, Engine: "claude"},
		Role{Name: "Writer #3", Kinds: []string{RoleImplementer}, Engine: "claude"},
		Role{Name: "QA", Kinds: []string{RoleQA}, Member: "qa", Engine: "codex"},
		Role{Name: "QA #2", Kinds: []string{RoleQA}, Member: "qa", Engine: "codex"})
	tasks := queueAll(t, s, p, "A", "B", "C", "D", "E", "F", "G")
	if got := claimed(t, s); len(got) != 3 {
		t.Fatalf("writers: %v", got)
	}
	if w := waiting(t, s, tasks[3].ID); w == nil || *w != (Wait{Kind: WaitStage, Stage: StageImplementing, Count: 3, Limit: 3}) {
		t.Fatalf("fourth: %+v", w)
	}
	for _, task := range tasks[:3] {
		finish(t, s, task.ID, TaskReviewing)
		claimed(t, s)
		if got := onBoard(t, s, task.ID); got.Place != StageReviewing {
			t.Fatalf("task did not reach Reviewing through scheduling: %+v", got)
		}
		judge(t, s, task.ID, "Reviewer", VerdictPass)
		claimed(t, s)
	}
	claims := 0
	for _, task := range tasks {
		claims += len(onBoard(t, s, task.ID).Claims)
	}
	if claims != 5 {
		t.Fatalf("two QA plus three new writers: %d claims", claims)
	}
	// C holds its finished review until QA has room.
	if c := onBoard(t, s, tasks[2].ID); c.Place != StageReviewing || c.Stage != StageReviewing || c.Waiting == nil || c.Waiting.Stage != StageQA {
		t.Fatalf("held C: %+v", c)
	}
	for _, task := range tasks[:5] {
		got := onBoard(t, s, task.ID)
		if !got.Active() || got.Waiting != nil && got.Waiting.Kind == WaitProjectCap {
			t.Fatalf("not active without cap: %+v", got)
		}
	}
	// Removing a seat evicts no task, and the pinned claims stay intact.
	book := *p.Playbook
	book.Roles = slices.DeleteFunc(slices.Clone(book.Roles), func(r Role) bool { return r.Name == "QA #2" })
	if _, err := s.SetPlaybook(testContext, p.ID, book); err != nil {
		t.Fatal(err)
	}
	claimed(t, s)
	for _, task := range tasks[:2] {
		if got := onBoard(t, s, task.ID); got.Place != StageQA || len(got.Claims) != 1 {
			t.Fatalf("evicted QA: %+v", got)
		}
	}
	finish(t, s, tasks[0].ID, TaskStopped)
	claimed(t, s)
	if c := onBoard(t, s, tasks[2].ID); c.Waiting == nil || c.Waiting.Count != 1 || c.Waiting.Limit != 1 {
		t.Fatalf("still full: %+v", c.Waiting)
	}
	finish(t, s, tasks[1].ID, TaskStopped)
	claimed(t, s)
	if c := onBoard(t, s, tasks[2].ID); c.Place != StageQA || len(c.Claims) != 1 {
		t.Fatalf("QA seat not freed: %+v", c)
	}
	for _, task := range tasks[:2] {
		if got := onBoard(t, s, task.ID); got.Place != "" || len(got.Claims) != 0 {
			t.Fatalf("stopped holds place or seat: %+v", got)
		}
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

// Explicit limits above the defaults allow several held tasks per stage.
func TestExplicitLimitsReplaceSeatDefaults(t *testing.T) {
	s, tasks := bottleneck(t, map[string]int{StageImplementing: 10, StageReviewing: 10, StageQA: 10})
	if got := claimed(t, s); !slices.Equal(got, []string{"C: reviewing by Reviewer", "D: writing by Writer"}) {
		t.Fatalf("with explicit limits: %v", got)
	}
	snap, _ := s.Snapshot(testContext)
	for i, stage := range []string{StageQA, StageReviewing, StageReviewing, StageImplementing} {
		got, _ := snap.FindTask(tasks[i].ID)
		if got.Stage != stage {
			t.Errorf("%s shows in %s, want %s", got.Objective, got.Stage, stage)
		}
		if got.Waiting != nil && got.Waiting.Kind == WaitStage {
			t.Errorf("%s waits for room: %+v", got.Objective, got.Waiting)
		}
	}
}

// A task moves on only when a person is free, the next stage has room and
// the project's cap allows: none of them overrides another.
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
	// Room in review, but the reviewer is busy: a finished draft waits in
	// implementing for them, taking no room in review until they take it.
	s, _ := fixture(t)
	p := staged(t, s, newProject(t, s), 3, map[string]int{StageReviewing: 2}, writer2)
	tasks := queueAll(t, s, p, "A", "B")
	claimed(t, s)
	finish(t, s, tasks[0].ID, TaskReviewing)
	finish(t, s, tasks[1].ID, TaskReviewing)
	if got := claimed(t, s); !slices.Equal(got, []string{"A: reviewing by Reviewer", "A: reviewing by QA"}) {
		t.Fatalf("with A and B written: %v", got)
	}
	if b := onBoard(t, s, tasks[1].ID); b.Stage != StageImplementing || b.Place != StageImplementing || b.Waiting == nil || b.Waiting.Kind != WaitMember || b.Waiting.Seat != "Reviewer" {
		t.Fatalf("B should wait in implementing for the reviewer: %s %s %+v", b.Stage, b.Place, b.Waiting)
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

// Limits apply to the working stages only, each from 1 to the most a
// project may have under way.
func TestStageLimitsAreForWorkingStages(t *testing.T) {
	playbook := Templates["draft"]
	playbook.StageLimits = map[string]int{StageImplementing: 2, StageReady: 1, StageQA: 0}
	if err := playbook.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, limits := range map[string]map[string]int{
		"To do":        {StageTodo: 1},
		"triage":       {StageTriage: 1},
		"done":         {StageDone: 1},
		"unknown":      {"testing": 1},
		"negative":     {StageQA: -1},
		"past the cap": {StageQA: maxActiveLimit + 1},
	} {
		playbook.StageLimits = limits
		if playbook.Validate() == nil {
			t.Errorf("%s was taken", name)
		}
	}
}
