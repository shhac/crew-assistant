package core

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
)

func checkTask(t *testing.T, s *Service, p Project) Task {
	t.Helper()
	out, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Build", OwnerChecks: []string{"Live check", "Tag release"}})
	if err != nil {
		t.Fatal(err)
	}
	out, err = s.UpdateTask(testContext, out.ID, func(task *Task, _ *Project) (string, error) {
		task.Status = TaskLanded
		task.OwnerSteps = []string{"Live check", "Check redirect"}
		return "Landed", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func TestOwnerCheckMarks(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	task := checkTask(t, s, p)
	mark := func(step, status, note, by string) Task {
		t.Helper()
		out, err := s.MarkOwnerCheck(testContext, p.ID, task.ID, step, status, note, by)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	snap, _ := s.Snapshot(testContext)
	if len(snap.AfterLanding) != 3 || len(snap.Tasks[0].Checklist) != 3 {
		t.Fatalf("legacy/dedup: %+v", snap.AfterLanding)
	}
	if _, err := s.MarkOwnerCheck(testContext, p.ID, task.ID, "Live check", "skipped", "  ", FromOwner); err == nil {
		t.Fatal("blank skip accepted")
	}
	if _, err := s.MarkOwnerCheck(testContext, p.ID, task.ID, "unknown", "done", "", FromOwner); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.MarkOwnerCheck(testContext, p.ID, task.ID, "Live check", "done", "", RoleImplementer); err == nil {
		t.Fatal("role mark accepted")
	}
	done := mark("Live check", "done", "discard", FromOwner)
	if len(done.CheckMarks) != 1 || done.Checklist[0].Status != "done" || done.Checklist[1].Status != "outstanding" || done.CheckMarks[0].Note != "" {
		t.Fatalf("%+v", done)
	}
	if out := mark("Live check", "done", "", FromAssistant); len(out.CheckMarks) != 1 {
		t.Fatal("duplicate mark")
	}
	skipped := mark("Tag release", "skipped", " not needed ", FromAssistant)
	if skipped.Checklist[1].Mark.Note != "not needed" || skipped.Checklist[1].Mark.At.IsZero() {
		t.Fatal(skipped.Checklist)
	}
	undone := mark("Tag release", "outstanding", "", FromOwner)
	item := undone.Checklist[1]
	if item.Status != "outstanding" || len(item.History) != 1 || item.History[0].By != FromAssistant || item.Mark.By != FromOwner || item.History[0].Note != "not needed" {
		t.Fatalf("%+v", item)
	}
	if out := mark("Tag release", "outstanding", "", FromOwner); len(out.CheckMarks) != 3 {
		t.Fatal("duplicate undo")
	}
	for _, status := range []string{TaskQueued, TaskStopped} {
		s.UpdateTask(testContext, task.ID, func(task *Task, _ *Project) (string, error) { task.Status = status; return "", nil })
		if _, err := s.MarkOwnerCheck(testContext, p.ID, task.ID, "Live check", "done", "", FromOwner); !errors.Is(err, ErrConflict) {
			t.Fatal(status, err)
		}
		snap, err := s.Snapshot(testContext)
		if err != nil || len(snap.AfterLanding) != 0 {
			t.Fatal("unfinished/stopped checks in backlog", snap.AfterLanding, err)
		}
	}
}
func TestOwnerCheckBulkAndOrdering(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	other := newProject(t, s)
	a := checkTask(t, s, p)
	b := checkTask(t, s, other)
	c := checkTask(t, s, p)
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := s.store.update(testContext, func(v *Snapshot) error {
		task(v, a.ID).LandedAt = old
		task(v, b.ID).LandedAt = old
		task(v, c.ID).LandedAt = old.Add(-time.Hour)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(testContext)
	if len(snap.AfterLanding) != 9 || snap.AfterLanding[0].TaskID != c.ID || snap.AfterLanding[1].Step != "Tag release" {
		t.Fatal(snap.AfterLanding)
	}
	want := a.ID
	if b.ID < a.ID {
		want = b.ID
	}
	if snap.AfterLanding[3].TaskID != want {
		t.Fatal("tie order", snap.AfterLanding)
	}
	s.MarkOwnerCheck(testContext, p.ID, a.ID, "Live check", "done", "", FromOwner)
	if _, err := s.ClearOwnerChecks(testContext, p.ID, " ", FromOwner); err == nil {
		t.Fatal("blank clear accepted")
	}
	n, err := s.ClearOwnerChecks(testContext, p.ID, "old backlog", FromOwner)
	if err != nil || n != 5 {
		t.Fatal(n, err)
	}
	snap, _ = s.Snapshot(testContext)
	if len(snap.AfterLanding) != 3 {
		t.Fatal(snap.AfterLanding)
	}
	for _, task := range snap.Tasks {
		for _, item := range task.Checklist {
			if task.ProjectID == other.ID && item.Status != "outstanding" {
				t.Fatal("other project changed")
			}
			if task.ID == a.ID && item.Step == "Live check" && item.Status != "done" {
				t.Fatal("done overwritten")
			}
			if item.Status == "skipped" && (!item.Mark.Bulk || item.Mark.By != FromOwner || item.Mark.At.IsZero()) {
				t.Fatal(item)
			}
		}
	}
	n, err = s.ClearOwnerChecks(testContext, p.ID, "again", FromOwner)
	if err != nil || n != 0 {
		t.Fatal(n, err)
	}
	restored, err := s.MarkOwnerCheck(testContext, p.ID, c.ID, "Tag release", "outstanding", "", FromAssistant)
	if err != nil || restored.Checklist[1].Status != "outstanding" || !restored.Checklist[1].History[0].Bulk {
		t.Fatal(restored, err)
	}
	snap, _ = s.Snapshot(testContext)
	if len(snap.AfterLanding) != 4 || snap.AfterLanding[0].TaskID != c.ID {
		t.Fatal(snap.AfterLanding)
	}
}
func TestOwnerCheckPersistenceAndLegacyTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewService(st, config.Default())
	p := newProject(t, s)
	a := checkTask(t, s, p)
	b := checkTask(t, s, p)
	at := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := s.store.update(testContext, func(v *Snapshot) error {
		v.Activity = slices.DeleteFunc(v.Activity, func(entry Activity) bool { return entry.TaskID == a.ID || entry.TaskID == b.ID })
		recordTask(v, at, task(v, a.ID), "task.landed", "legacy landing")
		// UpdateTask historically used the status as the activity kind for
		// later edits too. Those edits must not reorder the original landing.
		recordTask(v, at.Add(24*time.Hour), task(v, a.ID), "task.landed", "later edit")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, err = s.MarkOwnerCheck(testContext, p.ID, a.ID, "Live check", "skipped", "no longer needed", FromAssistant)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkOwnerCheck(testContext, p.ID, a.ID, "Tag release", "done", "", FromOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkOwnerCheck(testContext, p.ID, a.ID, "Tag release", "outstanding", "", FromAssistant); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	snap, err := NewService(st, config.Default()).Snapshot(testContext)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.AfterLanding) != 5 || !snap.AfterLanding[0].LandedAt.Equal(at) {
		t.Fatal(snap.AfterLanding)
	}
	for _, task := range snap.Tasks {
		if task.ID == a.ID {
			m := task.Checklist[0].Mark
			if m == nil || m.By != FromAssistant || m.Note != "no longer needed" || m.At.IsZero() {
				t.Fatal(task.Checklist)
			}
			item := task.Checklist[1]
			if item.Status != "outstanding" || item.Mark.By != FromAssistant || len(item.History) != 1 || item.History[0].Status != "done" || item.History[0].By != FromOwner || item.History[0].At.IsZero() {
				t.Fatal("undo history lost on restart", item)
			}
		}
		if task.ID == b.ID && task.Checklist[0].Status != "outstanding" {
			t.Fatal("inferred legacy completion")
		}
	}
	for _, c := range snap.AfterLanding {
		if c.TaskID == b.ID && !c.LandedAt.Equal(b.UpdatedAt) {
			t.Fatal("missing UpdatedAt fallback")
		}
	}
}

func TestOwnerCheckDerivedViewsAreNeverStored(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	task := checkTask(t, s, p)
	if err := s.store.update(testContext, func(v *Snapshot) error {
		v.AfterLanding = []OutstandingCheck{{Step: "stale"}}
		for i := range v.Tasks {
			v.Tasks[i].Checklist = []OwnerCheckItem{{Step: "stale", Status: "done"}}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := s.store.Snapshot(testContext)
	if err != nil || len(raw.AfterLanding) != 0 || len(raw.Tasks[0].Checklist) != 0 {
		t.Fatalf("stored derived views: %+v %v", raw, err)
	}
	snap, err := s.Snapshot(testContext)
	if err != nil || len(snap.AfterLanding) != 3 || snap.Tasks[0].Checklist[0].Step != task.OwnerChecks[0] {
		t.Fatalf("derived views not rebuilt: %+v %v", snap.AfterLanding, err)
	}
	// Stripping a stored copy must not mutate the service's read view.
	withoutRefs(snap.Tasks)
	if len(snap.Tasks[0].Checklist) != 3 {
		t.Fatal("read view mutated")
	}
}

func TestOwnerChecksRemainAvailableDuringLandingAgain(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		s, _ := fixture(t)
		p := newProject(t, s)
		landedTask := checkTask(t, s, p)
		first := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		if err := s.store.update(testContext, func(v *Snapshot) error {
			task := task(v, landedTask.ID)
			task.Status = TaskLanding
			if !legacy {
				task.LandedAt = first
			}
			recordTask(v, first, task, "task.delivered", "first delivery")
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		snap, err := s.Snapshot(testContext)
		if err != nil || len(snap.AfterLanding) != 3 || !snap.AfterLanding[0].LandedAt.Equal(first) || !snap.Tasks[0].LandedAt.Equal(first) {
			t.Fatalf("landing again lost checks: %+v %v", snap, err)
		}
		if legacy {
			raw, _ := s.store.Snapshot(testContext)
			if !raw.Tasks[0].LandedAt.IsZero() {
				t.Fatal("read backfilled legacy landing time")
			}
		}
		if _, err := s.MarkOwnerCheck(testContext, p.ID, landedTask.ID, "Live check", "done", "", FromOwner); err != nil {
			t.Fatal(err)
		}
		if n, err := s.ClearOwnerChecks(testContext, p.ID, "Old checks", FromOwner); err != nil || n != 2 {
			t.Fatal(n, err)
		}
		if _, err := s.MarkOwnerCheck(testContext, p.ID, landedTask.ID, "Live check", "outstanding", "", FromOwner); err != nil {
			t.Fatal(err)
		}
		snap, _ = s.Snapshot(testContext)
		if len(snap.AfterLanding) != 1 || !snap.AfterLanding[0].LandedAt.Equal(first) {
			t.Fatal(snap.AfterLanding)
		}
		// Re-landing can also catch up with the target, await PR checks or
		// ask the owner a question. Those phases must not hide follow-through.
		for _, status := range []string{TaskReviewing, TaskWriting, TaskWaiting, TaskAwaiting} {
			if _, err := s.UpdateTask(testContext, landedTask.ID, func(t *Task, _ *Project) (string, error) { t.Status = status; return "Catching up", nil }); err != nil {
				t.Fatal(err)
			}
			snap, _ = s.Snapshot(testContext)
			if len(snap.AfterLanding) != 1 || !snap.AfterLanding[0].LandedAt.Equal(first) {
				t.Fatal(status, snap.AfterLanding)
			}
			if _, err := s.MarkOwnerCheck(testContext, p.ID, landedTask.ID, "Live check", "done", "", FromOwner); err != nil {
				t.Fatal(status, err)
			}
			if _, err := s.MarkOwnerCheck(testContext, p.ID, landedTask.ID, "Live check", "outstanding", "", FromOwner); err != nil {
				t.Fatal(status, err)
			}
		}
	}
	// An initial landing has never reached the owner follow-through stage.
	s, _ := fixture(t)
	p := newProject(t, s)
	task, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Build", OwnerChecks: []string{"Live check"}})
	if err != nil {
		t.Fatal(err)
	}
	s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) { t.Status = TaskLanding; return "First landing", nil })
	snap, _ := s.Snapshot(testContext)
	if len(snap.AfterLanding) != 0 {
		t.Fatal("first landing exposed owner checks")
	}
	if _, err := s.MarkOwnerCheck(testContext, p.ID, task.ID, "Live check", "done", "", FromOwner); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}
