package work

import (
	"context"
	"errors"
	"fmt"
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
	harness "github.com/shhac/lib-agent-harness"
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
	// onDesigner runs in each designer turn before it answers, as its
	// session would; an error it returns fails the turn.
	designs    []string
	onDesigner func(spec roles.Spec) error
	// pm answers the PM's looks at the to-do list in order; after them, an
	// answer that sends on what is in triage and changes nothing else.
	pm []string
	// pmLand answers the PM's decisions to land in order; after them, land.
	// onPMLand runs before each, to change the world meanwhile.
	pmLand   []string
	onPMLand func()
	// route answers the PM's choices of where a task goes after its checks,
	// in order; after them, a reply that can't be read.
	route []string
	// escalate answers the PM's judgements of what remains at a round limit,
	// and ownerStep its judgements of a requirement the implementer can't
	// meet, each in order; after them, a reply that can't be read.
	escalate  []string
	ownerStep []string
	// pmOnPR answers the PM handed a pull request question, in order; after
	// them, an answer that does nothing.
	pmOnPR []string
}

// sendOn is the PM's plain answer to what waits in triage: every task sent
// on to the team, as the PM's prompt lists them.
func sendOn(prompt string) string {
	var out []string
	_, triage, ok := strings.Cut(prompt, "\nIn triage, oldest first:\n")
	for _, line := range strings.Split(triage, "\n") {
		if !ok || !strings.HasPrefix(line, "- ") {
			continue
		}
		id, _, _ := strings.Cut(strings.TrimPrefix(line, "- "), " ")
		out = append(out, fmt.Sprintf(`{"task": %q, "to": "research"}`, strings.TrimSuffix(id, ":")))
	}
	return "[" + strings.Join(out, ", ") + "]"
}

const (
	plainPlan   = `{"summary": "Do the task as asked.", "exists": [], "changes": ["the change"], "out_of_scope": [], "questions": [], "depends_on": []}`
	plainDesign = `{"input": "Keep it plain.", "escalate": null}`
)

// scripted is a kind of read-only turn the runner answers from a script:
// the prompts it is known by, its queue of replies and the reply once they
// run out. before runs first, and may fail the turn.
type scripted struct {
	markers  []string
	queue    *[]string
	fallback func(roles.Spec) string
	before   func(roles.Spec) error
}

func always(reply string) func(roles.Spec) string { return func(roles.Spec) string { return reply } }

// scripts are the turns the runner answers from a script, in the order it
// tries them.
func (r *scriptedRunner) scripts() []scripted {
	return []scripted{
		{markers: []string{"Plan this task before anything is written"}, queue: &r.plans, fallback: always(plainPlan)},
		{markers: []string{"asks for your design input"}, queue: &r.designs, fallback: always(plainDesign), before: r.onDesigner},
		{markers: []string{"Decide whether this change lands", "Decide whether this change opens", "Decide whether pull request"}, queue: &r.pmLand,
			fallback: always(`{"land": true, "reason": "it is signed off and nothing waits on it"}`),
			before: func(roles.Spec) error {
				if r.onPMLand != nil {
					r.onPMLand()
				}
				return nil
			}},
		{markers: []string{"handed you this about the pull request"}, queue: &r.pmOnPR, fallback: always(`{}`)},
		{markers: []string{"Decide where this task goes next"}, queue: &r.route, fallback: always("no choice")},
		{markers: []string{"Judge what remains at the round limit"}, queue: &r.escalate, fallback: always("no judgement")},
		{markers: []string{"Judge whether a requirement the implementer can't meet"}, queue: &r.ownerStep, fallback: always("no judgement")},
		{markers: []string{"You keep the to-do list"}, queue: &r.pm, fallback: func(spec roles.Spec) string {
			return fmt.Sprintf(`{"triage": %s, "order": [], "depends": [], "note": "", "questions": []}`, sendOn(spec.Prompt))
		}},
	}
}

// script is the scripted kind of turn spec is, if it is one.
func (r *scriptedRunner) script(spec roles.Spec) (scripted, bool) {
	if spec.Write {
		return scripted{}, false
	}
	for _, s := range r.scripts() {
		for _, marker := range s.markers {
			if strings.Contains(spec.Prompt, marker) {
				return s, true
			}
		}
	}
	return scripted{}, false
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
	if script, ok := r.script(spec); ok {
		if script.before != nil {
			if err := script.before(spec); err != nil {
				return roles.Result{}, err
			}
		}
		reply := script.fallback(spec)
		if queue := *script.queue; len(queue) > 0 {
			reply, *script.queue = queue[0], queue[1:]
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
	// A code team's reviewer and QA are both on Codex, and the scripted
	// runners answer checks from one list in team order: a safety cap of
	// one has them take turns, so each gets its own answer. A test that
	// runs them side by side sets a cap of its own.
	one := 1
	cfg.Engines.Codex.RoleRuns = &one
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
	a.meter = &quota.Meter{Inspect: func(context.Context, harness.Provider) (harness.AccountReport, error) {
		return harness.AccountReport{}, nil
	}}
	ctx := context.Background()
	p, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Thanks", Template: "draft", Brief: core.BriefInput{Goal: "Thank the team", Criteria: []string{"Warm tone"}}})
	if err != nil {
		t.Fatal(err)
	}
	// These scripted scenarios assume tasks start one at a time.
	if p, err = a.SetParallel(ctx, p.ID, 1); err != nil {
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
	runner := &scriptedRunner{reviews: []string{ask, revise, revise, revise, revise, pass}}
	a, _, _ := loopApp(t, runner, "")
	ctx := context.Background()
	task := settle(t, a)
	d := openDecision(t, a, task)
	if d.Kind != core.DecisionQuestion || !strings.Contains(d.Context, "whole team") {
		t.Fatalf("question %+v", d)
	}
	a.Core.AnswerDecision(ctx, d.ID, "The whole team")
	task = settle(t, a)
	// The reviewer that asked judged draft 1 again with the answer; rounds
	// 1 to 3 were revised; the limit of three rounds then comes to the
	// owner rather than a fourth attempt.
	d = openDecision(t, a, task)
	if d.Kind != core.DecisionEscalation || task.Round != 3 {
		t.Fatalf("expected an escalation at the round limit, got %+v / %+v", d, task)
	}
	if runner.seen[2].Write || !strings.Contains(runner.seen[2].Prompt, "The whole team") || !strings.Contains(runner.seen[3].Prompt, "The whole team") {
		t.Fatal("the owner's answer did not reach the reviewer that asked, then the writer")
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

// A locked keychain is the owner's to unlock, not a failure: the task waits
// and checks again, however often, without counting failures or asking.
func TestALockedKeychainWaitsWithoutCountingFailures(t *testing.T) {
	locked := &session.UnsupportedError{Engine: "claude", Operation: "login", Code: harness.CodeKeychainUnavailable}
	runner := &scriptedRunner{fail: []error{locked, locked, locked, locked}, reviews: []string{pass}}
	a, _, _ := loopApp(t, runner, "")
	for range 4 {
		task := settle(t, a)
		if task.Status != core.TaskWriting || task.Failures != 0 || task.RetryAt.IsZero() || !strings.Contains(task.Detail, "keychain") || task.DecisionID != "" {
			t.Fatalf("a locked keychain should wait quietly: %+v", task)
		}
		a.Core.UpdateTask(context.Background(), task.ID, func(t *core.Task, _ *core.Project) (string, error) {
			t.RetryAt = time.Time{}
			return "", nil
		})
	}
	if task := settle(t, a); task.Status != core.TaskWaiting || openDecision(t, a, task).Kind != core.DecisionDelivery {
		t.Fatalf("once unlocked the work should carry on: %+v", task)
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

func TestAPISandboxProofFailureBlocksOnceAcrossTicks(t *testing.T) {
	problem := &session.CapabilityError{Engine: harness.OpenAICompatible, Code: session.CapabilityProbeTimeout, Phase: session.BeforeLaunch}
	runner := &scriptedRunner{fail: []error{problem}}
	lp, _, queued := loopApp(t, runner, "")
	if _, err := lp.Core.UpdateTask(context.Background(), queued.ID, func(task *core.Task, project *core.Project) (string, error) {
		// The queued task takes its seats from the project when it starts.
		for i := range project.Playbook.Roles {
			if project.Playbook.Roles[i].Holds(core.RoleImplementer) {
				project.Playbook.Roles[i].Name = "Ash"
				project.Playbook.Roles[i].Engine = "openai-compatible"
				project.Playbook.Roles[i].Model = "fake-tools"
			}
		}
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
	task := settle(t, lp)
	d := openDecision(t, lp, task)
	if d.Kind != core.DecisionFailure || task.Failures != 1 || !task.RetryAt.IsZero() || !strings.HasPrefix(d.Context, "The sandbox for Ash’s commands") || !strings.Contains(d.Context, "nothing ran.") || !strings.Contains(d.Context, problem.Error()) {
		t.Fatalf("task %+v decision %+v", task, d)
	}
	firstCalls := len(runner.seen)
	settle(t, lp)
	v, err := lp.Core.Snapshot(context.Background())
	if err != nil || len(v.Decisions) != 1 || len(runner.seen) != firstCalls || runner.writes != 0 || firstCalls != 1 || runner.seen[0].Engine != "openai-compatible" {
		t.Fatalf("retried or repeated decision: %d calls, %d writes, %+v %v", len(runner.seen), runner.writes, v.Decisions, err)
	}
}

func TestAPIMemberSeatAndBaseSpecKeepProvider(t *testing.T) {
	lp := testLoop(t)
	cfg := lp.Config()
	cfg.Engines.Providers = []config.Provider{{ID: "fixture-api", HTTPEngine: config.HTTPEngine{BaseURL: "http://127.0.0.1:1234/v1"}}}
	lp.Config = func() config.Config { return cfg }
	seat := memberSeat(core.Member{ID: "member", Name: "Rune", Engine: "openai-compatible", Provider: "fixture-api", Model: "tools-model"}, []string{core.RoleReviewer}, "Read carefully")
	spec := lp.baseSpec(seat, "/work", "Review")
	if spec.AccountIdentity != "fixture-api:" {
		t.Fatal("provider identity missing", spec.AccountIdentity)
	}
	cfg.Engines.Providers[0].APIKeyEnv = "FIXTURE_API_KEY"
	changed := lp.baseSpec(seat, "/work", "Review")
	if changed.AccountIdentity == spec.AccountIdentity {
		t.Fatal("changing a key source retained the old session identity")
	}
	if seat.Provider != "fixture-api" || spec.Provider.API.BaseURL != cfg.Engines.Providers[0].BaseURL || spec.RuntimeHome != lp.runtimeHome(seat.Engine) || spec.Browser || strings.Contains(spec.Instructions, "GOCACHE") || strings.Contains(spec.Instructions, "PORT=") || !strings.Contains(spec.Instructions, "no web search") {
		t.Fatalf("seat %+v spec %+v", seat, spec)
	}
	cli := lp.baseSpec(core.Role{Engine: "codex", Instructions: "Keep it small"}, "/work", "Write")
	if cli.Instructions != "Keep it small" || cli.RuntimeHome != lp.runtimeHome("codex") || cli.Provider.Engine != "" {
		t.Fatalf("CLI changed: %+v", cli)
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
	for _, bad := range []string{"", "no json here", `{"outcome":"maybe","summary":"x"}`, `{"outcome":"revise","summary":"x","findings":[]}`, `{"outcome":"question","summary":"x","question":""}`, `{"outcome":"pass","summary":""}`, `{"outcome":"research","summary":"x","question":""}`} {
		if _, err := parseVerdict(bad, true); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	research := `{"outcome":"research","summary":"x","question":"Which API?","next":"land","note":"I want to see it again"}`
	if _, err := parseVerdict(research, false); err == nil {
		t.Error("research was accepted from a team with no researcher")
	}
	v, err := parseVerdict(research, true)
	if err != nil || v.Outcome != core.VerdictResearch || v.Question != "Which API?" || v.Next != core.NextLand || v.Note != "I want to see it again" {
		t.Fatalf("research verdict %+v %v", v, err)
	}
	if v, _ := parseVerdict(`{"outcome":"pass","summary":"x","next":"research","note":"a\nb"}`, false); v.Next != "" || v.Note != "a b" {
		t.Fatalf("a recommendation the team can't follow is dropped and the note kept to a line: %+v", v)
	}
}

func TestNearlyUsedSubscriptionHoldsTheRoleWithoutFailing(t *testing.T) {
	runner := &scriptedRunner{reviews: []string{pass}}
	a, _, _ := loopApp(t, runner, "")
	used := 95.0
	resets := time.Now().Add(2 * time.Hour)
	observation := harness.Observation{Quality: harness.Measured, ObservedAt: time.Now()}
	a.meter = &quota.Meter{Inspect: func(_ context.Context, o harness.Provider) (harness.AccountReport, error) {
		if o.Engine != harness.Codex {
			return harness.AccountReport{}, nil
		}
		return harness.AccountReport{Quota: harness.QuotaSnapshot{Observation: observation, Complete: true, Windows: []harness.QuotaWindow{{Observation: observation, ID: "codex/primary", Scope: "codex", UsedPercent: &used, ResetsAt: &resets}}}}, nil
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
	observation := harness.Observation{Quality: harness.Measured, ObservedAt: time.Now()}
	a.meter = &quota.Meter{Inspect: func(_ context.Context, o harness.Provider) (harness.AccountReport, error) {
		if o.Engine != harness.Codex {
			return harness.AccountReport{}, nil
		}
		return harness.AccountReport{Quota: harness.QuotaSnapshot{Observation: observation, Complete: true, Windows: []harness.QuotaWindow{{Observation: observation, ID: "codex/primary", Scope: "codex", UsedPercent: &used, ResetsAt: &resets}}}}, nil
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
	reads := map[harness.Engine]int{}
	a.meter = &quota.Meter{Inspect: func(_ context.Context, o harness.Provider) (harness.AccountReport, error) {
		reads[o.Engine]++
		return harness.AccountReport{}, nil
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
	if until, _ := a.UsageWait(ctx, "codex"); !until.IsZero() || reads[harness.Codex] != 0 {
		t.Fatalf("codex with its floors off should run without reading usage: %v, %d reads", until, reads[harness.Codex])
	}
	if until, _ := a.UsageWait(ctx, "openai-compatible"); !until.IsZero() {
		t.Fatal("an API engine was held")
	}
	cfg.Engines.Codex.UsageFloor = config.UsageFloor{}
	if until, _ := a.UsageWait(ctx, "codex"); !until.IsZero() || reads[harness.Codex] != 1 {
		t.Fatalf("codex allows unknown usage by default: %v, %d reads", until, reads[harness.Codex])
	}
}

func TestNonSandboxCapabilityFailureKeepsItsOwnReason(t *testing.T) {
	problem := &session.CapabilityError{Engine: harness.Claude, Code: session.CapabilityLoginUnavailable, Phase: session.BeforeLaunch}
	lp, _, _ := loopApp(t, &scriptedRunner{fail: []error{problem}}, "")
	task := settle(t, lp)
	d := openDecision(t, lp, task)
	if d.Context != problem.Error() || task.Failures != 1 || !task.RetryAt.IsZero() {
		t.Fatalf("%+v %+v", task, d)
	}
}
