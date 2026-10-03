package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestScheduleSerializesPreferredPeopleAndConsumesHandOn(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	pb := *p.Playbook
	seats := preferenceTask(RoleImplementer).Roles
	for i := range seats {
		seats[i].Engine = "codex"
	}
	pb.Roles = append(append([]Role{}, seats...), Role{Name: "Reviewer", Kinds: []string{RoleReviewer}, Engine: "codex"})
	pb.MaxActive = 0
	if _, err := s.SetPlaybook(testContext, p.ID, pb); err != nil {
		t.Fatal(err)
	}
	tasks := queueAll(t, s, p, "A", "B")
	for _, task := range tasks {
		_, err := s.UpdateTask(testContext, task.ID, func(task *Task, _ *Project) (string, error) {
			task.Roles = seats
			task.Status = TaskWriting
			task.Round = 2
			task.Revisions = []Revision{{N: 1, Seat: "Caspian"}}
			task.KeepThread(RoleImplementer, seats[1], json.RawMessage(`{"id":"thread"}`))
			return "", nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	out, err := s.Schedule(testContext, anyone)
	if err != nil || len(out) != 2 || out[0].Seat.Name != "Caspian" || out[1].Seat.Name != "Thea" {
		t.Fatalf("claims: %+v %v", out, err)
	}
	for _, turn := range out {
		if err := s.ReleaseClaim(testContext, turn.Task.ID, turn.Claim.Token); err != nil {
			t.Fatal(err)
		}
	}
	_, err = s.UpdateTask(testContext, tasks[0].ID, func(task *Task, _ *Project) (string, error) {
		task.AskHandOn(RoleImplementer, "", seats[1], "fresh eyes", time.Now())
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err = s.Schedule(testContext, anyone)
	if err != nil || len(out) != 2 || out[0].Seat.Name != "Thea" || len(out[0].Task.HandOn) != 0 {
		t.Fatalf("handed claims: %+v %v", out, err)
	}
}

func TestStalePlanAndVerdictRecordNeitherOutcomeNorHandOn(t *testing.T) {
	for _, kind := range []string{RoleResearcher, RoleReviewer} {
		t.Run(kind, func(t *testing.T) {
			s, _ := fixture(t)
			p := newProject(t, s)
			queued := queueAll(t, s, p, "A")[0]
			seat := Role{Name: "Second", Kinds: []string{kind}, Engine: "codex"}
			_, err := s.UpdateTask(testContext, queued.ID, func(task *Task, _ *Project) (string, error) {
				task.Status = TaskResearching
				task.Roles = []Role{seat}
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx := Fenced(context.Background(), queued.ID, "gone")
			if kind == RoleResearcher {
				_, err = s.RecordPlan(ctx, queued.ID, Plan{Role: seat.Name, Summary: "Plan"}, nil, nil, TurnEnd{HandOnWhy: "fresh eyes"})
			} else {
				_, err = s.UpdateTaskWithVerdict(ctx, queued.ID, Verdict{Role: seat.Name}, Screenshots{}, func(task *Task, _ *Project, v Verdict) (string, error) {
					task.Verdicts = append(task.Verdicts, v)
					return "", nil
				}, TurnEnd{HandOnWhy: "fresh eyes", Problems: []string{"malformed block"}})
			}
			if !errors.Is(err, ErrStale) {
				t.Fatalf("stale: %v", err)
			}
			snap, _ := s.Snapshot(testContext)
			task, _ := snap.FindTask(queued.ID)
			if task.Plan != nil || len(task.Verdicts) != 0 || len(task.HandOn) != 0 {
				t.Fatalf("stale record: %+v", task)
			}
		})
	}
}

func preferenceTask(kind string) Task {
	t := Task{ID: "t", ProjectID: "p", Status: TaskWriting, Roles: []Role{
		{Name: "Thea", Member: "thea", Kinds: []string{kind}},
		{Name: "Caspian", Member: "caspian", Kinds: []string{kind}},
	}, Revisions: []Revision{{N: 1, Seat: "Caspian"}}}
	t.KeepThread(RoleImplementer, t.Roles[1], json.RawMessage(`{"id":"thread"}`))
	return t
}

func TestSeatPreferenceClaimsWithoutWaiting(t *testing.T) {
	for _, kind := range []string{RoleImplementer, RoleResearcher} {
		for _, mode := range []string{"free", "busy", "both busy", "hand on", "hand on fallback"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				task := preferenceTask(kind)
				if kind == RoleResearcher {
					task.Status = TaskResearching
					task.Plan = &Plan{Role: "Caspian"}
				}
				v := Snapshot{}
				busy := map[string]busyWork{}
				if mode == "busy" || mode == "both busy" {
					busy[personKey(task.ProjectID, task.Roles, task.Roles[1])] = busyWork{wait: Wait{Kind: WaitMember, Seat: "Caspian", Objective: "Caspian's other task"}, projectID: "p"}
				}
				if mode == "both busy" || mode == "hand on fallback" {
					busy[personKey(task.ProjectID, task.Roles, task.Roles[0])] = busyWork{wait: Wait{Kind: WaitMember, Seat: "Thea"}, projectID: "p"}
				}
				if strings.HasPrefix(mode, "hand on") {
					recordTurnEndLogged(&v, &task, kind, "", task.Roles[1], TurnEnd{HandOnWhy: "A fresh view would help"}, time.Now())
				}
				out, wait := offer(&v, &task, busy, anyone, time.Now())
				if mode == "both busy" {
					if len(out) != 0 || wait == nil || wait.Seat != "Caspian" {
						t.Fatalf("out=%v wait=%+v", out, wait)
					}
					return
				}
				want := "Caspian"
				if mode == "busy" || mode == "hand on" {
					want = "Thea"
				}
				if wait != nil || len(out) != 1 || out[0].Seat.Name != want {
					t.Fatalf("want %s, out=%v wait=%+v", want, out, wait)
				}
				if len(task.HandOn) != 0 {
					t.Fatal("hand-on not consumed with claim")
				}
				if strings.HasPrefix(mode, "hand on") && len(v.Activity) == 0 {
					t.Fatal("reason not logged")
				}
				if mode == "hand on fallback" && !strings.Contains(v.Activity[len(v.Activity)-1].Summary, "no one else was free") {
					t.Fatalf("log: %+v", v.Activity)
				}
			})
		}
	}
}

func TestCheckerPreferenceForEachPreviousDraft(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		task := Task{ID: "t", ProjectID: "p", Status: TaskReviewing, Revisions: []Revision{{N: 1}, {N: 2}}, Roles: []Role{
			{Name: "Review", Member: "r", Kinds: []string{RoleReviewer}}, {Name: "Review 2", Member: "r", Kinds: []string{RoleReviewer}},
			{Name: "QA", Member: "q", Kinds: []string{RoleQA}}, {Name: "QA 2", Member: "q", Kinds: []string{RoleQA}},
		}, Verdicts: []Verdict{{Role: "Review 2", Revision: 1}, {Role: "QA 2", Revision: 1}}}
		busy := map[string]busyWork{}
		if fallback {
			for _, i := range []int{1, 3} {
				busy[personKey("p", task.Roles, task.Roles[i])] = busyWork{wait: Wait{Kind: WaitMember}}
			}
		}
		v := Snapshot{}
		out, wait := offer(&v, &task, busy, anyone, time.Now())
		if wait != nil || len(out) != 2 {
			t.Fatalf("out=%v wait=%v", out, wait)
		}
		want := []string{"Review 2", "QA 2"}
		if fallback {
			want = []string{"Review", "QA"}
		}
		for i := range out {
			if out[i].Seat.Name != want[i] {
				t.Fatalf("got %s want %s", out[i].Seat.Name, want[i])
			}
		}
	}
}

func TestDerivedTakersShareSchedulerPreference(t *testing.T) {
	task := preferenceTask(RoleImplementer)
	task.Threads, task.Revisions = nil, nil
	v := Snapshot{}
	derive(&v, &task)
	if len(task.Takes) != 1 || task.Takes[0].Preferred != "" || len(task.Takes[0].Others) != 2 {
		t.Fatalf("first: %+v", task.Takes)
	}
	task = preferenceTask(RoleImplementer)
	derive(&v, &task)
	if task.Takes[0].Preferred != "Caspian" || task.Takes[0].Did != "build 1" {
		t.Fatalf("next: %+v", task.Takes)
	}
	task.AskHandOn(RoleImplementer, "", task.Roles[1], "fresh eyes", time.Now())
	derive(&v, &task)
	if task.Takes[0].Preferred != "" || task.Takes[0].Others[0] != "Thea" {
		t.Fatalf("handed: %+v", task.Takes)
	}
	offer(&v, &task, map[string]busyWork{}, anyone, time.Now())
	derive(&v, &task)
	if len(task.Takes) != 0 {
		t.Fatalf("claimed: %+v", task.Takes)
	}
}

func TestHeldClaimsKeepTheirSeatAndHideTakerChoice(t *testing.T) {
	task := preferenceTask(RoleResearcher)
	task.Status = TaskResearching
	task.Plan = &Plan{Role: "Caspian"}
	task.Claims = []Claim{{Token: "held", Step: TaskResearching, Seat: "Thea", Held: "turn could not be confirmed ended"}}
	v := Snapshot{}
	derive(&v, &task)
	if task.Checking != "Thea" || len(task.Takes) != 0 {
		t.Fatalf("held: checking=%s takes=%+v", task.Checking, task.Takes)
	}
	if out, _ := offer(&v, &task, map[string]busyWork{}, anyone, time.Now()); len(out) != 0 {
		t.Fatal("held claim moved")
	}
}

func TestCheckingNamesPreviousDraftsSeatAndDropsRemovedPreference(t *testing.T) {
	task := Task{Status: TaskReviewing, Round: 2, Revisions: []Revision{{N: 1}, {N: 2}}, Roles: []Role{
		{Name: "First", Member: "reviewer", Kinds: []string{RoleReviewer}},
		{Name: "Second", Member: "reviewer", Kinds: []string{RoleReviewer}},
	}, Verdicts: []Verdict{{Role: "Second", Revision: 1}}}
	v := Snapshot{}
	derive(&v, &task)
	if task.Checking != "Second" || len(task.Takes) != 1 || task.Takes[0].Preferred != "Second" || task.Takes[0].Did != "checked draft 1" {
		t.Fatalf("checking: %+v", task)
	}
	task.Roles = task.Roles[:1]
	derive(&v, &task)
	if task.Checking != "First" || task.Takes[0].Preferred != "" || task.Takes[0].Did != "" {
		t.Fatalf("removed: %+v", task)
	}
}

func TestCheckerRechecksPreferLatestSeatOnSameDraft(t *testing.T) {
	task := Task{Status: TaskReviewing, Round: 2, Revisions: []Revision{{N: 1}, {N: 2}}, Roles: []Role{
		{Name: "First", Member: "r", Kinds: []string{RoleReviewer}}, {Name: "Second", Member: "r", Kinds: []string{RoleReviewer}},
	}, Verdicts: []Verdict{{Role: "Second", Revision: 1}, {Role: "First", Revision: 2, BriefVersion: 1}, {Role: "Second", Revision: 2, BriefVersion: 1}}}
	v := Snapshot{Projects: []Project{{ID: "p", Brief: Brief{Version: 2}}}}
	task.ProjectID = "p"
	derive(&v, &task)
	if task.Checking != "Second" || task.Takes[0].Preferred != "Second" || task.Takes[0].Did != "checked draft 2" {
		t.Fatalf("recheck: %+v", task)
	}
	busy := map[string]busyWork{personKey("p", task.Roles, task.Roles[1]): {wait: Wait{Kind: WaitMember}}}
	out, wait := offer(&v, &task, busy, anyone, time.Now())
	if len(out) != 1 || wait != nil || out[0].Seat.Name != "First" {
		t.Fatalf("fallback: %+v %v", out, wait)
	}
}

func TestNoTakersForFinishedTasksOrCompletedChecks(t *testing.T) {
	for _, status := range []string{TaskDelivered, TaskLanded, TaskStopped, TaskWaiting, TaskLanding, TaskAwaiting, TaskDeciding} {
		task := preferenceTask(RoleImplementer)
		task.Status = status
		derive(&Snapshot{}, &task)
		if len(task.Takes) != 0 {
			t.Fatalf("%s: %+v", status, task.Takes)
		}
	}
	task := preferenceTask(RoleResearcher)
	task.Status = TaskResearching
	task.Plan = nil
	task.Threads = nil
	derive(&Snapshot{}, &task)
	if task.Checking != "" {
		t.Fatalf("unclaimed rotation names a seat: %s", task.Checking)
	}
}

func TestHandOnLogsOnceWhenRecordedNotOnLaterUpdates(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	queued := queueAll(t, s, p, "A")[0]
	_, err := s.UpdateTask(testContext, queued.ID, func(task *Task, _ *Project) (string, error) {
		task.AskHandOn(RoleImplementer, "", Role{Name: "Writer"}, "fresh eyes", time.Now())
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	count := func() int {
		snap, _ := s.Snapshot(testContext)
		n := 0
		for _, a := range snap.Activity {
			if a.TaskID == queued.ID && a.Kind == "task.hand_on" {
				n++
			}
		}
		return n
	}
	if count() != 1 {
		t.Fatal("request not logged in recording update")
	}
	for range 3 {
		if _, err := s.UpdateTask(testContext, queued.ID, func(task *Task, _ *Project) (string, error) { task.Detail = "updated"; return "", nil }); err != nil {
			t.Fatal(err)
		}
	}
	if count() != 1 {
		t.Fatal("pending request was logged again")
	}
}

func TestWriterHandOnUsesServiceClock(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	queued := queueAll(t, s, p, "A")[0]
	now := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	s.now = func() time.Time { return now }
	out, err := s.UpdateTask(testContext, queued.ID, func(task *Task, _ *Project) (string, error) {
		task.AskHandOn(RoleImplementer, "", Role{Name: "Writer"}, "fresh eyes", time.Time{})
		return "", nil
	})
	if err != nil || len(out.HandOn) != 1 || !out.HandOn[0].At.Equal(now) {
		t.Fatalf("request: %+v %v", out.HandOn, err)
	}
	snap, _ := s.Snapshot(testContext)
	for _, a := range snap.Activity {
		if a.TaskID == queued.ID && a.Kind == "task.hand_on" {
			if !a.CreatedAt.Equal(now) {
				t.Fatal("log ignored service clock")
			}
			return
		}
	}
	t.Fatal("request log missing")
}
