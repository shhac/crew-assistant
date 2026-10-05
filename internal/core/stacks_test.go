package core

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// stackedProject is a code project landing by pull request into main, with
// stacking as stack says.
func stackedProject(t *testing.T, s *Service, stack bool) Project {
	t.Helper()
	p := newProject(t, s)
	playbook := *p.Playbook
	playbook.Medium, playbook.Repo, playbook.BranchPrefix = MediumGit, t.TempDir(), "crew/"
	playbook.Land = LandPolicy{PullRequests: true, Target: "main", GitHub: "o/r", Stack: stack}
	p, err := s.SetPlaybook(testContext, p.ID, playbook)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// opened gives a task an open pull request with a head pushed, as the loop
// does once it has published and opened it.
func opened(t *testing.T, s *Service, id, branch string, number int) {
	t.Helper()
	if _, err := s.UpdateTask(testContext, id, func(t *Task, p *Project) (string, error) {
		t.Playbook, t.Status = p.Playbook, TaskAwaiting
		t.Proposal = &Proposal{Branch: branch, Pushed: "abc123", Number: number}
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestStackingIsOnlyForPullRequests(t *testing.T) {
	if err := (LandPolicy{Via: LandPush, Target: "main", Stack: true}).validate(); err == nil || !strings.Contains(err.Error(), "stacking is only for") {
		t.Fatalf("stacking without pull requests: %v", err)
	}
	if err := (LandPolicy{PullRequests: true, Target: "main", GitHub: "o/r", Stack: true}).validate(); err != nil {
		t.Fatal(err)
	}
}

// A stacked task waits only until the task below has a pull request open
// to build on, never until it lands, and can't merge before it does.
func TestAStackedTaskStartsOnAnOpenPullRequestAndMergesAfterIt(t *testing.T) {
	s, _ := fixture(t)
	p := stackedProject(t, s, true)
	tasks := queueAll(t, s, p, "Schema", "API")
	parent, child := tasks[0], tasks[1]
	got, err := s.LinkTasks(testContext, Link{Project: p.ID, Task: child.ID, Relation: RelationStacksOn, Other: parent.ID, By: LinkedByPM})
	if err != nil || got.StacksOn != parent.ID || got.Stack == nil || got.Stack.Task != parent.ID || got.LinkedBy[linkKey(RelationStacksOn, parent.ID)].By != LinkedByPM {
		t.Fatalf("stacked %+v %v", got, err)
	}
	if !slices.Equal(got.WaitsFor, []string{"Schema"}) || len(got.WaitingOn) != 1 || got.WaitingOn[0].Task != parent.ID {
		t.Fatalf("waits for %v %+v before its parent's pull request opens", got.WaitsFor, got.WaitingOn)
	}
	snap, _ := s.Snapshot(testContext)
	if !heldBack(&snap, taskByID(t, s, child.ID)) {
		t.Fatal("a stacked task could start before there was a pull request to build on")
	}
	opened(t, s, parent.ID, "crew/schema", 7)
	got = taskByID(t, s, child.ID)
	if len(got.WaitsFor) != 0 || got.Stack == nil || !got.Stack.Open() || got.Stack.Branch != "crew/schema" || got.Stack.Number != 7 {
		t.Fatalf("still waits once the pull request is open: %+v", got)
	}
	snap, _ = s.Snapshot(testContext)
	if heldBack(&snap, got) {
		t.Fatal("held back from starting on an open pull request")
	}

	// Built on the parent's branch, it is held from merging until that has
	// merged, and then until it is replayed onto the target.
	got.From, got.Base = "crew/schema", "abc123"
	if held := strings.Join(LandingHeld(&p, got), "; "); !strings.Contains(held, "stacked on “Schema”, whose pull request has not merged") {
		t.Fatalf("held %q", held)
	}
	got.Stack.Status = TaskLanded
	if held := strings.Join(LandingHeld(&p, got), "; "); !strings.Contains(held, "still to be replayed onto the target") {
		t.Fatalf("held %q", held)
	}
	got.From = "main"
	if held := LandingHeld(&p, got); len(held) != 0 {
		t.Fatalf("held once replayed: %v", held)
	}
}

func TestStackingRefusesWhatItCannotDo(t *testing.T) {
	s, _ := fixture(t)
	p := stackedProject(t, s, true)
	tasks := queueAll(t, s, p, "A", "B", "C", "D")
	a, b, c, d := tasks[0], tasks[1], tasks[2], tasks[3]
	link := func(task, other, by string) error {
		_, err := s.LinkTasks(testContext, Link{Project: p.ID, Task: task, Relation: RelationStacksOn, Other: other, By: by})
		return err
	}
	if err := link(a.ID, a.ID, LinkedByOwner); err == nil {
		t.Fatal("stacked a task on itself")
	}
	if err := link(b.ID, a.ID, LinkedByOwner); err != nil {
		t.Fatal(err)
	}
	if err := link(b.ID, c.ID, LinkedByOwner); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "already stacked") {
		t.Fatalf("a second parent: %v", err)
	}
	if err := link(c.ID, b.ID, LinkedByOwner); err != nil {
		t.Fatal(err)
	}
	if err := link(a.ID, c.ID, LinkedByOwner); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "already builds on this task") {
		t.Fatalf("a stack that loops: %v", err)
	}
	// Waiting the other way round loops too.
	if _, err := s.LinkTasks(testContext, Link{Project: p.ID, Task: a.ID, Relation: RelationDependsOn, Other: c.ID, By: LinkedByOwner}); err == nil {
		t.Fatal("a task waits for one stacked on it")
	}
	other := stackedProject(t, s, true)
	elsewhere := queueAll(t, s, other, "Elsewhere")[0]
	if err := link(d.ID, elsewhere.ID, LinkedByOwner); !errors.Is(err, ErrNotFound) {
		t.Fatalf("across projects: %v", err)
	}
	// The team can't take away the owner's stacking, nor stack work already
	// begun; the owner can unstack it whenever.
	if _, err := s.UnlinkTasks(testContext, Link{Project: p.ID, Task: b.ID, Other: a.ID, By: LinkedByPM}); !errors.Is(err, ErrConflict) {
		t.Fatalf("the PM took away the owner's stacking: %v", err)
	}
	if _, err := s.UpdateTask(testContext, d.ID, func(t *Task, _ *Project) (string, error) {
		t.Status, t.Base, t.From = TaskWriting, "f00d", "main"
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := link(d.ID, a.ID, LinkedByPM); !errors.Is(err, ErrConflict) {
		t.Fatalf("the PM stacked work under way: %v", err)
	}
	if err := link(d.ID, a.ID, LinkedByOwner); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "has begun its work") {
		t.Fatalf("stacked work already begun from its own base: %v", err)
	}
	if got, err := s.UnlinkTasks(testContext, Link{Project: p.ID, Task: a.ID, Other: b.ID, By: LinkedByOwner}); err != nil || got.ID != a.ID || taskByID(t, s, b.ID).StacksOn != "" {
		t.Fatalf("the owner unstacked from the parent's end: %v", err)
	}

	off := stackedProject(t, s, false)
	pair := queueAll(t, s, off, "X", "Y")
	if _, err := s.LinkTasks(testContext, Link{Project: off.ID, Task: pair[1].ID, Relation: RelationStacksOn, Other: pair[0].ID, By: LinkedByOwner}); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "stacking is off") {
		t.Fatalf("stacked where stacking is off: %v", err)
	}
	if _, err := s.QueueTask(testContext, off.ID, TaskInput{Objective: "Z", StacksOn: pair[0].ID}); err == nil {
		t.Fatal("queued a stacked task where stacking is off")
	}
}

// Stacking on a task it waits for replaces the wait, and a task never also
// waits for the one it is stacked on.
func TestStackingReplacesWaitingForTheSameTask(t *testing.T) {
	s, _ := fixture(t)
	p := stackedProject(t, s, true)
	tasks := queueAll(t, s, p, "A", "B")
	a, b := tasks[0], tasks[1]
	if _, err := s.LinkTasks(testContext, Link{Project: p.ID, Task: b.ID, Relation: RelationDependsOn, Other: a.ID, By: LinkedByPM}); err != nil {
		t.Fatal(err)
	}
	got, err := s.LinkTasks(testContext, Link{Project: p.ID, Task: b.ID, Relation: RelationStacksOn, Other: a.ID, By: LinkedByPM})
	if err != nil || got.StacksOn != a.ID || len(got.DependsOn) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
	if _, has := got.LinkedBy[linkKey(RelationDependsOn, a.ID)]; has {
		t.Fatal("the replaced wait kept its mark")
	}
	snap, _ := s.Snapshot(testContext)
	if deps, err := dependencies(&snap, got, []string{a.ID}, false); err != nil || len(deps) != 0 {
		t.Fatalf("waits for the task it is stacked on: %v %v", deps, err)
	}
	queued, err := s.QueueTaskAs(testContext, p.ID, TaskInput{Objective: "C", StacksOn: b.Ref}, LinkedByPM)
	if err != nil || queued.StacksOn != b.ID || queued.Stack == nil || queued.Stack.Task != b.ID {
		t.Fatalf("queued stacked: %+v %v", queued, err)
	}
}

// A stacked task whose parent was stopped, or whose pull request closed
// without merging, can't go on where it is; one already replayed can.
func TestABrokenStackIsNamed(t *testing.T) {
	sitting := Task{From: "crew/a", Stack: &StackParent{Objective: "A", Status: TaskAwaiting, Branch: "crew/a", Head: "abc", Number: 7}}
	if why := sitting.StackBroken(); why != "" {
		t.Fatalf("an open parent broke the stack: %s", why)
	}
	closed := sitting
	closed.Stack = &StackParent{Objective: "A", Status: TaskWaiting, Branch: "crew/a", Head: "abc"}
	if why := closed.StackBroken(); !strings.Contains(why, "closed without merging") {
		t.Fatalf("closed: %q", why)
	}
	stopped := sitting
	stopped.Stack = &StackParent{Objective: "A", Status: TaskStopped, Branch: "crew/a", Head: "abc", Number: 7}
	if why := stopped.StackBroken(); !strings.Contains(why, "was stopped") {
		t.Fatalf("stopped: %q", why)
	}
	stopped.From = "main"
	if why := stopped.StackBroken(); why != "" {
		t.Fatalf("broken once replayed: %q", why)
	}
}
