package work

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/diagnostics"
	"github.com/shhac/crew-assistant/internal/lifecycle"
	"github.com/shhac/crew-assistant/internal/quota"
	"github.com/shhac/crew-assistant/internal/roles"
	harness "github.com/shhac/lib-agent-harness"
)

type pauseFailureWriter struct{ failed chan struct{} }

func (w pauseFailureWriter) Write(p []byte) (int, error) {
	select {
	case w.failed <- struct{}{}:
	default:
	}
	return len(p), nil
}

func TestEnginePauseRunRetriesStartupReadBeforeDispatch(t *testing.T) {
	a, g := gatedLoop(t)
	ctx := context.Background()
	if err := a.Core.PauseEngine(ctx, "claude", nil); err != nil {
		t.Fatal(err)
	}
	// Corrupt only this synthetic store's document to make its first read fail.
	db, err := sql.Open("sqlite", filepath.Join(a.Core.StateDirectory(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var payload string
	if err := db.QueryRow("SELECT payload FROM state WHERE id=1").Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE state SET payload='invalid' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	failed := make(chan struct{}, 1)
	a.Diagnostics = diagnostics.New(pauseFailureWriter{failed})
	graceful, stop := context.WithCancel(ctx)
	defer stop()
	done := runLoop(a, lifecycle.Stop{Graceful: graceful, Force: ctx})
	waitFor(t, failed, "startup did not report the failed read")
	if _, err := db.Exec("UPDATE state SET payload=? WHERE id=1", payload); err != nil {
		t.Fatal(err)
	}
	a.Nudge()
	// The reloaded pause must hold dispatch even after recovery.
	select {
	case <-g.entered:
		t.Fatal("persisted pause was ignored")
	case <-done:
		t.Fatal("Run exited after the failed startup read")
	case <-time.After(100 * time.Millisecond):
	}
	if err := a.ResumeEngine(ctx, "claude"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, g.entered, "dispatch did not recover after startup read failure")
	stop()
	close(g.release)
	waitFor(t, done, "Run did not drain")
}

func TestEnginePauseStatusAndObserverDoNotLift(t *testing.T) {
	a, _, _ := loopApp(t, &scriptedRunner{}, "")
	ctx := context.Background()
	until := time.Now().Add(time.Hour)
	if err := a.PauseEngine(ctx, "claude", &until); err != nil {
		t.Fatal(err)
	}
	a.Now = func() time.Time { return until }
	if _, err := a.EngineStatus(ctx); err != nil {
		t.Fatal(err)
	}
	observed := make(chan struct{}, 1)
	a.Now = func() time.Time {
		select {
		case observed <- struct{}{}:
		default:
		}
		return until
	}
	graceful, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); a.Run(lifecycle.Stop{Graceful: graceful, Force: ctx}, true) }()
	waitFor(t, observed, "observer did not read pauses")
	stop()
	waitFor(t, done, "observer did not stop")
	snap, err := a.Core.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.EnginePauses) != 1 || snap.Activity[0].Kind != "engine.paused" {
		t.Fatal("read-only path lifted pause", snap.Activity)
	}
}

func TestEnginePauseHoldsOnlyItsEngineAndResumes(t *testing.T) {
	ctx := context.Background()
	runner := &scriptedRunner{reviews: []string{pass}}
	a, p, task := loopApp(t, runner, "")
	if err := a.PauseEngine(ctx, "claude", nil); err != nil {
		t.Fatal(err)
	}
	step(t, a)
	snap, _ := a.Core.Snapshot(ctx)
	got, _ := snap.FindTask(task.ID)
	if len(runner.seen) != 0 || got.Waiting == nil || got.Waiting.Kind != core.WaitEnginePaused {
		t.Fatal(got, runner.seen)
	}
	// Work with a Codex seat still starts while Claude waits.
	other, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Other", Template: "draft", Brief: core.BriefInput{Goal: "Other", Criteria: []string{"Clear"}}})
	if err != nil {
		t.Fatal(err)
	}
	book := *other.Playbook
	for i := range book.Roles {
		if slices.Contains(book.Roles[i].Kinds, core.RoleImplementer) {
			book.Roles[i].Engine = "codex"
		}
	}
	if _, err := a.Core.SetPlaybook(ctx, other.ID, book); err != nil {
		t.Fatal(err)
	}
	otherTask, err := a.Core.QueueTask(ctx, other.ID, core.TaskInput{Objective: "Other note"})
	if err != nil {
		t.Fatal(err)
	}
	step(t, a)
	snap, _ = a.Core.Snapshot(ctx)
	otherTask, _ = snap.FindTask(otherTask.ID)
	if len(otherTask.Revisions) != 1 {
		t.Fatal("other engine did not run", otherTask)
	}
	// A new loop reloads the durable pause before admission.
	fresh := &Loop{Core: a.Core, Config: a.Config}
	if err := fresh.refreshEnginePauses(ctx); err != nil {
		t.Fatal(err)
	}
	if why := fresh.try("claude", false); why != core.WaitEnginePaused {
		t.Fatal(why)
	}
	// Claimed usage holds also release on an early owner resume.
	if held, err := a.holdForUsage(ctx, got, p.Playbook.Roles[0]); !held || err != nil {
		t.Fatal(held, err)
	}
	if err := a.ResumeEngine(ctx, "claude"); err != nil {
		t.Fatal(err)
	}
	step(t, a)
	snap, _ = a.Core.Snapshot(ctx)
	got, _ = snap.FindTask(task.ID)
	if len(got.Revisions) != 1 || got.HeldFor != "" {
		t.Fatal("did not resume", got)
	}
}

func TestEnginePauseRunningTurnFinishes(t *testing.T) {
	ctx := context.Background()
	started, release := make(chan struct{}), make(chan struct{})
	runner := &scriptedRunner{reviews: []string{pass}, onWriter: func(string) { close(started); <-release }}
	a, _, task := loopApp(t, runner, "")
	_, done, err := a.pass(ctx, true)
	if err != nil || len(done) != 1 {
		t.Fatal(err, len(done))
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("not started")
	}
	if err := a.PauseEngine(ctx, "claude", nil); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case p := <-done[0]:
		if p != nil {
			t.Fatal(p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("running turn blocked")
	}
	snap, _ := a.Core.Snapshot(ctx)
	got, _ := snap.FindTask(task.ID)
	if len(got.Revisions) != 1 {
		t.Fatal(got)
	}
}

func TestEnginePauseTimedLiftAndStatus(t *testing.T) {
	ctx := context.Background()
	a, p, task := loopApp(t, &scriptedRunner{}, "")
	until := time.Now().Add(time.Hour)
	if err := a.PauseEngine(ctx, "claude", &until); err != nil {
		t.Fatal(err)
	}
	states, err := a.EngineStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byEngine := map[string]EngineStatus{}
	for _, status := range states {
		byEngine[status.Engine] = status
	}
	if status := byEngine["claude"]; status.State != "paused" || status.Until == nil || !status.Until.Equal(until) {
		t.Fatal(status)
	}
	if status := byEngine["codex"]; status.State != "running" || status.Until != nil {
		t.Fatal(status)
	}

	if held, err := a.holdForUsage(ctx, task, p.Playbook.Roles[0]); !held || err != nil {
		t.Fatal(held, err)
	}
	// Advance the loop clock to the end, with no wall-clock sleeps.
	a.Now = func() time.Time { return until }
	step(t, a)
	snap, _ := a.Core.Snapshot(ctx)
	got, _ := snap.FindTask(task.ID)
	if len(got.Revisions) != 1 {
		t.Fatal(got)
	}
	found := false
	for _, activity := range snap.Activity {
		if activity.Kind == "engine.pause_ended" {
			found = true
		}
	}
	if !found {
		t.Fatal("no automatic lift activity")
	}
	a.meter = &quota.Meter{Inspect: func(_ context.Context, p harness.Provider) (harness.AccountReport, error) {
		return spentReading(p.Engine), nil
	}}
	states, err = a.EngineStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range states {
		byEngine[status.Engine] = status
	}
	if status := byEngine["claude"]; status.State != "held" || status.Until == nil || !strings.Contains(status.Reason, "usage") {
		t.Fatal(status)
	}

}

func TestEnginePauseQueuedWakeDoesNotHoldOtherEngines(t *testing.T) {
	now := time.Now().Add(24 * time.Hour)
	until := now.Add(time.Hour)
	snap := core.Snapshot{EnginePauses: map[string]core.EnginePause{"claude": {Until: &until}}, ChatTurns: []core.ChatTurn{{Origin: core.OriginWake, Status: "queued"}}}
	if chatPending(snap, "claude", now) {
		t.Fatal("held wake blocks other engines")
	}
	if !chatPending(snap, "claude", until) {
		t.Fatal("ended pause still hides wake")
	}
	snap.ChatTurns = append(snap.ChatTurns, core.ChatTurn{Status: "queued"})
	if !chatPending(snap, "claude", now) {
		t.Fatal("owner chat not protected")
	}
}

func TestEnginePauseClaimedStepCanFinishItsLaterTurns(t *testing.T) {
	runner := &scriptedRunner{reviews: []string{pass, pass}}
	a, _, _ := loopApp(t, runner, "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if !a.admit("claude") {
		t.Fatal("could not take slot")
	}
	defer a.free("claude")
	claimed := context.WithValue(ctx, slotKey{}, "claude")
	spec := roles.Spec{Engine: "claude"}
	if _, err := a.runRole(claimed, spec); err != nil {
		t.Fatal(err)
	}
	if err := a.PauseEngine(ctx, "claude", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.runRole(claimed, spec); err != nil {
		t.Fatal("claimed step did not finish", err)
	}
	if len(runner.seen) != 2 {
		t.Fatal(runner.seen)
	}
	if wait, detail := a.usageWait(ctx, core.Role{Engine: "claude"}); wait.IsZero() || !strings.Contains(detail, "you paused Claude") {
		t.Fatal(wait, detail)
	}
}

func TestEnginePausePMAndPMChatWait(t *testing.T) {
	ctx := context.Background()
	runner := &scriptedRunner{}
	a, p, _, _ := pmTeam(t, runner)
	if err := a.PauseEngine(ctx, "claude", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SendPMChat(ctx, p.ID, "hello", "Hello PM"); err != nil {
		t.Fatal(err)
	}
	step(t, a)
	if len(runner.seen) != 0 {
		t.Fatal("paused PM ran", runner.seen)
	}
	messages, _ := a.Core.PMChat(ctx, p.ID)
	if messages[0].Status != "waiting" {
		t.Fatal(messages)
	}
	if _, err := a.AskPM(ctx, p.ID, "What next?"); err == nil || !strings.Contains(err.Error(), "you paused Claude") {
		t.Fatal(err)
	}
}

func TestEnginePauseFailedWriteLeavesAdmissionUnchanged(t *testing.T) {
	a, _, _ := loopApp(t, &scriptedRunner{}, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.PauseEngine(ctx, "claude", nil); err == nil {
		t.Fatal("cancelled write succeeded")
	}
	if why := a.try("claude", false); why != "" {
		t.Fatal("failed write changed admission", why)
	}
	a.free("claude")
	snap, _ := a.Core.Snapshot(context.Background())
	if len(snap.EnginePauses) != 0 {
		t.Fatal(snap.EnginePauses)
	}
}

func TestEnginePauseAndGlobalPauseAreIndependent(t *testing.T) {
	ctx := context.Background()
	runner := &scriptedRunner{}
	a, _, task := loopApp(t, runner, "")
	if err := a.Core.SetPaused(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := a.PauseEngine(ctx, "claude", nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Core.SetPaused(ctx, false); err != nil {
		t.Fatal(err)
	}
	step(t, a)
	if len(runner.seen) != 0 {
		t.Fatal("global resume lifted engine pause")
	}
	if err := a.Core.SetPaused(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := a.ResumeEngine(ctx, "claude"); err != nil {
		t.Fatal(err)
	}
	step(t, a)
	if len(runner.seen) != 0 {
		t.Fatal("engine resume lifted global pause")
	}
	if err := a.Core.SetPaused(ctx, false); err != nil {
		t.Fatal(err)
	}
	step(t, a)
	snap, _ := a.Core.Snapshot(ctx)
	got, _ := snap.FindTask(task.ID)
	if len(got.Revisions) != 1 {
		t.Fatal(got)
	}
}

func TestEnginePauseRunDispatchesDespiteResumeFailure(t *testing.T) {
	a, runner := gatedLoop(t)
	ctx := context.Background()
	badProject, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Unrecoverable handoff", Template: "draft", Brief: core.BriefInput{Goal: "Synthetic handoff"}})
	if err != nil {
		t.Fatal(err)
	}
	badTask, err := a.Core.QueueTask(ctx, badProject.ID, core.TaskInput{Objective: "Old handoff"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Core.UpdateTask(ctx, badTask.ID, func(task *core.Task, project *core.Project) (string, error) {
		task.Handoff = &core.Handoff{}
		project.ScratchDirectory = ""
		project.Paused = true
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.resume(ctx); err == nil {
		t.Fatal("fixture must fail recovery")
	}
	failed := make(chan struct{}, 1)
	a.Diagnostics = diagnostics.New(pauseFailureWriter{failed})
	graceful, stop := context.WithCancel(ctx)
	defer stop()
	release := runner.release
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	done := runLoop(a, lifecycle.Stop{Graceful: graceful, Force: ctx})
	waitFor(t, failed, "recovery failure was not reported")
	waitFor(t, runner.entered, "unrelated work did not dispatch after recovery failed")
	stop()
	close(release)
	waitFor(t, done, "Run did not drain")
}

func TestEnginePauseBeforeRunHoldsPMAndUsageChecks(t *testing.T) {
	ctx := context.Background()
	a, project, _, _ := pmTeam(t, &scriptedRunner{})
	if err := a.Core.PauseEngine(ctx, "claude", nil); err != nil {
		t.Fatal(err)
	}
	fresh := New(a.Core, a.Config, false)
	fresh.meter = a.meter
	if until, why := fresh.UsageWait(ctx, "claude"); until.IsZero() || !strings.Contains(why, "you paused Claude") {
		t.Fatal(until, why)
	}
	if _, err := fresh.AskPM(ctx, project.ID, "What next?"); err == nil || !strings.Contains(err.Error(), "you paused Claude") {
		t.Fatal(err)
	}
	if why := fresh.try("claude", true); why != core.WaitEnginePaused {
		t.Fatal("admission ignored persisted pause", why)
	}
	if why := fresh.try("codex", true); why != "" {
		t.Fatal("unpaused engine held", why)
	}
	fresh.free("codex")
}

func TestEnginePauseInitialReadFailureHoldsUsageAndRetries(t *testing.T) {
	a, _, _ := loopApp(t, &scriptedRunner{}, "")
	ctx := context.Background()
	if err := a.Core.PauseEngine(ctx, "claude", nil); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(a.Core.StateDirectory(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var payload string
	if err := db.QueryRow("SELECT payload FROM state WHERE id=1").Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE state SET payload='invalid' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	fresh := New(a.Core, a.Config, false)
	fresh.meter = a.meter
	if until, why := fresh.UsageWait(ctx, "claude"); until.IsZero() || !strings.Contains(why, "pause state can be checked") {
		t.Fatal(until, why)
	}
	if why := fresh.try("claude", true); why == "" {
		t.Fatal("unknown pause state admitted turn")
	}
	if _, err := db.Exec("UPDATE state SET payload=? WHERE id=1", payload); err != nil {
		t.Fatal(err)
	}
	// A successful write to one engine must not mark a partial cache loaded.
	if err := fresh.PauseEngine(ctx, "codex", nil); err != nil {
		t.Fatal(err)
	}
	if until, why := fresh.UsageWait(ctx, "claude"); until.IsZero() || !strings.Contains(why, "you paused Claude") {
		t.Fatal(until, why)
	}
}
