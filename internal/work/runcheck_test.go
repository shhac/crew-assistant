//go:build !windows

package work

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/session"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/media/gitrepo"
	"github.com/shhac/crew-assistant/internal/roles"
)

type fakeCommands struct {
	mu                         sync.Mutex
	runs                       []session.CommandRequest
	starts                     []session.CommandRequest
	run                        func(context.Context, session.CommandRequest) (session.CommandResult, error)
	result                     session.CommandResult
	runErr, startErr, closeErr error
	started                    *fakeStarted
	closed                     bool
}

func (f *fakeCommands) Run(ctx context.Context, req session.CommandRequest) (session.CommandResult, error) {
	f.mu.Lock()
	f.runs = append(f.runs, req)
	f.mu.Unlock()
	if f.run != nil {
		return f.run(ctx, req)
	}
	return f.result, f.runErr
}
func (f *fakeCommands) Start(_ context.Context, req session.CommandRequest) (startedCommand, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts = append(f.starts, req)
	if f.startErr != nil {
		return nil, f.startErr
	}
	if f.started == nil {
		f.started = newFakeStarted()
	}
	return f.started, nil
}
func (f *fakeCommands) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return f.closeErr
}

type fakeStarted struct {
	done    chan struct{}
	once    sync.Once
	stopped bool
	result  session.CommandResult
	err     error
}

func newFakeStarted() *fakeStarted                            { return &fakeStarted{done: make(chan struct{})} }
func (f *fakeStarted) Stop()                                  { f.stopped = true; f.once.Do(func() { close(f.done) }) }
func (f *fakeStarted) Done() <-chan struct{}                  { return f.done }
func (f *fakeStarted) Result() (session.CommandResult, error) { <-f.done; return f.result, f.err }

func checkFixture(t *testing.T) (*Loop, gitMedium, string) {
	t.Helper()
	lp := testLoop(t)
	source := ownerRepo(t)
	repo, err := gitrepo.Open(context.Background(), t.TempDir(), source, nil, gitrepo.SignNever)
	if err != nil {
		t.Fatal(err)
	}
	return lp, gitMedium{repo: repo, playbook: core.Playbook{Medium: core.MediumGit, Check: "make check"}}, source
}

// The same hosted tool serves all engines, with loopback chosen by the
// playbook rather than the engine's session capabilities.
func TestRunCheckOnEveryEngineAndRole(t *testing.T) {
	lp, m, source := checkFixture(t)
	if err := os.WriteFile(filepath.Join(source, "uncommitted.txt"), []byte("draft"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, engine := range []string{"codex", "claude", "openai-compatible"} {
		for _, kind := range []string{core.RoleImplementer, core.RoleQA} {
			for _, loopback := range []bool{false, true} {
				t.Run(engine+"/"+kind+"/"+map[bool]string{false: "offline", true: "localhost"}[loopback], func(t *testing.T) {
					m.playbook.CheckLoopback = loopback
					var opts session.CommandSandboxOptions
					fake := &fakeCommands{result: session.CommandResult{Stdout: "tests passed"}}
					lp.commands = func(_ context.Context, o session.CommandSandboxOptions) (commandSandbox, error) {
						opts = o
						if o.Timeout > 10*time.Minute {
							return nil, errors.New("command timeout exceeds the v0.22.0 limit")
						}
						body, err := os.ReadFile(filepath.Join(o.WorkDir, "uncommitted.txt"))
						if err != nil || string(body) != "draft" {
							t.Errorf("copy missed draft: %s %v", body, err)
						}
						return fake, nil
					}
					status := core.TaskWriting
					if kind == core.RoleQA {
						status = core.TaskReviewing
					}
					spec, cleanup, err := lp.roleSpec(core.Task{ID: "task", Status: status}, core.Role{Name: "Member", Engine: engine, Kinds: []string{kind}}, source, true, m, "check", nil)
					if err != nil {
						t.Fatal(err)
					}
					if !hasTool(spec, "run_check") {
						t.Fatal("run_check missing")
					}
					result, err := spec.Handler.CallTool(context.Background(), session.ToolCall{Name: "run_check", Arguments: json.RawMessage("{}")})
					if err != nil || result.IsError || !strings.Contains(result.Content, "tests passed") {
						t.Fatalf("%+v %v", result, err)
					}
					if opts.WorkDir == source || opts.WorkDir == "" || opts.Loopback != loopback || !opts.Write || opts.Timeout != commandTimeout {
						t.Fatalf("sandbox options %+v", opts)
					}
					if len(fake.runs) != 1 || fake.runs[0].Command != "make check" {
						t.Fatalf("runs %+v", fake.runs)
					}
					if !fake.closed {
						t.Fatal("finished check sandbox not closed")
					}
					if _, err := os.Stat(opts.WorkDir); !os.IsNotExist(err) {
						t.Fatal("copy remained")
					}
					cleanup()
					after, _ := spec.Handler.CallTool(context.Background(), session.ToolCall{Name: "run_check", Arguments: json.RawMessage("{}")})
					if !after.IsError {
						t.Fatal("tool ran after turn ended")
					}
				})
			}
		}
	}
	for _, kind := range []string{core.RoleReviewer, core.RoleResearcher} {
		tools := lp.toolsFor(core.Task{}, kind, core.Role{})
		if tools.offers("run_check") {
			t.Fatal("check offered to " + kind)
		}
		if _, err := tools.call(context.Background(), "run_check", []byte("{}")); err == nil {
			t.Fatal("unoffered check admitted")
		}
	}
	tools := lp.toolsFor(core.Task{}, core.RoleImplementer, core.Role{})
	if tools.offers("run_check") {
		t.Fatal("documents offered check")
	}
}

func TestRunCheckPollsOneRunAndCancelsAtTurnEnd(t *testing.T) {
	lp, m, source := checkFixture(t)
	started, finish := make(chan struct{}), make(chan struct{})
	var opts session.CommandSandboxOptions
	fake := &fakeCommands{run: func(ctx context.Context, _ session.CommandRequest) (session.CommandResult, error) {
		close(started)
		select {
		case <-finish:
			return session.CommandResult{Stdout: "done"}, nil
		case <-ctx.Done():
			return session.CommandResult{}, ctx.Err()
		}
	}}
	lp.commands = func(_ context.Context, o session.CommandSandboxOptions) (commandSandbox, error) {
		opts = o
		return fake, nil
	}
	runs := newCheckRuns(lp, m, source, nil)
	runs.wait = 10 * time.Millisecond
	first, err := runs.call(context.Background())
	if err != nil || !strings.Contains(first, "still running") {
		t.Fatalf("%s %v", first, err)
	}
	<-started
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := runs.call(context.Background())
			if err != nil || !strings.Contains(out, "still running") {
				t.Errorf("%s %v", out, err)
			}
		}()
	}
	wg.Wait()
	fake.mu.Lock()
	count := len(fake.runs)
	fake.mu.Unlock()
	if count != 1 {
		t.Fatalf("%d runs", count)
	}
	// Cancelling one tool call leaves the turn's run alive.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runs.call(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(finish)
	<-runs.current.done
	out, err := runs.call(context.Background())
	if err != nil || !strings.Contains(out, "done") {
		t.Fatalf("%s %v", out, err)
	}
	runs.close()
	if !fake.closed {
		t.Fatal("not closed")
	}
	if _, err := os.Stat(opts.WorkDir); !os.IsNotExist(err) {
		t.Fatal("copy left behind")
	}

	// Cleanup cancels an unfinished check and waits until its sandbox settles.
	admitted := make(chan struct{})
	fake = &fakeCommands{run: func(ctx context.Context, _ session.CommandRequest) (session.CommandResult, error) {
		close(admitted)
		<-ctx.Done()
		return session.CommandResult{}, ctx.Err()
	}}
	runs = newCheckRuns(lp, m, source, nil)
	runs.wait = time.Millisecond
	_, _ = runs.call(context.Background())
	<-admitted
	runs.close()
	if !fake.closed {
		t.Fatal("unfinished sandbox not closed")
	}
	if _, err := os.Stat(opts.WorkDir); !os.IsNotExist(err) {
		t.Fatal("unfinished copy left")
	}
}

func TestRunCheckFailuresAndBoundedOutput(t *testing.T) {
	lp, m, source := checkFixture(t)
	refusal := &session.UnsupportedError{Engine: harness.OpenAICompatible, Operation: "run", Code: session.RefusedNotOffered, Capability: harness.Capability{Availability: harness.Unsupported, Reason: "synthetic sandbox refusal"}}
	cases := []struct {
		name            string
		result          session.CommandResult
		openErr, runErr error
		want            string
		isError         bool
	}{
		{name: "refused", openErr: refusal, want: "synthetic sandbox refusal", isError: true},
		{name: "failed", result: session.CommandResult{ExitCode: 2, Stderr: "test failed"}, want: "test failed"},
		{name: "timed out", result: session.CommandResult{ExitCode: -1, TimedOut: true}, want: "\"timed_out\":true"},
		{name: "unknown outcome", runErr: &session.TurnError{Engine: harness.OpenAICompatible, Code: session.CommandOutcomeUnknown}, want: session.CommandOutcomeUnknown, isError: true},
		{name: "process limit", runErr: &session.TurnError{Engine: harness.OpenAICompatible, Code: session.CommandProcessLimit}, want: session.CommandProcessLimit, isError: true},
		{name: "truncated", result: session.CommandResult{Stdout: strings.Repeat("x", 200<<10) + "last stdout", Stderr: strings.Repeat("y", 200<<10) + "last stderr"}, want: "last stderr"},
		{name: "escaped output", result: session.CommandResult{Stdout: strings.Repeat("\x00", 200<<10) + "last stdout", Stderr: strings.Repeat("\x01", 200<<10) + "last stderr"}, want: "last stderr"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := &fakeCommands{result: c.result, runErr: c.runErr}
			lp.commands = func(context.Context, session.CommandSandboxOptions) (commandSandbox, error) { return fake, c.openErr }
			runs := newCheckRuns(lp, m, source, nil)
			defer runs.close()
			tools := roleTools{checks: runs}
			out, err := tools.Handler().CallTool(context.Background(), session.ToolCall{Name: "run_check", Arguments: []byte("{}")})
			if err != nil || out.IsError != c.isError || !strings.Contains(out.Content, c.want) || len(out.Content) > 128<<10 {
				t.Fatalf("%+v %v", out, err)
			}
			if !c.isError {
				var got session.CommandResult
				if err := json.Unmarshal([]byte(out.Content), &got); err != nil {
					t.Fatal(err)
				}
				if got.ExitCode != c.result.ExitCode || got.TimedOut != c.result.TimedOut {
					t.Fatalf("%+v", got)
				}
				if c.name == "truncated" && (!got.Truncated || !strings.Contains(got.Stdout, "last stdout") || !strings.Contains(got.Stdout, "omitted")) {
					t.Fatalf("%+v", got)
				}
			}
		})
	}
}

func TestRunCheckNewCallAfterResultMakesFreshCopy(t *testing.T) {
	lp, m, source := checkFixture(t)
	var dirs []string
	lp.commands = func(_ context.Context, o session.CommandSandboxOptions) (commandSandbox, error) {
		dirs = append(dirs, o.WorkDir)
		return &fakeCommands{}, nil
	}
	runs := newCheckRuns(lp, m, source, nil)
	defer runs.close()
	for range 2 {
		if _, err := runs.call(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(dirs) != 2 || dirs[0] == dirs[1] {
		t.Fatalf("copies %v", dirs)
	}
}

func TestRunCheckCleanupFailurePreservesKnownResult(t *testing.T) {
	for _, exit := range []int{0, 2} {
		t.Run(string(rune('0'+exit)), func(t *testing.T) {
			lp, m, source := checkFixture(t)
			cleanupErr := errors.New("cleanup unknown")
			fake := &fakeCommands{result: session.CommandResult{ExitCode: exit, Stdout: "check evidence"}, closeErr: cleanupErr}
			lp.commands = func(context.Context, session.CommandSandboxOptions) (commandSandbox, error) { return fake, nil }
			runs := newCheckRuns(lp, m, source, nil)
			defer runs.close()
			var logged error
			runs.cleanupError = func(err error) { logged = err }
			out, err := runs.call(context.Background())
			var result session.CommandResult
			if err != nil || json.Unmarshal([]byte(out), &result) != nil || result.ExitCode != exit || result.Stdout != "check evidence" || logged != cleanupErr {
				t.Fatalf("result lost during cleanup: %s, %v, logged %v", out, err, logged)
			}
		})
	}
}

func TestHostedCheckPromptsKeepCheckWithTeam(t *testing.T) {
	p := core.Project{Playbook: &core.Playbook{Medium: core.MediumGit, Check: "make check"}}
	task := core.Task{Objective: "feature"}
	qa := checkerPrompt(p, task, core.Revision{}, core.Role{Kinds: []string{core.RoleQA}}, p.Playbook)
	writer := writerPrompt(p, task, "", false)
	pm := ownerStepPrompt(p, task, core.Unreachable{Criterion: "make check passes"})
	for _, limit := range []string{"no general network", "access to the owner's machine", "browser access is available only when the owner allows it"} {
		if !strings.Contains(pm, limit) {
			t.Fatalf("PM lost sandbox limit %q: %s", limit, pm)
		}
	}
	for name, prompt := range map[string]string{"QA": qa, "implementer": writer, "PM": pm} {
		if !strings.Contains(prompt, "run_check") {
			t.Errorf("%s missing hosted check", name)
		}
	}
	if strings.Contains(qa, "Run exactly this") || !strings.Contains(qa, "Repeat calls") || !strings.Contains(pm, "stays with the team") || !strings.Contains(writer, "never an owner step") {
		t.Fatal("old check or escalation guidance")
	}
}

// A daemon-owned app gives Codex QA a URL without asking its shell to start
// or reach a server. Turn cleanup stops it before removing its tree and port.
func TestCodexQAUsesDaemonHostedApp(t *testing.T) {
	runner := &appRunner{codeRunner: codeRunner{scriptedRunner: scriptedRunner{reviews: []string{pass}}}, qaReply: qaSawTheApp}
	lp, p := qaTeam(t, runner, "codex", core.Browser{On: true}, &testRecipe)
	fake := &fakeCommands{started: newFakeStarted()}
	var opts session.CommandSandboxOptions
	lp.commands = func(_ context.Context, o session.CommandSandboxOptions) (commandSandbox, error) {
		opts = o
		return fake, nil
	}
	task, _ := lp.Core.QueueTask(context.Background(), p.ID, core.TaskInput{Objective: "Add Feature"})
	task = settleCode(t, lp, task.ID)
	if task.Status != core.TaskWaiting || len(runner.qa) != 1 {
		t.Fatalf("%s %+v", task.Status, runner.qa)
	}
	qa := runner.qa[0]
	if !qa.Browser || qa.Loopback || portOf(qa) != "" || !hasTool(qa, "run_check") {
		t.Fatalf("%+v", qa)
	}
	if !slices.Contains(opts.Env, "GOCACHE="+filepath.Join(opts.WorkDir, ".crew", "go-build")) || !opts.Loopback || !slices.Contains(opts.Env, "PORT=43001") || len(fake.runs) != 2 || fake.runs[0].Command != testRecipe.Setup || fake.runs[1].Command != "curl -sf http://127.0.0.1:43001/health" || len(fake.starts) != 1 || !strings.Contains(fake.starts[0].Command, testRecipe.Start) {
		t.Fatalf("%+v runs %+v starts %+v", opts, fake.runs, fake.starts)
	}
	if !strings.Contains(qa.Prompt, "http://127.0.0.1:43001/") || strings.Contains(qa.Prompt, testRecipe.Start) || strings.Contains(qa.Prompt, "Stop everything") {
		t.Fatal(qa.Prompt)
	}
	if !fake.started.stopped || !fake.closed || len(lp.ports.held) != 0 {
		t.Fatal("app not reclaimed")
	}
	if _, err := os.Stat(opts.WorkDir); !os.IsNotExist(err) {
		t.Fatal("app copy remains")
	}
}

// checkingRunner calls the hosted check while the role turn is still running.
type checkingRunner struct {
	next   roles.Runner
	before func(context.Context, roles.Spec)
}

func (r checkingRunner) Run(ctx context.Context, spec roles.Spec) (roles.Result, error) {
	r.before(ctx, spec)
	return r.next.Run(ctx, spec)
}

func TestRoleTurnCancelsHostedCheckAndRemovesItsCopy(t *testing.T) {
	for _, kind := range []string{core.RoleImplementer, core.RoleQA} {
		t.Run(kind, func(t *testing.T) {
			runner := &appRunner{codeRunner: codeRunner{scriptedRunner: scriptedRunner{reviews: []string{pass}}}, qaReply: pass}
			lp, p := qaTeam(t, runner, "codex", core.Browser{}, nil)
			admitted := make(chan struct{})
			fake := &fakeCommands{run: func(ctx context.Context, _ session.CommandRequest) (session.CommandResult, error) {
				close(admitted)
				<-ctx.Done()
				return session.CommandResult{}, ctx.Err()
			}}
			var copyDir string
			lp.commands = func(_ context.Context, opts session.CommandSandboxOptions) (commandSandbox, error) {
				copyDir = opts.WorkDir
				return fake, nil
			}
			called := false
			lp.runner = checkingRunner{next: runner, before: func(_ context.Context, spec roles.Spec) {
				qa := strings.Contains(spec.Prompt, "Use run_check for the project check")
				if !hasTool(spec, "run_check") || (kind == core.RoleQA) != qa {
					return
				}
				if called {
					t.Fatal("multiple role turns")
				}
				called = true
				// The CLI's cancelled call must not cancel the turn-owned command.
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				out, err := spec.Handler.CallTool(ctx, session.ToolCall{Name: "run_check", Arguments: []byte("{}")})
				if err != nil || !out.IsError {
					t.Fatalf("%+v %v", out, err)
				}
				<-admitted
				if copyDir == spec.WorkDir {
					t.Fatal("ran in member workspace")
				}
				if qa {
					content, err := os.ReadFile(filepath.Join(copyDir, "feature.go"))
					if err != nil || !strings.Contains(string(content), "Feature") {
						t.Fatalf("QA copy missed revision: %s %v", content, err)
					}
				}
			}}
			task, _ := lp.Core.QueueTask(context.Background(), p.ID, core.TaskInput{Objective: "Add Feature"})
			task = settleCode(t, lp, task.ID)
			if !called || task.Status != core.TaskWaiting || !fake.closed {
				t.Fatalf("called %t task %s closed %t", called, task.Status, fake.closed)
			}
			if _, err := os.Stat(copyDir); !os.IsNotExist(err) {
				t.Fatal("check copy remained after turn")
			}
		})
	}
}

func TestCommandCleanupFailureIsKeptAfterTurnEnds(t *testing.T) {
	runner := &appRunner{codeRunner: codeRunner{scriptedRunner: scriptedRunner{reviews: []string{pass}}}, qaReply: pass}
	lp, p := qaTeam(t, runner, "codex", core.Browser{On: true}, &testRecipe)
	fake := &fakeCommands{started: newFakeStarted(), closeErr: &session.TurnError{Engine: harness.OpenAICompatible, Code: session.CommandCleanupUnknown}}
	lp.commands = func(context.Context, session.CommandSandboxOptions) (commandSandbox, error) { return fake, nil }
	task, _ := lp.Core.QueueTask(context.Background(), p.ID, core.TaskInput{Objective: "Add Feature"})
	// Preparation has no real turn yet: cleanup belongs only in diagnostics.
	lp.commandCleanupFor(lp.watchTurn(task, core.RoleQA, core.Role{Name: "Quinn"}, "", false))(fake.closeErr)
	task = settleCode(t, lp, task.ID)
	steps, err := lp.Core.TurnSteps(context.Background(), p.ID, task.ID, "Quinn")
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range steps {
		if strings.HasPrefix(step.Turn, "command-preparation-") {
			t.Fatalf("invented preparation turn: %+v", step)
		}
	}
	if !slices.ContainsFunc(steps, func(step core.TurnStep) bool {
		return step.Kind == core.StepNote && strings.Contains(step.Text, session.CommandCleanupUnknown)
	}) {
		t.Fatalf("no cleanup finding in activity: %+v", steps)
	}
}

func TestStartupReclaimsStaleCommandsWithoutRunningWorkspaceContent(t *testing.T) {
	lp := testLoop(t)
	opened := 0
	var opts session.CommandSandboxOptions
	fake := &fakeCommands{}
	lp.commands = func(_ context.Context, o session.CommandSandboxOptions) (commandSandbox, error) {
		opened++
		opts = o
		return fake, nil
	}
	if err := lp.sweepCommands(context.Background()); err != nil {
		t.Fatal(err)
	}
	if opened != 0 {
		t.Fatal("opened a sandbox with no stale commands")
	}
	stale := filepath.Join(lp.Core.StateDirectory(), "commands", "commands", "old-run")
	if err := os.MkdirAll(stale, 0700); err != nil {
		t.Fatal(err)
	}
	if err := lp.sweepCommands(context.Background()); err != nil {
		t.Fatal(err)
	}
	if opened != 1 || !fake.closed || len(fake.runs) != 0 || len(fake.starts) != 0 || opts.Loopback {
		t.Fatal("recovery ran content or failed to close")
	}
	if _, err := os.Stat(opts.WorkDir); !os.IsNotExist(err) {
		t.Fatal("recovery workspace remained")
	}
}
