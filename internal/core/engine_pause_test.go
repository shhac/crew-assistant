package core

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
)

func TestEnginePauseLifecycleAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewService(st, config.Default())
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	until := now.Add(time.Hour)
	if err := s.PauseEngine(testContext, "claude", &until); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s = NewService(st, config.Default())
	s.now = func() time.Time { return now }
	snap, _ := s.Snapshot(testContext)
	if p, ok := snap.EnginePaused("claude", now); !ok || !p.Until.Equal(until) {
		t.Fatal(p, ok)
	}
	if _, ok := snap.EnginePaused("codex", now); ok {
		t.Fatal("other engine paused")
	}
	if err := s.LiftEndedEnginePauses(testContext, until); err != nil {
		t.Fatal(err)
	}
	snap, _ = s.Snapshot(testContext)
	if len(snap.EnginePauses) != 0 || snap.Activity[0].Kind != "engine.pause_ended" {
		t.Fatal(snap.Activity)
	}
	// A fresh extension survives a lift based on the previous end.
	later := until.Add(time.Hour)
	if err := s.PauseEngine(testContext, "claude", &later); err != nil {
		t.Fatal(err)
	}
	if err := s.LiftEndedEnginePauses(testContext, until); err != nil {
		t.Fatal(err)
	}
	snap, _ = s.Snapshot(testContext)
	if _, ok := snap.EnginePaused("claude", until); !ok {
		t.Fatal("extension removed")
	}
	if err := s.ResumeEngine(testContext, "claude"); err != nil {
		t.Fatal(err)
	}
	snap, _ = s.Snapshot(testContext)
	count := len(snap.Activity)
	var version int
	if err := st.db.QueryRow("SELECT version FROM state WHERE id=1").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(snap.Activity, func(a Activity) bool { return a.Kind == "engine.resumed" }) {
		t.Fatal(snap.Activity)
	}
	_ = s.ResumeEngine(testContext, "claude")
	_ = s.LiftEndedEnginePauses(testContext, later)
	snap, _ = s.Snapshot(testContext)
	var afterVersion int
	if err := st.db.QueryRow("SELECT version FROM state WHERE id=1").Scan(&afterVersion); err != nil {
		t.Fatal(err)
	}
	if afterVersion != version {
		t.Fatal("no-op wrote the store", version, afterVersion)
	}
	if len(snap.Activity) != count {
		t.Fatal("no-op recorded activity")
	}
	if err := s.PauseEngine(testContext, "missing", nil); err == nil {
		t.Fatal("unknown accepted")
	}
	if err := s.PauseEngine(testContext, "claude", &now); err == nil {
		t.Fatal("past accepted")
	}
	var old Snapshot
	if err := json.Unmarshal([]byte(`{"paused":false}`), &old); err != nil {
		t.Fatal(err)
	}
	if _, paused := old.EnginePaused("claude", now); paused {
		t.Fatal("old state paused")
	}
}

func TestEnginePauseChatSkipsWakeButAllowsOwner(t *testing.T) {
	s, _ := fixture(t)
	_ = s.store.update(testContext, func(v *Snapshot) error {
		v.ChatTurns = []ChatTurn{{ID: "wake", Origin: OriginWake, Status: "queued"}, {ID: "owner", Message: "Hello", Status: "queued"}}
		return nil
	})
	_ = s.PauseEngine(testContext, "claude", nil)
	turn, err := s.StartNextChat(testContext, "claude")
	if err != nil || turn.ID != "owner" {
		t.Fatal(turn, err)
	}
	_ = s.store.update(testContext, func(v *Snapshot) error { v.ChatTurns[1].Status = "answered"; return nil })
	_ = s.ResumeEngine(testContext, "claude")
	turn, err = s.StartNextChat(testContext, "claude")
	if err != nil || turn.ID != "wake" {
		t.Fatal(turn, err)
	}
}

func TestResumeExpiredEnginePauseLeavesAutomaticLift(t *testing.T) {
	s, _ := fixture(t)
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	until := now.Add(time.Hour)
	if err := s.PauseEngine(testContext, "claude", &until); err != nil {
		t.Fatal(err)
	}
	now = until
	if err := s.ResumeEngine(testContext, "claude"); err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(testContext)
	if len(snap.EnginePauses) != 1 || snap.Activity[0].Kind != "engine.paused" {
		t.Fatal(snap.Activity)
	}
	if err := s.LiftEndedEnginePauses(testContext, now); err != nil {
		t.Fatal(err)
	}
	snap, _ = s.Snapshot(testContext)
	if snap.Activity[0].Kind != "engine.pause_ended" {
		t.Fatal(snap.Activity)
	}
}

func TestEnginePauseSeatFallbackAndWaiting(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	task, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Write"})
	book := *p.Playbook
	book.Roles = append(book.Roles, Role{Name: "Other writer", Kinds: []string{RoleImplementer}, Engine: "codex"})
	_, err := s.SetPlaybook(testContext, p.ID, book)
	if err != nil {
		t.Fatal(err)
	}
	admit := func(r Role) string {
		if r.Engine == "claude" {
			return WaitEnginePaused
		}
		return ""
	}
	got, err := s.Schedule(testContext, admit)
	if err != nil || len(got) != 1 || got[0].Seat.Engine != "codex" {
		t.Fatal(got, err)
	}
	// A fresh project with no alternate writer records the precise wait.
	p = newProject(t, s)
	task, _ = s.QueueTask(testContext, p.ID, TaskInput{Objective: "Wait"})
	_, err = s.Schedule(testContext, admit)
	if err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(testContext)
	task, _ = snap.FindTask(task.ID)
	if task.Waiting == nil || task.Waiting.Kind != WaitEnginePaused || task.Waiting.Engine != "claude" {
		t.Fatal(task.Waiting)
	}
}
