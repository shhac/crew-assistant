package core

import (
	"testing"
	"time"
)

func TestQueuedFirstRoundsRotateWithoutSkippingQueueStart(t *testing.T) {
	for _, researching := range []bool{false, true} {
		t.Run(map[bool]string{false: "build", true: "research"}[researching], func(t *testing.T) {
			s, cfg := fixture(t)
			p := newProject(t, s)
			pb := *p.Playbook
			pb.MaxActive = 1
			kind, step := RoleImplementer, TaskWriting
			if researching {
				kind, step = RoleResearcher, TaskResearching
			}
			pb.Roles = []Role{{Name: "A", Kinds: []string{RoleImplementer}, Engine: "codex"}, {Name: "B", Kinds: []string{RoleImplementer}, Engine: "codex"}, {Name: "Check", Kinds: []string{RoleReviewer}, Engine: "codex"}}
			if researching {
				pb.Roles[0].Kinds = append(pb.Roles[0].Kinds, RoleResearcher)
				pb.Roles[1].Kinds = append(pb.Roles[1].Kinds, RoleResearcher)
			}
			if _, err := s.SetPlaybook(testContext, p.ID, pb); err != nil {
				t.Fatal(err)
			}
			tasks := queueAll(t, s, p, "A task", "B task", "C task", "D task")
			for i, task := range tasks {
				// No manual status or role changes: Schedule starts from TaskQueued.
				s = NewService(s.store, cfg)
				before, _ := s.Snapshot(testContext)
				queued, _ := before.FindTask(task.ID)
				if queued.Status != TaskQueued {
					t.Fatalf("not queued: %+v", queued)
				}
				out, err := s.Schedule(testContext, anyone)
				want := []string{"A", "B"}[i%2]
				if err != nil || len(out) != 1 || out[0].Claim.Seat != want || out[0].Claim.Step != step {
					t.Fatalf("first round %d: %+v %v", i, out, err)
				}
				if err := s.ReleaseClaim(testContext, task.ID, out[0].Claim.Token); err != nil {
					t.Fatal(err)
				}
				s = NewService(s.store, cfg)
				retry, err := s.Schedule(testContext, anyone)
				if err != nil || len(retry) != 1 || retry[0].Seat.Name != want {
					t.Fatalf("durable retry: %+v %v", retry, err)
				}
				snap, _ := s.Snapshot(testContext)
				if snap.Projects[0].SeatRotation[kind] != want {
					t.Fatal("rotation not saved with claim")
				}
				if _, err := s.UpdateTask(testContext, task.ID, func(task *Task, _ *Project) (string, error) { task.Status = TaskStopped; return "", nil }); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestFirstRoundsRotateDurablyPerProjectAndRole(t *testing.T) {
	s, cfg := fixture(t)
	p := newProject(t, s)
	pb := *p.Playbook
	pb.MaxActive = 0
	pb.Roles = []Role{
		{Name: "A", Kinds: []string{RoleImplementer, RoleResearcher}, Engine: "codex"},
		{Name: "B", Kinds: []string{RoleImplementer, RoleResearcher}, Engine: "codex"},
		{Name: "Reviewer", Kinds: []string{RoleReviewer}, Engine: "codex"},
	}
	if _, err := s.SetPlaybook(testContext, p.ID, pb); err != nil {
		t.Fatal(err)
	}
	other := newProject(t, s)
	if _, err := s.SetPlaybook(testContext, other.ID, pb); err != nil {
		t.Fatal(err)
	}
	for _, run := range []struct{ project, kind, want string }{
		{p.ID, RoleImplementer, "A"}, {p.ID, RoleImplementer, "B"},
		{p.ID, RoleResearcher, "A"}, {p.ID, RoleResearcher, "B"},
		{other.ID, RoleImplementer, "A"}, {p.ID, RoleImplementer, "A"},
	} {
		queued := queueAll(t, s, Project{ID: run.project}, "First turn")[0]
		_, err := s.UpdateTask(testContext, queued.ID, func(task *Task, _ *Project) (string, error) {
			task.Roles = pb.Roles
			task.Status = TaskWriting
			if run.kind == RoleResearcher {
				task.Status = TaskResearching
			}
			return "", nil
		})
		if err != nil {
			t.Fatal(err)
		}
		// A fresh service gets its choice only from the durable project record.
		s = NewService(s.store, cfg)
		out, err := s.Schedule(testContext, anyone)
		if err != nil || len(out) != 1 || out[0].Seat.Name != run.want {
			t.Fatalf("%+v: claims=%+v error=%v", run, out, err)
		}
		_, err = s.UpdateTask(testContext, queued.ID, func(task *Task, _ *Project) (string, error) { task.Status = TaskStopped; return "", nil })
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestFirstRoundRotationSkipsBusyAndRefusedSeats(t *testing.T) {
	for _, mode := range []string{"free", "busy", "refused", "none admitted"} {
		t.Run(mode, func(t *testing.T) {
			task := preferenceTask(RoleImplementer)
			task.Revisions, task.Threads = nil, nil
			task.Roles = append(task.Roles, Role{Name: "Third", Kinds: []string{RoleImplementer}})
			v := Snapshot{Projects: []Project{{ID: "p", SeatRotation: map[string]string{RoleImplementer: "Thea"}}}}
			busy := map[string]busyWork{}
			if mode == "busy" {
				busy[personKey("p", task.Roles, task.Roles[1])] = busyWork{wait: Wait{Kind: WaitMember}}
			}
			admit := func(r Role) string {
				if mode == "none admitted" || (mode == "refused" && r.Name == "Caspian") {
					return WaitEngineCap
				}
				return ""
			}
			out, wait := offer(&v, &task, busy, admit, time.Now())
			if mode == "none admitted" {
				if len(out) != 0 || wait == nil || v.Projects[0].SeatRotation[RoleImplementer] != "Thea" {
					t.Fatalf("blocked turn advanced: %+v %+v", out, v.Projects[0])
				}
				return
			}
			want := "Caspian"
			if mode != "free" {
				want = "Third"
			}
			if len(out) != 1 || wait != nil || out[0].Seat.Name != want || v.Projects[0].SeatRotation[RoleImplementer] != want {
				t.Fatalf("want %s: out=%+v wait=%+v project=%+v", want, out, wait, v.Projects[0])
			}
		})
	}
}

func TestFirstDraftChecksRotateWithinTheirGroups(t *testing.T) {
	task := Task{ID: "t", ProjectID: "p", Status: TaskReviewing, Revisions: []Revision{{N: 1}}, Roles: []Role{
		{Name: "R1", Member: "r", Kinds: []string{RoleReviewer}}, {Name: "R2", Member: "r", Kinds: []string{RoleReviewer}},
		{Name: "Q1", Member: "q", Kinds: []string{RoleQA}}, {Name: "Q2", Member: "q", Kinds: []string{RoleQA}},
	}}
	v := Snapshot{Projects: []Project{{ID: "p"}}}
	for i := range 3 {
		task.Claims = nil
		task.FirstSeats = nil // Each iteration represents a new task.
		out, wait := offer(&v, &task, map[string]busyWork{}, anyone, time.Now())
		want := []string{"R1", "Q1"}
		if i == 1 {
			want = []string{"R2", "Q2"}
		}
		if wait != nil || len(out) != 2 {
			t.Fatalf("checks: %+v %v", out, wait)
		}
		for j := range out {
			if out[j].Seat.Name != want[j] {
				t.Fatalf("round %d: got %s want %s", i, out[j].Seat.Name, want[j])
			}
		}
	}
}

func TestLaterRoundsAndHandOnIgnoreFirstRoundCursor(t *testing.T) {
	for _, mode := range []string{"preferred", "busy", "hand on", "removed"} {
		t.Run(mode, func(t *testing.T) {
			task := preferenceTask(RoleImplementer)
			v := Snapshot{Projects: []Project{{ID: "p", SeatRotation: map[string]string{RoleImplementer: "Thea"}}}}
			busy := map[string]busyWork{}
			want := "Caspian"
			if mode == "busy" {
				busy[personKey("p", task.Roles, task.Roles[1])] = busyWork{wait: Wait{Kind: WaitMember}}
				want = "Thea"
			}
			if mode == "hand on" {
				task.AskHandOn(RoleImplementer, "", task.Roles[1], "fresh eyes", time.Now())
				want = "Thea"
			}
			if mode == "removed" {
				task.Roles = append(task.Roles[:1], Role{Name: "Third", Kinds: []string{RoleImplementer}})
				want = "Thea"
			}
			out, wait := offer(&v, &task, busy, anyone, time.Now())
			if len(out) != 1 || wait != nil || out[0].Seat.Name != want || v.Projects[0].SeatRotation[RoleImplementer] != "Thea" {
				t.Fatalf("want %s: %+v %+v", want, out, v.Projects[0])
			}
		})
	}
}

func TestOwnersDraftDoesNotCountAsEarlierBuilderWork(t *testing.T) {
	task := preferenceTask(RoleImplementer)
	task.Threads = nil
	task.Revisions = []Revision{{N: 1, By: DraftByOwner}, {N: 2, CleanMergeOf: 1}}
	v := Snapshot{Projects: []Project{{ID: "p", SeatRotation: map[string]string{RoleImplementer: "Thea"}}}}
	out, wait := offer(&v, &task, map[string]busyWork{}, anyone, time.Now())
	if wait != nil || len(out) != 1 || out[0].Seat.Name != "Caspian" {
		t.Fatalf("first builder: %+v %v", out, wait)
	}
}

func TestFirstClaimRetryKeepsSeatAndRotation(t *testing.T) {
	for _, kind := range []string{RoleImplementer, RoleResearcher, RoleReviewer} {
		t.Run(kind, func(t *testing.T) {
			task := Task{ID: "t", ProjectID: "p", Status: TaskWriting, Roles: []Role{{Name: "A", Kinds: []string{kind}, Member: "same"}, {Name: "B", Kinds: []string{kind}, Member: "same"}}}
			if kind == RoleResearcher {
				task.Status = TaskResearching
			}
			if kind == RoleReviewer {
				task.Status = TaskReviewing
				task.Revisions = []Revision{{N: 1}}
			}
			v := Snapshot{Projects: []Project{{ID: "p"}}}
			first, wait := offer(&v, &task, map[string]busyWork{}, anyone, time.Now())
			if wait != nil || len(first) != 1 {
				t.Fatalf("first: %+v %v", first, wait)
			}
			cursor := v.Projects[0].SeatRotation[seatKey(kind, first[0].Claim.Group)]
			task.Claims = nil // A failed turn records no outcome and releases its claim.
			retry, wait := offer(&v, &task, map[string]busyWork{}, anyone, time.Now())
			if wait != nil || len(retry) != 1 || retry[0].Seat.Name != first[0].Seat.Name {
				t.Fatalf("retry: %+v %v", retry, wait)
			}
			if v.Projects[0].SeatRotation[seatKey(kind, first[0].Claim.Group)] != cursor {
				t.Fatal("retry advanced rotation")
			}
		})
	}
}

func TestCheckingMatchesCurrentStageAcrossClaimGroups(t *testing.T) {
	task := Task{ID: "t", Status: TaskReviewing, Roles: []Role{{Name: "R", Kinds: []string{RoleReviewer}}, {Name: "Q", Kinds: []string{RoleQA}}}, Revisions: []Revision{{N: 1}}, Claims: []Claim{{Step: TaskReviewing, Seat: "Q"}, {Step: TaskReviewing, Seat: "R"}}}
	derive(&Snapshot{}, &task)
	if task.Checking != "R" {
		t.Fatalf("checking=%q stage=%q", task.Checking, task.Stage)
	}
	task.Roles = append(task.Roles, Role{Name: "R2", Kinds: []string{RoleReviewer}})
	task.Claims = append(task.Claims, Claim{Step: TaskReviewing, Seat: "R2"})
	derive(&Snapshot{}, &task)
	if task.Checking != "" {
		t.Fatalf("multiple concurrent checkers named one seat: %q", task.Checking)
	}
}

func TestFirstBuildRetryFallsBackWithoutAdvancingRotation(t *testing.T) {
	task := Task{ID: "t", ProjectID: "p", Status: TaskWriting, Roles: []Role{{Name: "A", Kinds: []string{RoleImplementer}}, {Name: "B", Kinds: []string{RoleImplementer}}}}
	v := Snapshot{Projects: []Project{{ID: "p"}}}
	first, _ := offer(&v, &task, map[string]busyWork{}, anyone, time.Now())
	task.Claims = nil
	busy := map[string]busyWork{personKey("p", task.Roles, first[0].Seat): {wait: Wait{Kind: WaitMember}}}
	fallback, wait := offer(&v, &task, busy, anyone, time.Now())
	if wait != nil || len(fallback) != 1 || fallback[0].Seat.Name != "B" {
		t.Fatalf("fallback: %+v %v", fallback, wait)
	}
	if v.Projects[0].SeatRotation[RoleImplementer] != "A" {
		t.Fatal("retry advanced cursor")
	}
	task.Claims = nil
	retry, wait := offer(&v, &task, map[string]busyWork{}, anyone, time.Now())
	if wait != nil || len(retry) != 1 || retry[0].Seat.Name != "B" {
		t.Fatalf("retry lost latest claimant: %+v %v", retry, wait)
	}
}

func TestRemovedFirstClaimantReturnsToRotation(t *testing.T) {
	for _, kind := range []string{RoleImplementer, RoleResearcher, RoleReviewer} {
		t.Run(kind, func(t *testing.T) {
			task := Task{ID: "t", ProjectID: "p", Status: TaskWriting, FirstSeats: map[string]string{seatKey(kind, ""): "Removed"}, Roles: []Role{{Name: "A", Kinds: []string{kind}, Member: "m"}, {Name: "B", Kinds: []string{kind}, Member: "m"}}}
			group := ""
			if kind == RoleResearcher {
				task.Status = TaskResearching
			}
			if kind == RoleReviewer {
				task.Status = TaskReviewing
				task.Revisions = []Revision{{N: 1}}
				group = groupKey(task.Roles[0])
				task.FirstSeats = map[string]string{seatKey(kind, group): "Removed"}
			}
			v := Snapshot{Projects: []Project{{ID: "p", SeatRotation: map[string]string{seatKey(kind, group): "A"}}}}
			out, wait := offer(&v, &task, map[string]busyWork{}, anyone, time.Now())
			if wait != nil || len(out) != 1 || out[0].Seat.Name != "B" {
				t.Fatalf("removed claimant: %+v %v", out, wait)
			}
		})
	}
}
func TestCheckingOnlyUsesTheCurrentKindsChoice(t *testing.T) {
	roles := []Role{{Name: "Ada", Kinds: []string{RoleResearcher, RoleImplementer}}, {Name: "Bo", Kinds: []string{RoleImplementer}}, {Name: "Pim", Kinds: []string{RolePM, RoleImplementer}}}
	v := Snapshot{Projects: []Project{{ID: "p", Playbook: &Playbook{Roles: roles}}}}
	for _, status := range []string{TaskResearching, TaskTriage} {
		task := Task{ID: "t", ProjectID: "p", Status: status, Roles: roles, Waiting: &Wait{Kind: WaitMember, Seat: "Ada"}}
		derive(&v, &task)
		want := "Ada"
		if status == TaskTriage {
			want = "Pim"
		}
		if task.Checking != want {
			t.Fatalf("%s checking=%q want=%q", status, task.Checking, want)
		}
	}
}
