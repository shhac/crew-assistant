package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/shhac/crew-assistant/internal/autopilot"
	"github.com/shhac/crew-assistant/internal/config"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func duplicateFixture(t *testing.T) (*Service, Project, Task, Task) {
	t.Helper()
	s, _ := fixture(t)
	p := newProject(t, s)
	a, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "First", Criteria: []string{"Works"}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Second", Criteria: []string{"Works"}})
	if err != nil {
		t.Fatal(err)
	}
	return s, p, a, b
}
func TestPMDuplicateAtomicReplayUndoAndConcurrency(t *testing.T) {
	s, p, a, b := duplicateFixture(t)
	var wg sync.WaitGroup
	results := make(chan PMAction, 12)
	errs := make(chan error, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := s.CloseDuplicate(testContext, p.ID, a.ID, b.ID, "same requested outcome", "PM", a)
			results <- v
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	id := ""
	for result := range results {
		if id != "" && result.ID != id {
			t.Fatal("duplicate action")
		}
		id = result.ID
	}
	snap, _ := s.Snapshot(testContext)
	got := task(&snap, a.ID)
	if got.Status != TaskStopped || got.DuplicateOf != b.ID || len(got.PMActions) != 1 || len(got.Notes) != 1 {
		t.Fatalf("%+v", got)
	}
	n := 0
	for _, e := range snap.Activity {
		if e.Kind == "pm.action" {
			n++
		}
	}
	if n != 1 {
		t.Fatal("activity count", n)
	}
	if _, err := s.UndoPMAction(testContext, p.ID, a.ID, id, RolePM); err == nil {
		t.Fatal("non-owner undo")
	}
	restored, err := s.UndoPMAction(testContext, p.ID, a.ID, id, FromOwner)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Status != TaskQueued || restored.DuplicateOf != "" || restored.PMActions[0].Status != "undone" {
		t.Fatal(restored)
	}
	snap, _ = s.Snapshot(testContext)
	var ids []string
	for _, q := range snap.Tasks {
		if q.ProjectID == p.ID && q.Status == TaskQueued {
			ids = append(ids, q.ID)
		}
	}
	if !slices.Equal(ids, []string{a.ID, b.ID}) {
		t.Fatal("queue position", ids)
	}
	if _, err = s.UndoPMAction(testContext, p.ID, a.ID, id, FromOwner); err == nil {
		t.Fatal("double undo")
	}
	if _, err = s.CloseDuplicate(testContext, p.ID, a.ID, b.ID, "again", "PM", restored); err == nil {
		t.Fatal("undone replay")
	}
}
func TestPMDuplicateRefusalsAreAtomic(t *testing.T) {
	cases := map[string]func(*Snapshot, *Task, *Task){
		"nonqueued":       func(v *Snapshot, a, b *Task) { a.Status = TaskWriting },
		"started":         func(v *Snapshot, a, b *Task) { a.StartedAt = time.Now() },
		"attempt":         func(v *Snapshot, a, b *Task) { a.Attempt = 1 },
		"round":           func(v *Snapshot, a, b *Task) { a.Round = 1 },
		"plan":            func(v *Snapshot, a, b *Task) { a.Plan = &Plan{} },
		"draft":           func(v *Snapshot, a, b *Task) { a.Revisions = []Revision{{N: 1}} },
		"claim":           func(v *Snapshot, a, b *Task) { a.Claims = []Claim{{}} },
		"decision":        func(v *Snapshot, a, b *Task) { a.DecisionID = "owner" },
		"prior learnings": func(v *Snapshot, a, b *Task) { a.Roles = []Role{{Learnings: []Learning{{}}}} },
		"prior decision":  func(v *Snapshot, a, b *Task) { v.Decisions = append(v.Decisions, Decision{TaskID: a.ID}) },
		"dependent":       func(v *Snapshot, a, b *Task) { b.DependsOn = []string{a.ID} },
		"stack":           func(v *Snapshot, a, b *Task) { b.StacksOn = a.ID },
		"blocker":         func(v *Snapshot, a, b *Task) { b.Blockers = []Blocker{{Task: a.ID}} },
		"owner hold":      func(v *Snapshot, a, b *Task) { a.Blockers = []Blocker{{By: LinkedByOwner}} },
		"owner link":      func(v *Snapshot, a, b *Task) { a.LinkedBy = map[string]LinkMark{"any": {By: LinkedByOwner}} },
		"policy off": func(v *Snapshot, a, b *Task) {
			project(v, a.ProjectID).Settings.PMPolicies = map[string]bool{"close_duplicate": false}
		},
		"high risk":      func(v *Snapshot, a, b *Task) { a.Criteria = []string{"Fix P1 security data-loss problem"} },
		"target risk":    func(v *Snapshot, a, b *Task) { b.Criteria = []string{"Security patch"} },
		"project hold":   func(v *Snapshot, a, b *Task) { project(v, a.ProjectID).Paused = true },
		"target project": func(v *Snapshot, a, b *Task) { b.ProjectID = "elsewhere" },
	}
	reasons := map[string]string{
		"nonqueued": "only a queued task", "started": "work never started", "attempt": "work never started", "round": "work never started",
		"plan": "work never started", "draft": "work never started", "claim": "work never started", "decision": "work never started",
		"prior learnings": "prior role learnings", "prior decision": "decision history", "dependent": "unfinished task", "stack": "unfinished task", "blocker": "unfinished task",
		"owner hold": "owner hold overrides", "owner link": "owner link overrides", "policy off": "owner disabled close_duplicate",
		"high risk": "stays with the owner", "target risk": "stays with the owner", "project hold": "owner project hold overrides",
		"target project": "not found",
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			s, p, a, b := duplicateFixture(t)
			if reasons[name] == "" {
				t.Fatal("missing guard-specific expectation", name)
			}
			err := s.store.update(testContext, func(v *Snapshot) error { change(v, task(v, a.ID), task(v, b.ID)); return nil })
			if err != nil {
				t.Fatal(err)
			}
			before, _ := s.Snapshot(testContext)
			_, err = s.CloseDuplicate(testContext, p.ID, a.ID, b.ID, "duplicate", "PM", a)
			want := ErrConflict
			if name == "target project" {
				want = ErrNotFound
			}
			if !errors.Is(err, want) || !strings.Contains(err.Error(), reasons[name]) {
				t.Fatalf("want %v with %q, got %v", want, reasons[name], err)
			}
			after, _ := s.Snapshot(testContext)
			if !reflect.DeepEqual(task(&before, a.ID), task(&after, a.ID)) || len(before.Activity) != len(after.Activity) {
				t.Fatal("refusal changed the record")
			}
			snap, _ := s.Snapshot(testContext)
			if len(task(&snap, a.ID).PMActions) != 0 {
				t.Fatal("partial action")
			}
		})
	}
	s, p, a, b := duplicateFixture(t)
	for _, target := range []string{a.ID, "missing"} {
		if _, err := s.CloseDuplicate(testContext, p.ID, a.ID, target, "duplicate", "PM", a); err == nil {
			t.Fatal(target)
		}
	}
	_, err := s.CloseDuplicate(testContext, p.ID, a.ID, b.ID, "", "PM", a)
	if err == nil {
		t.Fatal("missing reason")
	}
	// Live policy check refuses a tool offered before owner disablement.
	off := false
	if err = s.SetPMPolicy(testContext, "project", p.ID, "close_duplicate", &off, 0, FromOwner); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CloseDuplicate(testContext, p.ID, a.ID, b.ID, "same", "PM", a); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}
func TestPMUndoRefusesOwnerChangesEvenAtSameTimestamp(t *testing.T) {
	s, p, a, b := duplicateFixture(t)
	action, err := s.CloseDuplicate(testContext, p.ID, a.ID, b.ID, "same", "PM", a)
	if err != nil {
		t.Fatal(err)
	}
	err = s.store.update(testContext, func(v *Snapshot) error { task(v, a.ID).TextVersion++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.UndoPMAction(testContext, p.ID, a.ID, action.ID, FromOwner); err == nil {
		t.Fatal("undo erased owner edit")
	}
}

func TestPMDuplicateAndGeoffreyCannotUseStaleAuthority(t *testing.T) {
	s, p, a, b := duplicateFixture(t)
	// An ordinary project choice with a prospective task binding models a
	// coordinator look. Its digest must change after a duplicate closure.
	d, err := s.CreateDecision(testContext, DecisionInput{ProjectID: p.ID, Title: "Choice", Context: "Synthetic", Recommendation: "One", Choices: []string{"One", "Two"}})
	if err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(testContext)
	decision(&snap, d.ID).TaskID = a.ID
	before, err := DecisionActionDigest(snap, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.CloseDuplicate(testContext, p.ID, a.ID, b.ID, "Same", "PM", a)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := s.Snapshot(testContext)
	decision(&after, d.ID).TaskID = a.ID
	digest, err := DecisionActionDigest(after, d.ID)
	if err != nil || digest == before {
		t.Fatal("stale digest", digest, err)
	}
	args, _ := json.Marshal(ResolveChoiceArgs{DecisionID: d.ID, Choice: "One"})
	if err := decisionChoiceAction(s).Check(after, ConcreteAction{Kind: "resolve-choice", ProjectID: p.ID, TaskID: a.ID, TargetDigest: before, Args: args}); !errors.Is(err, ErrConflict) {
		t.Fatal("Geoffrey accepted stale PM state", err)
	}
	// A real decision gained after admission invalidates the reverse direction.
	s, p, a, b = duplicateFixture(t)
	_, err = s.OpenTaskDecision(testContext, a.ID, DecisionChoice, DecisionInput{Title: "Choice", Context: "Synthetic", Recommendation: "One", Choices: []string{"One", "Two"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CloseDuplicate(testContext, p.ID, a.ID, b.ID, "Same", "PM", a); err == nil {
		t.Fatal("PM acted over owner decision")
	}
	// Disabling PM authority remains explicit.
	off := false
	if err = s.SetPMPolicy(testContext, "project", p.ID, "close_duplicate", &off, 0, FromOwner); err != nil {
		t.Fatal(err)
	}

	snap, _ = s.Snapshot(testContext)
	if PMPolicyOn(snap, p.ID, "close_duplicate") {
		t.Fatal("policy unexpectedly enabled")
	}
}
func TestPMDuplicateUndoToleratesTargetAndLinkChanges(t *testing.T) {
	s, p, a, b := duplicateFixture(t)
	action, err := s.CloseDuplicate(testContext, p.ID, a.ID, b.ID, "Same", "PM", a)
	if err != nil {
		t.Fatal(err)
	}
	err = s.store.update(testContext, func(v *Snapshot) error {
		target := task(v, b.ID)
		target.Status = TaskStopped
		target.Objective = "Renamed target"
		v.Tasks = append(v.Tasks, Task{ID: "later", ProjectID: p.ID, Status: TaskQueued, DependsOn: []string{a.ID}})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.UndoPMAction(testContext, p.ID, a.ID, action.ID, FromOwner); err != nil {
		t.Fatal(err)
	}
}

func TestGeoffreyActCannotEnablePMPolicy(t *testing.T) {
	s, _, f, p := autopilotFixture(t, autopilot.Act)
	off := false
	if err := s.SetPMPolicy(testContext, "project", p.ID, "close_duplicate", &off, 0, FromOwner); err != nil {
		t.Fatal(err)
	}
	receipt, err := f.Submit(testContext, "independent-authority", "Synthetic rename", renameProposal(p, "Changed"))
	if err != nil || receipt.Status != "performed" {
		t.Fatal(receipt, err)
	}
	snap, _ := s.Snapshot(testContext)
	if PMPolicyOn(snap, p.ID, "close_duplicate") {
		t.Fatal("Geoffrey enabled PM authority")
	}
}
func TestPMActionAndTeamlessChoicesSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewService(store, config.Default())
	p := newProject(t, s)
	first, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Duplicate", Criteria: []string{"Works"}})
	if err != nil {
		t.Fatal(err)
	}
	target, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Original", Criteria: []string{"Works"}})
	if err != nil {
		t.Fatal(err)
	}
	off := false
	if err = s.SetPMPolicy(testContext, "project", p.ID, "rescope", &off, 0, FromOwner); err != nil {
		t.Fatal(err)
	}
	action, err := s.CloseDuplicate(testContext, p.ID, first.ID, target.ID, "Same", "PM", first)
	if err != nil {
		t.Fatal(err)
	}
	alternative, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Another target"})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s = NewService(store, config.Default())
	replay, err := s.CloseDuplicate(testContext, p.ID, first.ID, target.ID, "Same", "PM", first)
	if err != nil || replay.ID != action.ID {
		t.Fatal(replay, err)
	}
	restored, err := s.UndoPMAction(testContext, p.ID, first.ID, action.ID, FromOwner)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s = NewService(store, config.Default())
	for _, id := range []string{alternative.ID, target.ID} {
		if _, err = s.CloseDuplicate(testContext, p.ID, first.ID, id, "Same", "PM", restored); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "owner override") {
			t.Fatal("restart lost task-wide undo fence", err)
		}
	}
	snap, err := s.Snapshot(testContext)
	if err != nil {
		t.Fatal(err)
	}
	if PMPolicyOn(snap, p.ID, "rescope") || project(&snap, p.ID).Settings.PMPoliciesRevision != 1 {
		t.Fatal("restart lost explicit policy")
	}
}

func TestCheckedPMActionRollsBackPartialEffectsAndRejectsUnknownPolicy(t *testing.T) {
	s, p, a, _ := duplicateFixture(t)
	failure := errors.New("synthetic partial failure")
	var out PMAction
	err := s.store.update(testContext, func(v *Snapshot) error {
		q := task(v, a.ID)
		return applyPMAction(v, q, "close_duplicate", "synthetic", &out, func() error {
			q.Status = TaskStopped
			q.Detail = "partial"
			q.Notes = append(q.Notes, Note{ID: "partial"})
			return failure
		})
	})
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(testContext)
	q := task(&snap, a.ID)
	if q.Status != TaskQueued || q.Detail != "" || len(q.Notes) != 0 || len(q.PMActions) != 0 {
		t.Fatal("partial effects committed", q)
	}
	called := false
	err = s.store.update(testContext, func(v *Snapshot) error {
		return applyPMAction(v, task(v, a.ID), "unknown", "synthetic", &out, func() error { called = true; return nil })
	})
	if err == nil || called {
		t.Fatal("unknown policy ran", err)
	}
	if !PMPolicyOn(snap, p.ID, "close_duplicate") {
		t.Fatal("unexpected policy change")
	}
}

func TestPMUndoAllowsCheckedIncomingLinksWithoutOverwritingThem(t *testing.T) {
	s, p, a, b := duplicateFixture(t)
	action, err := s.CloseDuplicate(testContext, p.ID, a.ID, b.ID, "Same", "PM", a)
	if err != nil {
		t.Fatal(err)
	}
	later := action.At.Add(time.Hour)
	s.now = func() time.Time { return later }
	_, err = s.LinkTasks(testContext, Link{Project: p.ID, Task: b.ID, Other: a.ID, Relation: RelationRelatesTo, By: LinkedByOwner})
	if err != nil {
		t.Fatal(err)
	}
	off := false
	if err = s.SetPMPolicy(testContext, "project", p.ID, "close_duplicate", &off, 0, FromOwner); err != nil {
		t.Fatal(err)
	}
	restored, err := s.UndoPMAction(testContext, p.ID, a.ID, action.ID, FromOwner)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(restored.RelatesTo, b.ID) {
		t.Fatal("undo lost owner's later link")
	}
	snap, _ := s.Snapshot(testContext)
	if PMPolicyOn(snap, p.ID, "close_duplicate") {
		t.Fatal("undo changed owner policy")
	}
}
func TestPMUndoRefusesAStartedTaskEvenIfClosureDetailRemains(t *testing.T) {
	s, p, a, b := duplicateFixture(t)
	action, err := s.CloseDuplicate(testContext, p.ID, a.ID, b.ID, "Same", "PM", a)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.UpdateTask(testContext, a.ID, func(q *Task, _ *Project) (string, error) { q.StartedAt = action.At; q.Attempt = 1; return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.UndoPMAction(testContext, p.ID, a.ID, action.ID, FromOwner); err == nil {
		t.Fatal("undo erased restarted work")
	}
}

func TestPMOwnerUndoVetoesEveryDuplicateTarget(t *testing.T) {
	s, p, a, b := duplicateFixture(t)
	c, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Another candidate", Criteria: []string{"Works"}})
	if err != nil {
		t.Fatal(err)
	}
	action, err := s.CloseDuplicate(testContext, p.ID, a.ID, b.ID, "Same", "PM", a)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := s.UndoPMAction(testContext, p.ID, a.ID, action.ID, FromOwner)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []Task{c, b} {
		if _, err = s.CloseDuplicate(testContext, p.ID, a.ID, target.ID, "Another look", "PM", restored); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "owner override") {
			t.Fatalf("target %s: %v", target.ID, err)
		}
	}
	snap, _ := s.Snapshot(testContext)
	got := task(&snap, a.ID)
	if got.Status != TaskQueued || len(got.PMActions) != 1 || got.PMActions[0].Status != "undone" {
		t.Fatal(got)
	}
}
func TestPMDuplicateRefusesFinishedAndDuplicateTargets(t *testing.T) {
	for _, status := range []string{TaskStopped, TaskDelivered, TaskLanded, "duplicate"} {
		t.Run(status, func(t *testing.T) {
			s, p, a, b := duplicateFixture(t)
			_, err := s.UpdateTask(testContext, b.ID, func(q *Task, _ *Project) (string, error) {
				if status == "duplicate" {
					q.DuplicateOf = "another"
				} else {
					q.Status = status
				}
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.CloseDuplicate(testContext, p.ID, a.ID, b.ID, "Same", "PM", a)
			if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "target must be unfinished") {
				t.Fatal(err)
			}
			snap, _ := s.Snapshot(testContext)
			if task(&snap, a.ID).Status != TaskQueued || len(task(&snap, a.ID).PMActions) > 0 {
				t.Fatal("request lost")
			}
		})
	}
}
func TestPMDuplicateMutualClosureLeavesOneTaskCarryingWork(t *testing.T) {
	for _, parallel := range []bool{false, true} {
		t.Run(fmt.Sprint(parallel), func(t *testing.T) {
			s, p, a, b := duplicateFixture(t)
			close := func(source, target Task) error {
				_, err := s.CloseDuplicate(testContext, p.ID, source.ID, target.ID, "Same", "PM", source)
				return err
			}
			var results []error
			if parallel {
				ch := make(chan error, 2)
				go func() { ch <- close(a, b) }()
				go func() { ch <- close(b, a) }()
				results = []error{<-ch, <-ch}
			} else {
				results = []error{close(a, b), close(b, a)}
			}
			success := 0
			for _, err := range results {
				if err == nil {
					success++
				} else if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "target must be unfinished") {
					t.Fatal(err)
				}
			}
			if success != 1 {
				t.Fatal("closures", results)
			}
			snap, _ := s.Snapshot(testContext)
			for _, id := range []string{a.ID, b.ID} {
				source := task(&snap, id)
				if source.Status == TaskStopped {
					if task(&snap, source.DuplicateOf).Status != TaskQueued {
						t.Fatal("no task carries the work")
					}
				}
			}
		})
	}
}
func TestPMDuplicateRefusesPendingMessagesAndWakes(t *testing.T) {
	for _, state := range []string{MessageWaiting, MessageWorking} {
		t.Run("message/"+state, func(t *testing.T) {
			s, p, a, b := duplicateFixture(t)
			err := s.store.update(testContext, func(v *Snapshot) error {
				task(v, a.ID).Messages = []TeamMessage{{ID: "message", Status: state}}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.CloseDuplicate(testContext, p.ID, a.ID, b.ID, "Same", "PM", a)
			if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "open team messages") {
				t.Fatal(err)
			}
			snap, _ := s.Snapshot(testContext)
			q := task(&snap, a.ID)
			if q.Status != TaskQueued || q.Messages[0].Status != state || len(q.PMActions) > 0 {
				t.Fatal(q)
			}
		})
	}
	for _, fired := range []bool{false, true} {
		t.Run(fmt.Sprintf("wake/fired=%t", fired), func(t *testing.T) {
			s, p, a, b := duplicateFixture(t)
			wake, err := s.RegisterWake(testContext, WakeInput{Owner: WakeTask, TaskID: a.ID, ProjectID: p.ID, On: WakeOnTime, Target: "later", Prompt: "Continue"})
			if err != nil {
				t.Fatal(err)
			}
			if fired {
				wake, err = s.FireWake(testContext, wake.ID, "ready", "event", false)
				if err != nil {
					t.Fatal(err)
				}
			}
			_, err = s.CloseDuplicate(testContext, p.ID, a.ID, b.ID, "Same", "PM", a)
			if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "pending wake") {
				t.Fatal(err)
			}
			snap, _ := s.Snapshot(testContext)
			if task(&snap, a.ID).Status != TaskQueued || wakeByID(&snap, wake.ID).Status != wake.Status || len(task(&snap, a.ID).PMActions) > 0 {
				t.Fatal("refusal changed a wake or task")
			}
		})
	}
}
func TestPMDuplicateAllowsSettledMessagesAndUnrelatedOrCancelledWakes(t *testing.T) {
	s, p, a, b := duplicateFixture(t)
	err := s.store.update(testContext, func(v *Snapshot) error {
		task(v, a.ID).Messages = []TeamMessage{{ID: "answered", Status: MessageAnswered}, {ID: "closed", Status: MessageClosed}}
		v.Wakes = append(v.Wakes, Wake{ID: "cancelled", TaskID: a.ID, Status: WakeCancelled}, Wake{ID: "delivered", TaskID: a.ID, Status: WakeDelivered}, Wake{ID: "other", TaskID: b.ID, Status: WakeWaiting})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	action, err := s.CloseDuplicate(testContext, p.ID, a.ID, b.ID, "Same", "PM", a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.UndoPMAction(testContext, p.ID, a.ID, action.ID, FromOwner); err != nil {
		t.Fatal(err)
	}
}
func TestPMUndoRestoresPositionAfterRemainingQueueChanges(t *testing.T) {
	s, p, a, b := duplicateFixture(t)
	c, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Third"})
	if err != nil {
		t.Fatal(err)
	}
	action, err := s.CloseDuplicate(testContext, p.ID, b.ID, a.ID, "Same", "PM", b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.OrderTasks(testContext, p.ID, []string{c.ID, a.ID}, OrderedByOwner); err != nil {
		t.Fatal(err)
	}
	if _, err = s.UpdateTask(testContext, c.ID, func(q *Task, _ *Project) (string, error) {
		q.Status = TaskWriting
		q.StartedAt = s.now()
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
	d, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "New arrival"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.UndoPMAction(testContext, p.ID, b.ID, action.ID, FromOwner); err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(testContext)
	if ids := queuedOf(&snap, p.ID); !slices.Equal(ids, []string{a.ID, b.ID, d.ID}) {
		t.Fatal("restored prior relative index", ids)
	}
}

func TestPMDuplicatePerformedReplayRemainsIdempotentAfterTargetFinishes(t *testing.T) {
	s, p, a, b := duplicateFixture(t)
	action, err := s.CloseDuplicate(testContext, p.ID, a.ID, b.ID, "Same", "PM", a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.UpdateTask(testContext, b.ID, func(q *Task, _ *Project) (string, error) { q.Status = TaskStopped; return "", nil }); err != nil {
		t.Fatal(err)
	}
	replay, err := s.CloseDuplicate(testContext, p.ID, a.ID, b.ID, "Same", "PM", a)
	if err != nil || replay.ID != action.ID {
		t.Fatal("performed request was not reconciled", replay, err)
	}
}
func TestPMUndoClampsPositionWhenRemainingQueueIsEmpty(t *testing.T) {
	s, p, a, b := duplicateFixture(t)
	c, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Third"})
	if err != nil {
		t.Fatal(err)
	}
	action, err := s.CloseDuplicate(testContext, p.ID, c.ID, a.ID, "Same", "PM", c)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a.ID, b.ID} {
		if _, err = s.UpdateTask(testContext, id, func(q *Task, _ *Project) (string, error) { q.Status = TaskWriting; return "", nil }); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.UndoPMAction(testContext, p.ID, c.ID, action.ID, FromOwner); err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(testContext)
	if ids := queuedOf(&snap, p.ID); !slices.Equal(ids, []string{c.ID}) {
		t.Fatal(ids)
	}
}

func TestPMDuplicateCannotCloseTheCarrierOfOtherDuplicateRequests(t *testing.T) {
	s, p, a, b := duplicateFixture(t)
	c, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Third equivalent request"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CloseDuplicate(testContext, p.ID, a.ID, b.ID, "Same", "PM", a); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CloseDuplicate(testContext, p.ID, b.ID, c.ID, "Same", "PM", b); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "carries another duplicate") {
		t.Fatal("duplicate chain admitted", err)
	}
	// Keep B as the canonical carrier and fold C directly into it instead.
	if _, err = s.CloseDuplicate(testContext, p.ID, c.ID, b.ID, "Same", "PM", c); err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(testContext)
	if task(&snap, b.ID).Status != TaskQueued || task(&snap, a.ID).DuplicateOf != b.ID || task(&snap, c.ID).DuplicateOf != b.ID {
		t.Fatal("canonical work lost")
	}
}

func TestDuplicateOwnerHistoryUsesReadableLabels(t *testing.T) {
	s, p, a, b := duplicateFixture(t)
	action, err := s.CloseDuplicate(testContext, p.ID, a.ID, b.ID, "Same purpose", "PM", a)
	if err != nil {
		t.Fatal(err)
	}
	if action.Effects.DuplicateLabel != b.Ref {
		t.Fatal("readable label not saved", action)
	}
	snap, _ := s.Snapshot(testContext)
	closed := task(&snap, a.ID)
	if closed.Detail != "Duplicate of "+b.Ref+": Same purpose" || closed.Notes[0].Text != closed.Detail {
		t.Fatal(closed)
	}
	if _, err = s.UndoPMAction(testContext, p.ID, a.ID, action.ID, FromOwner); err != nil {
		t.Fatal(err)
	}
	snap, _ = s.Snapshot(testContext)
	for _, entry := range snap.Activity {
		if entry.Kind == "pm.action" || entry.Kind == "pm.action.undone" {
			if strings.Contains(entry.Summary, a.ID) || strings.Contains(entry.Summary, b.ID) || strings.Contains(entry.Summary, "close_duplicate") {
				t.Fatal("internal labels in owner activity", entry)
			}
		}
	}
}
