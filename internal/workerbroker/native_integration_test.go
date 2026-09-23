//go:build !windows

package workerbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/diagnostics"
	"github.com/shhac/crew-assistant/internal/integrations/worker"
	"github.com/shhac/lib-agent-harness/session"
)

// These tests drive a real Broker against a synthetic coding CLI that speaks the
// engines' actual protocols and reaches its tools through the library's real MCP
// bridge. Nothing here contacts a model, an account or a container runtime: the
// "container" is a local command runner over a temporary directory, and what the
// "CLI" decides to do comes from a script rather than from inference.
//
// They replace the tests that covered the previous contract's stateless
// action-envelope loop. That loop is gone, so those tests could not be kept;
// what they were really about — admission, holds, pause and stop, peer
// messages, decisions, evidence — is covered here and in the unit tests beside
// them, against the contract that replaced it.

const (
	testImage       = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testContainerID = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

var (
	buildFake    sync.Once
	fakeBinary   string
	fakeBuildErr error
)

// fakeCLI builds the synthetic harness once per test binary.
func fakeCLI(t *testing.T) string {
	t.Helper()
	buildFake.Do(func() {
		dir, err := os.MkdirTemp("", "fakecli-")
		if err != nil {
			fakeBuildErr = err
			return
		}
		fakeBinary = filepath.Join(dir, "fakecli")
		build := exec.Command("go", "build", "-o", fakeBinary, "./testdata/fakecli")
		build.Stderr = os.Stderr
		fakeBuildErr = build.Run()
	})
	if fakeBuildErr != nil {
		t.Fatalf("could not build the synthetic harness: %v", fakeBuildErr)
	}
	return fakeBinary
}

// workspaceRunner stands in for the container runtime: it answers the same
// commands the broker issues, against the isolated copy the broker itself
// prepared. The point is that the broker's own tool handler runs for real, not
// that Docker does.
type workspaceRunner struct {
	mu   sync.Mutex
	root string
	// slow makes one named command take a stated time, so a test can drive the
	// unsettled-command path without waiting for a real timeout.
	slow map[string]time.Duration
}

func (r *workspaceRunner) Run(ctx context.Context, args []string, input []byte) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("no command")
	}
	switch args[0] {
	case "image":
		return []byte(testImage), nil
	case "run":
		for i, arg := range args {
			if arg == "--mount" && i+1 < len(args) {
				r.mu.Lock()
				r.root = strings.TrimSuffix(strings.TrimPrefix(args[i+1], "type=bind,src="), ",dst=/workspace")
				r.mu.Unlock()
			}
		}
		return []byte(testContainerID), nil
	case "rm":
		return nil, nil
	case "container":
		if len(args) > 1 && args[1] == "inspect" {
			name := args[len(args)-1]
			return []byte(testContainerID + " " + strings.TrimPrefix(name, "agent-assistant-")), nil
		}
		return nil, nil
	case "exec":
	default:
		return nil, nil
	}
	// exec [--interactive] <container> /bin/sh -c <script> <label> <target>.
	// Only writes are interactive, which is how one is told from a read.
	writing := args[1] == "--interactive"
	target := args[len(args)-1]
	if writing {
		path := r.local(target)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return nil, err
		}
		return nil, os.WriteFile(path, input, 0644)
	}
	if strings.HasPrefix(target, "/workspace/") {
		return os.ReadFile(r.local(target))
	}
	r.mu.Lock()
	delay := r.slow[target]
	r.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return []byte("interrupted"), ctx.Err()
		}
	}
	if strings.Contains(target, "false") {
		return []byte("check failed"), errors.New("command failed")
	}
	return []byte("ran: " + target + "\n"), nil
}

func (r *workspaceRunner) local(path string) string {
	return filepath.Join(r.workspace(), filepath.FromSlash(strings.TrimPrefix(path, "/workspace/")))
}

func (r *workspaceRunner) workspace() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.root
}

type harness struct {
	broker *Broker
	source string
	runner *workspaceRunner
	// diagnostics is the daemon's own error log. A failure an owner has to act on
	// arrives here, so a test can check that it arrived at all.
	diagnostics *syncBuffer
	mu          sync.Mutex
	admit       func(context.Context) error
	budget      int64
}

// syncBuffer collects diagnostics while several goroutines are writing them.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newHarness(t *testing.T, engine string, turns []map[string]any, env map[string]string) *harness {
	t.Helper()
	binary := fakeCLI(t)
	// A synthetic file-backed login. No real credential is read or written.
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"synthetic":"test"}`), 0600); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	if err := os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(turns)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_CLI_SCRIPT", string(encoded))
	t.Setenv("WORKER_TEST_TOKEN", "fixture-auth")
	for key, value := range env {
		t.Setenv(key, value)
	}
	h := &harness{source: source, runner: &workspaceRunner{slow: map[string]time.Duration{}}, diagnostics: &syncBuffer{}}
	cfg := Config{
		Diagnostics: diagnostics.New(h.diagnostics),
		Command:     h.runner, StateDir: state, Workspace: source,
		ProjectID: "project-one", Image: testImage, MaxConcurrent: 1, TokenEnv: "WORKER_TEST_TOKEN",
		Engine: engine, Model: "fake-model", Effort: "medium",
		CodexBin: binary, CodexHome: home, ClaudeBin: binary, ClaudeHome: home,
		BridgeCommand: session.Bridge{Path: binary, Args: []string{"bridge"}},
		Admit: func(ctx context.Context) error {
			h.mu.Lock()
			admit := h.admit
			h.mu.Unlock()
			if admit == nil {
				return nil
			}
			return admit(ctx)
		},
		TokenBudget: func() int64 { h.mu.Lock(); defer h.mu.Unlock(); return h.budget },
	}
	broker, err := New(cfg)
	if err != nil {
		t.Fatalf("broker: %v", err)
	}
	h.broker = broker
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = broker.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done; _ = broker.Close() })
	return h
}

func (h *harness) hold(admit func(context.Context) error) {
	h.mu.Lock()
	h.admit = admit
	h.mu.Unlock()
}

func (h *harness) start(t *testing.T, task string) string {
	t.Helper()
	in := worker.StartRequest{
		DispatchKey: "dispatch-one", AgentID: "agent-one", ProjectID: "project-one", Role: "worker",
		Task: task, AcceptanceCriteria: "the change is made and verified",
		Capabilities: []string{"implement"},
		Prohibitions: []string{"deployment", "production_data_access", "purchases"},
	}
	response := request(t, h.broker, "/runs", in.DispatchKey, in)
	if response.Code != 201 {
		t.Fatal(response.Code, response.Body.String())
	}
	var run worker.Run
	if err := json.Unmarshal(response.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	return run.ID
}

// insert writes an assignment straight into durable state, for the cases where
// what a test is about is how the daemon treats a run it finds already there.
func (h *harness) insert(t *testing.T, shape func(*storedRun)) string {
	t.Helper()
	id := uid()
	run := &storedRun{
		Run: worker.Run{
			ID: id, DispatchKey: uid(), Status: "blocked", Summary: "saved",
			UpdatedAt: now(), Evidence: []string{},
			ControlCapabilities: []string{"pause", "resume", "stop"},
		},
		Request: worker.StartRequest{
			DispatchKey: uid(), AgentID: "agent-one", ProjectID: "project-one", Role: "worker",
			Task: "continue earlier work", AcceptanceCriteria: "the change is made and verified",
			Capabilities: []string{"implement"},
			Prohibitions: []string{"deployment", "production_data_access", "purchases"},
		},
		Container: "agent-assistant-" + id,
		Messages:  []string{}, Commands: []commandRecord{},
	}
	shape(run)
	h.broker.mu.Lock()
	h.broker.state.Runs[id] = run
	err := h.broker.saveLocked()
	h.broker.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// await polls until the run reaches one of the wanted statuses, or gives up.
func (h *harness) await(t *testing.T, id string, within time.Duration, wanted ...string) storedRun {
	t.Helper()
	deadline := time.Now().Add(within)
	var last storedRun
	for time.Now().Before(deadline) {
		run, err := h.broker.snapshot(id)
		if err == nil {
			last = run
			for _, status := range wanted {
				if run.Run.Status == status {
					return run
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("run stayed in %q (%s); wanted one of %v", last.Run.Status, last.Run.Summary, wanted)
	return last
}

// settled is every way an assignment can stop. Tests wait for one of these and
// then say which they expected, so a wrong outcome reports itself rather than
// timing out with nothing to read.
var settled = []string{"completed", "blocked", "waiting", "interrupted", "cancelled", "failed", "paused", "usage_wait"}

func turnScript(entries ...map[string]any) map[string]any { return map[string]any{"steps": entries} }
func tool(name string, args map[string]any) map[string]any {
	return map[string]any{"tool": name, "args": args}
}

// The whole point of the refactor: a worker does several tool calls inside one
// native turn, against the isolated workspace, and reports when it is done.
func TestNativeWorkerCompletesAMultiStepAssignment(t *testing.T) {
	for _, engine := range []string{"claude", "codex"} {
		t.Run(engine, func(t *testing.T) {
			h := newHarness(t, engine, []map[string]any{turnScript(
				tool("read_file", map[string]any{"path": "main.go"}),
				tool("write_file", map[string]any{"path": "main.go", "content": "package main\n\nfunc main() {}\n"}),
				tool("run_command", map[string]any{"command": "go build ./..."}),
				tool("finish", map[string]any{"summary": "added a main function and built it"}),
			)}, nil)
			id := h.start(t, "add a main function")
			run := h.await(t, id, 90*time.Second, settled...)
			if run.Run.Status != "completed" {
				t.Fatalf("assignment ended %q: %s", run.Run.Status, run.Run.Summary)
			}
			if run.Run.Summary != "added a main function and built it" {
				t.Errorf("the acceptance summary was not preserved: %q", run.Run.Summary)
			}
			// The original project folder is never touched.
			original, err := os.ReadFile(filepath.Join(h.source, "main.go"))
			if err != nil || string(original) != "package main\n" {
				t.Fatalf("the source workspace was modified: %q %v", original, err)
			}
			// The patch the assistant judges comes from the daemon, not the worker.
			patch, err := os.ReadFile(filepath.Join(h.broker.cfg.StateDir, "runs", id, "artifacts", "changes.patch"))
			if err != nil || !strings.Contains(string(patch), "+func main() {}") {
				t.Fatalf("the worker's edit did not reach the collected patch: %q %v", patch, err)
			}
			if len(run.Commands) != 1 || !run.Commands[0].Success {
				t.Errorf("command evidence was not recorded: %+v", run.Commands)
			}
			if len(run.Run.Evidence) == 0 {
				t.Error("no evidence was collected")
			}
			// One durable session reference per assignment.
			if run.Session == nil || run.Run.Session == nil || run.Run.Session.Engine != engine {
				t.Errorf("no session reference was recorded: %+v", run.Session)
			}
			// Streamed activity is visible, with tool names and how the turn ended.
			tools, turns := map[string]bool{}, 0
			for _, entry := range run.Run.Activity {
				if entry.Kind == "tool" && entry.Tool != "" {
					tools[entry.Tool] = true
				}
				if entry.Kind == "turn" {
					turns++
				}
			}
			for _, want := range []string{"read_file", "write_file", "run_command", "finish"} {
				if !tools[want] {
					t.Errorf("activity did not record %s: %+v", want, run.Run.Activity)
				}
			}
			if turns == 0 {
				t.Error("activity did not record the turn itself")
			}
			// Consumption is accounted, and the cache-creation tokens that make a
			// first long prompt expensive are in it.
			if run.Run.Usage.InputTokens <= 0 || run.Run.Usage.OutputTokens <= 0 || run.Run.Usage.UnknownCalls != 0 {
				t.Errorf("usage was not accounted: %+v", run.Run.Usage)
			}
			if run.Run.Usage.ObservedInputTokens <= 0 {
				t.Errorf("no streamed observation was published: %+v", run.Run.Usage)
			}
		})
	}
}

// The deadline that used to end a worker's turn is gone. A turn that takes
// longer than the old five-minute boundary is ordinary work; what this checks is
// that no elapsed-time rule stops it, not that it is slow.
func TestALongTurnIsNotADeadline(t *testing.T) {
	h := newHarness(t, "claude", []map[string]any{turnScript(
		tool("finish", map[string]any{"summary": "took its time"}),
	)}, map[string]string{"FAKE_CLI_TURN_DELAY": "3s"})
	id := h.start(t, "think about it")
	started := time.Now()
	run := h.await(t, id, 120*time.Second, settled...)
	if run.Run.Status != "completed" {
		t.Fatalf("a slow turn ended %q: %s", run.Run.Status, run.Run.Summary)
	}
	if elapsed := time.Since(started); elapsed < 3*time.Second {
		t.Fatalf("the turn did not actually take its time: %s", elapsed)
	}
	for _, entry := range run.Run.Activity {
		if entry.Status == "interrupted" || entry.Status == "failed" {
			t.Errorf("a long turn was stopped: %+v", entry)
		}
	}
}

// A worker that asks a question stops with a durable, prepared decision rather
// than finishing, and its session is kept for the answer.
func TestAskingADecisionBlocksWithDurableState(t *testing.T) {
	for _, engine := range []string{"claude", "codex"} {
		t.Run(engine, func(t *testing.T) {
			h := newHarness(t, engine, []map[string]any{turnScript(
				tool("ask_decision", map[string]any{
					"question": "which database?", "recommendation": "keep sqlite",
					"why": "the schema is small", "options": []any{"sqlite", "postgres"},
				}),
			)}, nil)
			id := h.start(t, "choose a database")
			run := h.await(t, id, 90*time.Second, settled...)
			if run.Run.Status != "blocked" || run.Run.Decision == nil {
				t.Fatalf("no prepared decision: %q %+v", run.Run.Status, run.Run.Decision)
			}
			if run.Run.Decision.Question != "which database?" || len(run.Run.Decision.Options) != 2 {
				t.Errorf("the decision lost its content: %+v", run.Run.Decision)
			}
			if run.Session == nil {
				t.Error("the coding session was not preserved for the answer")
			}
		})
	}
}

// A peer message waits for the daemon to acknowledge routing, and is durable in
// the meantime. The worker has no way to reach a peer itself.
func TestPeerMessageWaitsForDaemonDelivery(t *testing.T) {
	h := newHarness(t, "claude", []map[string]any{turnScript(
		tool("send_message", map[string]any{"target_agent_id": "agent-two", "message": "what did you change?"}),
	)}, nil)
	id := h.start(t, "coordinate")
	run := h.await(t, id, 90*time.Second, settled...)
	if run.Run.Status != "waiting" || run.Run.Message == nil {
		t.Fatalf("no pending peer message: %q %+v", run.Run.Status, run.Run.Message)
	}
	if run.Run.Message.TargetAgentID != "agent-two" {
		t.Errorf("the message lost its target: %+v", run.Run.Message)
	}
}

// Finishing is not accepting. The daemon collects the patch and the command log
// itself, and a worker's summary is only a summary.
func TestFinishDoesNotAcceptTheWorkItself(t *testing.T) {
	h := newHarness(t, "claude", []map[string]any{turnScript(
		tool("write_file", map[string]any{"path": "notes.txt", "content": "changed\n"}),
		tool("run_command", map[string]any{"command": "false"}),
		tool("finish", map[string]any{"summary": "all tests pass"}),
	)}, nil)
	id := h.start(t, "make a change")
	run := h.await(t, id, 90*time.Second, settled...)
	if run.Run.Status != "completed" {
		t.Fatalf("unexpected status %q: %s", run.Run.Status, run.Run.Summary)
	}
	// The worker claimed tests pass; the recorded evidence says otherwise, and
	// that is what the assistant reviews.
	if len(run.Commands) != 1 || run.Commands[0].Success {
		t.Fatalf("the failed command was not recorded as failed: %+v", run.Commands)
	}
	joined := strings.Join(run.Run.Evidence, "\n")
	for _, want := range []string{
		"0 SUCCEEDED, 1 FAILED",
		"Command 1 FAILED: false",
		"check failed",
		"Content patch SHA-256:",
		"not proof that acceptance criteria are met",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("a real failed check was hidden by the worker's summary; missing %q in %s", want, joined)
		}
	}
	// And the change it did make is in the collected patch, whatever it claimed.
	patch, err := os.ReadFile(filepath.Join(h.broker.cfg.StateDir, "runs", id, "artifacts", "changes.patch"))
	if err != nil || !strings.Contains(string(patch), "+changed") {
		t.Fatalf("the worker's edit did not reach the collected patch: %q %v", patch, err)
	}
}

// A command that outlived its limit may still be writing, so an acceptance
// report on top of it is held rather than published.
func TestUnsettledCommandHoldsTheAcceptanceReport(t *testing.T) {
	h := newHarness(t, "claude", []map[string]any{turnScript(
		tool("run_command", map[string]any{"command": "sleep forever"}),
		tool("finish", map[string]any{"summary": "done"}),
	)}, nil)
	h.runner.mu.Lock()
	h.runner.slow["sleep forever"] = 5 * time.Minute
	h.runner.mu.Unlock()
	id := h.start(t, "run something slow")
	run := h.await(t, id, 180*time.Second, settled...)
	if run.Run.Status != "blocked" {
		t.Fatalf("an unsettled command did not hold the report: %q %s", run.Run.Status, run.Run.Summary)
	}
	if !strings.Contains(run.Run.Summary, "never confirmed stopped") {
		t.Errorf("the hold did not explain itself: %q", run.Run.Summary)
	}
}

// A subscription hold is a wait, and it keeps its own kind: flattening it into a
// budget decision would turn something that clears by itself into something that
// waits for a person.
func TestSubscriptionHoldIsPreservedAsAResumableWait(t *testing.T) {
	h := newHarness(t, "claude", []map[string]any{turnScript(
		tool("finish", map[string]any{"summary": "never reached"}),
	)}, nil)
	h.hold(func(context.Context) error {
		return &worker.HoldError{Hold: worker.ResourceHold{
			Kind: worker.HoldSubscriptionQuota, Reason: "Worker paused: subscription headroom is low",
			NextCheckAt: time.Now().Add(time.Minute),
		}}
	})
	id := h.start(t, "do some work")
	run := h.await(t, id, 90*time.Second, settled...)
	if run.Run.Status != "usage_wait" || run.Run.ResourceHold == nil {
		t.Fatalf("no resource hold: %q %+v", run.Run.Status, run.Run.ResourceHold)
	}
	if run.Run.ResourceHold.Kind != worker.HoldSubscriptionQuota {
		t.Errorf("the hold changed kind: %+v", run.Run.ResourceHold)
	}
	if run.Run.ResourceHold.OwnerAction {
		t.Error("a wait that clears by itself was marked as needing an owner decision")
	}
	// A hold spends no provider recovery allowance and schedules no retry.
	if run.Run.ProviderFailures != 0 || !run.Run.RetryAt.IsZero() {
		t.Errorf("a hold was recorded as a provider failure: %+v", run.Run)
	}
	// Nothing was asked of a model, so nothing was consumed.
	if run.Run.Usage.InputTokens != 0 || run.Run.Usage.UnknownCalls != 0 {
		t.Errorf("a refused turn was accounted for: %+v", run.Run.Usage)
	}
	// And it is resumable: the hold clears and the same assignment continues.
	h.hold(nil)
	if code := request(t, h.broker, "/runs/"+id+"/resume", "resume-one", map[string]string{"instruction": "Continue"}).Code; code != 200 {
		t.Fatal("a self-clearing wait refused an explicit resume", code)
	}
	resumed := h.await(t, id, 90*time.Second, settled...)
	if resumed.Run.Status != "completed" {
		t.Fatalf("the assignment did not continue after its hold cleared: %q %s", resumed.Run.Status, resumed.Run.Summary)
	}
}

// Direction that arrives before a worker starts is delivered with its first
// turn, and is not dequeued until the harness has actually taken it.
func TestQueuedDirectionIsDeliveredWithTheFirstTurn(t *testing.T) {
	h := newHarness(t, "claude", []map[string]any{turnScript(
		tool("write_file", map[string]any{"path": "prompt.txt", "content": "recorded"}),
		tool("finish", map[string]any{"summary": "followed the direction"}),
	)}, map[string]string{"FAKE_CLI_PROMPT_FILE": filepath.Join(t.TempDir(), "prompts.txt")})
	id := h.start(t, "wait for direction")
	if err := h.broker.update(id, func(run *storedRun) error {
		run.Messages = append(run.Messages, "prefer the smaller change")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	run := h.await(t, id, 90*time.Second, settled...)
	if run.Run.Status != "completed" {
		t.Fatalf("unexpected status %q: %s", run.Run.Status, run.Run.Summary)
	}
	if run.InFlight != nil {
		t.Errorf("delivered direction was left in flight: %+v", run.InFlight)
	}
	if len(run.Messages) != 0 {
		t.Errorf("direction was left queued after delivery: %v", run.Messages)
	}
	if !run.Briefed {
		t.Error("the assignment was never recorded as briefed")
	}
	prompts, err := os.ReadFile(os.Getenv("FAKE_CLI_PROMPT_FILE"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(prompts), "prefer the smaller change") {
		t.Errorf("the direction never reached the harness: %q", prompts)
	}
	if strings.Count(string(prompts), "prefer the smaller change") != 1 {
		t.Errorf("the direction was said more than once: %q", prompts)
	}
}

// Direction that arrives mid-turn reaches the worker without waiting for a turn
// that might run for an hour, and it is said exactly once.
func TestDirectionReachesALiveTurnExactlyOnce(t *testing.T) {
	for _, engine := range []string{"claude", "codex"} {
		t.Run(engine, func(t *testing.T) {
			prompts := filepath.Join(t.TempDir(), "prompts.txt")
			// Codex steers the turn that is running, so its first turn carries on to
			// the report. Claude cannot, so its turn is stopped and a second one
			// begins with the direction. Both end with the worker reporting.
			held := []map[string]any{map[string]any{"hold": true}}
			turns := []map[string]any{
				turnScript(held...),
				turnScript(tool("finish", map[string]any{"summary": "took the new direction"})),
			}
			if engine == "codex" {
				turns = []map[string]any{turnScript(append(held, tool("finish", map[string]any{"summary": "took the new direction"}))...)}
			}
			h := newHarness(t, engine, turns, map[string]string{"FAKE_CLI_PROMPT_FILE": prompts})
			id := h.start(t, "work until redirected")
			h.awaitRunning(t, id)
			if err := h.broker.update(id, func(run *storedRun) error {
				run.Messages = append(run.Messages, "stop and check the schema first")
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			run := h.await(t, id, 120*time.Second, settled...)
			if run.Run.Status != "completed" {
				t.Fatalf("a steered worker ended %q: %s", run.Run.Status, run.Run.Summary)
			}
			text, err := os.ReadFile(prompts)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(text), "stop and check the schema first") {
				t.Fatalf("the direction never reached the harness: %q", text)
			}
			if got := strings.Count(string(text), "stop and check the schema first"); got != 1 {
				t.Errorf("the direction was delivered %d times: %q", got, text)
			}
			if run.Steering != nil || len(run.Messages) != 0 {
				t.Errorf("direction was left undelivered after the worker acted on it: %+v %v", run.Steering, run.Messages)
			}
			var delivered bool
			for _, entry := range run.Run.Activity {
				if entry.Kind == "steering" && (entry.Status == "delivered" || entry.Status == "retained") {
					delivered = true
				}
			}
			if !delivered {
				t.Errorf("the delivery was not recorded: %+v", run.Run.Activity)
			}
		})
	}
}

// Direction delivered by stopping a turn must not start the replacement itself.
// A budget that is already spent has to stop the worker before another turn
// runs, not after — otherwise the turn the owner's limit was meant to prevent
// has already happened, and its tools with it.
func TestRedirectingAnExhaustedWorkerStartsNoFurtherTurn(t *testing.T) {
	prompts := filepath.Join(t.TempDir(), "prompts.txt")
	h := newHarness(t, "claude", []map[string]any{
		turnScript(map[string]any{"hold": true}),
		turnScript(tool("finish", map[string]any{"summary": "must never run"})),
	}, map[string]string{"FAKE_CLI_PROMPT_FILE": prompts})
	id := h.start(t, "work until redirected")
	h.awaitRunning(t, id)
	// The budget is exhausted, and direction arrives at the same moment.
	h.mu.Lock()
	h.budget = 1
	h.mu.Unlock()
	if err := h.broker.update(id, func(run *storedRun) error {
		run.Messages = append(run.Messages, "change course")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	run := h.await(t, id, 120*time.Second, settled...)
	if run.Run.Status != "usage_wait" {
		t.Fatalf("an exhausted worker did not stop for its budget: %q %s", run.Run.Status, run.Run.Summary)
	}
	// Either the budget itself stopped it, or the interrupted turn's consumption
	// could not be established and that stopped it first. Both are owner
	// decisions, and both must happen before another turn, not after it.
	hold := run.Run.ResourceHold
	if hold == nil || !hold.OwnerAction {
		t.Fatalf("the stop was not recorded as an owner decision: %+v", hold)
	}
	if hold.Kind != worker.HoldTokenBudget && hold.Kind != worker.HoldUsageUnknown {
		t.Fatalf("an exhausted worker stopped for the wrong reason: %+v", hold)
	}
	text, err := os.ReadFile(prompts)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(text), "change course") {
		t.Fatalf("an unadmitted turn was started with the new direction: %q", text)
	}
	if strings.Count(string(text), "\n") > 1 {
		t.Fatalf("more than the first turn was started: %q", text)
	}
	// The direction is not lost. It is durable, and confirmed never sent, so the
	// next admitted turn says it.
	queued := append([]string{}, run.Messages...)
	for _, record := range []*inFlightInput{run.Steering, run.InFlight} {
		if record == nil {
			continue
		}
		if record.Delivery == deliveryUnknown {
			t.Fatalf("direction that was never sent was recorded as uncertain: %+v", record)
		}
		queued = append(queued, record.Text)
	}
	if !strings.Contains(strings.Join(queued, "\n"), "change course") {
		t.Fatalf("the direction was lost when the worker was stopped: %+v %+v %v", run.Steering, run.InFlight, run.Messages)
	}
}

// An owner pause stops the work at a point it can resume from, without
// destroying the session or letting another tool run.
func TestOwnerPauseCheckpointsALiveTurn(t *testing.T) {
	h := newHarness(t, "claude", []map[string]any{
		turnScript(map[string]any{"hold": true}),
	}, nil)
	id := h.start(t, "work until paused")
	h.awaitRunning(t, id)
	if code := request(t, h.broker, "/runs/"+id+"/pause", "pause-one", struct{}{}).Code; code != 200 {
		t.Fatal("pause refused", code)
	}
	run := h.await(t, id, 120*time.Second, settled...)
	if run.Run.Status != "paused" {
		t.Fatalf("a pause did not checkpoint the worker: %q %s", run.Run.Status, run.Run.Summary)
	}
	if run.Session == nil {
		t.Error("pausing discarded the coding session")
	}
	var checkpointed bool
	for _, entry := range run.Run.Activity {
		if entry.Kind == "turn" && entry.Status == "checkpointed" {
			checkpointed = true
		}
	}
	if !checkpointed {
		t.Errorf("the checkpoint was not recorded: %+v", run.Run.Activity)
	}
}

// A turn ending closes the daemon's tool channel, and it stays closed until an
// admitted turn opens it again. A harness that asks for a tool in between must
// be refused — otherwise a write lands after the assignment was checkpointed,
// and the evidence collected in between describes a workspace that was still
// moving.
//
// This drives one turn against the real harness transport and then holds the
// session open, because the question is what the channel does while the session
// is alive and quiet. Waiting for the whole assignment to finish would close the
// session first, and a late call that never arrived proves nothing either way.
// The library proves the same barrier deterministically in
// TestATurnEndingClosesToolAdmissionWithoutCancellingWhatRuns; what is being
// established here is that the application wires it up.
func TestNoToolRunsBetweenATerminalTurnAndTheNextAdmittedOne(t *testing.T) {
	gate := filepath.Join(t.TempDir(), "gate")
	outcome := filepath.Join(t.TempDir(), "trailing")
	h := newHarness(t, "claude", []map[string]any{
		// The turn ends, then the harness asks for one more tool anyway.
		turnScript(map[string]any{"trailing": "write_file"}),
	}, map[string]string{
		"FAKE_CLI_TRAILING_GATE":   gate,
		"FAKE_CLI_TRAILING_RESULT": outcome,
	})
	runner, id := h.session(t, "end a turn and keep calling")
	defer runner.close()

	prompt, err := h.broker.nextTurnInput(id)
	if err != nil || prompt == "" {
		t.Fatalf("no opening prompt: %q %v", prompt, err)
	}
	if !runner.turn(t.Context(), prompt) {
		run, _ := h.broker.snapshot(id)
		t.Fatalf("the turn did not complete: %q %s", run.Run.Status, run.Run.Summary)
	}
	// The turn is over and its tools are settled. The session is still open, so
	// the harness is still there to ask — which is the situation being tested.
	if h.broker.snapshotMust(t, id).Run.Status != "running" {
		t.Fatal("the assignment ended before the late call could be made")
	}

	// Now let the harness make its late request.
	if err = os.WriteFile(gate, []byte("go"), 0600); err != nil {
		t.Fatal(err)
	}
	recorded := awaitFileContents(t, outcome, 30*time.Second)
	if !strings.HasPrefix(recorded, "refused:") {
		t.Fatalf("a tool call after the turn ended was not refused: %q", recorded)
	}
	if !strings.Contains(recorded, "paused") {
		t.Errorf("the refusal did not say the channel was closed: %q", recorded)
	}
	// It was refused, not merely unlucky: nothing reached the workspace.
	if _, err = os.Stat(filepath.Join(h.runner.workspace(), "after-the-turn.txt")); err == nil {
		t.Fatal("a tool call after the turn ended wrote to the workspace")
	}
	// And the daemon recorded it. There is no turn left to observe it on — which
	// is the point — so it lands in the daemon's own diagnostics.
	if log := h.diagnostics.String(); !strings.Contains(log, "tool_refused") || !strings.Contains(log, "write_file") {
		t.Errorf("the late call was not reported anywhere: %s", log)
	}
}

// awaitFileContents waits for a file the synthetic harness writes when it has
// done something, so a test can wait for an event rather than for a duration.
func awaitFileContents(t *testing.T, path string, within time.Duration) string {
	t.Helper()
	for deadline := time.Now().Add(within); time.Now().Before(deadline); {
		if raw, err := os.ReadFile(path); err == nil && len(raw) > 0 {
			return strings.TrimSpace(string(raw))
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the harness never recorded an outcome at %s", path)
	return ""
}

// A session outlives the call that opened it. Binding the harness process to the
// startup handshake killed every worker the moment it was ready.
func TestSessionOutlivesTheCallThatOpenedIt(t *testing.T) {
	h := newHarness(t, "claude", []map[string]any{
		turnScript(map[string]any{"hold": true}),
	}, nil)
	id := h.start(t, "hold a turn open")
	h.awaitRunning(t, id)
	// Well past the point where a handshake-scoped context would have ended it.
	time.Sleep(3 * time.Second)
	run, err := h.broker.snapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	if run.Run.Status != "running" || run.SessionPhase != phaseOpen {
		t.Fatalf("the session died after open returned: %q/%q %s", run.Run.Status, run.SessionPhase, run.Run.Summary)
	}
}

// A harness that fails its turn stops the assignment with the harness's own
// classification, and never with an automatic retry: by the time a native turn
// fails, its tools may already have run.
func TestNativeTurnFailureKeepsItsCodeAndIsNotRetried(t *testing.T) {
	h := newHarness(t, "claude", []map[string]any{
		turnScript(map[string]any{"fail": true}),
	}, nil)
	id := h.start(t, "fail the turn")
	run := h.await(t, id, 90*time.Second, settled...)
	if run.Run.Status != "blocked" {
		t.Fatalf("a failed native turn did not stop the assignment: %q %s", run.Run.Status, run.Run.Summary)
	}
	if run.Run.ModelFailureCode == "" || run.Run.ModelFailureCode == "untyped_error" {
		t.Errorf("the harness's own failure code was lost: %+v", run.Run)
	}
	if run.Run.ModelFailureEngine != "claude" {
		t.Errorf("the failure lost its engine: %q", run.Run.ModelFailureEngine)
	}
	if !run.Run.RetryAt.IsZero() || run.Run.ProviderFailures != 0 {
		t.Errorf("a native turn was scheduled for automatic retry: %+v", run.Run)
	}
	if run.Run.ModelFailureEvidence != evidenceNativeHarness {
		t.Errorf("the failure was not attributed to the harness: %q", run.Run.ModelFailureEvidence)
	}
}

// An assignment from the previous contract is not resumed automatically, keeps
// everything it produced, and is described honestly.
func TestLegacyAssignmentMigratesOnExplicitResumeOnly(t *testing.T) {
	h := newHarness(t, "claude", []map[string]any{turnScript(
		tool("finish", map[string]any{"summary": "continued the earlier attempt"}),
	)}, nil)
	// An assignment saved by the previous contract, exactly as recovery would
	// find it: a transcript, a workspace with real changes, and no session. It is
	// inserted rather than dispatched, because dispatching it is what the owner
	// is about to decide.
	legacy := t.TempDir()
	if err := os.WriteFile(filepath.Join(legacy, "changed.txt"), []byte("new content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	id := h.insert(t, func(run *storedRun) {
		run.Run.Status = "blocked"
		run.Run.Summary = "Stopped under the previous worker contract"
		run.Transcript = []modelMessage{{Role: "assistant", Content: "earlier attempt"}}
		run.WorkDir = legacy
		run.Baseline = map[string][]byte{"changed.txt": []byte("old content\n")}
		run.Commands = []commandRecord{{Command: "go test ./...", Success: false, Output: "FAIL"}}
	})
	// Queued by the owner. The first dispatch refuses to continue it silently.
	if code := request(t, h.broker, "/runs/"+id+"/resume", "resume-one", map[string]string{"instruction": "Continue"}).Code; code != 200 {
		t.Fatal("resume refused", code)
	}
	run := h.await(t, id, 90*time.Second, settled...)
	if run.Run.Status != "blocked" {
		t.Fatalf("a legacy assignment was continued without saying so: %q %s", run.Run.Status, run.Run.Summary)
	}
	if !strings.Contains(run.Run.Summary, "previous worker contract") {
		t.Errorf("the migration did not explain itself: %q", run.Run.Summary)
	}
	// The brief describes what actually changed, from preserved evidence.
	brief := handoffBrief(run)
	if !strings.Contains(brief, "changed.txt") {
		t.Errorf("the handover brief did not report the real changes: %s", brief)
	}
	if strings.Contains(brief, "no file changes") {
		t.Errorf("the handover brief invented an empty result: %s", brief)
	}
	if !strings.Contains(brief, "0 succeeded, 1 failed") {
		t.Errorf("the handover brief lost the command evidence: %s", brief)
	}
	// A second, explicit resume performs the handover and the work continues.
	if code := request(t, h.broker, "/runs/"+id+"/resume", "resume-two", map[string]string{"instruction": "Continue"}).Code; code != 200 {
		t.Fatal("the handover refused an explicit resume", code)
	}
	continued := h.await(t, id, 90*time.Second, settled...)
	if continued.Run.Status != "completed" {
		t.Fatalf("the migrated assignment did not continue: %q %s", continued.Run.Status, continued.Run.Summary)
	}
	if !continued.Migrated {
		t.Error("the handover was not recorded, so it could happen again")
	}
	if len(continued.Commands) != 1 {
		t.Errorf("the earlier attempt's command evidence was discarded: %+v", continued.Commands)
	}
}

// session opens one assignment's real coding session and hands back the runner
// driving it, for the cases where the question is what happens between turns
// rather than how an assignment ends. The caller owns closing it.
func (h *harness) session(t *testing.T, task string) (*nativeRunner, string) {
	t.Helper()
	// Its own container directory, since nothing here starts a container.
	h.runner.mu.Lock()
	h.runner.root = t.TempDir()
	h.runner.mu.Unlock()
	id := h.insert(t, func(run *storedRun) {
		run.Run.Status = "running"
		run.Request.Task = task
	})
	toolDir, err := h.broker.toolDirectory(id)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := h.broker.snapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	runner := &nativeRunner{broker: h.broker, id: id, run: stored}
	if err = runner.open(t.Context(), toolDir); err != nil {
		t.Fatalf("the coding session did not open: %v", err)
	}
	return runner, id
}

func (b *Broker) snapshotMust(t *testing.T, id string) storedRun {
	t.Helper()
	run, err := b.snapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

// awaitRunning waits for a worker whose session is open and whose turn is live.
func (h *harness) awaitRunning(t *testing.T, id string) storedRun {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	var last storedRun
	for time.Now().Before(deadline) {
		run, err := h.broker.snapshot(id)
		if err == nil {
			last = run
			if run.SessionPhase == phaseOpen && run.Run.Status == "running" && run.Briefed {
				return run
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the worker never reached a live turn: %q/%q %s", last.Run.Status, last.SessionPhase, last.Run.Summary)
	return last
}
