// check-project keeps the coverage summary at the end of the complete check.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/shhac/crew-assistant/internal/checktest"
)

func main() { os.Exit(run()) }
func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var save func(string, checktest.Report, bool) error
	if path := os.Getenv(checktest.ProgressEnv); path != "" {
		invocation := os.Getenv(checktest.InvocationEnv)
		if invocation == "" {
			fmt.Fprintln(os.Stderr, "check progress has no invocation identity")
			return 1
		}
		save = func(stage string, report checktest.Report, done bool) error {
			return checktest.SaveProgress(path, checktest.Progress{Invocation: invocation, Stage: stage, Report: report, Done: done})
		}
	}
	return runContextProgress(ctx, exec.Command, os.Stdout, os.Stderr, save)
}

func runWith(newCommand func(string, ...string) *exec.Cmd, stdout, stderr io.Writer) int {
	return runContext(context.Background(), newCommand, stdout, stderr)
}

func runContext(ctx context.Context, newCommand func(string, ...string) *exec.Cmd, stdout, stderr io.Writer) int {
	return runContextProgress(ctx, newCommand, stdout, stderr, nil)
}

func runContextProgress(ctx context.Context, newCommand func(string, ...string) *exec.Cmd, stdout, stderr io.Writer, save func(string, checktest.Report, bool) error) (code int) {
	factory := newCommand
	newCommand = func(name string, args ...string) *exec.Cmd {
		cmd := factory(name, args...)
		managed := exec.CommandContext(ctx, cmd.Path, cmd.Args[1:]...)
		managed.Env, managed.Dir = cmd.Env, cmd.Dir
		managed.ExtraFiles = cmd.ExtraFiles
		managed.WaitDelay = 2 * time.Second
		settleChildren(managed)
		return managed
	}
	report := checktest.Report{}
	stage := "vet"
	checkpoint := func(done bool) error {
		if save == nil {
			return nil
		}
		return save(stage, report, done)
	}
	destination, errorsDestination := stdout, stderr
	logTail, errorTail := &checktest.Tail{Limit: 5 << 10}, &checktest.Tail{Limit: 4 << 10}
	stdout, stderr = logTail, errorTail
	defer func() {
		if ctx.Err() != nil {
			report.Complete = false
			code = 1
			fmt.Fprintf(errorsDestination, "Project check interrupted during %s: %v; coverage incomplete\n", stage, ctx.Err())
		}
		if err := checkpoint(true); err != nil {
			code = 1
			report.Complete = false
			fmt.Fprintln(errorsDestination, "save check progress:", err)
		}
		fmt.Fprint(destination, logTail.String())
		fmt.Fprint(errorsDestination, errorTail.String())
		fmt.Fprintf(destination, "Project check terminal stage: %s\n", stage)
		report.Write(destination)
	}()
	if err := checkpoint(false); err != nil {
		fmt.Fprintln(stderr, "save check progress:", err)
		return 1
	}
	command := func(name string, args ...string) error {
		if err := checkpoint(false); err != nil {
			return fmt.Errorf("save check progress: %w", err)
		}
		cmd := newCommand(name, args...)
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		return cmd.Run()
	}
	if err := command("go", "vet", "./..."); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	stage = "Go tests"
	timeout := os.Getenv("TEST_TIMEOUT")
	if timeout == "" {
		timeout = "30m"
	}
	if err := checkpoint(false); err != nil {
		fmt.Fprintln(stderr, "save check progress:", err)
		return 1
	}
	cmd := newCommand("go", "test", "-json", "./...", "-count=1", "-timeout", timeout)
	cmd.Stderr = stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err = cmd.Start(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	report, err = checktest.ReadProgress(pipe, runtime.GOOS, func(observed checktest.Report) error {
		report = observed
		return checkpoint(false)
	})
	if err != nil {
		_ = pipe.Close()
		_ = cmd.Cancel()
	}
	waitErr := cmd.Wait()
	if err != nil {
		fmt.Fprintln(stderr, err)
	}
	if waitErr != nil {
		fmt.Fprintln(stderr, waitErr)
	}
	if err != nil || waitErr != nil || report.Failed {
		return 1
	}
	for _, target := range []string{"check-ui-types", "check-ui-bundle", "check-ui-tests"} {
		stage = "frontend " + target
		if err := command("make", target); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	stage = "complete"
	return 0
}
