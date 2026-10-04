//go:build !windows

package work

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/checktest"
	"github.com/shhac/crew-assistant/internal/testutil"
	"github.com/shhac/lib-agent-harness/process"
	"github.com/shhac/lib-agent-harness/sandbox"
)

func TestHostedCoverageProducer(t *testing.T) {
	if os.Getenv("CREW_COVERAGE_PRODUCER") != "1" {
		return
	}
	testutil.StartFixtureGroupGuardian(t)
	path := os.Getenv(checktest.ProgressEnv)
	defer os.WriteFile(filepath.Join(filepath.Dir(path), "producer-defer"), []byte("ran"), 0600)
	input := "{\"Action\":\"output\",\"Package\":\"fixture\",\"Test\":\"TestRequired\",\"Output\":\"required capability fixture execution denied\\n\"}\n{\"Action\":\"skip\",\"Package\":\"fixture\",\"Test\":\"TestRequired\"}\n"
	_, err := checktest.ReadProgress(strings.NewReader(input), "darwin", func(r checktest.Report) error {
		return checktest.SaveProgress(path, checktest.Progress{Invocation: os.Getenv(checktest.InvocationEnv), Stage: "Go tests", Report: r})
	})
	if err == nil {
		t.Fatal("fixture stream should be incomplete")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(binary, "-test.run=^TestHostedHeartbeatProducer$")
	child.Env = append(os.Environ(), "CREW_COVERAGE_HEARTBEAT=1")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	for {
		time.Sleep(time.Second)
	}
}

func TestHostedHeartbeatProducer(t *testing.T) {
	if os.Getenv("CREW_COVERAGE_HEARTBEAT") != "1" {
		return
	}
	path := os.Getenv(checktest.ProgressEnv)
	for {
		if err := os.WriteFile(filepath.Join(filepath.Dir(path), "heartbeat"), []byte(time.Now().String()), 0600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The injected command boundary uses the published harness's actual SIGKILL
// operation. CheckRuns must recover the record before its copy is removed,
// including when turn cleanup has ended the tool that would return the result.
func TestHostedCheckRecoversAfterForcedTermination(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"timeout", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			lp, m, source := checkFixture(t)
			var dir string
			observed := make(chan struct{})
			lp.commands = func(_ context.Context, opts sandbox.Options) (commandSandbox, error) {
				dir = opts.WorkDir
				return &fakeCommands{run: func(ctx context.Context, _ sandbox.CommandRequest) (sandbox.CommandResult, error) {
					commandCtx := ctx
					if mode == "timeout" && os.Getenv("CREW_OUTER_COVERAGE_HEARTBEAT") == "" {
						var cancel context.CancelFunc
						commandCtx, cancel = context.WithTimeout(ctx, 3*time.Second)
						defer cancel()
					}
					cmd, child, err := process.Command(commandCtx, binary, "-test.run=^TestHostedCoverageProducer$")
					if err != nil {
						return sandbox.CommandResult{}, err
					}
					defer child.Close()
					cmd.Env = append(opts.Env, "CREW_COVERAGE_PRODUCER=1", "TMPDIR="+dir)
					cmd.Env = append(cmd.Env, "CREW_OUTER_COVERAGE_HEARTBEAT="+os.Getenv("CREW_OUTER_COVERAGE_HEARTBEAT"))
					closeGuard, err := testutil.GuardFixtureGroup(cmd)
					if err != nil {
						return sandbox.CommandResult{}, err
					}
					defer closeGuard()
					cmd.Dir = opts.WorkDir
					var output bytes.Buffer
					cmd.Stdout, cmd.Stderr = &output, &output
					done := make(chan error, 1)
					go func() { done <- child.Run() }()
					deadline := time.Now().Add(10 * time.Second)
					for {
						_, err := os.Stat(filepath.Join(dir, "heartbeat"))
						if err == nil {
							break
						}
						if time.Now().After(deadline) {
							child.Stop()
							<-done
							return sandbox.CommandResult{}, fmt.Errorf("coverage producer did not start: %s", &output)
						}
						time.Sleep(10 * time.Millisecond)
					}
					close(observed)
					if os.Getenv("CREW_OUTER_COVERAGE_HEARTBEAT") != "" {
						if err := os.WriteFile(os.Getenv("CREW_OUTER_COVERAGE_HEARTBEAT"), []byte(filepath.Join(dir, "heartbeat")), 0600); err != nil {
							return sandbox.CommandResult{}, err
						}
						select {}
					}
					<-commandCtx.Done()
					child.Stop()
					if err := <-done; err == nil {
						return sandbox.CommandResult{}, fmt.Errorf("forced producer exited successfully")
					}
					if _, err := os.Stat(filepath.Join(dir, "producer-defer")); !os.IsNotExist(err) {
						return sandbox.CommandResult{}, fmt.Errorf("producer defer ran after forced stop")
					}
					before, _ := os.ReadFile(filepath.Join(dir, "heartbeat"))
					time.Sleep(30 * time.Millisecond)
					after, _ := os.ReadFile(filepath.Join(dir, "heartbeat"))
					if string(before) != string(after) {
						return sandbox.CommandResult{}, fmt.Errorf("producer survived containment stop")
					}
					if mode == "cancel" {
						return sandbox.CommandResult{ExitCode: -1}, ctx.Err()
					}
					return sandbox.CommandResult{ExitCode: -1, TimedOut: true}, nil
				}}, nil
			}
			runs := newCheckRuns(lp, m, source, nil)
			defer runs.close()
			var notes []string
			runs.coverageNote = func(s string) { notes = append(notes, s) }
			reply := make(chan string, 1)
			go func() {
				result, err := runs.call(context.Background())
				if err != nil {
					result = err.Error()
				}
				reply <- result
			}()
			select {
			case <-observed:
			case content := <-reply:
				t.Fatal("hosted producer did not observe coverage:", content)
			case <-time.After(15 * time.Second):
				t.Fatal("hosted producer did not observe coverage")
			}
			if mode == "cancel" {
				runs.close()
			}
			var result struct {
				sandbox.CommandResult
				Coverage checkCoveragePage `json:"coverage"`
			}
			content := <-reply
			if err := json.Unmarshal([]byte(content), &result); err != nil {
				t.Fatal(content, err)
			}
			if result.ExitCode == 0 || result.Coverage.Complete || result.Coverage.GoComplete || result.Coverage.Stage != "Go tests" || result.Coverage.SkipCount != 1 || result.Coverage.RequiredSkipFailures != 1 || len(result.Coverage.Skips) != 1 || result.Coverage.Skips[0].Test != "TestRequired" {
				t.Fatalf("lost hosted coverage: %s", content)
			}
			if mode == "timeout" && !result.TimedOut {
				t.Fatal("lost hosted timeout")
			}
			if mode == "cancel" && !strings.Contains(result.Stderr, "Hosted check interrupted: context canceled") {
				t.Fatal("unexpected command failure:", result.Stderr)
			}
			if !strings.Contains(result.Stdout, "fixture execution denied") || !strings.Contains(result.Stdout, "skip count: 1") || !strings.Contains(result.Stdout, "coverage incomplete: true") {
				t.Fatal(content)
			}
			if mode == "cancel" && !strings.Contains(strings.Join(notes, "\n"), "TestRequired") {
				t.Fatal("turn cancellation lost recorded coverage", notes)
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatal("check copy remained after recovery")
			}
		})
	}
}

func TestFixtureGroupGuardian(t *testing.T) { testutil.FixtureGroupGuardian() }

func TestEnclosingCheckSettlesHostedFixtureGroup(t *testing.T) {
	// The actual runner kills its Go-test child before deferred cleanup;
	// process.Command's independent fixture group must still settle.
	dir := t.TempDir()
	runner := filepath.Join(dir, "check-project")
	build := exec.Command("go", "build", "-o", runner, "../../cmd/check-project")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build runner: %s %v", out, err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	shim := "#!/bin/sh\ncase \"$1\" in\nvet|test) exec \"$CREW_OUTER_WORK_BINARY\" -test.run=^TestHostedCheckRecoversAfterForcedTermination$ ;;\n*) exec \"$CREW_OUTER_REAL_GO\" \"$@\" ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte(shim), 0700); err != nil {
		t.Fatal(err)
	}
	heartbeat := filepath.Join(dir, "heartbeat")
	cmd := exec.Command(runner)
	// This nested runner stays in the enclosing check's group. Only the
	// process.Command fixture deliberately detaches, with its own guardian.
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "CREW_CHECK_PROCESS_GROUP=1", "CREW_OUTER_WORK_BINARY="+binary, "CREW_OUTER_COVERAGE_HEARTBEAT="+heartbeat)
	cmd.Env = append(cmd.Env, "CREW_OUTER_REAL_GO="+realGo)
	cmd.Env = append(cmd.Env, checktest.ProgressEnv+"="+filepath.Join(dir, "outer-progress"), checktest.InvocationEnv+"=outer-fixture")
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Signal(syscall.SIGTERM) })
	deadline := time.Now().Add(15 * time.Second)
	for {
		select {
		case err := <-done:
			t.Fatalf("enclosing runner exited before fixture startup: %v\n%s", err, &output)
		default:
		}
		if _, err := os.Stat(heartbeat); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Signal(os.Interrupt)
			select {
			case <-done:
				t.Fatalf("hosted fixture did not start: %s", &output)
			case <-time.After(5 * time.Second):
				t.Fatal("hosted fixture did not start or settle")
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("enclosing check passed after interruption")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("enclosing check did not settle")
	}
	time.Sleep(200 * time.Millisecond)
	location, err := os.ReadFile(heartbeat)
	if err != nil {
		t.Fatal(err)
	}
	heartbeat = string(location)
	before, err := os.ReadFile(heartbeat)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	after, err := os.ReadFile(heartbeat)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("process.Command fixture group survived enclosing interruption")
	}
	if !strings.Contains(output.String(), "interrupted during vet") {
		t.Fatalf("missing enclosing interruption diagnostic: %s", &output)
	}
}

func TestRunCheckPagesKeepAllSkipEvidenceAndStartFresh(t *testing.T) {
	lp, m, source := checkFixture(t)
	runCount := 0
	lp.commands = func(_ context.Context, opts sandbox.Options) (commandSandbox, error) {
		runCount++
		var path, invocation string
		for _, entry := range opts.Env {
			if value, ok := strings.CutPrefix(entry, checktest.ProgressEnv+"="); ok {
				path = value
			}
			if value, ok := strings.CutPrefix(entry, checktest.InvocationEnv+"="); ok {
				invocation = value
			}
		}
		return &fakeCommands{run: func(context.Context, sandbox.CommandRequest) (sandbox.CommandResult, error) {
			p := checktest.Progress{Invocation: invocation, Stage: "complete", Done: true, Report: checktest.Report{Complete: true}}
			if runCount == 1 || runCount == 3 {
				for i := range 150 {
					p.Report.Skips = append(p.Report.Skips, checktest.Skip{Package: "fixture", Test: fmt.Sprintf("TestRequired/%03d", i), Reason: strings.Repeat("capability denied; ", 50)})
				}
				for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
					p.Report.Skips = append(p.Report.Skips, checktest.Skip{Package: "github.com/shhac/crew-assistant/internal/cli", Test: "TestGeneratedShellCompletionSyntax/" + shell, Reason: "script generated; " + shell + " unavailable for syntax check", Exception: "optional shell: " + shell})
				}
			}
			if err := checktest.SaveProgress(path, p); err != nil {
				return sandbox.CommandResult{}, err
			}
			return sandbox.CommandResult{Stdout: strings.Repeat("noisy assertion output\n", 10000)}, nil
		}}, nil
	}
	runs := newCheckRuns(lp, m, source, nil)
	defer runs.close()
	seen := map[string]bool{}
	known := 0
	for {
		content, err := runs.call(context.Background())
		if err != nil || len(content) > 120<<10 {
			t.Fatal(err, len(content))
		}
		var result struct {
			sandbox.CommandResult
			Coverage checkCoveragePage `json:"coverage"`
		}
		if err := json.Unmarshal([]byte(content), &result); err != nil {
			t.Fatal(err)
		}
		if runCount != 1 || result.ExitCode == 0 || result.Coverage.Complete || result.Coverage.SkipCount != 154 || result.Coverage.RequiredSkipFailures != 150 {
			t.Fatal(content)
		}
		for _, skip := range result.Coverage.Skips {
			if seen[skip.Test] || skip.Reason == "" {
				t.Fatal("duplicated or incomplete evidence:", skip)
			}
			seen[skip.Test] = true
			if skip.Exception != "" {
				known++
			}
		}
		if !result.Coverage.More {
			break
		}
	}
	if len(seen) != 154 || known != 4 {
		t.Fatal("lost required or known skips", len(seen), known)
	}
	content, err := runs.call(context.Background())
	if err != nil || runCount != 2 || !strings.Contains(content, `"skip_count":0`) {
		t.Fatal("fresh check reused previous evidence:", content, err)
	}
	var notes []string
	runs.coverageNote = func(s string) { notes = append(notes, s) }
	content, err = runs.call(context.Background())
	if err != nil || !strings.Contains(content, `"more":true`) {
		t.Fatal(content, err)
	}
	runs.close()
	joined := strings.Join(notes, "\n")
	for _, want := range []string{"TestRequired/149", "TestGeneratedShellCompletionSyntax/fish", "observed skip count: 154"} {
		if !strings.Contains(joined, want) {
			t.Fatal("turn end lost unread evidence:", want)
		}
	}
	count := len(notes)
	runs.close()
	if len(notes) != count {
		t.Fatal("repeated cleanup duplicated coverage notes")
	}
}
