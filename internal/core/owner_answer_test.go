package core

import (
	"context"
	"errors"
	"fmt"
	"github.com/shhac/crew-assistant/internal/config"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestOwnerAnswerUndoSettlementMergesCurrentEvidence(t *testing.T) {
	for _, scenario := range []string{"same draft", "new draft", "newer evidence"} {
		t.Run(scenario, func(t *testing.T) {
			s, _ := fixture(t)
			p := newProject(t, s)
			owner := "I will review after landing"
			task, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Build", Criteria: []string{owner, "Team"}})
			if err != nil {
				t.Fatal(err)
			}
			original := Unreachable{Criterion: owner, Revision: 1, Why: "Original refusal"}
			unrelated := Unreachable{Criterion: "Team", Revision: 1, Why: "Unrelated refusal"}
			_, err = s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
				t.Revisions = []Revision{{N: 1}}
				t.Unreachable = []Unreachable{original, unrelated}
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			folded, err := s.EditTask(testContext, EditInput{Project: p.ID, Task: task.ID, Kind: RolePM, OwnerChecks: []string{owner}})
			if err != nil {
				t.Fatal(err)
			}
			edit := folded.Edits[len(folded.Edits)-1]
			if !slices.Equal(edit.Settled, []Unreachable{original}) {
				t.Fatal("settlement evidence not recorded")
			}
			newer := Unreachable{Criterion: owner, Revision: 1, Why: "Newer refusal"}
			_, err = s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
				switch scenario {
				case "new draft":
					t.Revisions = append(t.Revisions, Revision{N: 2})
				case "newer evidence":
					t.Unreachable = append(t.Unreachable, newer)
				}
				t.Unreachable = append(t.Unreachable, Unreachable{Criterion: "Later unrelated", Revision: 1, Why: "Retain"})
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "same draft" {
				before := mustSnapshot(t, s)
				if _, err := s.store.db.Exec("CREATE TRIGGER refuse_undo BEFORE UPDATE ON state BEGIN SELECT RAISE(ABORT, 'test failure'); END;"); err != nil {
					t.Fatal(err)
				}
				if _, err := s.UndoTaskEdit(testContext, p.ID, task.ID, edit.ID); err == nil {
					t.Fatal("undo unexpectedly committed")
				}
				if !reflect.DeepEqual(before, mustSnapshot(t, s)) {
					t.Fatal("failed undo partially restored evidence")
				}
				if _, err := s.store.db.Exec("DROP TRIGGER refuse_undo"); err != nil {
					t.Fatal(err)
				}
			}
			undone, err := s.UndoTaskEdit(testContext, p.ID, task.ID, edit.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(undone.Unreachable, unrelated) || !slices.Contains(undone.Unreachable, Unreachable{Criterion: "Later unrelated", Revision: 1, Why: "Retain"}) {
				t.Fatal("undo clobbered unrelated evidence")
			}
			switch scenario {
			case "same draft":
				if !slices.Contains(undone.Unreachable, original) {
					t.Fatal("undo lost original refusal")
				}
				redone, err := s.UndoTaskEdit(testContext, p.ID, task.ID, undone.Edits[len(undone.Edits)-1].ID)
				if err != nil || slices.Contains(redone.Unreachable, original) {
					t.Fatalf("redo: %v", err)
				}
				again, err := s.UndoTaskEdit(testContext, p.ID, task.ID, redone.Edits[len(redone.Edits)-1].ID)
				if err != nil || !slices.Contains(again.Unreachable, original) {
					t.Fatalf("undo redo: %v", err)
				}
			case "new draft":
				if slices.Contains(undone.Unreachable, original) {
					t.Fatal("undo resurrected obsolete draft evidence")
				}
			case "newer evidence":
				if !slices.Contains(undone.Unreachable, newer) || slices.Contains(undone.Unreachable, original) {
					t.Fatal("undo overwrote newer evidence")
				}
			}
		})
	}
}

func TestOwnerAnswerReplaysExistingLongChecks(t *testing.T) {
	for _, source := range []string{"creation", "plan", "step"} {
		t.Run(source, func(t *testing.T) {
			s, _ := fixture(t)
			p := plannedProject(t, s)
			long := strings.Repeat("x", 700)
			in := TaskInput{Objective: "Build", Criteria: []string{"Team"}}
			if source == "creation" {
				in.OwnerChecks = []string{long}
			}
			if source == "plan" {
				in.Criteria = append(in.Criteria, long)
			}
			task, err := s.QueueTask(testContext, p.ID, in)
			if err != nil {
				t.Fatal(err)
			}
			switch source {
			case "creation":
				long = task.OwnerChecks[0]
			case "plan":
				s.NextTask(testContext)
				task, err = s.RecordPlan(testContext, task.ID, Plan{Summary: "Build", Role: "Researcher", OwnerChecks: []string{long}}, nil, nil)
			case "step":
				task, err = s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) { t.OwnerSteps = []string{long}; return "", nil })
			}
			if err != nil {
				t.Fatal(err)
			}
			before := mustSnapshot(t, s)
			replay, err := s.EditTask(testContext, EditInput{Project: p.ID, Task: task.ID, Kind: RolePM, OwnerChecks: []string{long, long}})
			if err != nil || !reflect.DeepEqual(task, replay) || !reflect.DeepEqual(before, mustSnapshot(t, s)) {
				t.Fatalf("duplicate-only replay: %v", err)
			}
			mixed, err := s.EditTask(testContext, EditInput{Project: p.ID, Task: task.ID, Kind: RoleResearcher, Add: []string{"Regression"}, OwnerChecks: []string{long}})
			if err != nil || !slices.Contains(mixed.Criteria, "Regression") || !slices.Equal(mixed.OwnerChecks, task.OwnerChecks) || !slices.Equal(mixed.OwnerSteps, task.OwnerSteps) {
				t.Fatalf("mixed replay: %v", err)
			}
		})
	}
}

func TestOwnerAnswerExistingLargeChecklist(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	checks := make([]string, maxCriteria+1)
	for i := range checks {
		checks[i] = fmt.Sprintf("I will check %d after landing", i)
	}
	task, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Build", Criteria: []string{"Existing"}, OwnerChecks: checks})
	if err != nil {
		t.Fatal(err)
	}
	added, err := s.EditTask(testContext, EditInput{Project: p.ID, Task: task.ID, Kind: RoleQA, Add: []string{"Regression"}})
	if err != nil || !slices.Equal(added.OwnerChecks, checks) || !slices.Equal(added.Criteria, []string{"Existing", "Regression"}) {
		t.Fatalf("ordinary team edit: %+v %v", added, err)
	}
	before := mustSnapshot(t, s)
	replayed, err := s.EditTask(testContext, EditInput{Project: p.ID, Task: task.ID, Kind: RolePM, OwnerChecks: []string{checks[0], checks[0]}})
	if err != nil || !reflect.DeepEqual(added, replayed) || !reflect.DeepEqual(before, mustSnapshot(t, s)) {
		t.Fatalf("duplicate replay: %+v %v", replayed, err)
	}
	mixed, err := s.EditTask(testContext, EditInput{Project: p.ID, Task: task.ID, Kind: RoleResearcher, Add: []string{"Docs"}, OwnerChecks: []string{checks[0]}})
	if err != nil || !slices.Equal(mixed.OwnerChecks, checks) || !slices.Contains(mixed.Criteria, "Docs") {
		t.Fatalf("mixed edit with unchanged checklist: %+v %v", mixed, err)
	}
	before = mustSnapshot(t, s)
	if _, err := s.EditTask(testContext, EditInput{Project: p.ID, Task: task.ID, Kind: RolePM, Add: []string{"Uncommitted"}, OwnerChecks: []string{"I will check something new"}}); err == nil {
		t.Fatal("accepted growth beyond limit")
	}
	if !reflect.DeepEqual(before, mustSnapshot(t, s)) {
		t.Fatal("failed growth partially committed")
	}
}

func TestOwnerAnswerRestartAndFailedWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewService(st, config.Default())
	p := newProject(t, s)
	task, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Build", Criteria: []string{"Existing"}})
	if err != nil {
		t.Fatal(err)
	}
	in := EditInput{Project: p.ID, Task: task.ID, Kind: RolePM, By: "PM", Add: []string{"Regression"}, OwnerChecks: []string{"After landing, I will review it."}}
	if _, err := s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
		t.Unreachable = []Unreachable{{Criterion: in.OwnerChecks[0], Revision: 1}, {Criterion: "Unrelated pending", Revision: 1}}
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
	reopen := func() {
		t.Helper()
		st.Close()
		st, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		s = NewService(st, config.Default())
	}
	defer func() { st.Close() }()
	reopen()
	if got := taskByID(t, s, task.ID); len(got.OwnerChecks) != 0 || len(got.Edits) != 0 {
		t.Fatal("restart changed task before folding")
	}
	before := mustSnapshot(t, s)
	if _, err := st.db.Exec("CREATE TRIGGER refuse_edit BEFORE UPDATE ON state BEGIN SELECT RAISE(ABORT, 'test failure'); END;"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EditTask(testContext, in); err == nil {
		t.Fatal("write succeeded")
	}
	if !reflect.DeepEqual(before, mustSnapshot(t, s)) {
		t.Fatal("failed write retained partial edit")
	}
	if _, err := st.db.Exec("DROP TRIGGER refuse_edit"); err != nil {
		t.Fatal(err)
	}
	got, err := s.EditTask(testContext, in)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Unreachable, []Unreachable{{Criterion: "Unrelated pending", Revision: 1}}) {
		t.Fatalf("pending settlement: %+v", got.Unreachable)
	}
	reopen()
	// A later role failure does not undo the committed edit. Its retry
	// starts from the durable record and adds nothing.
	replayed, err := s.EditTask(testContext, in)
	if err != nil || !reflect.DeepEqual(got, replayed) {
		t.Fatalf("restart replay: %+v %v", replayed, err)
	}
}

func TestOwnerAnswerAtomicEditAndReplay(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	task, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Build", Criteria: []string{"Existing"}, OwnerChecks: []string{"Earlier"}})
	clause := "After landing, I will review it visually."
	in := EditInput{Project: p.ID, Task: task.ID, By: "Planner", Kind: RoleResearcher, Add: []string{"Add the regression test"}, OwnerChecks: []string{clause, clause}}
	got, err := s.EditTask(testContext, in)
	if err != nil || !slices.Equal(got.Criteria, []string{"Existing", "Add the regression test"}) || !slices.Equal(got.OwnerChecks, []string{"Earlier", clause}) || len(got.Edits) != 1 || got.Edits[0].By != "Planner" || got.TextVersion != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	replay, err := s.EditTask(testContext, in)
	if err != nil || !reflect.DeepEqual(got, replay) {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	undone, err := s.UndoTaskEdit(testContext, p.ID, task.ID, got.Edits[0].ID)
	if err != nil || !slices.Equal(undone.Criteria, task.Criteria) || !slices.Equal(undone.OwnerChecks, task.OwnerChecks) {
		t.Fatalf("undo: %+v %v", undone, err)
	}
}

func TestOwnerAnswerRefusalsAreAtomic(t *testing.T) {
	for _, scenario := range []string{"unauthorized", "stale", "missing version", "cancelled", "stopped", "moved", "too long"} {
		t.Run(scenario, func(t *testing.T) {
			s, _ := fixture(t)
			p := newProject(t, s)
			task, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Build", Criteria: []string{"Existing"}})
			ctx := testContext
			version := 99
			in := EditInput{Project: p.ID, Task: task.ID, Kind: RolePM, Add: []string{"Team"}, OwnerChecks: []string{"I will review it"}}
			switch scenario {
			case "unauthorized":
				in.Kind = RoleQA
			case "stale":
				in.Criteria = []string{"Replacement"}
				in.TextVersion = &version
			case "missing version":
				in.Criteria = []string{"Replacement"}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "stopped":
				finish(t, s, task.ID, TaskStopped)
			case "moved":
				in.While = TaskWriting
			case "too long":
				in.OwnerChecks = []string{fmt.Sprintf("%0501d", 0)}
			}
			before := mustSnapshot(t, s)
			if _, err := s.EditTask(ctx, in); err == nil {
				t.Fatal("accepted invalid edit")
			}
			after := mustSnapshot(t, s)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("partial write")
			}
		})
	}
}

func TestOwnerAnswerConcurrentAdditions(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	task, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Build", Criteria: []string{"Existing"}})
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.EditTask(testContext, EditInput{Project: p.ID, Task: task.ID, Kind: RolePM, Add: []string{fmt.Sprintf("Team %d", i%3)}, OwnerChecks: []string{fmt.Sprintf("I will check %d", i%3)}})
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	got := taskByID(t, s, task.ID)
	if len(got.Criteria) != 4 || len(got.OwnerChecks) != 3 || len(got.Edits) != 3 || got.TextVersion != 3 {
		t.Fatalf("%+v", got)
	}
	stale := 0
	if _, err := s.EditTask(testContext, EditInput{Project: p.ID, Task: task.ID, Kind: RolePM, Criteria: []string{"Lost"}, OwnerChecks: []string{"I will check again"}, TextVersion: &stale}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}

func TestOwnerAnswerDeduplicatesExistingStep(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	task, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Build", Criteria: []string{"Existing"}})
	s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
		t.OwnerSteps = []string{"I will check live"}
		return "", nil
	})
	got, err := s.EditTask(testContext, EditInput{Project: p.ID, Task: task.ID, Kind: RolePM, OwnerChecks: []string{"I will check live"}})
	if err != nil || len(got.OwnerChecks) != 0 || len(got.Edits) != 0 || got.TextVersion != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}
