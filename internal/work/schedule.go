package work

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/shhac/lib-agent-harness/session"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/diagnostics"
)

// jobs are the claimed steps running now, each in a goroutine of its own.
type jobs struct {
	mu      sync.Mutex
	running map[string]job
	wg      sync.WaitGroup
}

type job struct {
	task   string
	cancel context.CancelFunc
}

// hasTask reports a step of the task running now.
func (j *jobs) hasTask(taskID string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, r := range j.running {
		if r.task == taskID {
			return true
		}
	}
	return false
}

// cancelTask stops every step of a task running now.
func (j *jobs) cancelTask(taskID string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, r := range j.running {
		if r.task == taskID {
			r.cancel()
		}
	}
}

// pass is one look at the work: the owner's answers and the PM are dealt
// with here, in order, and every step that can start now is claimed and
// started, each in a goroutine of its own. It returns what it started, so a
// caller can wait for those steps to end; with waited, a step that panics
// hands its panic to whoever waits for it.
func (lp *Loop) pass(ctx context.Context, waited bool) (bool, []<-chan any, error) {
	snap, err := lp.Core.Snapshot(ctx)
	if err != nil {
		return false, nil, err
	}
	if snap.Paused {
		return false, nil, nil
	}
	// No new role turn is claimed while a chat message waits or is answered.
	lp.noteChat(chatPending(snap))
	if err := lp.settleDeliveries(ctx, snap); err != nil {
		return false, nil, err
	}
	if progressed, err := lp.settleAnswers(ctx, snap); progressed || err != nil {
		return progressed, nil, err
	}
	started, err := lp.answerMessages(ctx, snap, waited)
	if err != nil {
		return len(started) > 0, started, err
	}
	// A pass that set the PM looking ends there, as one that settled an
	// answer does; the next pass follows at once.
	looking, progressed, err := lp.managePM(ctx, snap, waited)
	started = append(started, looking...)
	if progressed || len(looking) > 0 || err != nil {
		return true, started, err
	}
	if err := lp.releaseUsageHolds(ctx, snap); err != nil {
		return len(started) > 0, started, err
	}
	taken := &slots{lp: lp}
	claimed, err := lp.Core.Schedule(ctx, taken.admit)
	if err != nil {
		taken.giveBack()
		return len(started) > 0, started, err
	}
	for _, s := range claimed {
		started = append(started, lp.launch(ctx, s, waited))
	}
	return len(started) > 0, started, nil
}

// launch runs a claimed step in a goroutine of its own, under a context of
// its own that stopping the task cancels. Everything the step records is
// fenced by its claim, which is cleared when it ends, freeing its seat. The
// channel returned closes when the step ends; with waited, a panic is sent
// on it rather than raised in the goroutine, and the claim stays, as it
// would were the daemon to stop there.
func (lp *Loop) launch(ctx context.Context, s core.Scheduled, waited bool) <-chan any {
	id, token := s.Task.ID, s.Claim.Token
	return lp.run(ctx, claimed{task: id, project: s.Task.ProjectID, token: token, seat: s.Seat}, waited,
		func(ctx context.Context) error { return lp.step(core.Fenced(ctx, id, token), s) },
		func(ctx context.Context) error { return lp.Core.ReleaseClaim(ctx, id, token) })
}

// claimed is a claim a goroutine runs: a task's, or, with no task, the
// project's own, and the seat that took it, if any.
type claimed struct {
	task, project, token string
	seat                 core.Role
}

// run runs a claimed step in a goroutine, as launch describes, and clears
// its claim with release once it ends.
func (lp *Loop) run(ctx context.Context, c claimed, waited bool, step, release func(context.Context) error) <-chan any {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan any, 1)
	token := c.token
	lp.jobs.mu.Lock()
	if lp.jobs.running == nil {
		lp.jobs.running = map[string]job{}
	}
	lp.jobs.running[token] = job{task: c.task, cancel: cancel}
	lp.jobs.mu.Unlock()
	lp.jobs.wg.Add(1)
	go func() {
		defer lp.jobs.wg.Done()
		defer close(done)
		if waited {
			defer func() {
				if r := recover(); r != nil {
					done <- r
				}
			}()
		}
		if c.seat.Name != "" {
			ctx = context.WithValue(ctx, slotKey{}, c.seat.Engine)
		}
		err := step(ctx)
		cancel()
		lp.jobs.mu.Lock()
		delete(lp.jobs.running, token)
		lp.jobs.mu.Unlock()
		if c.seat.Name != "" {
			lp.free(c.seat.Engine)
		}
		event := diagnostics.Event{Component: "daemon", Stage: "task_loop", ProjectID: c.project}
		if releaseErr := release(context.WithoutCancel(ctx)); releaseErr != nil {
			lp.Diagnostics.Failure(event, releaseErr)
		}
		switch {
		case errors.Is(err, core.ErrStale):
			// A stopped or superseded turn's result is dropped, and only
			// diagnostics hear of it.
			event.Stage = "task_stale"
			lp.Diagnostics.Failure(event, err)
		case err != nil && ctx.Err() == nil:
			lp.Diagnostics.Failure(event, err)
		}
		lp.Nudge()
	}()
	return done
}

// step runs one claimed step of a task, as the task stands now.
func (lp *Loop) step(ctx context.Context, s core.Scheduled) error {
	snap, err := lp.Core.Snapshot(ctx)
	if err != nil {
		return err
	}
	t, ok := findTask(snap, "", s.Task.ID)
	if !ok || t.Finished() {
		return nil
	}
	p, ok := findProject(snap, t.ProjectID)
	if !ok {
		return core.ErrNotFound
	}
	if s.Claim.Step == core.StepMessage {
		return lp.answerMessage(ctx, p, t, s)
	}
	// The task moved on since the step was claimed.
	if t.Status != s.Claim.Step {
		return nil
	}
	m, err := lp.mediumFor(ctx, p, taskPlaybook(p, t))
	if err != nil {
		return lp.roleFailed(ctx, t, "The workspace", err)
	}
	// A check runs beside the task's other checks and never moves the task;
	// direction that arrived meanwhile waits for them to end.
	if s.Claim.Shared {
		if len(t.Revisions) == 0 || t.DirectionPending > 0 || t.Revisions[len(t.Revisions)-1].N != s.Claim.Revision {
			return nil
		}
		return lp.check(ctx, p, t, m, s.Seat)
	}
	if sent, err := lp.backToWriter(ctx, t); sent {
		return err
	}
	switch t.Status {
	case core.TaskResearching:
		return lp.researchTask(ctx, p, t, m, s.Seat)
	case core.TaskDesigning:
		return lp.design(ctx, p, t, m, s.Seat)
	case core.TaskWriting:
		return lp.write(ctx, p, t, m, s.Seat)
	case core.TaskReviewing:
		return lp.review(ctx, p, t)
	case core.TaskDeciding:
		return lp.decide(ctx, p, t)
	case core.TaskLanding:
		return lp.land(ctx, p, t, m)
	}
	return nil
}

// seatWait is how long a step waits before looking again for a seat that
// was busy with other work.
const seatWait = 30 * time.Second

// holdSeat has the claimed step running with ctx hold seat too, for a turn
// no seat took the step for, such as the PM's choice while a task is
// decided. It says whether it could: not while the seat is busy elsewhere.
// Outside a claimed step there is nothing to hold it by.
func (lp *Loop) holdSeat(ctx context.Context, taskID, seat string) (bool, error) {
	claimTask, token, ok := core.FenceOf(ctx)
	if !ok || claimTask != taskID {
		return true, nil
	}
	return lp.Core.HoldSeat(ctx, taskID, token, seat)
}

// waitForSeat has a task look again shortly for a seat busy with other
// work. Waiting for a seat is no failure.
func (lp *Loop) waitForSeat(ctx context.Context, t core.Task, seat string) error {
	_, err := lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.RetryAt, t.HeldFor = time.Now().Add(seatWait), ""
		t.Detail = fmt.Sprintf("Waiting for %s, who is busy with other work", seat)
		return "", nil
	})
	return err
}

// launchDir is where a claimed turn records its launch, named by its token.
func (lp *Loop) launchDir(token string) string {
	return filepath.Join(lp.launchRoot(), strings.ReplaceAll(token, "/", "-"))
}

func (lp *Loop) launchRoot() string {
	return filepath.Join(lp.Core.StateDirectory(), "roles", "launch")
}

// reclaimTurns makes sure no turn a stopped daemon started is still running,
// before anything is scheduled: each launch recorded is ended where the
// harness can prove it is the one it launched. It returns the claims whose
// turns could not be confirmed ended, with why: their tasks, or for the PM's
// look their project, are held rather than have the step run again beside
// them. running is whether any launch, claimed or not, could not be
// confirmed ended, so what such a turn may still be using must stay.
func (lp *Loop) reclaimTurns(ctx context.Context, snap core.Snapshot) (held map[string]string, running bool) {
	held = map[string]string{}
	entries, err := os.ReadDir(lp.launchRoot())
	if err != nil {
		return held, false
	}
	byDir := map[string]string{}
	named := func(claims []core.Claim) {
		for _, c := range claims {
			byDir[filepath.Base(lp.launchDir(c.Token))] = c.Token
		}
	}
	for _, t := range snap.Tasks {
		named(t.Claims)
	}
	for _, p := range snap.Projects {
		named(p.Claims)
	}
	reclaim := lp.reclaim
	if reclaim == nil {
		reclaim = session.Reclaim
	}
	for _, e := range entries {
		dir := filepath.Join(lp.launchRoot(), e.Name())
		_, err := reclaim(ctx, dir)
		if errors.Is(err, session.ErrUnreclaimed) || errors.Is(err, session.ErrUncertainLaunch) {
			lp.Diagnostics.Failure(diagnostics.Event{Component: "daemon", Stage: "task_reclaim"}, err)
			if token, ok := byDir[e.Name()]; ok {
				held[token] = err.Error()
			}
			running = true
			continue
		}
		if err != nil {
			lp.Diagnostics.Failure(diagnostics.Event{Component: "daemon", Stage: "task_reclaim"}, err)
		}
		os.RemoveAll(dir)
	}
	return held, running
}

// heldTask reports a task one of whose turns may still be running.
func heldTask(t core.Task, held map[string]string) bool {
	return slices.ContainsFunc(t.Claims, func(c core.Claim) bool { _, ok := held[c.Token]; return ok || c.Held != "" })
}
