package core

import (
	"errors"
	"slices"
	"testing"
	"time"
)

// anyone admits every seat's turn.
func anyone(Role) string { return "" }

// claimed is what one look at the work claims, as "task objective: step
// by seat".
func claimed(t *testing.T, s *Service) []string {
	t.Helper()
	out, err := s.Schedule(testContext, anyone)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range out {
		got = append(got, c.Task.Objective+": "+c.Claim.Step+" by "+c.Claim.Seat)
	}
	return got
}

// finish ends a claimed step: its claim is cleared and the task moves to
// status, with a draft when it is past writing.
func finish(t *testing.T, s *Service, id, status string) {
	t.Helper()
	if _, err := s.UpdateTask(testContext, id, func(t *Task, _ *Project) (string, error) {
		t.Claims, t.Status = nil, status
		if status != TaskWriting && len(t.Revisions) == 0 {
			t.Revisions = []Revision{{N: 1}}
		}
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
}

func queueAll(t *testing.T, s *Service, p Project, objectives ...string) []Task {
	t.Helper()
	var out []Task
	for _, o := range objectives {
		task, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: o})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, task)
	}
	return out
}

// With one implementer seat and an explicit cap of one, a project starts and
// moves its tasks as it did when the loop worked one task at a time: the
// next queued task starts only once the one under way waits on the owner or
// ends, and a task waiting on the owner or in triage never holds the cap.
func TestOneSeatAndACapOfOneWorkAsBefore(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	playbook := *p.Playbook
	playbook.MaxActive = 1
	if _, err := s.SetPlaybook(testContext, p.ID, playbook); err != nil {
		t.Fatal(err)
	}
	tasks := queueAll(t, s, p, "A", "B")
	if got := claimed(t, s); !slices.Equal(got, []string{"A: writing by Writer"}) {
		t.Fatalf("first look: %v", got)
	}
	// Nothing more while A's writer works.
	if got := claimed(t, s); len(got) != 0 {
		t.Fatalf("claimed beside a step under way: %v", got)
	}
	// A in review: its check runs, and B still waits for the cap.
	finish(t, s, tasks[0].ID, TaskReviewing)
	if got := claimed(t, s); !slices.Equal(got, []string{"A: reviewing by Reviewer"}) {
		t.Fatalf("with A in review: %v", got)
	}
	snap, _ := s.Snapshot(testContext)
	if b, _ := snap.FindTask(tasks[1].ID); b.Status != TaskQueued {
		t.Fatalf("B started past the cap: %+v", b)
	}
	// A waiting on the owner holds nothing, so B starts.
	finish(t, s, tasks[0].ID, TaskWaiting)
	if got := claimed(t, s); !slices.Equal(got, []string{"B: writing by Writer"}) {
		t.Fatalf("with A waiting: %v", got)
	}
	// A task in triage never starts, or counts, however long it waits.
	finish(t, s, tasks[1].ID, TaskWaiting)
	triaged, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "C"})
	s.UpdateTask(testContext, triaged.ID, func(t *Task, _ *Project) (string, error) {
		t.Status = TaskTriage
		return "", nil
	})
	if got := claimed(t, s); len(got) != 0 {
		t.Fatalf("a task in triage started: %v", got)
	}
}

// Above a cap of one, the implementer starts the next queued task, in queue
// order, while one it wrote is checked; queued tasks past the cap never start
// just because they are queued, and a busy seat starts nothing.
func TestTheImplementerTakesTheNextTaskWhileOneIsCheckedWithinTheCap(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	playbook := *p.Playbook
	playbook.MaxActive = 2
	if _, err := s.SetPlaybook(testContext, p.ID, playbook); err != nil {
		t.Fatal(err)
	}
	tasks := queueAll(t, s, p, "A", "B", "C")
	// C goes before B.
	if _, err := s.OrderTasks(testContext, p.ID, []string{tasks[0].ID, tasks[2].ID, tasks[1].ID}, OrderedByOwner); err != nil {
		t.Fatal(err)
	}
	if got := claimed(t, s); !slices.Equal(got, []string{"A: writing by Writer"}) {
		t.Fatalf("one implementer seat starts one task: %v", got)
	}
	finish(t, s, tasks[0].ID, TaskReviewing)
	if got := claimed(t, s); !slices.Equal(got, []string{"A: reviewing by Reviewer", "C: writing by Writer"}) {
		t.Fatalf("with A in review: %v", got)
	}
	// The cap is reached: B stays queued even once the writer is free.
	finish(t, s, tasks[2].ID, TaskReviewing)
	finish(t, s, tasks[0].ID, TaskDeciding)
	got := claimed(t, s)
	if slices.ContainsFunc(got, func(c string) bool { return c[0] == 'B' }) {
		t.Fatalf("B started past the cap: %v", got)
	}
	snap, _ := s.Snapshot(testContext)
	if b, _ := snap.FindTask(tasks[1].ID); b.Status != TaskQueued {
		t.Fatalf("B %+v", b)
	}
}

// Seats filled from one member are interchangeable: two implementer seats
// write two tasks at once, and two reviewer seats of one member give a draft
// one verdict between them, not one each.
func TestSeatsOfOneTemplateWorkApartAndJudgeAsOne(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	playbook := *p.Playbook
	playbook.Roles = slices.Clone(playbook.Roles)
	playbook.Roles[1].Member = "claudius"
	for _, seat := range []string{"Writer", "Reviewer"} {
		added, err := playbook.AddSeat(seat)
		if err != nil {
			t.Fatal(err)
		}
		if added.Name != seat+" #2" {
			t.Fatalf("added %q", added.Name)
		}
	}
	if _, err := s.SetPlaybook(testContext, p.ID, playbook); err != nil {
		t.Fatal(err)
	}
	if cap := playbook.ActiveCap(); cap != 0 {
		t.Fatalf("the overall cap defaults to no limit: %d", cap)
	}
	tasks := queueAll(t, s, p, "A", "B")
	if got := claimed(t, s); !slices.Equal(got, []string{"A: writing by Writer", "B: writing by Writer #2"}) {
		t.Fatalf("two writers: %v", got)
	}
	finish(t, s, tasks[0].ID, TaskReviewing)
	if got := claimed(t, s); !slices.Equal(got, []string{"A: reviewing by Reviewer"}) {
		t.Fatalf("one check for the member's group: %v", got)
	}
	snap, _ := s.Snapshot(testContext)
	a, _ := snap.FindTask(tasks[0].ID)
	if groups := a.CheckerGroups(); len(groups) != 1 || len(groups[0].Seats) != 2 {
		t.Fatalf("groups %+v", groups)
	}
	// Either seat's verdict is the member's.
	a.Verdicts = []Verdict{{Revision: 1, Role: "Reviewer #2", BriefVersion: 1, Outcome: VerdictPass}}
	if !a.Judged("Reviewer", 1, 1) || len(a.Checkers()) != 1 {
		t.Fatalf("a seat's verdict should count for its member: %+v", a.Checkers())
	}
}

// A member's seats check a draft one at a time between them: a message to
// one seat waits while another of the member's seats checks the task, and a
// check waits while one answers a message, so the member never gives two
// verdicts at once.
func TestAMemberNeverChecksADraftTwiceAtOnce(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	playbook := *p.Playbook
	playbook.Roles = slices.Clone(playbook.Roles)
	playbook.Roles[1].Member = "claudius"
	playbook.AddSeat("Reviewer")
	if _, err := s.SetPlaybook(testContext, p.ID, playbook); err != nil {
		t.Fatal(err)
	}
	a := queueAll(t, s, p, "A")[0]
	claimed(t, s)
	finish(t, s, a.ID, TaskReviewing)
	ask := func(to string) TeamMessage {
		t.Helper()
		m, err := s.SendTeamMessage(testContext, p.ID, a.ID, to, FromOwner, "Is it warm enough?")
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	// The check is under way on Reviewer; a message to Reviewer #2 waits.
	check, _ := s.Schedule(testContext, anyone)
	if len(check) != 1 || check[0].Claim.Seat != "Reviewer" {
		t.Fatalf("check %+v", check)
	}
	m := ask("Reviewer #2")
	if _, ok, err := s.ClaimMessage(testContext, a.ID, m.ID, anyone); err != nil || ok {
		t.Fatalf("a message was answered beside the member's check: %v %v", ok, err)
	}
	s.ReleaseClaim(testContext, a.ID, check[0].Claim.Token)
	// Now the message goes first, and the check waits for it.
	answering, ok, err := s.ClaimMessage(testContext, a.ID, m.ID, anyone)
	if err != nil || !ok || answering.Claim.Seat != "Reviewer #2" {
		t.Fatalf("the message %+v %v %v", answering, ok, err)
	}
	if got := claimed(t, s); len(got) != 0 {
		t.Fatalf("a check was claimed beside the member's message: %v", got)
	}
	s.ReleaseClaim(testContext, a.ID, answering.Claim.Token)
	if got := claimed(t, s); !slices.Equal(got, []string{"A: reviewing by Reviewer"}) {
		t.Fatalf("once the message was answered: %v", got)
	}
}

// A project lands one task at a time.
func TestAProjectLandsOneTaskAtATime(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	playbook := *p.Playbook
	playbook.MaxActive = 2
	s.SetPlaybook(testContext, p.ID, playbook)
	tasks := queueAll(t, s, p, "A", "B")
	claimed(t, s)
	finish(t, s, tasks[0].ID, TaskLanding)
	claimed(t, s)
	finish(t, s, tasks[1].ID, TaskLanding)
	finish(t, s, tasks[0].ID, TaskLanding)
	if got := claimed(t, s); len(got) != 1 {
		t.Fatalf("two landings at once: %v", got)
	}
	if got := claimed(t, s); len(got) != 0 {
		t.Fatalf("a second landing beside the first: %v", got)
	}
}

// A landing stopped while its delivery is still going out ends its claim at
// once, but the next task lands only once that delivery is settled.
func TestALandingWaitsForAStoppedDeliveryToSettle(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	playbook := *p.Playbook
	playbook.MaxActive = 2
	s.SetPlaybook(testContext, p.ID, playbook)
	tasks := queueAll(t, s, p, "A", "B")
	claimed(t, s)
	finish(t, s, tasks[0].ID, TaskLanding)
	claimed(t, s)
	finish(t, s, tasks[1].ID, TaskLanding)
	finish(t, s, tasks[0].ID, TaskLanding)
	if got := claimed(t, s); !slices.Equal(got, []string{"A: landing by "}) {
		t.Fatalf("A should land first: %v", got)
	}
	// A is stopped mid-delivery: its claim goes, its delivery doesn't.
	stopped, err := s.UpdateTask(testContext, tasks[0].ID, func(t *Task, _ *Project) (string, error) {
		t.Status, t.Delivering = TaskStopped, &Delivering{Revision: 1}
		return "", nil
	})
	if err != nil || len(stopped.Claims) != 0 {
		t.Fatalf("stopped %+v %v", stopped, err)
	}
	if got := claimed(t, s); len(got) != 0 {
		t.Fatalf("B landed beside A's delivery: %v", got)
	}
	s.UpdateTask(testContext, tasks[0].ID, func(t *Task, _ *Project) (string, error) {
		t.Delivering = nil
		return "", nil
	})
	if got := claimed(t, s); !slices.Equal(got, []string{"B: landing by "}) {
		t.Fatalf("once A's delivery settled: %v", got)
	}
}

// A turn whose claim has gone records nothing, anywhere; a restart clears
// every claim onto a fresh attempt, except one whose turn may still run,
// which keeps holding its task and seat.
func TestAStaleTurnRecordsNothingAndARestartClearsClaims(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	tasks := queueAll(t, s, p, "A", "B")
	out, _ := s.Schedule(testContext, anyone)
	c := out[0].Claim
	fenced := Fenced(testContext, tasks[0].ID, c.Token)
	if _, err := s.UpdateTask(fenced, tasks[0].ID, func(t *Task, _ *Project) (string, error) {
		t.Detail = "recorded"
		return "", nil
	}); err != nil {
		t.Fatalf("a live claim was refused: %v", err)
	}
	// Stopping the task takes its claim back.
	if _, err := s.UpdateTask(testContext, tasks[0].ID, func(t *Task, _ *Project) (string, error) {
		t.Status = TaskStopped
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
	_, err := s.UpdateTask(fenced, tasks[0].ID, func(t *Task, _ *Project) (string, error) {
		t.Detail = "late"
		return "", nil
	})
	if !errors.Is(err, ErrStale) {
		t.Fatalf("a stale turn recorded: %v", err)
	}
	// Nor anything on another task.
	if _, err = s.UpdateTask(fenced, tasks[1].ID, func(t *Task, _ *Project) (string, error) { return "", nil }); !errors.Is(err, ErrStale) {
		t.Fatalf("a stale turn changed another task: %v", err)
	}
	snap, _ := s.Snapshot(testContext)
	if a, _ := snap.FindTask(tasks[0].ID); a.Detail == "late" || len(a.Claims) != 0 {
		t.Fatalf("A %+v", a)
	}

	// B's claim, and a held one, left by a daemon that stopped.
	out, _ = s.Schedule(testContext, anyone)
	b := out[0]
	before := b.Task.Attempt
	if err = s.RecoverClaims(testContext, nil); err != nil {
		t.Fatal(err)
	}
	snap, _ = s.Snapshot(testContext)
	got, _ := snap.FindTask(b.Task.ID)
	if len(got.Claims) != 0 || got.Attempt != before+1 || got.Failures != 0 || got.Status != TaskWriting {
		t.Fatalf("after recovery %+v", got)
	}
	if _, err = s.UpdateTask(Fenced(testContext, b.Task.ID, b.Claim.Token), b.Task.ID, func(t *Task, _ *Project) (string, error) { return "", nil }); !errors.Is(err, ErrStale) {
		t.Fatalf("a turn from before the restart recorded: %v", err)
	}
	out, _ = s.Schedule(testContext, anyone)
	if len(out) != 1 || out[0].Claim.Token == b.Claim.Token {
		t.Fatalf("the step should be claimed afresh: %+v", out)
	}
	if err = s.RecoverClaims(testContext, map[string]string{out[0].Claim.Token: "still running"}); err != nil {
		t.Fatal(err)
	}
	snap, _ = s.Snapshot(testContext)
	got, _ = snap.FindTask(b.Task.ID)
	if len(got.Claims) != 1 || got.Claims[0].Held != "still running" {
		t.Fatalf("a held claim %+v", got.Claims)
	}
	if again, _ := s.Schedule(testContext, anyone); len(again) != 0 {
		t.Fatalf("a held task was claimed again: %+v", again)
	}
}

// pmWriter is a project whose one seat, Pim, both implements and keeps the
// list, with task A under way in writing and the list due a look.
func pmWriter(t *testing.T) (*Service, Project, Task) {
	t.Helper()
	s, _ := fixture(t)
	p := newProject(t, s)
	playbook := *p.Playbook
	playbook.Roles = []Role{{Name: "Pim", Kinds: []string{RolePM, RoleImplementer}, Engine: "claude"}, playbook.Roles[1]}
	if _, err := s.SetPlaybook(testContext, p.ID, playbook); err != nil {
		t.Fatal(err)
	}
	a := queueAll(t, s, p, "A")[0]
	finish(t, s, a.ID, TaskWriting)
	s.UpdateTask(testContext, a.ID, func(t *Task, p *Project) (string, error) {
		t.Roles, t.Round = playbook.Roles, 1
		p.PMDue = true
		return "", nil
	})
	return s, p, a
}

// The PM's look is a claimed step like any other: a seat that both keeps the
// list and implements does one at a time, whichever it took first.
func TestThePMsLookAndItsSeatsOtherWorkExcludeEachOther(t *testing.T) {
	s, p, _ := pmWriter(t)
	c, seat, ok, err := s.ClaimPM(testContext, p.ID, anyone)
	if err != nil || !ok || seat.Name != "Pim" || c.Step != StepPM {
		t.Fatalf("the PM's look %+v %+v %v %v", c, seat, ok, err)
	}
	if got := claimed(t, s); len(got) != 0 {
		t.Fatalf("Pim was given A to write while it keeps the list: %v", got)
	}
	if _, _, again, _ := s.ClaimPM(testContext, p.ID, anyone); again {
		t.Fatal("a second look was claimed beside the first")
	}
	if err = s.ReleaseProjectClaim(testContext, p.ID, c.Token); err != nil {
		t.Fatal(err)
	}
	if got := claimed(t, s); !slices.Equal(got, []string{"A: writing by Pim"}) {
		t.Fatalf("once the look ended: %v", got)
	}
	if _, _, ok, _ = s.ClaimPM(testContext, p.ID, anyone); ok {
		t.Fatal("the PM looked while its seat writes A")
	}
}

// A step no seat took holds the seat it comes to need, as deciding holds
// the PM's while the PM chooses, only while the seat is free.
func TestAStepHoldsTheSeatItNeedsOnlyWhileItIsFree(t *testing.T) {
	s, p, a := pmWriter(t)
	finish(t, s, a.ID, TaskDeciding)
	out, _ := s.Schedule(testContext, anyone)
	if len(out) != 1 || out[0].Claim.Seat != "" {
		t.Fatalf("deciding takes no seat of its own: %+v", out)
	}
	decide := out[0].Claim
	look, _, ok, _ := s.ClaimPM(testContext, p.ID, anyone)
	if !ok {
		t.Fatal("the PM could not look")
	}
	if held, err := s.HoldSeat(testContext, a.ID, decide.Token, "Pim"); err != nil || held {
		t.Fatalf("held a seat busy with the PM's look: %v %v", held, err)
	}
	s.ReleaseProjectClaim(testContext, p.ID, look.Token)
	if held, err := s.HoldSeat(testContext, a.ID, decide.Token, "Pim"); err != nil || !held {
		t.Fatalf("could not hold a free seat: %v %v", held, err)
	}
	if _, _, ok, _ = s.ClaimPM(testContext, p.ID, anyone); ok {
		t.Fatal("the PM looked while deciding held its seat")
	}
}

// A restart clears the PM's claim onto a fresh attempt, so a look from
// before it records nothing, unless its turn may still run, when the claim
// stays and no other look starts.
func TestARestartClearsThePMsClaimUnlessItsTurnMayStillRun(t *testing.T) {
	s, p, _ := pmWriter(t)
	c, _, _, _ := s.ClaimPM(testContext, p.ID, anyone)
	late := FencedProject(testContext, p.ID, c.Token)
	if err := s.RecoverClaims(testContext, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyPM(late, p.ID, PMAnswer{Note: "late"}); !errors.Is(err, ErrStale) {
		t.Fatalf("a look from before the restart recorded: %v", err)
	}
	snap, _ := s.Snapshot(testContext)
	if got := projectIn(snap, p.ID); len(got.Claims) != 0 || !got.PMDue {
		t.Fatalf("the list should still be due a look: %+v", got)
	}
	again, _, ok, _ := s.ClaimPM(testContext, p.ID, anyone)
	if !ok || again.Token == c.Token {
		t.Fatalf("the look should be claimed afresh: %+v", again)
	}
	if err := s.RecoverClaims(testContext, map[string]string{again.Token: "still running"}); err != nil {
		t.Fatal(err)
	}
	snap, _ = s.Snapshot(testContext)
	if got := projectIn(snap, p.ID); len(got.Claims) != 1 || got.Claims[0].Held != "still running" {
		t.Fatalf("a held look %+v", got.Claims)
	}
	if _, _, ok, _ = s.ClaimPM(testContext, p.ID, anyone); ok {
		t.Fatal("another look started beside one that may still run")
	}
	if got := claimed(t, s); len(got) != 0 {
		t.Fatalf("Pim's seat was given other work: %v", got)
	}
}

// A step no seat is free for, or that admit refuses, stays unclaimed and
// counts as no failure.
func TestAStepWithNoSlotStaysUnclaimed(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	queueAll(t, s, p, "A")
	out, err := s.Schedule(testContext, func(Role) string { return WaitEngineCap })
	if err != nil || len(out) != 0 {
		t.Fatalf("claimed without a slot: %+v %v", out, err)
	}
	snap, _ := s.Snapshot(testContext)
	if a := snap.Tasks[0]; a.Status != TaskQueued || a.Failures != 0 {
		t.Fatalf("A %+v", a)
	}
}

// Seats are numbered as the owner names them, and a seat can go while
// another holds its roles; a team needs an implementer, however many.
func TestSeatsAreAddedAndRemovedByName(t *testing.T) {
	p := Templates["draft"]
	for _, want := range []string{"Writer #2", "Writer #3"} {
		added, err := p.AddSeat("writer")
		if err != nil || added.Name != want {
			t.Fatalf("added %q %v", added.Name, err)
		}
	}
	if names := seatNames(p); !slices.Equal(names, []string{"Writer", "Writer #2", "Writer #3", "Reviewer"}) {
		t.Fatalf("seats %v", names)
	}
	if added, _ := p.AddSeat("Writer #3"); added.Name != "Writer #4" {
		t.Fatalf("a copy of a copy is numbered from the first: %q", added.Name)
	}
	if err := p.RemoveSeat("Writer #2"); err != nil || p.Validate() != nil {
		t.Fatalf("removing a copy: %v", err)
	}
	if _, err := p.AddSeat("Nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("added a copy of no seat: %v", err)
	}
	if p.Unseat(RoleImplementer); slices.ContainsFunc(p.Roles, func(r Role) bool { return r.Holds(RoleImplementer) }) {
		t.Fatalf("unseating a kind leaves no seat holding it: %v", seatNames(p))
	}
	if Templates["draft"].Roles[0].Name != "Writer" || len(Templates["draft"].Roles) != 2 {
		t.Fatal("a test changed the shared template")
	}
}

func seatNames(p Playbook) []string {
	var out []string
	for _, r := range p.Roles {
		out = append(out, r.Name)
	}
	return out
}

// seated gives p's team the seats roles, with the draft template's reviewer
// unless one of them reviews, and cap tasks under way at once, or the
// default for 0.
func seated(t *testing.T, s *Service, p Project, cap int, roles ...Role) Project {
	t.Helper()
	playbook := *p.Playbook
	playbook.Roles, playbook.MaxActive = roles, cap
	if !slices.ContainsFunc(roles, func(r Role) bool { return r.Holds(RoleReviewer) }) {
		playbook.Roles = append(playbook.Roles, p.Playbook.Roles[1])
	}
	p, err := s.SetPlaybook(testContext, p.ID, playbook)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// waiting is what a task's next step waits for, as the record has it.
func waiting(t *testing.T, s *Service, id string) *Wait {
	t.Helper()
	snap, _ := s.Snapshot(testContext)
	task, _ := snap.FindTask(id)
	return task.Waiting
}

func lucius(kinds ...string) Role {
	return Role{Name: "Lucius", Kinds: kinds, Engine: "claude", Member: "lucius"}
}

// A member seated in two projects is one person: they take the projects'
// steps one after another, the one asked for first first, whichever project
// comes first, and the other says it waits for them and what they are on.
func TestAMemberSeatedInTwoProjectsWorksOneStepAtATime(t *testing.T) {
	s, _ := fixture(t)
	first := seated(t, s, newProject(t, s), 0, lucius(RoleImplementer))
	second := seated(t, s, newProject(t, s), 0, lucius(RoleImplementer))
	second, err := s.SetProjectTitle(testContext, second.ID, "Notes")
	if err != nil {
		t.Fatal(err)
	}
	at := s.now()
	s.now = func() time.Time { return at.Add(time.Minute) }
	b := queueAll(t, s, second, "B")[0]
	s.now = func() time.Time { return at.Add(2 * time.Minute) }
	a := queueAll(t, s, first, "A")[0]
	if got := claimed(t, s); !slices.Equal(got, []string{"B: writing by Lucius"}) {
		t.Fatalf("Lucius should take the task asked for first, and only it: %v", got)
	}
	snap, _ := s.Snapshot(testContext)
	onB, _ := snap.FindTask(b.ID)
	if w := waiting(t, s, a.ID); w == nil || *w != (Wait{Kind: WaitMember, Seat: "Lucius", Member: "lucius", On: onB.Ref, Objective: onB.Objective, Project: second.Title}) || onB.Ref == "" {
		t.Fatalf("A should wait for Lucius, busy on %s: %+v", onB.Ref, w)
	}
	if got := claimed(t, s); len(got) != 0 {
		t.Fatalf("Lucius was given a second step: %v", got)
	}
	finish(t, s, b.ID, TaskWaiting)
	if got := claimed(t, s); !slices.Equal(got, []string{"A: writing by Lucius"}) {
		t.Fatalf("once B waits on the owner: %v", got)
	}
	if w := waiting(t, s, a.ID); w != nil {
		t.Fatalf("a claimed step still says it waits: %+v", w)
	}
	// A started task's next step waits for them too, and a restart forgets
	// what it waited for.
	finish(t, s, b.ID, TaskWriting)
	if got := claimed(t, s); len(got) != 0 {
		t.Fatalf("B's next round beside A's: %v", got)
	}
	if w := waiting(t, s, b.ID); w == nil || w.Seat != "Lucius" || w.On == "" {
		t.Fatalf("B's round should wait for Lucius: %+v", w)
	}
	if err := s.RecoverClaims(testContext, nil); err != nil {
		t.Fatal(err)
	}
	if w := waiting(t, s, b.ID); w != nil {
		t.Fatalf("a restart kept what B waited for: %+v", w)
	}
}

// A member holding two roles in a project, in a seat for each, is one
// person: a task's check and the next task's writing wait for each other,
// within the project's cap.
func TestAMemberHoldingTwoRolesDoesOneAtATime(t *testing.T) {
	s, _ := fixture(t)
	reviewer := lucius(RoleReviewer)
	reviewer.Name = "Lucius #2"
	p := seated(t, s, newProject(t, s), 2, lucius(RoleImplementer), reviewer)
	tasks := queueAll(t, s, p, "A", "B")
	if got := claimed(t, s); !slices.Equal(got, []string{"A: writing by Lucius"}) {
		t.Fatalf("first look: %v", got)
	}
	finish(t, s, tasks[0].ID, TaskReviewing)
	if got := claimed(t, s); !slices.Equal(got, []string{"A: reviewing by Lucius #2"}) {
		t.Fatalf("Lucius should check A, and not write B beside it: %v", got)
	}
	if w := waiting(t, s, tasks[1].ID); w == nil || *w != (Wait{Kind: WaitMember, Seat: "Lucius #2", Member: "lucius", On: p.TaskRef(tasks[0].Number), Objective: "A"}) {
		t.Fatalf("B should wait for Lucius, reviewing: %+v", w)
	}
}

// Several people in one role work several tasks at once, up to the
// project's cap: seats filled from one member are two people, and a
// template's own seat is a person of its own. Past the cap, the next task
// says it waits for the cap.
func TestSeveralPeopleInOneRoleWorkWithinTheCap(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	claudius := Role{Name: "Claudius", Kinds: []string{RoleImplementer}, Engine: "claude", Member: "claudius"}
	claudius2 := claudius
	claudius2.Name = "Claudius #2"
	p = seated(t, s, p, 3, p.Playbook.Roles[0], claudius, claudius2)
	tasks := queueAll(t, s, p, "A", "B", "C", "D")
	if got := claimed(t, s); !slices.Equal(got, []string{"A: writing by Writer", "B: writing by Claudius", "C: writing by Claudius #2"}) {
		t.Fatalf("three people should write three tasks: %v", got)
	}
	if w := waiting(t, s, tasks[3].ID); w == nil || *w != (Wait{Kind: WaitProjectCap, Active: 3, Cap: 3}) {
		t.Fatalf("D should wait for the cap: %+v", w)
	}
}

// A step whose person is free but whose turn admit refuses says what holds
// it back: an engine's safety cap, or the owner's own use.
func TestAStepSaysWhatHoldsItsTurnBack(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	a := queueAll(t, s, p, "A")[0]
	for why, want := range map[string]Wait{
		WaitEngineCap: {Kind: WaitEngineCap, Engine: p.Playbook.Roles[0].Engine},
		WaitOwner:     {Kind: WaitOwner},
	} {
		if out, _ := s.Schedule(testContext, func(Role) string { return why }); len(out) != 0 {
			t.Fatalf("claimed past admit: %+v", out)
		}
		if w := waiting(t, s, a.ID); w == nil || *w != want {
			t.Fatalf("A should wait for %s: %+v", why, w)
		}
	}
}

// A person keeping a project's list is busy with it, as the step waiting
// for them says.
func TestAStepWaitsForAPersonKeepingTheList(t *testing.T) {
	s, p, a := pmWriter(t)
	if _, _, ok, _ := s.ClaimPM(testContext, p.ID, anyone); !ok {
		t.Fatal("the PM could not look")
	}
	claimed(t, s)
	if w := waiting(t, s, a.ID); w == nil || *w != (Wait{Kind: WaitMember, Seat: "Pim", List: p.Title}) {
		t.Fatalf("A should wait for Pim, busy with the list: %+v", w)
	}
}

// A member's seats in two projects are one person to every claim: the PM
// deciding a task in one project holds them, so the assistant's question
// to them in the other project waits until the decision ends.
func TestAMemberHeldInOneProjectIsBusyInAnother(t *testing.T) {
	s, _ := fixture(t)
	pim := Role{Name: "Pim", Kinds: []string{RolePM}, Engine: "claude", Member: "pim"}
	first := newProject(t, s)
	first = seated(t, s, first, 0, first.Playbook.Roles[0], pim)
	second := newProject(t, s)
	second = seated(t, s, second, 0, second.Playbook.Roles[0], pim)
	a := queueAll(t, s, first, "A")[0]
	claimed(t, s)
	finish(t, s, a.ID, TaskDeciding)
	out, _ := s.Schedule(testContext, anyone)
	if len(out) != 1 {
		t.Fatalf("deciding %+v", out)
	}
	if held, err := s.HoldSeat(testContext, a.ID, out[0].Claim.Token, "Pim"); err != nil || !held {
		t.Fatalf("could not hold Pim's seat: %v %v", held, err)
	}
	if _, _, ok, _ := s.ClaimPMQuestion(testContext, second.ID, anyone); ok {
		t.Fatal("Pim answered in one project while deciding in another")
	}
	s.ReleaseClaim(testContext, a.ID, out[0].Claim.Token)
	if _, _, ok, _ := s.ClaimPMQuestion(testContext, second.ID, anyone); !ok {
		t.Fatal("Pim stayed busy once the decision ended")
	}
}
