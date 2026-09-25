package core

import (
	"errors"
	"strings"
	"testing"
)

// plannedProject is a writing project whose team plans each task first.
func plannedProject(t *testing.T, s *Service) Project {
	t.Helper()
	p := newProject(t, s)
	playbook := *p.Playbook
	playbook.Roles = append([]Role{{Name: "Planner", Kinds: []string{RolePlanner}, Engine: "claude"}}, playbook.Roles...)
	p, err := s.SetPlaybook(testContext, p.ID, playbook)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAMemberAndASeatHoldSeveralRolesButNeverReviewTheirOwnWork(t *testing.T) {
	s, _ := fixture(t)
	ada, err := s.SaveMember(testContext, "", MemberInput{Name: "Ada", Kinds: []string{RolePlanner, RoleImplementer}, Engine: "claude"})
	if err != nil || !ada.Holds(RolePlanner) || !ada.Holds(RoleImplementer) {
		t.Fatalf("member %+v %v", ada, err)
	}
	if old, err := s.SaveMember(testContext, "", MemberInput{Name: "Old", Kind: RoleReviewer, Engine: "codex"}); err != nil || !old.Holds(RoleReviewer) {
		t.Fatalf("an older client's single kind: %+v %v", old, err)
	}
	for _, in := range []MemberInput{
		{Name: "Planner", Kinds: []string{RoleReviewer}, Engine: "codex"},
		{Name: "Zed", Kinds: []string{}, Engine: "codex"},
		{Name: "Zed", Kinds: []string{RoleQA, RoleQA}, Engine: "codex"},
		{Name: "Zed", Kinds: []string{"manager"}, Engine: "codex"},
	} {
		if _, err := s.SaveMember(testContext, "", in); err == nil {
			t.Errorf("accepted %+v", in)
		}
	}
	base := Templates["code"]
	base.Repo, base.Deliver = "/repo", "owner"
	base.Check = "make check"
	for kinds, ok := range map[string]bool{
		"planner implementer":  true,
		"planner reviewer":     true,
		"implementer reviewer": false,
		"reviewer qa":          false,
	} {
		p := base
		p.Roles = []Role{
			{Name: "Seat", Kinds: strings.Fields(kinds), Engine: "claude"},
			{Name: "Impl", Kinds: []string{RoleImplementer}, Engine: "claude"},
			{Name: "Rev", Kinds: []string{RoleReviewer}, Engine: "codex"},
		}
		if strings.Contains(kinds, RoleImplementer) {
			p.Roles = p.Roles[:1:1]
			p.Roles = append(p.Roles, Role{Name: "Rev", Kinds: []string{RoleReviewer}, Engine: "codex"})
		}
		if err := p.Validate(); (err == nil) != ok {
			t.Errorf("a seat holding %s: valid %v, want %v (%v)", kinds, err == nil, ok, err)
		}
	}
}

func TestOlderStateWithOneKindIsReadAsSeveral(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	m, _ := s.SaveMember(testContext, "", MemberInput{Name: "Rune", Kinds: []string{RoleReviewer}, Engine: "codex"})
	s.store.update(testContext, func(v *Snapshot) error {
		v.Members[0].Kinds, v.Members[0].LegacyKind = nil, RoleReviewer
		roles := project(v, p.ID).Playbook.Roles
		roles[0].Kinds, roles[0].LegacyKind = nil, RoleImplementer
		return nil
	})
	snap, _ := s.Snapshot(testContext)
	if got := snap.Members[0]; got.ID != m.ID || !got.Holds(RoleReviewer) || got.LegacyKind != "" {
		t.Fatalf("member %+v", got)
	}
	if role := snap.Projects[0].Playbook.Roles[0]; !role.Holds(RoleImplementer) || role.LegacyKind != "" {
		t.Fatalf("seat %+v", role)
	}
}

func TestAMessageReachesASeatsWorkingRoleAndNeverAPlannerAlone(t *testing.T) {
	team := []Role{{Name: "Ada", Kinds: []string{RolePlanner, RoleImplementer}}, {Name: "Plan", Kinds: []string{RolePlanner}}, {Name: "Rune", Kinds: []string{RoleReviewer}}}
	if r, err := addressee(team, "implementer"); err != nil || r.Name != "Ada" || r.Working() != RoleImplementer {
		t.Fatalf("by kind: %+v %v", r, err)
	}
	if r, _ := addressee(team, "Plan"); r.Working() != "" {
		t.Fatal("a seat that only plans has no working role")
	}
	s, _ := fixture(t)
	p := plannedProject(t, s)
	task, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "A draft"})
	if _, err := s.SendTeamMessage(testContext, p.ID, task.ID, "Planner", FromOwner, "Hello"); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "only plans") {
		t.Fatalf("a message to the planner: %v", err)
	}
}

func TestATaskStartsOnlyOnceWhatItDependsOnHasFinished(t *testing.T) {
	s, _ := fixture(t)
	p := plannedProject(t, s)
	first, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "First"})
	second, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Second", DependsOn: []string{first.ID, first.ID, " "}})
	if err != nil || len(second.DependsOn) != 1 {
		t.Fatalf("dependencies %+v %v", second.DependsOn, err)
	}
	other := newProject(t, s)
	elsewhere, _ := s.QueueTask(testContext, other.ID, TaskInput{Objective: "Elsewhere"})
	if _, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Third", DependsOn: []string{elsewhere.ID}}); err == nil {
		t.Fatal("a dependency in another project was accepted")
	}
	// Move Second ahead of First: it still waits.
	if _, err := s.OrderTasks(testContext, p.ID, []string{second.ID, first.ID}, OrderedByOwner); err != nil {
		t.Fatal(err)
	}
	started, _, _ := s.NextTask(testContext)
	if started.ID != first.ID || started.Status != TaskPlanning {
		t.Fatalf("the first task that can start should plan first: %+v", started)
	}
	snap, _ := s.Snapshot(testContext)
	waiting, _ := findSnapshotTask(snap, second.ID)
	if len(waiting.WaitsFor) != 1 || waiting.WaitsFor[0] != "First" {
		t.Fatalf("waits for %v", waiting.WaitsFor)
	}
	// A stopped dependency no longer holds it back.
	s.UpdateTask(testContext, first.ID, func(t *Task, _ *Project) (string, error) {
		t.Status = TaskStopped
		return "", nil
	})
	next, _, _ := s.NextTask(testContext)
	if next.ID != second.ID {
		t.Fatalf("the second task should start once the first finished: %+v", next)
	}
}

func TestAPlanMovesTheTaskOnAndNeverMakesALoop(t *testing.T) {
	s, _ := fixture(t)
	p := plannedProject(t, s)
	a, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "A"})
	b, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "B", DependsOn: []string{a.ID}})
	started, _, _ := s.NextTask(testContext)
	if started.ID != a.ID {
		t.Fatal(started)
	}
	// A cannot wait for B, which waits for A: that dependency is dropped.
	planned, outcome, err := s.RecordPlan(testContext, a.ID, Plan{Summary: "Do A", Questions: []string{"Which colour?"}}, []string{b.ID})
	if err != nil || outcome != PlanAsks || len(planned.DependsOn) != 0 || planned.Status != TaskPlanning || planned.Plan == nil {
		t.Fatalf("questions keep the task in planning with its plan: %+v %s %v", planned, outcome, err)
	}
	s.UpdateTask(testContext, a.ID, func(t *Task, _ *Project) (string, error) {
		t.Status, t.Plan = TaskPlanning, nil
		return "", nil
	})
	if written, outcome, _ := s.RecordPlan(testContext, a.ID, Plan{Summary: "Do A"}, nil); outcome != PlanWrites || written.Status != TaskWriting || written.Plan.Role != "" || written.Plan.At.IsZero() {
		t.Fatalf("a clear plan starts the writer: %+v %s", written, outcome)
	}
	if _, _, err := s.RecordPlan(testContext, a.ID, Plan{Summary: "again"}, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("a task no longer planning took a plan: %v", err)
	}
	c, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "C"})
	s.UpdateTask(testContext, a.ID, func(t *Task, _ *Project) (string, error) {
		t.Status = TaskWaiting
		return "", nil
	})
	started, _, _ = s.NextTask(testContext)
	if started.ID != c.ID {
		t.Fatalf("C should start while A waits and B waits for A: %+v", started)
	}
	s.UpdateTask(testContext, c.ID, func(t *Task, _ *Project) (string, error) {
		t.Base, t.Branch = "abc", "crew-task/c"
		return "", nil
	})
	// One impossible id among the planner's doesn't lose the rest.
	back, outcome, _ := s.RecordPlan(testContext, c.ID, Plan{Summary: "C builds on A"}, []string{"not-a-task", c.ID, a.ID})
	if outcome != PlanWaits || back.Status != TaskQueued || back.Plan != nil || back.Base != "" || back.Branch != "" || len(back.WaitsFor) != 1 {
		t.Fatalf("a task that waits goes back to the queue to plan again later: %+v %s", back, outcome)
	}
}

func TestPlanningStagesAndTheFirstRound(t *testing.T) {
	task := Task{Round: 1}
	task.NextRound()
	if task.Round != 1 {
		t.Fatal("a round went up before anything was written")
	}
	v := &Snapshot{Decisions: []Decision{{ID: "q", Kind: DecisionQuestion, Status: DecisionOpen}}, Tasks: []Task{
		{Status: TaskPlanning, Roles: []Role{{Name: "Ada", Kinds: []string{RolePlanner, RoleImplementer}}}},
		{Status: TaskWaiting, DecisionID: "q"},
	}}
	deriveStages(v)
	if v.Tasks[0].Stage != StagePlanning || v.Tasks[0].Checking != "Ada" || v.Tasks[1].Stage != StagePlanning {
		t.Fatalf("stages %s %q %s", v.Tasks[0].Stage, v.Tasks[0].Checking, v.Tasks[1].Stage)
	}
	var d Decision
	d.Kind, d.Context = DecisionQuestion, "Which colour?"
	task.AddDirection(&d, "Blue")
	if task.Direction[0] != "Answer to a question (Which colour?): Blue" {
		t.Fatal(task.Direction)
	}
}

func findSnapshotTask(v Snapshot, id string) (Task, bool) {
	for _, t := range v.Tasks {
		if t.ID == id {
			return t, true
		}
	}
	return Task{}, false
}
