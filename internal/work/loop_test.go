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
}

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
	if spec.Write {
		r.writes++
		if r.onWriter != nil {
			r.onWriter(spec.WorkDir)
		}
		body := "Draft " + string(rune('0'+r.writes))
		if err := os.WriteFile(filepath.Join(spec.WorkDir, "note.md"), []byte(body), 0600); err != nil {
			return roles.Result{}, err
		}
		return roles.Result{Text: "Wrote the note.", Session: []byte(`{"engine":"claude","id":"writer"}`)}, nil
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
	if !ok || d.Status != "open" {
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
	if d.Kind != decisionDelivery || d.Choices[0] != choiceApprove || !strings.Contains(d.Context, dest) {
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
	if _, err := a.Core.ResolveDecision(context.Background(), d.ID, choiceApprove); err != nil {
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
	if d.Kind != decisionQuestion || !strings.Contains(d.Context, "whole team") {
		t.Fatalf("question %+v", d)
	}
	a.Core.ResolveDecision(ctx, d.ID, "The whole team")
	task = settle(t, a)
	// Round 2 answered the question; rounds 2 and 3 were revised; the limit
	// of three rounds then comes to the owner rather than a fourth attempt.
	d = openDecision(t, a, task)
	if d.Kind != decisionEscalation || task.Round != 3 {
		t.Fatalf("expected an escalation at the round limit, got %+v / %+v", d, task)
	}
	if !strings.Contains(runner.seen[2].Prompt, "The whole team") {
		t.Fatal("the owner's answer did not reach the writer")
	}
	a.Core.ResolveDecision(ctx, d.ID, choiceAnotherRound)
	task = settle(t, a)
	d = openDecision(t, a, task)
	if d.Kind != decisionEscalation || task.Round != 4 {
		t.Fatalf("another round should end at another escalation, got %+v", task)
	}
	a.Core.ResolveDecision(ctx, d.ID, "Make it shorter")
	task = settle(t, a)
	d = openDecision(t, a, task)
	if d.Kind != decisionDelivery || !strings.Contains(runner.seen[len(runner.seen)-2].Prompt, "Make it shorter") {
		t.Fatalf("free-text direction did not produce a new round: %+v", task)
	}
	a.Core.ResolveDecision(ctx, d.ID, choiceApprove)
	if task = settle(t, a); task.Status != core.TaskDelivered || task.DeliveredTo != "" {
		t.Fatalf("approval without a folder should just mark it delivered: %+v", task)
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
	if task = settle(t, a); task.Status != core.TaskWaiting || openDecision(t, a, task).Kind != decisionDelivery {
		t.Fatalf("the retry should have carried on: %+v", task)
	}

	permanent := &session.CapabilityError{Engine: "codex", Code: session.CapabilitySandboxUnavailable, Phase: session.BeforeLaunch}
	runner = &scriptedRunner{fail: []error{permanent}, reviews: []string{pass}}
	a, _, _ = loopApp(t, runner, "")
	task = settle(t, a)
	d := openDecision(t, a, task)
	if d.Kind != decisionFailure || task.ResumeStatus != core.TaskWriting {
		t.Fatalf("a sandbox problem should reach the owner at once: %+v %+v", d, task)
	}
	a.Core.ResolveDecision(context.Background(), d.ID, choiceTryAgain)
	if task = settle(t, a); openDecision(t, a, task).Kind != decisionDelivery {
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
	a.Core.ResolveDecision(ctx, d.ID, choiceChanges)
	task = settle(t, a)
	if openDecision(t, a, task).Kind != decisionDelivery || len(task.Revisions) != 2 || task.Revisions[1].BriefVersion != 2 {
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
