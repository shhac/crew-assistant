package core

import (
	"errors"
	"fmt"
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
	planned, err := s.RecordPlan(testContext, a.ID, Plan{Summary: "Do A", Questions: []string{"Which colour?"}}, []string{b.ID}, nil)
	if err != nil || len(planned.DependsOn) != 0 || planned.Status != TaskResearching || planned.Plan == nil {
		t.Fatalf("questions keep the task in research with its plan: %+v %v", planned, err)
	}
	s.UpdateTask(testContext, a.ID, func(t *Task, _ *Project) (string, error) {
		t.Status, t.Plan = TaskResearching, nil
		return "", nil
	})
	if written, _ := s.RecordPlan(testContext, a.ID, Plan{Summary: "Do A"}, nil, nil); written.Status != TaskWriting || written.Plan.Role != "" || written.Plan.At.IsZero() {
		t.Fatalf("a clear plan starts the writer: %+v", written)
	}
	if _, err := s.RecordPlan(testContext, a.ID, Plan{Summary: "again"}, nil, nil); !errors.Is(err, ErrConflict) {
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
	back, _ := s.RecordPlan(testContext, c.ID, Plan{Summary: "C builds on A"}, []string{"not-a-task", c.ID, a.ID}, nil)
	if back.Status != TaskQueued || back.Plan != nil || back.Base != "" || back.Branch != "" || len(back.WaitsFor) != 1 {
		t.Fatalf("a task that waits goes back to the queue to plan again later: %+v", back)
	}
}

// splitTasks is the tasks split off from id, in the order they were queued.
func splitTasks(t *testing.T, s *Service, id string) []Task {
	t.Helper()
	snap, _ := s.Snapshot(testContext)
	var out []Task
	for _, task := range snap.Tasks {
		if task.SplitFrom == id {
			out = append(out, task)
		}
	}
	return out
}

func activityOf(t *testing.T, s *Service, taskID, kind string) []string {
	t.Helper()
	snap, _ := s.Snapshot(testContext)
	var out []string
	for _, a := range snap.Activity {
		if a.TaskID == taskID && a.Kind == kind {
			out = append(out, a.Summary)
		}
	}
	return out
}

func setStatus(t *testing.T, s *Service, id, status string) {
	t.Helper()
	if _, err := s.UpdateTask(testContext, id, func(t *Task, _ *Project) (string, error) {
		t.Status = status
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAPlanQueuesWhatItSplitsOffAndWhatWaitedWaitsForItToo(t *testing.T) {
	s, _ := fixture(t)
	p := plannedProject(t, s)
	a, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "A"})
	waiting, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "D", DependsOn: []string{a.ID}})
	begun, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "E", DependsOn: []string{a.ID}})
	done, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "F", DependsOn: []string{a.ID}})
	if started, _, _ := s.NextTask(testContext); started.ID != a.ID {
		t.Fatal(started)
	}
	setStatus(t, s, begun.ID, TaskWriting)
	setStatus(t, s, done.ID, TaskStopped)
	planned, err := s.RecordPlan(testContext, a.ID, Plan{Summary: "Do A, not the rest", SplitOff: []SplitPart{
		{Objective: " Export as CSV ", Criteria: []string{"Every column", " "}},
		{Objective: "Import CSV"},
	}}, nil, nil)
	if err != nil || planned.Status != TaskWriting {
		t.Fatalf("%+v %v", planned, err)
	}
	split := splitTasks(t, s, a.ID)
	if len(split) != 2 || split[0].Objective != "Export as CSV" || !slices.Equal(split[0].Criteria, []string{"Every column"}) || split[1].Objective != "Import CSV" {
		t.Fatalf("split off %+v", split)
	}
	for _, part := range split {
		if part.ProjectID != p.ID || part.Status != TaskQueued || !slices.Equal(part.DependsOn, []string{a.ID}) || part.LinkedBy["depends_on:"+a.ID].By != "role:researcher" || part.Ref == "" {
			t.Fatalf("a split part %+v", part)
		}
	}
	if got := planned.Plan.SplitOff; len(got) != 2 || got[0].Task != split[0].ID || got[1].Task != split[1].ID {
		t.Fatalf("the plan names %+v", got)
	}
	if got := activityOf(t, s, a.ID, "task.split"); len(got) != 1 || got[0] != "Split Export as CSV; Import CSV off A" {
		t.Fatalf("the original's activity %q", got)
	}
	if got := activityOf(t, s, split[0].ID, "task.queued"); len(got) != 1 || got[0] != "Export as CSV split off A" {
		t.Fatalf("the part's activity %q", got)
	}
	snap, _ := s.Snapshot(testContext)
	d, _ := findSnapshotTask(snap, waiting.ID)
	if !slices.Equal(d.DependsOn, []string{a.ID, split[0].ID, split[1].ID}) || d.LinkedBy["depends_on:"+split[1].ID].By != "role:researcher" {
		t.Fatalf("a task that waited for A waits for the parts too: %+v %+v", d.DependsOn, d.LinkedBy)
	}
	if got := activityOf(t, s, waiting.ID, "task.linked"); len(got) != 2 {
		t.Fatalf("its activity %q", got)
	}
	for _, id := range []string{begun.ID, done.ID} {
		if other, _ := findSnapshotTask(snap, id); !slices.Equal(other.DependsOn, []string{a.ID}) || len(activityOf(t, s, id, "task.linked")) != 0 {
			t.Fatalf("begun or finished work was made to wait: %+v", other)
		}
	}
	// A split part never waits for its sibling.
	if !slices.Equal(split[1].DependsOn, []string{a.ID}) {
		t.Fatal(split[1].DependsOn)
	}
}

func TestPlanningAgainNeverQueuesAPartTwice(t *testing.T) {
	s, _ := fixture(t)
	p := plannedProject(t, s)
	a, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "A"})
	s.NextTask(testContext)
	asked, err := s.RecordPlan(testContext, a.ID, Plan{Summary: "Do A", Questions: []string{"Which?"}, SplitOff: []SplitPart{{Objective: "Export CSV"}, {Objective: "Import CSV"}, {Objective: "Print"}}}, nil, nil)
	if err != nil || asked.Status != TaskResearching || len(splitTasks(t, s, a.ID)) != 3 {
		t.Fatalf("questions keep the plan and queue its parts: %+v %v", asked, err)
	}
	first := splitTasks(t, s, a.ID)
	// The owner stops one part, and the PM renames another.
	setStatus(t, s, first[1].ID, TaskStopped)
	if _, err := s.EditTask(testContext, EditInput{Project: p.ID, Task: first[2].ID, By: "Pim", Kind: RolePM, Objective: "Print a report"}); err != nil {
		t.Fatal(err)
	}
	// A task comes to wait for A while its questions are open.
	late, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Late", DependsOn: []string{a.ID}})
	s.UpdateTask(testContext, a.ID, func(t *Task, _ *Project) (string, error) {
		t.Plan.Answered = true
		return "", nil
	})
	// Spacing repeated or taken away, and case changed, name the same part.
	again, err := s.RecordPlan(testContext, a.ID, Plan{Summary: "Do A", SplitOff: []SplitPart{{Objective: "export  csv"}, {Objective: "ExportCSV"}, {Objective: "IMPORT CSV"}, {Objective: "print"}, {Objective: "Share"}, {Objective: "share "}}}, nil, nil)
	if err != nil || again.Status != TaskWriting {
		t.Fatalf("%+v %v", again, err)
	}
	split := splitTasks(t, s, a.ID)
	if len(split) != 4 || split[3].Objective != "Share" || split[1].Status != TaskStopped {
		t.Fatalf("only the new part is queued: %+v", split)
	}
	var named []string
	for _, part := range again.Plan.SplitOff {
		named = append(named, part.Task)
	}
	if !slices.Equal(named, []string{split[0].ID, split[1].ID, split[2].ID, split[3].ID}) {
		t.Fatalf("the plan names %v", named)
	}
	// It waits for every part still to do, those queued before included,
	// but not for the one the owner stopped.
	snap, _ := s.Snapshot(testContext)
	waiting, _ := findSnapshotTask(snap, late.ID)
	if !slices.Equal(waiting.DependsOn, []string{a.ID, split[0].ID, split[2].ID, split[3].ID}) || len(activityOf(t, s, late.ID, "task.linked")) != 3 {
		t.Fatalf("a task that came to wait for A: %v", waiting.DependsOn)
	}
	// Each plan's activity names every part it splits off, those queued
	// before included, by the title each has now. The clock is frozen, so
	// the plans' activity has the same time and no order between them.
	if got := activityOf(t, s, a.ID, "task.split"); len(got) != 2 || !slices.Contains(got, "Split Export CSV; Import CSV; Print a report; Share off A") || !slices.Contains(got, "Split Export CSV; Import CSV; Print off A") {
		t.Fatalf("the original's activity %q", got)
	}

	// A plan that only names parts queued before queues nothing and still
	// names them.
	s.UpdateTask(testContext, a.ID, func(t *Task, _ *Project) (string, error) {
		t.Status, t.Plan.Answered = TaskResearching, true
		return "", nil
	})
	reused, err := s.RecordPlan(testContext, a.ID, Plan{Summary: "Do A", SplitOff: []SplitPart{{Objective: "Print"}, {Objective: "share"}}}, nil, nil)
	if err != nil || len(splitTasks(t, s, a.ID)) != 4 || len(reused.Plan.SplitOff) != 2 || reused.Plan.SplitOff[0].Task != split[2].ID || reused.Plan.SplitOff[1].Task != split[3].ID {
		t.Fatalf("a plan of parts queued before: %+v %v", reused.Plan, err)
	}
	if got := activityOf(t, s, a.ID, "task.split"); len(got) != 3 || !slices.Contains(got, "Split Print a report; Share off A") {
		t.Fatalf("the original's activity %q", got)
	}
}

func TestOnlyAKeptPlanSplitsWorkOff(t *testing.T) {
	s, _ := fixture(t)
	p := plannedProject(t, s)
	a, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "A"})
	b, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "B"})
	if started, _, _ := s.NextTask(testContext); started.ID != a.ID {
		t.Fatal(started)
	}
	parts := []SplitPart{{Objective: " "}}
	for i := range MaxSplitOff + 2 {
		parts = append(parts, SplitPart{Objective: fmt.Sprintf("Part %d", i)})
	}
	// A task sent back to the queue to wait keeps no plan, and splits
	// nothing off.
	back, err := s.RecordPlan(testContext, a.ID, Plan{Summary: "After B", SplitOff: parts}, []string{b.ID}, nil)
	if err != nil || back.Status != TaskQueued || back.Plan != nil || len(splitTasks(t, s, a.ID)) != 0 {
		t.Fatalf("%+v %v", back, err)
	}
	setStatus(t, s, a.ID, TaskResearching)
	s.UpdateTask(testContext, a.ID, func(t *Task, _ *Project) (string, error) {
		t.DependsOn = nil
		return "", nil
	})
	planned, err := s.RecordPlan(testContext, a.ID, Plan{Summary: "Do A", SplitOff: parts}, nil, nil)
	split := splitTasks(t, s, a.ID)
	if err != nil || len(split) != MaxSplitOff || split[0].Objective != "Part 0" || len(planned.Plan.SplitOff) != MaxSplitOff {
		t.Fatalf("a blank part and those past the limit are left out: %+v %v", split, err)
	}
	snap, _ := s.Snapshot(testContext)
	count := len(snap.Tasks)
	if _, err := s.RecordPlan(testContext, a.ID, Plan{Summary: "again", SplitOff: []SplitPart{{Objective: "Late"}}}, nil, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("a task no longer researching took a plan: %v", err)
	}
	if snap, _ = s.Snapshot(testContext); len(snap.Tasks) != count {
		t.Fatal("a refused plan queued work")
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
