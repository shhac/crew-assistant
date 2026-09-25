package core

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// plannedProject is a writing project whose team researches each task first.
func plannedProject(t *testing.T, s *Service) Project {
	t.Helper()
	p := newProject(t, s)
	playbook := *p.Playbook
	playbook.Roles = append([]Role{{Name: "Researcher", Kinds: []string{RoleResearcher}, Engine: "claude"}}, playbook.Roles...)
	p, err := s.SetPlaybook(testContext, p.ID, playbook)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAMemberAndASeatHoldSeveralRolesButNeverReviewTheirOwnWork(t *testing.T) {
	s, _ := fixture(t)
	ada, err := s.SaveMember(testContext, "", MemberInput{Name: "Ada", Kinds: []string{RoleResearcher, RoleImplementer}, Engine: "claude"})
	if err != nil || !ada.Holds(RoleResearcher) || !ada.Holds(RoleImplementer) {
		t.Fatalf("member %+v %v", ada, err)
	}
	if old, err := s.SaveMember(testContext, "", MemberInput{Name: "Old", Kind: RoleReviewer, Engine: "codex"}); err != nil || !old.Holds(RoleReviewer) {
		t.Fatalf("an older client's single kind: %+v %v", old, err)
	}
	for _, in := range []MemberInput{
		{Name: "Researcher", Kinds: []string{RoleReviewer}, Engine: "codex"},
		{Name: "designer", Kinds: []string{RoleReviewer}, Engine: "codex"},
		{Name: "Zed", Kinds: []string{"planner"}, Engine: "codex"},
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
		"researcher implementer":          true,
		"researcher reviewer":             true,
		"designer implementer":            true,
		"researcher designer reviewer pm": true,
		"implementer reviewer":            false,
		"reviewer qa":                     false,
		"planner reviewer":                false,
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
	// A task under way during the upgrade keeps its team too.
	s.QueueTask(testContext, p.ID, TaskInput{Objective: "Under way"})
	running, _, _ := s.NextTask(testContext)
	s.store.update(testContext, func(v *Snapshot) error {
		v.Members[0].Kinds, v.Members[0].LegacyKind = nil, RoleReviewer
		roles := project(v, p.ID).Playbook.Roles
		roles[0].Kinds, roles[0].LegacyKind = nil, RoleImplementer
		t := task(v, running.ID)
		t.Roles[0].Kinds, t.Roles[0].LegacyKind = nil, RoleImplementer
		t.Playbook.Roles[0].Kinds, t.Playbook.Roles[0].LegacyKind = nil, RoleImplementer
		return nil
	})
	snap, _ := s.Snapshot(testContext)
	if got := snap.Members[0]; got.ID != m.ID || !got.Holds(RoleReviewer) || got.LegacyKind != "" {
		t.Fatalf("member %+v", got)
	}
	if role := snap.Projects[0].Playbook.Roles[0]; !role.Holds(RoleImplementer) || role.LegacyKind != "" {
		t.Fatalf("seat %+v", role)
	}
	pinned := task(&snap, running.ID)
	if len(pinned.RolesOf(RoleImplementer)) != 1 || !pinned.Playbook.Roles[0].Holds(RoleImplementer) || pinned.Roles[0].LegacyKind != "" {
		t.Fatalf("task roles %+v, playbook %+v", pinned.Roles, pinned.Playbook.Roles)
	}
}

func TestOlderPlannerStateIsReadAsTheResearcher(t *testing.T) {
	s, _ := fixture(t)
	p := plannedProject(t, s)
	s.SaveMember(testContext, "", MemberInput{Name: "Ada", Kinds: []string{RoleResearcher, RoleImplementer}, Engine: "claude", Model: "opus", Instructions: "Small commits."})
	s.QueueTask(testContext, p.ID, TaskInput{Objective: "Under way"})
	running, _, _ := s.NextTask(testContext)
	// Rewrite the state as a build from before the rename left it: planner
	// kinds, the template's Planner seat, and a task mid-plan that had
	// failed once.
	legacy := func(roles []Role) {
		for i := range roles {
			if roles[i].Holds(RoleResearcher) {
				roles[i].Kinds[slices.Index(roles[i].Kinds, RoleResearcher)] = "planner"
				if roles[i].Member == "" {
					roles[i].Name = "Planner"
				}
			}
		}
	}
	s.store.update(testContext, func(v *Snapshot) error {
		v.Members[0].Kinds = []string{"planner", RoleImplementer}
		v.Members[0].Learnings = []Learning{{ID: "l1", When: "Reading a repository", Text: "Start with its README.", Source: LearnedByOwner}}
		legacy(project(v, p.ID).Playbook.Roles)
		t := task(v, running.ID)
		legacy(t.Roles)
		legacy(t.Playbook.Roles)
		t.Status, t.ResumeStatus, t.Plan = "planning", "planning", &Plan{Summary: "Half done", Role: "Planner"}
		// A wake waiting for the task to move on from planning.
		v.Wakes = append(v.Wakes, Wake{ID: "w", TaskID: t.ID, On: WakeOnTask, Baseline: "planning", Status: WakeWaiting})
		return nil
	})
	snap, _ := s.Snapshot(testContext)
	m := snap.Members[0]
	if !slices.Equal(m.Kinds, []string{RoleResearcher, RoleImplementer}) || m.Model != "opus" || m.Instructions != "Small commits." || len(m.Learnings) != 1 {
		t.Fatalf("member %+v", m)
	}
	seat := snap.Projects[0].Playbook.Roles[0]
	if seat.Name != "Researcher" || !slices.Equal(seat.Kinds, []string{RoleResearcher}) {
		t.Fatalf("seat %+v", seat)
	}
	pinned := task(&snap, running.ID)
	if r, ok := pinned.Researcher(); !ok || r.Name != "Researcher" || pinned.Playbook.Roles[0].Name != "Researcher" || pinned.Plan.Role != "Researcher" {
		t.Fatalf("task team %+v, plan %+v", pinned.Roles, pinned.Plan)
	}
	if pinned.Status != TaskResearching || pinned.ResumeStatus != TaskResearching || pinned.Stage != StageResearching || !pinned.Active() {
		t.Fatalf("task %s resume %s stage %s", pinned.Status, pinned.ResumeStatus, pinned.Stage)
	}
	if w := snap.Wakes[0]; w.Baseline != TaskResearching || w.Status != WakeWaiting {
		t.Fatalf("wake %+v", w)
	}
	// Read again, nothing changes: the migration is done once.
	again, _ := s.Snapshot(testContext)
	if again.Projects[0].Playbook.Roles[0].Name != "Researcher" || task(&again, running.ID).Status != TaskResearching {
		t.Fatal("reading twice changed the team")
	}
	if err := snap.Projects[0].Playbook.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestAMessageReachesASeatsWorkingRoleAndNeverAResearcherAlone(t *testing.T) {
	team := []Role{{Name: "Ada", Kinds: []string{RoleResearcher, RoleImplementer}}, {Name: "Plan", Kinds: []string{RoleResearcher}}, {Name: "Rune", Kinds: []string{RoleReviewer}}}
	if r, err := addressee(team, "implementer"); err != nil || r.Name != "Ada" || r.Working() != RoleImplementer {
		t.Fatalf("by kind: %+v %v", r, err)
	}
	if r, _ := addressee(team, "Plan"); r.Working() != "" {
		t.Fatal("a seat that only researches has no working role")
	}
	s, _ := fixture(t)
	p := plannedProject(t, s)
	task, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "A draft"})
	if _, err := s.SendTeamMessage(testContext, p.ID, task.ID, "Researcher", FromOwner, "Hello"); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "only researches") {
		t.Fatalf("a message to the researcher: %v", err)
	}
	withPM := pmProject(t, s)
	task, _ = s.QueueTask(testContext, withPM.ID, TaskInput{Objective: "Another draft"})
	if _, err := s.SendTeamMessage(testContext, withPM.ID, task.ID, "Pim", FromOwner, "Hello"); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "only keeps the to-do list") {
		t.Fatalf("a message to the PM: %v", err)
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
	if started.ID != first.ID || started.Status != TaskResearching {
		t.Fatalf("the first task that can start should be researched first: %+v", started)
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
	planned, err := s.RecordPlan(testContext, a.ID, Plan{Summary: "Do A", Questions: []string{"Which colour?"}}, []string{b.ID})
	if err != nil || len(planned.DependsOn) != 0 || planned.Status != TaskResearching || planned.Plan == nil {
		t.Fatalf("questions keep the task in research with its plan: %+v %v", planned, err)
	}
	s.UpdateTask(testContext, a.ID, func(t *Task, _ *Project) (string, error) {
		t.Status, t.Plan = TaskResearching, nil
		return "", nil
	})
	if written, _ := s.RecordPlan(testContext, a.ID, Plan{Summary: "Do A"}, nil); written.Status != TaskWriting || written.Plan.Role != "" || written.Plan.At.IsZero() {
		t.Fatalf("a clear plan starts the writer: %+v", written)
	}
	if _, err := s.RecordPlan(testContext, a.ID, Plan{Summary: "again"}, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("a task no longer researching took a plan: %v", err)
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
	// One impossible id among the researcher's doesn't lose the rest.
	back, _ := s.RecordPlan(testContext, c.ID, Plan{Summary: "C builds on A"}, []string{"not-a-task", c.ID, a.ID})
	if back.Status != TaskQueued || back.Plan != nil || back.Base != "" || back.Branch != "" || len(back.WaitsFor) != 1 {
		t.Fatalf("a task that waits goes back to the queue to plan again later: %+v", back)
	}
}

func TestResearchStagesAndTheFirstRound(t *testing.T) {
	task := Task{Round: 1}
	task.NextRound()
	if task.Round != 1 {
		t.Fatal("a round went up before anything was written")
	}
	v := &Snapshot{Decisions: []Decision{{ID: "q", Kind: DecisionQuestion, Status: DecisionOpen}}, Tasks: []Task{
		{Status: TaskResearching, Roles: []Role{{Name: "Ada", Kinds: []string{RoleResearcher, RoleImplementer}}}},
		{Status: TaskWaiting, DecisionID: "q"},
	}}
	deriveStages(v)
	if v.Tasks[0].Stage != StageResearching || v.Tasks[0].Checking != "Ada" || v.Tasks[1].Stage != StageResearching {
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
