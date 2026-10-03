//go:build !windows

package work

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/session"

	"github.com/shhac/crew-assistant/internal/core"
)

func TestDaemonAppFailuresAreReportedAndReclaimed(t *testing.T) {
	for _, scenario := range []string{"setup", "start", "early exit", "readiness", "linux refusal", "open refusal"} {
		t.Run(scenario, func(t *testing.T) {
			lp := testLoop(t)
			lp.appWait = 5 * time.Millisecond
			app := appRun{recipe: &testRecipe, port: 43000, tree: t.TempDir(), release: func() {}}
			fake := &fakeCommands{started: newFakeStarted()}
			switch scenario {
			case "setup":
				fake.result = session.CommandResult{ExitCode: 2, Stderr: "synthetic build failed"}
			case "start":
				fake.startErr = errors.New("synthetic start failed")
			case "early exit":
				fake.started.result = session.CommandResult{ExitCode: 3, Stderr: "synthetic crash"}
				fake.started.once.Do(func() { close(fake.started.done) })
			case "readiness":
				fake.run = func(_ context.Context, req session.CommandRequest) (session.CommandResult, error) {
					if req.Command == testRecipe.Setup {
						return session.CommandResult{}, nil
					}
					return session.CommandResult{ExitCode: 1, Stdout: "synthetic health failed"}, nil
				}
			case "linux refusal":
				fake.startErr = &session.UnsupportedError{Engine: harness.OpenAICompatible, Operation: "start", Code: session.RefusedNotOffered, Capability: harness.Capability{Availability: harness.Unsupported, Reason: "private localhost is unreachable from the host"}}
			}
			lp.commands = func(context.Context, session.CommandSandboxOptions) (commandSandbox, error) {
				if scenario == "open refusal" {
					return nil, errors.New("synthetic sandbox refusal")
				}
				return fake, nil
			}
			app.start(context.Background(), lp, nil)
			defer app.stop()
			if app.running() || app.unavailable == "" || !strings.Contains(appPrompt(app), app.unavailable) {
				t.Fatalf("%+v", app)
			}
			want := map[string]string{"setup": "synthetic build failed", "start": "synthetic start failed", "early exit": "synthetic crash", "readiness": "synthetic health failed", "linux refusal": "private localhost", "open refusal": "synthetic sandbox refusal"}[scenario]
			if !strings.Contains(app.unavailable, want) {
				t.Fatal(app.unavailable)
			}
			if scenario != "open refusal" && !fake.closed {
				t.Fatal("failed app sandbox remained open")
			}
			if len(fake.starts) > 0 && fake.startErr == nil && !fake.started.stopped {
				t.Fatal("failed app not stopped")
			}
		})
	}
}

func TestDaemonAppFailureBecomesQAFinding(t *testing.T) {
	runner := &appRunner{codeRunner: codeRunner{scriptedRunner: scriptedRunner{reviews: []string{pass}}}, qaReply: pass}
	lp, p := qaTeam(t, runner, "codex", core.Browser{On: true}, &testRecipe)
	fake := &fakeCommands{result: session.CommandResult{ExitCode: 7, Stderr: "setup does not build"}}
	lp.commands = func(context.Context, session.CommandSandboxOptions) (commandSandbox, error) { return fake, nil }
	task, _ := lp.Core.QueueTask(context.Background(), p.ID, core.TaskInput{Objective: "Add Feature"})
	task = settleCode(t, lp, task.ID)
	v := verdictBy(task, "Quinn")
	if v.Outcome != core.VerdictPass || len(v.Findings) != 1 || v.Findings[0].Criterion != "Running the app" || !strings.Contains(v.Findings[0].Note, "exit code 7") || !strings.Contains(v.Findings[0].Note, "setup does not build") {
		t.Fatalf("%+v", v)
	}
	if strings.Contains(runner.qa[0].Prompt, "has started it") || !fake.closed || len(lp.ports.held) != 0 {
		t.Fatal("failed app supplied or not reclaimed")
	}
}

func TestAppOutputCannotReadOutsideItsTree(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("synthetic credential"), 0600); err != nil {
		t.Fatal(err)
	}
	tree := t.TempDir()
	path := filepath.Join(tree, ".crew-app.log")
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if got := appLogTail(path); got != "" {
		t.Fatalf("read outside app tree: %q", got)
	}
}
