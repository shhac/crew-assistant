package core

import (
	"errors"
	"slices"
	"testing"
)

func linkedProject(t *testing.T) (*Service, Project, []Task) {
	t.Helper()
	s, _ := fixture(t)
	p := newProject(t, s)
	var tasks []Task
	for _, objective := range []string{"Schema", "API", "Dashboard"} {
		task, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: objective, Criteria: []string{"Done"}})
		if err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, task)
	}
	return s, p, tasks
}

func taskByID(t *testing.T, s *Service, id string) Task {
	t.Helper()
	snap, err := s.Snapshot(testContext)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range snap.Tasks {
		if task.ID == id {
			return task
		}
	}
	t.Fatalf("no task %s", id)
	return Task{}
}

func TestTasksLinkEachWayAndOnceAPair(t *testing.T) {
	s, p, tasks := linkedProject(t)
	schema, api, dashboard := tasks[0], tasks[1], tasks[2]
	if _, err := s.LinkTasks(testContext, Link{Project: p.ID, Task: api.ID, Relation: RelationDependsOn, Other: schema.ID, By: LinkedByOwner}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LinkTasks(testContext, Link{Project: p.ID, Task: api.ID, Relation: RelationBlocks, Other: dashboard.ID, By: LinkedByAssistant}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LinkTasks(testContext, Link{Project: p.ID, Task: schema.ID, Relation: RelationRelatesTo, Other: dashboard.ID, By: LinkedByOwner}); err != nil {
		t.Fatal(err)
	}
	if got := taskByID(t, s, api.ID); !slices.Equal(got.DependsOn, []string{schema.ID}) || !slices.Equal(got.Blocks, []string{dashboard.ID}) {
		t.Fatalf("api depends on %v, blocks %v", got.DependsOn, got.Blocks)
	}
	if got := taskByID(t, s, dashboard.ID); !slices.Equal(got.DependsOn, []string{api.ID}) || !slices.Equal(got.RelatesTo, []string{schema.ID}) || len(got.WaitsFor) != 1 {
		t.Fatalf("dashboard %+v", got)
	}
	if got := taskByID(t, s, schema.ID); !slices.Equal(got.RelatesTo, []string{dashboard.ID}) || !slices.Equal(got.Blocks, []string{api.ID}) {
		t.Fatalf("schema %+v", got)
	}
	for name, err := range map[string]error{
		"a second link for a pair": func() error {
			_, err := s.LinkTasks(testContext, Link{Project: p.ID, Task: schema.ID, Relation: RelationDependsOn, Other: dashboard.ID, By: LinkedByOwner})
			return err
		}(),
		"a loop": func() error {
			_, err := s.LinkTasks(testContext, Link{Project: p.ID, Task: schema.ID, Relation: RelationDependsOn, Other: api.ID, By: LinkedByOwner})
			return err
		}(),
		"itself": func() error {
			_, err := s.LinkTasks(testContext, Link{Project: p.ID, Task: schema.ID, Relation: RelationRelatesTo, Other: schema.ID, By: LinkedByOwner})
			return err
		}(),
		"another project": func() error {
			_, err := s.LinkTasks(testContext, Link{Project: "elsewhere", Task: schema.ID, Relation: RelationRelatesTo, Other: api.ID, By: LinkedByOwner})
			return err
		}(),
		"an unknown relation": func() error {
			_, err := s.LinkTasks(testContext, Link{Project: p.ID, Task: api.ID, Relation: "duplicates", Other: dashboard.ID, By: LinkedByOwner})
			return err
		}(),
	} {
		if err == nil {
			t.Errorf("%s was allowed", name)
		}
	}
	if _, err := s.UnlinkTasks(testContext, Link{Project: p.ID, Task: dashboard.ID, Other: api.ID, By: LinkedByOwner}); err != nil {
		t.Fatal(err)
	}
	if got := taskByID(t, s, dashboard.ID); len(got.DependsOn) != 0 || len(got.WaitsFor) != 0 {
		t.Fatalf("unlinking left %+v", got)
	}
}

// The owner's and the assistant's links hold against the team; the team
// may take back only what the team set, and may make only a task that has
// not begun its work wait.
func TestTheTeamLeavesTheOwnersLinksAlone(t *testing.T) {
	s, p, tasks := linkedProject(t)
	schema, api, dashboard := tasks[0], tasks[1], tasks[2]
	member := TeamLinker("", RoleResearcher)
	s.LinkTasks(testContext, Link{Project: p.ID, Task: api.ID, Relation: RelationDependsOn, Other: schema.ID, By: LinkedByOwner})
	if _, err := s.UnlinkTasks(testContext, Link{Project: p.ID, Task: api.ID, Other: schema.ID, By: member}); !errors.Is(err, ErrConflict) {
		t.Fatalf("the team took back the owner's link: %v", err)
	}
	if _, err := s.LinkTasks(testContext, Link{Project: p.ID, Task: dashboard.ID, Relation: RelationRelatesTo, Other: schema.ID, By: member}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UnlinkTasks(testContext, Link{Project: p.ID, Task: schema.ID, Other: dashboard.ID, By: member}); err != nil {
		t.Fatalf("the team couldn't take back its own link: %v", err)
	}
	if _, err := s.UnlinkTasks(testContext, Link{Project: p.ID, Task: api.ID, Other: schema.ID, By: LinkedByAssistant}); err != nil {
		t.Fatalf("the assistant couldn't take back the owner's link: %v", err)
	}
	s.UpdateTask(testContext, dashboard.ID, func(task *Task, _ *Project) (string, error) {
		task.Status = TaskWriting
		return "", nil
	})
	if _, err := s.LinkTasks(testContext, Link{Project: p.ID, Task: dashboard.ID, Relation: RelationDependsOn, Other: api.ID, By: member}); !errors.Is(err, ErrConflict) {
		t.Fatalf("the team held back work under way: %v", err)
	}
	if _, err := s.LinkTasks(testContext, Link{Project: p.ID, Task: dashboard.ID, Relation: RelationDependsOn, Other: api.ID, By: LinkedByOwner}); err != nil {
		t.Fatalf("the owner couldn't: %v", err)
	}
}

// The PM sets the team's part of what a task waits for; the owner's stays,
// its own changes don't bring it back to look, and an answer that only
// repeats the owner's links changes nothing.
func TestThePMKeepsTheOwnersDependencies(t *testing.T) {
	s, p, tasks := linkedProject(t)
	schema, api, dashboard := tasks[0], tasks[1], tasks[2]
	s.LinkTasks(testContext, Link{Project: p.ID, Task: dashboard.ID, Relation: RelationDependsOn, Other: schema.ID, By: LinkedByOwner})
	if _, err := s.ApplyPM(testContext, p.ID, PMAnswer{Depends: map[string][]string{dashboard.ID: {api.ID}}}); err != nil {
		t.Fatal(err)
	}
	got := taskByID(t, s, dashboard.ID)
	if !slices.Equal(got.DependsOn, []string{schema.ID, api.ID}) || got.LinkedBy[linkKey(RelationDependsOn, api.ID)].By != LinkedByPM {
		t.Fatalf("depends on %v, marks %v", got.DependsOn, got.LinkedBy)
	}
	changed, _ := s.ApplyPM(testContext, p.ID, PMAnswer{Depends: map[string][]string{dashboard.ID: {api.ID, schema.ID}}})
	if changed != "" {
		t.Fatalf("repeating the owner's link changed %q", changed)
	}
	if _, err := s.ApplyPM(testContext, p.ID, PMAnswer{Depends: map[string][]string{dashboard.ID: {}}}); err != nil {
		t.Fatal(err)
	}
	if got := taskByID(t, s, dashboard.ID); !slices.Equal(got.DependsOn, []string{schema.ID}) {
		t.Fatalf("the PM dropped the owner's link: %v", got.DependsOn)
	}
	s.LinkTasks(testContext, Link{Project: p.ID, Task: api.ID, Relation: RelationRelatesTo, Other: schema.ID, By: LinkedByPM})
	if snap, _ := s.Snapshot(testContext); projectIn(snap, p.ID).PMDue {
		t.Fatal("the PM's own link brought it back to look")
	}
}

func projectIn(s Snapshot, id string) Project {
	for _, p := range s.Projects {
		if p.ID == id {
			return p
		}
	}
	return Project{}
}

// A team member blocking another task makes that task wait, so it may only
// if that task hasn't begun; and the owner's link holds from either end.
func TestTheTeamBlocksOnlyWorkNotYetBegun(t *testing.T) {
	s, p, tasks := linkedProject(t)
	schema, api := tasks[0], tasks[1]
	member := TeamLinker("", RoleResearcher)
	s.UpdateTask(testContext, api.ID, func(task *Task, _ *Project) (string, error) {
		task.Status = TaskWriting
		return "", nil
	})
	if _, err := s.LinkTasks(testContext, Link{Project: p.ID, Task: schema.ID, Relation: RelationBlocks, Other: api.ID, By: member}); !errors.Is(err, ErrConflict) {
		t.Fatalf("the team held back work under way: %v", err)
	}
	s.LinkTasks(testContext, Link{Project: p.ID, Task: api.ID, Relation: RelationDependsOn, Other: schema.ID, By: LinkedByOwner})
	if _, err := s.UnlinkTasks(testContext, Link{Project: p.ID, Task: schema.ID, Other: api.ID, By: member}); !errors.Is(err, ErrConflict) {
		t.Fatalf("the team took the owner's link back from the other end: %v", err)
	}
	if _, err := s.UnlinkTasks(testContext, Link{Project: p.ID, Task: api.ID, Other: schema.ID, By: LinkedByOwner, While: TaskQueued}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a change for a task that had moved on went through: %v", err)
	}
}

// A researcher naming what the owner already set keeps the owner's mark, and
// the PM can't make a task that has begun wait for more.
func TestMarksStayWithWhoeverSetALinkFirst(t *testing.T) {
	s, p, tasks := linkedProject(t)
	schema, api, dashboard := tasks[0], tasks[1], tasks[2]
	s.LinkTasks(testContext, Link{Project: p.ID, Task: dashboard.ID, Relation: RelationDependsOn, Other: schema.ID, By: LinkedByOwner})
	s.UpdateTask(testContext, dashboard.ID, func(task *Task, _ *Project) (string, error) {
		task.Status = TaskResearching
		return "", nil
	})
	if _, err := s.RecordPlan(testContext, dashboard.ID, Plan{Summary: "Plan"}, []string{schema.ID, api.ID}); err != nil {
		t.Fatal(err)
	}
	got := taskByID(t, s, dashboard.ID)
	if got.LinkedBy[linkKey(RelationDependsOn, schema.ID)].By != LinkedByOwner || got.LinkedBy[linkKey(RelationDependsOn, api.ID)].By != TeamLinker("", RoleResearcher) {
		t.Fatalf("marks %v", got.LinkedBy)
	}
	s.UpdateTask(testContext, api.ID, func(task *Task, _ *Project) (string, error) {
		task.Status = TaskWriting
		return "", nil
	})
	s.ApplyPM(testContext, p.ID, PMAnswer{Depends: map[string][]string{api.ID: {schema.ID}}})
	if got := taskByID(t, s, api.ID); len(got.DependsOn) != 0 {
		t.Fatalf("the PM made work under way wait: %v", got.DependsOn)
	}
}
