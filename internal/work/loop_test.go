package work

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/quota"
	"github.com/shhac/crew-assistant/internal/roles"
	"github.com/shhac/lib-agent-harness/session"
)

// scriptedRunner plays each role from a script: writers write files into
// their working directory, reviewers answer with the next scripted verdict.
type scriptedRunner struct {
	mu       sync.Mutex
	reviews  []string
	writes   int
	fail     []error
	seen     []roles.Spec
	onWriter func(dir string)
	// writerText replaces the writer's reply when set.
	writerText string
	// writerReplies replace the writer's reply, one turn each, before
	// writerText does.
	writerReplies []string
	// plans answer researcher turns in order; after them, a plan with nothing
	// unclear and nothing to wait for.
	plans []string
	// designs answer designer turns in order; after them, plain design input.
	designs []string
	// pm answers the PM's looks at the to-do list in order; after them, an
	// answer that changes nothing.
	pm []string
}

const (
	plainPlan   = `{"summary": "Do the task as asked.", "exists": [], "changes": ["the change"], "out_of_scope": [], "questions": [], "depends_on": []}`
	plainDesign = `{"input": "Keep it plain.", "escalate": null}`
)

func (r *scriptedRunner) Run(_ context.Context, spec roles.Spec) (roles.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, spec)
	if len(r.fail) > 0 {
		err := r.fail[0]
		r.fail = r.fail[1:]
		if err != nil {
			return roles.Result{}, err
		}
	}
	if !spec.Write && strings.Contains(spec.Prompt, "Plan this task before anything is written") {
		reply := plainPlan
		if len(r.plans) > 0 {
			reply, r.plans = r.plans[0], r.plans[1:]
		}
		return roles.Result{Text: reply}, nil
	}
	if !spec.Write && strings.Contains(spec.Prompt, "asks for your design input") {
		reply := plainDesign
		if len(r.designs) > 0 {
			reply, r.designs = r.designs[0], r.designs[1:]
		}
		return roles.Result{Text: reply}, nil
	}
	if !spec.Write && strings.Contains(spec.Prompt, "You keep the to-do list") {
		reply := `{"order": [], "depends": [], "note": "", "questions": []}`
		if len(r.pm) > 0 {
			reply, r.pm = r.pm[0], r.pm[1:]
		}
		return roles.Result{Text: reply}, nil
	}
	if spec.Write {
		r.writes++
		if r.onWriter != nil {
			r.onWriter(spec.WorkDir)
		}
		body := "Draft " + string(rune('0'+r.writes))
		if err := os.WriteFile(filepath.Join(spec.WorkDir, "note.md"), []byte(body), 0600); err != nil {
			return roles.Result{}, err
		}
		text := "Wrote the note."
		if r.writerText != "" {
			text = r.writerText
		}
		if len(r.writerReplies) > 0 {
			text, r.writerReplies = r.writerReplies[0], r.writerReplies[1:]
		}
		return roles.Result{Text: text, Session: []byte(`{"engine":"claude","id":"writer"}`)}, nil
	}
	if len(r.reviews) == 0 {
		return roles.Result{}, errors.New("no scripted review left")
	}
	reply := r.reviews[0]
	r.reviews = r.reviews[1:]
	return roles.Result{Text: reply}, nil
}

const (
	revise = `{"outcome":"revise","summary":"Too formal.","findings":[{"criterion":"Warm tone","note":"Soften the opening"}],"question":""}`
	pass   = "Looks good.\n```json\n{\"outcome\":\"pass\",\"summary\":\"Meets the brief.\",\"findings\":[],\"question\":\"\"}\n```"
	ask    = `{"outcome":"question","summary":"Unclear audience.","findings":[],"question":"Is this for the whole team or one person?"}`
)

// testLoop is a loop over a fresh, private store.
func testLoop(t *testing.T) *Loop {
	t.Helper()
	cfg := config.Default()
	s, err := core.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return New(core.NewService(s, cfg), func() config.Config { return cfg }, false)
}

func loopApp(t *testing.T, runner *scriptedRunner, deliverTo string) (*Loop, core.Project, core.Task) {
	t.Helper()
	a := testLoop(t)
	a.runner = runner
	// No real account is ever read from a test; an empty reading is unknown.
	a.meter = &quota.Meter{Inspect: func(context.Context, session.Options) (session.Inspection, error) { return session.Inspection{}, nil }}
	ctx := context.Background()
	p, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Thanks", Template: "draft", Brief: core.BriefInput{Goal: "Thank the team", Criteria: []string{"Warm tone"}}})
	if err != nil {
		t.Fatal(err)
	}
	if deliverTo != "" {
		playbook := *p.Playbook
		playbook.DeliverTo = deliverTo
		if p, err = a.Core.SetPlaybook(ctx, p.ID, playbook); err != nil {
			t.Fatal(err)
		}
	}
	task, err := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Thank-you note"})
	if err != nil {
		t.Fatal(err)
	}
	return a, p, task
}

// settle runs loop steps until the loop has nothing left to do.
func settle(t *testing.T, a *Loop) core.Task {
	t.Helper()
	for i := 0; i < 50; i++ {
		progressed, err := a.loopStep(context.Background(), false)
		if err != nil {
			t.Fatal(err)
		}
		if !progressed {
			snap, _ := a.Core.Snapshot(context.Background())
			return snap.Tasks[0]
		}
	}
	t.Fatal("the loop did not settle")
	return core.Task{}
}

func openDecision(t *testing.T, a *Loop, task core.Task) core.Decision {
	t.Helper()
	snap, _ := a.Core.Snapshot(context.Background())
	d, ok := findDecision(snap, task.DecisionID)
	if !ok || d.Status != core.DecisionOpen {
		t.Fatalf("no open decision for %+v", task)
	}
	return d
}

func TestLoopRevisesUntilReviewersPassThenDeliversOnApproval(t *testing.T) {
	runner := &scriptedRunner{reviews: []string{revise, pass}}
	dest := t.TempDir()
	a, p, _ := loopApp(t, runner, dest)
	task := settle(t, a)
	if task.Status != core.TaskWaiting || len(task.Revisions) != 2 || task.Round != 2 {
		t.Fatalf("task %+v", task)
	}
	d := openDecision(t, a, task)
	if d.Kind != core.DecisionDelivery || d.Choices[0] != choiceApprove || !strings.Contains(d.Context, dest) {
		t.Fatalf("delivery decision %+v", d)
	}
	// The second writer turn resumed the first and was told what to change.
	writer := runner.seen[2]
	if !writer.Write || string(writer.Resume) != `{"engine":"claude","id":"writer"}` || !strings.Contains(writer.Prompt, "Soften the opening") {
		t.Fatalf("revision turn %+v", writer)
	}
	// Reviewers read a copy, never the workspace, and cannot write.
	reviewer := runner.seen[1]
	if reviewer.Write || reviewer.WorkDir == writer.WorkDir || reviewer.Engine != "codex" {
		t.Fatalf("reviewer spec %+v", reviewer)
	}
	if _, err := a.Core.ChooseDecision(context.Background(), d.ID, choiceApprove); err != nil {
		t.Fatal(err)
	}
	task = settle(t, a)
	if task.Status != core.TaskDelivered || task.DeliveredTo == "" {
		t.Fatalf("not delivered: %+v", task)
	}
	raw, err := os.ReadFile(filepath.Join(task.DeliveredTo, "note.md"))
	if err != nil || string(raw) != "Draft 2" {
		t.Fatalf("delivered %q %v", raw, err)
	}
	if filepath.Dir(task.DeliveredTo) != dest || p.ID == "" {
		t.Fatal("delivered outside the chosen folder")
	}
}

func TestOwnerChangesQuestionsAndRoundLimits(t *testing.T) {
	runner := &scriptedRunner{reviews: []string{ask, revise, revise, revise, pass}}
	a, _, _ := loopApp(t, runner, "")
	ctx := context.Background()
	task := settle(t, a)
	d := openDecision(t, a, task)
	if d.Kind != core.DecisionQuestion || !strings.Contains(d.Context, "whole team") {
		t.Fatalf("question %+v", d)
	}
	a.Core.AnswerDecision(ctx, d.ID, "The whole team")
	task = settle(t, a)
	// Round 2 answered the question; rounds 2 and 3 were revised; the limit
	// of three rounds then comes to the owner rather than a fourth attempt.
	d = openDecision(t, a, task)
	if d.Kind != core.DecisionEscalation || task.Round != 3 {
		t.Fatalf("expected an escalation at the round limit, got %+v / %+v", d, task)
	}
	if !strings.Contains(runner.seen[2].Prompt, "The whole team") {
		t.Fatal("the owner's answer did not reach the writer")
	}
	a.Core.ChooseDecision(ctx, d.ID, choiceAnotherRound)
	task = settle(t, a)
	d = openDecision(t, a, task)
	if d.Kind != core.DecisionEscalation || task.Round != 4 {
		t.Fatalf("another round should end at another escalation, got %+v", task)
	}
	a.Core.AnswerDecision(ctx, d.ID, "Make it shorter")
	task = settle(t, a)
	d = openDecision(t, a, task)
	if d.Kind != core.DecisionDelivery || !strings.Contains(runner.seen[len(runner.seen)-2].Prompt, "Make it shorter") {
		t.Fatalf("free-text direction did not produce a new round: %+v", task)
	}
	a.Core.ChooseDecision(ctx, d.ID, choiceApprove)
	if task = settle(t, a); task.Status != core.TaskDelivered || task.DeliveredTo != "" {
		t.Fatalf("approval without a folder should just mark it delivered: %+v", task)
	}
}

// The owner's own words are direction, even when they spell a choice: typing
// "approve" in answer to what should change must not land the change, and
// typing "stop" in answer to a question must not stop the request.
func TestTypedAnswersAreDirectionNotChoices(t *testing.T) {
	runner := &scriptedRunner{reviews: []string{pass, ask, pass}}
	a, _, _ := loopApp(t, runner, "")
	ctx := context.Background()
	task := settle(t, a)
	d := openDecision(t, a, task)
	if d.Kind != core.DecisionDelivery {
		t.Fatalf("delivery %+v", d)
	}
	if d, _ = a.Core.AnswerDecision(ctx, d.ID, "approve"); d.Disposition != core.DispositionCustom {
		t.Fatalf("typed words were recorded as a choice: %+v", d)
	}
	task = settle(t, a)
	d = openDecision(t, a, task)
	if task.Status != core.TaskWaiting || task.Round != 2 || d.Kind != core.DecisionQuestion {
		t.Fatalf("a typed approve should go back for a round, got %+v", task)
	}
	if !strings.Contains(runner.seen[2].Prompt, "approve") {
		t.Fatal("the owner's words did not reach the writer")
	}
	a.Core.AnswerDecision(ctx, d.ID, "Stop")
	if task = settle(t, a); task.Status == core.TaskStopped || !strings.Contains(strings.Join(task.Direction, "\n"), "): Stop") {
		t.Fatalf("a typed stop should be an answer to the question, got %+v", task)
	}
	if _, err := a.Core.ChooseDecision(ctx, openDecision(t, a, task).ID, "Ship it"); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("a choice the decision does not offer was accepted: %v", err)
	}
}

func TestFailuresRetryQuietlyThenAskOnce(t *testing.T) {
	boom := errors.New("provider unavailable")
	runner := &scriptedRunner{fail: []error{boom}, reviews: []string{pass}}
	a, _, _ := loopApp(t, runner, "")
	task := settle(t, a)
	if task.Status != core.TaskWriting || task.Failures != 1 || task.RetryAt.IsZero() {
		t.Fatalf("a first failure should wait and retry: %+v", task)
	}
	a.Core.UpdateTask(context.Background(), task.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.RetryAt = time.Time{}
		return "", nil
	})
	if task = settle(t, a); task.Status != core.TaskWaiting || openDecision(t, a, task).Kind != core.DecisionDelivery {
		t.Fatalf("the retry should have carried on: %+v", task)
	}

	permanent := &session.CapabilityError{Engine: "codex", Code: session.CapabilitySandboxUnavailable, Phase: session.BeforeLaunch}
	runner = &scriptedRunner{fail: []error{permanent}, reviews: []string{pass}}
	a, _, _ = loopApp(t, runner, "")
	task = settle(t, a)
	d := openDecision(t, a, task)
	if d.Kind != core.DecisionFailure || task.ResumeStatus != core.TaskWriting {
		t.Fatalf("a sandbox problem should reach the owner at once: %+v %+v", d, task)
	}
	a.Core.ChooseDecision(context.Background(), d.ID, choiceTryAgain)
	if task = settle(t, a); openDecision(t, a, task).Kind != core.DecisionDelivery {
		t.Fatalf("try again should resume the failed step: %+v", task)
	}
}

func TestAWriterTurnStartsFromTheLastRevision(t *testing.T) {
	runner := &scriptedRunner{reviews: []string{revise, pass}}
	runner.onWriter = func(dir string) {
		if _, err := os.Stat(filepath.Join(dir, "stray.txt")); err == nil {
			panic("leftovers from an interrupted turn reached the writer")
		}
	}
	a, _, _ := loopApp(t, runner, "")
	// Something an interrupted turn left in the workspace.
	snap, _ := a.Core.Snapshot(context.Background())
	workspace := filepath.Join(snap.Projects[0].ScratchDirectory, "workspace")
	os.MkdirAll(workspace, 0700)
	os.WriteFile(filepath.Join(workspace, "stray.txt"), []byte("junk"), 0600)
	task := settle(t, a)
	if task.Status != core.TaskWaiting {
		t.Fatalf("task %+v", task)
	}
	for _, r := range task.Revisions {
		for _, f := range r.Files {
			if f == "stray.txt" {
				t.Fatal("a stray file became part of a draft")
			}
		}
	}
}

func TestBriefChangeRechecksBeforeDelivery(t *testing.T) {
	runner := &scriptedRunner{reviews: []string{pass, pass}}
	a, p, _ := loopApp(t, runner, "")
	ctx := context.Background()
	task := settle(t, a)
	d := openDecision(t, a, task)
	if _, err := a.Core.UpdateBrief(ctx, p.ID, core.BriefInput{Goal: "Thank the team for the launch", Criteria: []string{"Warm tone", "Mentions the launch"}}); err != nil {
		t.Fatal(err)
	}
	a.Core.ChooseDecision(ctx, d.ID, choiceChanges)
	task = settle(t, a)
	if openDecision(t, a, task).Kind != core.DecisionDelivery || len(task.Revisions) != 2 || task.Revisions[1].BriefVersion != 2 {
		t.Fatalf("the new draft should answer brief 2: %+v", task)
	}
	for _, v := range task.Verdicts {
		if v.Revision == 2 && v.BriefVersion != 2 {
			t.Fatal("a verdict was judged against an older brief")
		}
	}
}

func TestPausedAndNoDispatchDoNothing(t *testing.T) {
	runner := &scriptedRunner{reviews: []string{pass}}
	a, _, _ := loopApp(t, runner, "")
	if progressed, _ := a.loopStep(context.Background(), true); progressed || len(runner.seen) != 0 {
		t.Fatal("no-dispatch boot ran a role")
	}
	a.Core.SetPaused(context.Background(), true)
	if progressed, _ := a.loopStep(context.Background(), false); progressed || len(runner.seen) != 0 {
		t.Fatal("paused loop ran a role")
	}
}

func TestParseVerdictRejectsUnusableReviews(t *testing.T) {
	for _, bad := range []string{"", "no json here", `{"outcome":"maybe","summary":"x"}`, `{"outcome":"revise","summary":"x","findings":[]}`, `{"outcome":"question","summary":"x","question":""}`, `{"outcome":"pass","summary":""}`} {
		if _, err := parseVerdict(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestNearlyUsedSubscriptionHoldsTheRoleWithoutFailing(t *testing.T) {
	runner := &scriptedRunner{reviews: []string{pass}}
	a, _, _ := loopApp(t, runner, "")
	used := 95.0
	resets := time.Now().Add(2 * time.Hour)
	observation := session.Observation{Quality: session.Measured, ObservedAt: time.Now()}
	a.meter = &quota.Meter{Inspect: func(_ context.Context, o session.Options) (session.Inspection, error) {
		if o.Engine != session.Codex {
			return session.Inspection{}, nil
		}
		return session.Inspection{Quota: session.QuotaSnapshot{Observation: observation, Complete: true, Windows: []session.QuotaWindow{{Observation: observation, ID: "codex/primary", Scope: "codex", UsedPercent: &used, ResetsAt: &resets}}}}, nil
	}}
	task := settle(t, a)
	if task.Status != core.TaskReviewing || task.Failures != 0 || !task.RetryAt.Equal(resets.UTC()) || !strings.Contains(task.Detail, "usage to reset") {
		t.Fatalf("expected the codex reviewer to wait for its window: %+v", task)
	}
	if len(runner.seen) != 1 || !runner.seen[0].Write {
		t.Fatalf("only the claude writer should have run: %d turns", len(runner.seen))
	}
}

// Work held by a usage limit goes again as soon as the limit no longer holds
// it, such as the owner raising it, without waiting for the usage to reset;
// a failure's own backoff is never cut short.
func TestRaisingAUsageLimitLetsHeldWorkGoAtOnce(t *testing.T) {
	runner := &scriptedRunner{reviews: []string{pass}}
	a, _, _ := loopApp(t, runner, "")
	used := 95.0
	resets := time.Now().Add(48 * time.Hour)
	observation := session.Observation{Quality: session.Measured, ObservedAt: time.Now()}
	a.meter = &quota.Meter{Inspect: func(_ context.Context, o session.Options) (session.Inspection, error) {
		if o.Engine != session.Codex {
			return session.Inspection{}, nil
		}
		return session.Inspection{Quota: session.QuotaSnapshot{Observation: observation, Complete: true, Windows: []session.QuotaWindow{{Observation: observation, ID: "codex/primary", Scope: "codex", UsedPercent: &used, ResetsAt: &resets}}}}, nil
	}}
	task := settle(t, a)
	if task.HeldFor != "codex" || !task.RetryAt.After(time.Now()) {
		t.Fatalf("the reviewer should be held for Codex: %+v", task)
	}
	if task = settle(t, a); task.HeldFor != "codex" {
		t.Fatal("work was let go while the limit still held it")
	}
	raised := a.Config()
	low := 2
	raised.Engines.Codex.UsageFloor = config.UsageFloor{FiveHourPercent: &low, WeekPercent: &low}
	a.Config = func() config.Config { return raised }
	if task = settle(t, a); task.Status != core.TaskWaiting || task.HeldFor != "" || len(task.Verdicts) == 0 {
		t.Fatalf("raising the limit should let the reviewer go now: %+v", task)
	}

	// A hold made before holds were marked is let go the same way.
	a.Core.UpdateTask(context.Background(), task.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.Status, t.RetryAt, t.Detail = core.TaskReviewing, resets, "Waiting for Codex usage to reset (codex primary is 95.0% consumed)"
		return "", nil
	})
	if task = settle(t, a); task.RetryAt.After(time.Now()) {
		t.Fatalf("an older hold wasn't let go: %+v", task)
	}

	// A failure's backoff isn't a usage hold.
	a.Core.UpdateTask(context.Background(), task.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.Status, t.RetryAt = core.TaskReviewing, time.Now().Add(time.Hour)
		return "", nil
	})
	if task = settle(t, a); !task.RetryAt.After(time.Now()) {
		t.Fatal("a failure's backoff was cut short")
	}
}

func TestStoppingATaskSticksEvenMidTurn(t *testing.T) {
	runner := &scriptedRunner{reviews: []string{pass}}
	var a *Loop
	var taskID, projectID string
	// The owner stops the task while the writer's turn is still running.
	runner.onWriter = func(string) {
		if _, err := a.StopTask(context.Background(), projectID, taskID); err != nil {
			panic(err)
		}
	}
	a, p, task := loopApp(t, runner, "")
	taskID, projectID = task.ID, p.ID
	task = settle(t, a)
	if task.Status != core.TaskStopped || len(task.Revisions) != 0 || len(runner.seen) != 1 {
		t.Fatalf("the finished turn restarted a stopped task: %+v", task)
	}

	runner = &scriptedRunner{reviews: []string{pass}}
	a, p, _ = loopApp(t, runner, "")
	task = settle(t, a)
	d := openDecision(t, a, task)
	if task, _ = a.StopTask(context.Background(), p.ID, task.ID); task.Status != core.TaskStopped {
		t.Fatalf("stop %+v", task)
	}
	snap, _ := a.Core.Snapshot(context.Background())
	if closed, _ := findDecision(snap, d.ID); closed.Status != "dismissed" {
		t.Fatalf("the waiting decision stayed open: %+v", closed)
	}
	if _, err := a.StopTask(context.Background(), p.ID, task.ID); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("stopping twice: %v", err)
	}
}

// Choosing a team wakes the loop, whoever chose it, so queued work that was
// waiting on a team starts now rather than at the next tick.
func TestChoosingATeamWakesTheLoop(t *testing.T) {
	a := testLoop(t)
	ctx := context.Background()
	p, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Notes", Brief: core.BriefInput{Goal: "Notes", Criteria: []string{"Short"}}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-a.loopWake:
	default:
	}
	if _, err = a.SetTeam(ctx, p.ID, TeamChoice{Template: "draft"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-a.loopWake:
	default:
		t.Fatal("setting a team did not wake the loop")
	}
	if _, err = a.SetTeam(ctx, p.ID, TeamChoice{Template: "nonsense"}); err == nil {
		t.Fatal("an unknown template was accepted")
	}
	select {
	case <-a.loopWake:
		t.Fatal("a refused team woke the loop")
	default:
	}
}

// Stop is the owner's to choose on every decision a task brings them, and it
// always ends the task for good.
func TestChoosingStopEndsTheTaskWhateverItWasWaitingOn(t *testing.T) {
	for _, kind := range []string{core.DecisionFailure, core.DecisionQuestion, core.DecisionDelivery, core.DecisionEscalation, core.DecisionUpdate} {
		for _, resume := range []string{"", core.TaskWriting} {
			runner := &scriptedRunner{reviews: []string{pass, pass, pass, pass}}
			a, _, task := loopApp(t, runner, "")
			ctx := context.Background()
			a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
				t.Status, t.ResumeStatus = core.TaskWaiting, resume
				return "", nil
			})
			d, err := a.Core.OpenTaskDecision(ctx, task.ID, kind, core.DecisionInput{Title: "Well?", Context: "It needs you.", Recommendation: "Go on", Choices: []string{choiceTryAgain, choiceStop}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := a.Core.ChooseDecision(ctx, d.ID, choiceStop); err != nil {
				t.Fatal(err)
			}
			if got := settle(t, a); got.Status != core.TaskStopped || runner.writes != 0 {
				t.Errorf("%s (resuming %q): %s after %d writes", kind, resume, got.Status, runner.writes)
			}
		}
	}
}

// Each engine answers to its own settings: a pause while Claude's usage
// can't be read doesn't hold Codex, floors of 0 don't read usage at all, and
// an API engine is never held.
func TestUsageSettingsBelongToTheirEngine(t *testing.T) {
	a := testLoop(t)
	reads := map[session.Engine]int{}
	a.meter = &quota.Meter{Inspect: func(_ context.Context, o session.Options) (session.Inspection, error) {
		reads[o.Engine]++
		return session.Inspection{}, nil
	}}
	cfg := config.Default()
	cfg.Engines.Claude.OnUnknownUsage = config.OnUnknownUsagePause
	off := 0
	cfg.Engines.Codex.UsageFloor = config.UsageFloor{FiveHourPercent: &off, WeekPercent: &off}
	a.Config = func() config.Config { return cfg }
	ctx := context.Background()
	if until, why := a.UsageWait(ctx, "claude"); !until.After(time.Now()) || !strings.Contains(why, "Claude usage can be checked") {
		t.Fatalf("claude should wait while its usage can't be read: %v %q", until, why)
	}
	if until, _ := a.UsageWait(ctx, "codex"); !until.IsZero() || reads[session.Codex] != 0 {
		t.Fatalf("codex with its floors off should run without reading usage: %v, %d reads", until, reads[session.Codex])
	}
	if until, _ := a.UsageWait(ctx, "openai-compatible"); !until.IsZero() {
		t.Fatal("an API engine was held")
	}
	cfg.Engines.Codex.UsageFloor = config.UsageFloor{}
	if until, _ := a.UsageWait(ctx, "codex"); !until.IsZero() || reads[session.Codex] != 1 {
		t.Fatalf("codex allows unknown usage by default: %v, %d reads", until, reads[session.Codex])
	}
}
