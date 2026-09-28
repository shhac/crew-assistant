package work

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shhac/lib-agent-harness/session"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/quota"
	"github.com/shhac/crew-assistant/internal/roles"
	harness "github.com/shhac/lib-agent-harness"
)

// parallelRunner plays a writing team whose turns can wait on one another,
// to show which run at once. It records every turn with its seat, and any
// seat seen at work on two turns at once.
type parallelRunner struct {
	mu      sync.Mutex
	running map[string]int
	twice   []string
	turns   []string
	// onTurn runs before each turn replies, outside the lock; an error fails
	// the turn.
	onTurn func(ctx context.Context, seat, objective string, write bool) error
}

// turnSeat is the seat a turn runs as.
func turnSeat(spec roles.Spec) string {
	if l, ok := spec.Observer.(*liveTurn); ok {
		return l.who.Seat
	}
	return ""
}

func (r *parallelRunner) Run(ctx context.Context, spec roles.Spec) (roles.Result, error) {
	seat, objective := turnSeat(spec), objectiveOf(spec.Prompt)
	r.mu.Lock()
	if r.running == nil {
		r.running = map[string]int{}
	}
	r.running[seat]++
	if r.running[seat] > 1 {
		r.twice = append(r.twice, seat)
	}
	r.turns = append(r.turns, seat+": "+objective)
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.running[seat]--
		r.mu.Unlock()
	}()
	if r.onTurn != nil {
		if err := r.onTurn(ctx, seat, objective, spec.Write); err != nil {
			return roles.Result{}, err
		}
	}
	if spec.Write {
		if err := os.WriteFile(filepath.Join(spec.WorkDir, "note.md"), []byte(objective), 0o600); err != nil {
			return roles.Result{}, err
		}
		return roles.Result{Text: "Wrote " + objective + ".", Session: sessionRef(objective, 1)}, nil
	}
	return roles.Result{Text: pass}, nil
}

func (r *parallelRunner) turnsOf(who string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, turn := range r.turns {
		if turn == who {
			n++
		}
	}
	return n
}

// parallelApp is a writing project, Writer on Claude and Reviewer on Codex,
// under a cap of maxActive tasks, or the default for 0.
func parallelApp(t *testing.T, runner roles.Runner, maxActive int) (*Loop, core.Project) {
	t.Helper()
	a := testLoop(t)
	a.runner = runner
	a.meter = &quota.Meter{Inspect: func(context.Context, harness.Provider) (harness.AccountReport, error) {
		return harness.AccountReport{}, nil
	}}
	ctx := context.Background()
	p, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Notes", Template: "draft", Brief: core.BriefInput{Goal: "Write notes"}})
	if err != nil {
		t.Fatal(err)
	}
	if maxActive > 0 {
		if p, err = a.SetParallel(ctx, p.ID, maxActive); err != nil {
			t.Fatal(err)
		}
	}
	return a, p
}

func queue(t *testing.T, a *Loop, p core.Project, objective string) core.Task {
	t.Helper()
	task, err := a.Core.QueueTask(context.Background(), p.ID, core.TaskInput{Objective: objective})
	if err != nil {
		t.Fatal(err)
	}
	return task
}

// within waits for ch, and says whether it came in time.
func within(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	case <-time.After(10 * time.Second):
		return false
	}
}

// While one task is checked, the implementer writes the next queued task,
// within the project's cap; each seat still works on one task at a time.
func TestTheImplementerWritesTheNextTaskWhileOneIsChecked(t *testing.T) {
	runner := &parallelRunner{}
	a, p := parallelApp(t, runner, 2)
	writingB := make(chan struct{})
	var once sync.Once
	overlapped := false
	runner.onTurn = func(_ context.Context, _, objective string, write bool) error {
		switch {
		case write && objective == "B":
			once.Do(func() { close(writingB) })
		case !write && objective == "A":
			// A's check waits to see B written beside it.
			overlapped = within(writingB)
		}
		return nil
	}
	first, second := queue(t, a, p, "A"), queue(t, a, p, "B")
	settle(t, a)
	if !overlapped {
		t.Fatal("B was not written while A was checked")
	}
	for _, id := range []string{first.ID, second.ID} {
		if task := taskByID(t, a, id); task.Status != core.TaskWaiting || len(task.Revisions) != 1 || len(task.Claims) != 0 {
			t.Fatalf("task %+v", task)
		}
	}
	if len(runner.twice) != 0 {
		t.Fatalf("a seat worked on two tasks at once: %v", runner.twice)
	}
}

// With the default cap of one and one implementer, the next task starts
// only once the one under way waits on the owner, as before.
func TestACapOfOneStartsTheNextTaskOnlyOnceTheFirstWaits(t *testing.T) {
	runner := &parallelRunner{}
	a, p := parallelApp(t, runner, 0)
	first, second := queue(t, a, p, "A"), queue(t, a, p, "B")
	var during string
	runner.onTurn = func(_ context.Context, _, objective string, write bool) error {
		if write && objective == "B" {
			during = taskByID(t, a, first.ID).Status
		}
		return nil
	}
	settle(t, a)
	if during != core.TaskWaiting {
		t.Fatalf("B was written while A was %s", during)
	}
	if b := taskByID(t, a, second.ID); b.Status != core.TaskWaiting {
		t.Fatalf("B %+v", b)
	}
}

// A reviewer and QA check the same draft at once, each in a copy of its
// own, when their engine allows two turns at once; the task moves on only
// once both verdicts are in.
func TestReviewerAndQACheckOneDraftAtOnce(t *testing.T) {
	a, runner, _, task := codeTask(t, pass, pass)
	two := 2
	cfg := config.Default()
	cfg.Engines.Codex.RoleRuns = &two
	a.Config = func() config.Config { return cfg }
	qaStarted := make(chan struct{})
	overlapped := false
	var during string
	a.runner = checkRunner{codeRunner: runner, onCheck: func(spec roles.Spec) {
		if !spec.Write {
			overlapped = within(qaStarted)
			return
		}
		close(qaStarted)
		// QA holds on until the reviewer's verdict is in: the task has not
		// moved on without QA's.
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if now := taskByID(t, a, task.ID); len(now.Verdicts) == 1 {
				during = now.Status
				return
			}
		}
	}}
	settle(t, a)
	if !overlapped || during != core.TaskReviewing {
		t.Fatalf("the reviewer and QA did not check at once (%v), or the task moved on with one verdict (%s)", overlapped, during)
	}
	if done := taskByID(t, a, task.ID); done.Status != core.TaskWaiting || len(done.Verdicts) != 2 {
		t.Fatalf("task %+v", done)
	}
}

// Role turns on one engine run no more at once than the owner allows,
// across every task: by default one each.
func TestTurnsOnOneEngineWaitForAFreeSlot(t *testing.T) {
	a, runner, _, task := codeTask(t, pass, pass)
	var mu sync.Mutex
	now, most := 0, 0
	a.runner = checkRunner{codeRunner: runner, onCheck: func(roles.Spec) {
		mu.Lock()
		now++
		most = max(most, now)
		mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		now--
		mu.Unlock()
	}}
	settle(t, a)
	if most != 1 {
		t.Fatalf("%d checks on one engine ran at once", most)
	}
	if done := taskByID(t, a, task.ID); done.Status != core.TaskWaiting || len(done.Verdicts) != 2 {
		t.Fatalf("task %+v", done)
	}
	if !a.admit("codex") || a.admit("codex") {
		t.Fatal("the default bound is one turn at once")
	}
	a.free("codex")
	if !a.admit("codex") {
		t.Fatal("a freed slot is not taken again")
	}
}

// No new role turn starts while the owner is chatting or composing, nor for
// a moment after; a turn already running goes on to the end.
func TestNoNewRoleTurnStartsWhileTheOwnerIsBusy(t *testing.T) {
	runner := &parallelRunner{}
	a, p := parallelApp(t, runner, 0)
	var chatting func()
	runner.onTurn = func(_ context.Context, _, _ string, write bool) error {
		// The owner starts chatting while the writer works.
		if write && chatting == nil {
			chatting = a.Interactive()
		}
		return nil
	}
	task := queue(t, a, p, "A")
	settle(t, a)
	if got := taskByID(t, a, task.ID); len(got.Revisions) != 1 || got.Status != core.TaskReviewing {
		t.Fatalf("the running turn should finish and nothing more start: %+v", got)
	}
	if n := runner.turnsOf("Reviewer: A"); n != 0 {
		t.Fatal("a check started while the owner was chatting")
	}
	chatting()
	if progressed, _ := a.loopStep(context.Background(), false); progressed || runner.turnsOf("Reviewer: A") != 0 {
		t.Fatal("a check started straight after the chat, before the grace ended")
	}
	a.gate.mu.Lock()
	a.gate.quietFrom = time.Time{}
	a.gate.mu.Unlock()
	a.NoteInteractive()
	if progressed, _ := a.loopStep(context.Background(), false); progressed {
		t.Fatal("a check started while a message waited to be answered")
	}
	a.gate.mu.Lock()
	a.gate.quietFrom = time.Time{}
	a.gate.mu.Unlock()
	if got := settle(t, a); got.Status != core.TaskWaiting || runner.turnsOf("Reviewer: A") != 1 {
		t.Fatalf("the check did not run once the owner was done: %+v", got)
	}
}

// quiet lets the moment after the owner's use pass at once.
func quiet(a *Loop) {
	a.gate.mu.Lock()
	a.gate.quietFrom = time.Time{}
	a.gate.mu.Unlock()
}

// A chat message holds new role turns back for as long as it waits in the
// queue, however long that is, and while it is answered; only once it is
// gone, and a moment after, do they start.
func TestAQueuedChatHoldsRoleTurnsUntilItIsAnswered(t *testing.T) {
	runner := &parallelRunner{}
	a, p := parallelApp(t, runner, 0)
	ctx := context.Background()
	queue(t, a, p, "A")
	if _, err := a.Core.EnqueueChat(ctx, "c1", "How is it going?"); err != nil {
		t.Fatal(err)
	}
	// The owner holds the message while they edit it.
	if _, err := a.Core.HoldChat(ctx, "c1", "editing", time.Hour); err != nil {
		t.Fatal(err)
	}
	a.NoteInteractive()
	for range 2 {
		// Well past the moment's grace, the message still waits.
		quiet(a)
		if progressed, err := a.loopStep(ctx, false); progressed || err != nil || len(runner.turns) != 0 {
			t.Fatalf("a role turn started while a chat message waited: %v %v %v", progressed, err, runner.turns)
		}
	}
	if err := a.Core.ReleaseChatHold(ctx, "c1"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Core.StartNextChat(ctx); err != nil {
		t.Fatal(err)
	}
	quiet(a)
	if progressed, _ := a.loopStep(ctx, false); progressed || len(runner.turns) != 0 {
		t.Fatal("a role turn started while the message was answered")
	}
	if err := a.Core.FinishChat(ctx, "c1", "completed", "Going well.", ""); err != nil {
		t.Fatal(err)
	}
	if progressed, _ := a.loopStep(ctx, false); progressed || len(runner.turns) != 0 {
		t.Fatal("a role turn started straight after the answer, before the grace ended")
	}
	quiet(a)
	if got := settle(t, a); got.Status != core.TaskWaiting || runner.turnsOf("Writer: A") != 1 {
		t.Fatalf("the work did not go on once the chat was done: %+v", got)
	}
}

// A step claimed just before the owner starts chatting or composing starts
// its turn only once they are done: whether the owner is busy is checked as
// the turn starts, not only when its step was claimed.
func TestAClaimedStepWaitsForChatThatBeganAfterItWasClaimed(t *testing.T) {
	runner := &parallelRunner{}
	a, p := parallelApp(t, runner, 0)
	ctx := context.Background()
	queue(t, a, p, "A")
	taken := &slots{lp: a}
	claimed, err := a.Core.Schedule(ctx, taken.admit)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claimed %+v %v", claimed, err)
	}
	// The composer asks for a suggestion between the claim and the turn.
	composing := a.Interactive()
	ran := a.launch(ctx, claimed[0], true)
	time.Sleep(300 * time.Millisecond)
	if n := runner.turnsOf("Writer: A"); n != 0 {
		t.Fatal("the turn started while the owner was composing")
	}
	composing()
	quiet(a)
	a.wake()
	select {
	case r := <-ran:
		if r != nil {
			t.Fatal(r)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the turn never started once the owner was done")
	}
	if n := runner.turnsOf("Writer: A"); n != 1 {
		t.Fatalf("%d turns", n)
	}
}

// lateToolRunner is a writer that leaves a note through its tools, loses
// its claim while the task stays where it was, as when the daemon restarts
// under it, and then tries to leave another.
type lateToolRunner struct {
	revoke      func() error
	early, late error
}

func (r *lateToolRunner) Run(ctx context.Context, spec roles.Spec) (roles.Result, error) {
	note := func(text string) error {
		result, err := spec.Handler.CallTool(context.Background(), session.ToolCall{Name: "add_note", Arguments: []byte(`{"text": "` + text + `"}`)})
		if err == nil && result.IsError {
			err = errors.New(result.Content)
		}
		return err
	}
	r.early = note("While working")
	if err := r.revoke(); err != nil {
		return roles.Result{}, err
	}
	r.late = note("After the claim went")
	return roles.Result{}, errors.New("the turn was cut off")
}

// A role's tools act for its turn only while the turn holds its claim: once
// the claim is gone, as when the daemon restarted under a turn still
// running, a tool call the turn makes changes nothing, though the task is
// still where it was.
func TestALateToolCallRecordsNothing(t *testing.T) {
	runner := &lateToolRunner{}
	a, p := parallelApp(t, runner, 0)
	ctx := context.Background()
	task := queue(t, a, p, "A")
	runner.revoke = func() error { return a.Core.RecoverClaims(ctx, nil) }
	if _, err := a.loopStep(ctx, false); err != nil {
		t.Fatal(err)
	}
	if runner.early != nil || runner.late == nil {
		t.Fatalf("early %v, late %v", runner.early, runner.late)
	}
	got := taskByID(t, a, task.ID)
	if len(got.Notes) != 1 || got.Notes[0].Text != "While working" || got.Status != core.TaskWriting || got.Failures != 0 {
		t.Fatalf("task %+v, notes %+v", got, got.Notes)
	}
}

// Stopping a task cancels only its own turn: nothing the turn returns is
// recorded, its seat is freed for other work, and the other task goes on.
func TestStoppingOneTaskLeavesTheOthersWorking(t *testing.T) {
	runner := &parallelRunner{}
	a, p := parallelApp(t, runner, 2)
	first, second := queue(t, a, p, "A"), queue(t, a, p, "B")
	writingB := make(chan struct{})
	runner.onTurn = func(ctx context.Context, _, objective string, write bool) error {
		switch {
		case write && objective == "B":
			close(writingB)
			<-ctx.Done()
			return ctx.Err()
		case !write && objective == "A":
			if !within(writingB) {
				return errors.New("B never started")
			}
			if _, err := a.StopTask(context.Background(), p.ID, second.ID); err != nil {
				return err
			}
		}
		return nil
	}
	settle(t, a)
	stopped := taskByID(t, a, second.ID)
	if stopped.Status != core.TaskStopped || len(stopped.Revisions) != 0 || stopped.Failures != 0 || len(stopped.Claims) != 0 || stopped.Detail != "You stopped it" {
		t.Fatalf("the stopped task %+v", stopped)
	}
	if done := taskByID(t, a, first.ID); done.Status != core.TaskWaiting || len(done.Verdicts) != 1 {
		t.Fatalf("the other task %+v", done)
	}
	// The writer's seat is free for the next task.
	runner.onTurn = nil
	third := queue(t, a, p, "C")
	settle(t, a)
	if c := taskByID(t, a, third.ID); c.Status != core.TaskWaiting || len(c.Revisions) != 1 {
		t.Fatalf("C %+v", c)
	}
}

// A restart resumes each task at its step without running a step twice: a
// turn the old daemon left that can't be confirmed ended keeps its task
// held, and the rest are claimed afresh and run once.
func TestARestartResumesEachClaimOnceAndHoldsAnUnconfirmedOne(t *testing.T) {
	runner := &parallelRunner{}
	a, p := parallelApp(t, runner, 0)
	ctx := context.Background()
	if _, err := a.AddSeat(ctx, p.ID, "Writer"); err != nil {
		t.Fatal(err)
	}
	first, second := queue(t, a, p, "A"), queue(t, a, p, "B")
	// The old daemon claimed both writers' turns, then stopped.
	claimed, err := a.Core.Schedule(ctx, func(core.Role) bool { return true })
	if err != nil || len(claimed) != 2 {
		t.Fatalf("claimed %+v %v", claimed, err)
	}
	tokens := map[string]string{}
	for _, c := range claimed {
		tokens[c.Task.ID] = c.Claim.Token
		if err := os.MkdirAll(a.launchDir(c.Claim.Token), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	before := taskByID(t, a, second.ID).Attempt
	again := New(a.Core, a.Config, false)
	again.runner, again.meter = runner, a.meter
	again.reclaim = func(_ context.Context, dir string) (session.Reclamation, error) {
		if dir == again.launchDir(tokens[first.ID]) {
			return session.Reclamation{Found: true}, session.ErrUnreclaimed
		}
		return session.Reclamation{Confirmed: true}, nil
	}
	if err = again.resume(ctx); err != nil {
		t.Fatal(err)
	}
	held := taskByID(t, again, first.ID)
	if len(held.Claims) != 1 || held.Claims[0].Held == "" || !strings.Contains(held.Detail, "Held") {
		t.Fatalf("the unconfirmed turn's task should stay held: %+v", held)
	}
	resumed := taskByID(t, again, second.ID)
	if len(resumed.Claims) != 0 || resumed.Attempt != before+1 || resumed.Failures != 0 {
		t.Fatalf("the other claim should be cleared onto a fresh attempt: %+v", resumed)
	}
	if _, err = os.Stat(again.launchDir(tokens[second.ID])); !os.IsNotExist(err) {
		t.Fatal("a confirmed launch was left behind")
	}
	if _, err = os.Stat(again.launchDir(tokens[first.ID])); err != nil {
		t.Fatal("an unconfirmed launch was removed")
	}
	settle(t, again)
	if n := runner.turnsOf("Writer: A") + runner.turnsOf("Writer #2: A"); n != 0 {
		t.Fatalf("the held task ran again: %d turns", n)
	}
	if n := runner.turnsOf("Writer: B") + runner.turnsOf("Writer #2: B"); n != 1 {
		t.Fatalf("B's round ran %d times", n)
	}
	if b := taskByID(t, again, second.ID); b.Status != core.TaskWaiting || len(b.Revisions) != 1 {
		t.Fatalf("B %+v", b)
	}
}

// seatRunner plays the team from a script and counts the turns one seat,
// which writes and keeps the list, has going at once.
type seatRunner struct {
	*scriptedRunner
	mu        sync.Mutex
	busy      int
	twice     bool
	looks     int
	onWriting func()
}

func (r *seatRunner) Run(ctx context.Context, spec roles.Spec) (roles.Result, error) {
	look := strings.Contains(spec.Prompt, "You keep the to-do list")
	if spec.Write || look {
		r.mu.Lock()
		r.busy++
		r.twice = r.twice || r.busy > 1
		if look {
			r.looks++
		}
		write := r.onWriting
		if spec.Write {
			r.onWriting = nil
		}
		r.mu.Unlock()
		defer func() {
			r.mu.Lock()
			r.busy--
			r.mu.Unlock()
		}()
		if spec.Write && write != nil {
			write()
		}
	}
	return r.scriptedRunner.Run(ctx, spec)
}

// A member who both keeps the list and implements does one at a time: the
// PM's look waits while its seat writes, and runs once the seat is free.
func TestThePMWaitsForItsSeatToFinishWriting(t *testing.T) {
	runner := &seatRunner{scriptedRunner: &scriptedRunner{reviews: []string{pass, pass}}}
	a, p, _ := loopApp(t, runner.scriptedRunner, "")
	a.runner = runner
	// Claude has room for two turns, so only the seat can hold the PM back.
	two := 2
	cfg := config.Default()
	cfg.Engines.Claude.RoleRuns = &two
	a.Config = func() config.Config { return cfg }
	ctx := context.Background()
	pim, err := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Pim", Kinds: []string{core.RoleImplementer, core.RolePM}, Engine: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if p, err = a.SetTeam(ctx, p.ID, TeamChoice{Template: "draft", Implementer: pim.ID, PM: pim.ID}); err != nil {
		t.Fatal(err)
	}
	if seat, _ := p.PMSeat(); !seat.Holds(core.RoleImplementer) {
		t.Fatalf("Pim should hold both in one seat: %+v", p.Playbook.Roles)
	}
	lookedWhileWriting := -1
	runner.onWriting = func() {
		// New work arrives while Pim writes, so the list is due a look.
		if _, err := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Second note"}); err != nil {
			t.Error(err)
			return
		}
		snap, _ := a.Core.Snapshot(ctx)
		looking, _, err := a.managePM(ctx, snap, true)
		if err != nil {
			t.Error(err)
		}
		lookedWhileWriting = len(looking)
	}
	settle(t, a)
	if lookedWhileWriting != 0 {
		t.Fatalf("the PM looked while its seat wrote: %d", lookedWhileWriting)
	}
	if runner.looks == 0 || runner.twice {
		t.Fatalf("looks %d, one seat on two turns at once: %v", runner.looks, runner.twice)
	}
	snap, _ := a.Core.Snapshot(ctx)
	if project, _ := findProject(snap, p.ID); project.PMDue || len(project.Claims) != 0 {
		t.Fatalf("project %+v", project)
	}
}

// A restart reclaims the PM's look a stopped daemon left: one confirmed
// ended runs again, once; one that can't be confirmed holds the project's
// list rather than look beside it.
func TestARestartRunsThePMsLookOnceOrHoldsIt(t *testing.T) {
	runner := &scriptedRunner{reviews: []string{pass, pass}}
	a, p, _, second := pmTeam(t, runner)
	ctx := context.Background()
	c, _, ok, err := a.Core.ClaimPM(ctx, p.ID, func(core.Role) bool { return true })
	if err != nil || !ok {
		t.Fatalf("claim %v %v", ok, err)
	}
	if err = os.MkdirAll(a.launchDir(c.Token), 0o700); err != nil {
		t.Fatal(err)
	}
	looks := func() int {
		n := 0
		for _, spec := range runner.seen {
			if strings.Contains(spec.Prompt, "You keep the to-do list") {
				n++
			}
		}
		return n
	}
	restarted := func(confirmed bool) *Loop {
		again := New(a.Core, a.Config, false)
		again.runner, again.meter = runner, a.meter
		again.reclaim = func(context.Context, string) (session.Reclamation, error) {
			if confirmed {
				return session.Reclamation{Confirmed: true}, nil
			}
			return session.Reclamation{Found: true}, session.ErrUnreclaimed
		}
		if err := again.resume(ctx); err != nil {
			t.Fatal(err)
		}
		return again
	}
	held := restarted(false)
	step(t, held)
	snap, _ := held.Core.Snapshot(ctx)
	if project, _ := findProject(snap, p.ID); looks() != 0 || len(project.Claims) != 1 || project.Claims[0].Held == "" {
		t.Fatalf("a look that may still run was run beside: %d looks, %+v", looks(), project.Claims)
	}
	resumed := restarted(true)
	settle(t, resumed)
	if looks() != 1 {
		t.Fatalf("the PM looked %d times", looks())
	}
	if task := taskByID(t, resumed, second.ID); task.Status == core.TaskTriage {
		t.Fatalf("the look's answer was not recorded: %+v", task)
	}
}

// askRunner plays the team from a script and runs onAsked while the PM
// answers the assistant.
type askRunner struct {
	*scriptedRunner
	onAsked func()
}

func (r *askRunner) Run(ctx context.Context, spec roles.Spec) (roles.Result, error) {
	if r.onAsked != nil && strings.Contains(spec.Prompt, "The owner's assistant asks you") {
		r.onAsked()
	}
	return r.scriptedRunner.Run(ctx, spec)
}

// The assistant's question to the PM takes the PM's seat and a turn on its
// engine as any of the PM's work does, so it never runs beside the PM's
// other work or past the bound; being the owner's own ask, in their chat,
// it never waits either, for them or for the chat: a busy PM is said to be
// busy.
func TestTheAssistantsQuestionTakesThePMsSeatAndATurn(t *testing.T) {
	scripted := &scriptedRunner{reviews: []string{pass, pass}}
	a, p, _, _ := pmTeam(t, scripted)
	runner := &askRunner{scriptedRunner: scripted}
	a.runner = runner
	ctx := context.Background()
	asks := func() int {
		n := 0
		for _, spec := range scripted.seen {
			if strings.Contains(spec.Prompt, "The owner's assistant asks you") {
				n++
			}
		}
		return n
	}
	// The PM is looking at the list: its seat is busy.
	look, _, ok, err := a.Core.ClaimPM(ctx, p.ID, func(core.Role) bool { return true })
	if err != nil || !ok {
		t.Fatalf("look %v %v", ok, err)
	}
	if _, err = a.AskPM(ctx, p.ID, "Why?"); !errors.Is(err, core.ErrConflict) || !strings.Contains(err.Error(), "busy") || asks() != 0 {
		t.Fatalf("asked a PM busy looking at the list: %v, %d", err, asks())
	}
	a.Core.ReleaseProjectClaim(ctx, p.ID, look.Token)
	// Its engine has no turn free.
	if !a.admit("claude") {
		t.Fatal("no slot to fill")
	}
	if _, err = a.AskPM(ctx, p.ID, "Why?"); !errors.Is(err, core.ErrConflict) || asks() != 0 {
		t.Fatalf("asked past the engine's bound: %v, %d", err, asks())
	}
	a.free("claude")
	// Asked in the owner's chat, it answers, holding the seat and the turn
	// while it does.
	chatting := a.Interactive()
	defer chatting()
	var seatFree, slotFree bool
	runner.onAsked = func() {
		_, _, seatFree, _ = a.Core.ClaimPM(ctx, p.ID, func(core.Role) bool { return true })
		if slotFree = a.take("claude", true); slotFree {
			a.free("claude")
		}
	}
	done := make(chan error, 1)
	go func() {
		_, err := a.AskPM(ctx, p.ID, "Why is the second note waiting?")
		done <- err
	}()
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the question waited on the owner's own chat")
	}
	if err != nil || asks() != 1 || seatFree || slotFree {
		t.Fatalf("answer %v, asks %d, seat free %v, slot free %v", err, asks(), seatFree, slotFree)
	}
	snap, _ := a.Core.Snapshot(ctx)
	if project, _ := findProject(snap, p.ID); len(project.Claims) != 0 {
		t.Fatalf("the question's claim was left: %+v", project.Claims)
	}
	if !a.take("claude", true) {
		t.Fatal("the question's turn was not given back")
	}
}

// A message to a reviewer busy checking another task waits for that seat,
// then is answered on the draft it was about.
func TestAMessageWaitsForItsSeatToBeFree(t *testing.T) {
	runner := &parallelRunner{}
	a, p := parallelApp(t, runner, 2)
	ctx := context.Background()
	first, second := queue(t, a, p, "A"), queue(t, a, p, "B")
	var sent core.TeamMessage
	var startedBeside int
	runner.onTurn = func(_ context.Context, seat, objective string, write bool) error {
		if write || objective != "B" || sent.ID != "" {
			return nil
		}
		// While the reviewer checks B, the owner asks it about A.
		var err error
		if sent, err = a.MessageTeam(ctx, p.ID, first.ID, seat, core.FromOwner, "Is A warm enough?"); err != nil {
			return err
		}
		snap, err := a.Core.Snapshot(ctx)
		if err != nil {
			return err
		}
		started, err := a.answerMessages(ctx, snap, true)
		startedBeside = len(started)
		return err
	}
	settle(t, a)
	if sent.ID == "" || startedBeside != 0 {
		t.Fatalf("the message was answered by a seat at work on another task: %d", startedBeside)
	}
	answered := taskByID(t, a, first.ID)
	if m := answered.Messages[0]; m.ID != sent.ID || m.Status != core.MessageAnswered || runner.turnsOf("Reviewer: A") != 2 {
		t.Fatalf("message %+v after %d checks of A", m, runner.turnsOf("Reviewer: A"))
	}
	if b := taskByID(t, a, second.ID); b.Status != core.TaskWaiting || len(b.Messages) != 0 {
		t.Fatalf("B %+v", b)
	}
}

// Changes land one at a time, each catching up first. When two tasks built
// side by side conflict, the conflict comes to the owner, with letting the
// implementer resolve it recommended: nothing is forced or resolved unasked.
func TestAConflictBetweenTasksBuiltSideBySideComesToTheOwner(t *testing.T) {
	source := ownerRepo(t)
	runner := &codeRunner{scriptedRunner: scriptedRunner{reviews: []string{pass, pass, pass, pass, pass, pass}}}
	a, _, _ := loopApp(t, &runner.scriptedRunner, "")
	a.runner = runner
	ctx := context.Background()
	p := codeProject(t, a, source)
	if _, err := a.SetParallel(ctx, p.ID, 2); err != nil {
		t.Fatal(err)
	}
	first := queue(t, a, p, "Add A")
	second := queue(t, a, p, "Add B")
	settle(t, a)
	first, second = taskByID(t, a, first.ID), taskByID(t, a, second.ID)
	if first.Status != core.TaskWaiting || second.Status != core.TaskWaiting || first.Base != second.Base {
		t.Fatalf("both should wait for approval, built from the same start: %+v\n%+v", first, second)
	}
	if _, err := a.Core.ChooseDecision(ctx, openDecision(t, a, first).ID, choiceApprove); err != nil {
		t.Fatal(err)
	}
	settle(t, a)
	if first = taskByID(t, a, first.ID); first.Status != core.TaskDelivered {
		t.Fatalf("A did not land: %+v", first)
	}
	second = taskByID(t, a, second.ID)
	d := openDecision(t, a, second)
	if !strings.Contains(d.Title, "“Add A”") || d.Recommendation != choiceResolve || d.Choices[0] != choiceResolve || len(second.Revisions) != 1 {
		t.Fatalf("the conflict should come to the owner, unresolved: %+v / %+v", d, second)
	}
	if _, err := a.Core.ChooseDecision(ctx, d.ID, choiceResolve); err != nil {
		t.Fatal(err)
	}
	settle(t, a)
	second = taskByID(t, a, second.ID)
	d = openDecision(t, a, second)
	if !d.Approves() || len(second.Revisions) != 2 || second.Base != first.Revisions[0].Ref {
		t.Fatalf("the implementer should resolve it and ask again: %+v / %+v", d, second)
	}
	resolving := 0
	for _, spec := range runner.seen {
		if spec.Write && strings.Contains(spec.Prompt, "conflict markers you must resolve: feature.go") {
			resolving++
		}
	}
	if resolving != 1 {
		t.Fatalf("the implementer should be given the conflict once it was the owner's choice: %d rounds", resolving)
	}
}

// Giving a role to someone else keeps as many seats for it as it had: the
// work it runs at once stays as the owner set it.
func TestChangingARoleKeepsItsSeats(t *testing.T) {
	a, p := parallelApp(t, &parallelRunner{}, 0)
	ctx := context.Background()
	claudius, _ := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Claudius", Kinds: []string{core.RoleImplementer}, Engine: "claude"})
	ada, _ := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Ada", Kinds: []string{core.RoleImplementer}, Engine: "claude"})
	if _, err := a.SetSeat(ctx, p.ID, core.RoleImplementer, claudius.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.AddSeat(ctx, p.ID, "Claudius"); err != nil {
		t.Fatal(err)
	}
	implementers := func(p core.Project) []string {
		var out []string
		for _, r := range p.Playbook.Roles {
			if r.Holds(core.RoleImplementer) {
				out = append(out, r.Name+"/"+r.Member)
			}
		}
		return out
	}
	p, err := a.SetSeat(ctx, p.ID, core.RoleImplementer, ada.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := implementers(p); !slices.Equal(got, []string{"Ada/" + ada.ID, "Ada #2/" + ada.ID}) {
		t.Fatalf("given to Ada: %v", got)
	}
	if p, err = a.SetSeat(ctx, p.ID, core.RoleImplementer, ""); err != nil {
		t.Fatal(err)
	}
	if got := implementers(p); !slices.Equal(got, []string{"Writer/", "Writer #2/"}) {
		t.Fatalf("given back to the template: %v", got)
	}
	if p.Playbook.ActiveCap() != 2 || len(p.Playbook.Roles) != 3 {
		t.Fatalf("team %+v", p.Playbook.Roles)
	}
}

// A conflict with a task that was built after this one, not beside it, is
// the implementer's to resolve, as it always was, whatever the project's
// cap: here B started only once A waited for approval.
func TestAConflictWithWorkBuiltAfterGoesToTheImplementer(t *testing.T) {
	source := ownerRepo(t)
	runner := &codeRunner{scriptedRunner: scriptedRunner{reviews: []string{pass, pass, pass, pass, pass, pass}}}
	a, _, _ := loopApp(t, &runner.scriptedRunner, "")
	a.runner = runner
	ctx := context.Background()
	p := codeProject(t, a, source)
	if _, err := a.SetParallel(ctx, p.ID, 2); err != nil {
		t.Fatal(err)
	}
	first := queue(t, a, p, "Add A")
	settle(t, a)
	second := queue(t, a, p, "Add B")
	settle(t, a)
	first, second = taskByID(t, a, first.ID), taskByID(t, a, second.ID)
	if first.Status != core.TaskWaiting || second.Status != core.TaskWaiting || first.BuiltBeside(second) {
		t.Fatalf("B should be built after A: %+v\n%+v", first, second)
	}
	if _, err := a.Core.ChooseDecision(ctx, openDecision(t, a, second).ID, choiceApprove); err != nil {
		t.Fatal(err)
	}
	settle(t, a)
	first = taskByID(t, a, first.ID)
	d := openDecision(t, a, first)
	if !d.Approves() || len(first.Revisions) != 2 {
		t.Fatalf("the implementer should have resolved it without asking: %+v / %+v", d, first)
	}
	resolved := slices.ContainsFunc(runner.seen, func(spec roles.Spec) bool {
		return spec.Write && strings.Contains(spec.Prompt, "conflict markers you must resolve: feature.go")
	})
	if !resolved {
		t.Fatal("the implementer was not given the conflict")
	}
}

// A task stopped while its change is being delivered, after the change went
// out but before that was recorded, is settled from where the change went:
// it is recorded as landed, not left stopped with its change delivered.
func TestAStopWhileLandingRecordsWhereTheChangeWent(t *testing.T) {
	a, _, p, task := codeTask(t, pass, pass)
	ctx := context.Background()
	settle(t, a)
	task = taskByID(t, a, task.ID)
	if task.Status != core.TaskWaiting {
		t.Fatalf("task %+v", task)
	}
	r := task.Revisions[len(task.Revisions)-1]
	// The landing has recorded its intent, as it does before delivering.
	if _, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.Status, t.DecisionID, t.Approved = core.TaskLanding, "", r.N
		t.Delivering = &core.Delivering{Revision: r.N}
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.StopTask(ctx, p.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	// The delivery, already under way, goes to its end.
	m, stopped := a.testMedium(t, p.ID, task.ID)
	branch, err := m.deliver(ctx, stopped, r)
	if err != nil {
		t.Fatal(err)
	}
	step(t, a)
	done := taskByID(t, a, task.ID)
	if done.Status != core.TaskDelivered || done.DeliveredTo != branch || done.Delivering != nil {
		t.Fatalf("the delivered change should be recorded: %+v", done)
	}
	// One stopped before its change went anywhere stays stopped.
	b, _, p2, other := codeTask(t, pass, pass)
	settle(t, b)
	other = taskByID(t, b, other.ID)
	n := other.Revisions[len(other.Revisions)-1].N
	b.Core.UpdateTask(ctx, other.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.Status, t.DecisionID, t.Delivering = core.TaskLanding, "", &core.Delivering{Revision: n}
		return "", nil
	})
	if _, err = b.StopTask(ctx, p2.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	step(t, b)
	if got := taskByID(t, b, other.ID); got.Status != core.TaskStopped || got.Delivering != nil {
		t.Fatalf("a change that never went out should leave the task stopped: %+v", got)
	}
}

// A seat that takes a task's next round carries on the conversation of its
// earlier rounds, whichever seat of the same member had them.
func TestAnotherSeatOfTheSameMemberCarriesOnTheTasksConversation(t *testing.T) {
	runner := &threadRunner{reviews: map[string][]string{"A": {revise, pass}}}
	a, p, _ := threadsApp(t, runner)
	ctx := context.Background()
	p, err := a.AddSeat(ctx, p.ID, "Ada")
	if err != nil {
		t.Fatal(err)
	}
	if p.Playbook.Roles[1].Name != "Ada #2" || p.Playbook.Roles[1].Member != p.Playbook.Roles[0].Member {
		t.Fatalf("seats %+v", p.Playbook.Roles)
	}
	task := queue(t, a, p, "A")
	// Round 1 by Ada, then Ada is busy elsewhere when round 2 comes.
	for taskByID(t, a, task.ID).Status != core.TaskReviewing {
		if _, err := a.loopStep(ctx, false); err != nil {
			t.Fatal(err)
		}
	}
	other := queue(t, a, p, "Elsewhere")
	if _, err = a.Core.UpdateTask(ctx, other.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.Status, t.Claims = core.TaskWaiting, []core.Claim{{Token: t.ID + "/9", Step: core.StepAdopt, Seat: "Ada"}}
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
	settle(t, a)
	writes := runner.writerTurns("A")
	if len(writes) != 2 || turnSeat(writes[0]) != "Ada" || turnSeat(writes[1]) != "Ada #2" {
		t.Fatalf("rounds by %v", writes)
	}
	if string(writes[1].Resume) != string(sessionRef("A", 1)) {
		t.Fatalf("Ada #2 should carry on round 1's conversation: %s", writes[1].Resume)
	}
}
