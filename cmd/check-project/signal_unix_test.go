//go:build !windows

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/checktest"
	"github.com/shhac/crew-assistant/internal/testutil"
)

func TestSignalRunnerFixture(t *testing.T) {
	if os.Getenv("CREW_SIGNAL_RUNNER") != "1" {
		return
	}
	testutil.StartFixtureGroupGuardian(t)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	binary, _ := os.Executable()
	factory := func(name string, args ...string) *exec.Cmd {
		operation := "vet"
		if name == "go" && args[0] == "test" {
			operation = "hang"
		}
		cmd := exec.Command(binary, "-test.run=^TestCommandFixture$")
		cmd.Env = append(os.Environ(), "CREW_CHECK_FIXTURE="+operation)
		return cmd
	}
	var save func(string, checktest.Report, bool) error
	if path := os.Getenv(checktest.ProgressEnv); path != "" {
		save = func(stage string, report checktest.Report, done bool) error {
			return checktest.SaveProgress(path, checktest.Progress{Invocation: os.Getenv(checktest.InvocationEnv), Stage: stage, Done: done, Report: report})
		}
	}
	os.Exit(runContextProgress(ctx, factory, os.Stdout, os.Stderr, save))
}

func TestSignalsReportIncompleteCoverage(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, sig := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			dir := t.TempDir()
			ready := filepath.Join(dir, "ready")
			outer := filepath.Join(dir, "outer")
			if err := checktest.SaveProgress(outer, checktest.Progress{Invocation: "outer", Stage: "Go tests"}); err != nil {
				t.Fatal(err)
			}
			t.Setenv(checktest.ProgressEnv, outer)
			t.Setenv(checktest.InvocationEnv, "outer")
			before, err := os.ReadFile(outer)
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(binary, "-test.run=^TestSignalRunnerFixture$")
			cmd.Env = append(os.Environ(), "CREW_SIGNAL_RUNNER=1", "CREW_CHECK_READY="+ready, checktest.ProgressEnv+"="+filepath.Join(dir, "fixture"), checktest.InvocationEnv+"=fixture")
			cmd.Env = append(cmd.Env, groupEnv+"=1")
			closeGuard, err := testutil.GuardFixtureGroup(cmd)
			if err != nil {
				t.Fatal(err)
			}
			defer closeGuard()
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill() })
			deadline := time.Now().Add(10 * time.Second)
			for {
				if _, err := os.Stat(ready); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("active stage never started")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("interrupted check passed")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("interrupted child did not settle")
			}
			after, err := os.ReadFile(outer)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("outer checkpoint modified: %s %v", after, err)
			}
			progress, err := checktest.LoadProgress(dir, "fixture", "fixture")
			if err != nil || progress == nil || len(progress.Report.Skips) != 1 {
				t.Fatalf("fixture progress: %+v %v", progress, err)
			}
			for _, want := range []string{"TestRequired", "loopback denied", "skip count: 1", "complete: false", "interrupted during Go tests"} {
				if !strings.Contains(output.String(), want) {
					t.Fatalf("missing %s: %s", want, &output)
				}
			}
		})
	}
}

func TestNestedInterruptionSettlesGrandchild(t *testing.T) {
	// Model an ordinary standalone runner rather than this test's hosted group.
	t.Setenv(groupEnv, "")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	ready, heartbeat := filepath.Join(dir, "ready"), filepath.Join(dir, "heartbeat")
	if path := os.Getenv("CREW_OUTER_HEARTBEAT"); path != "" {
		heartbeat = path
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	factory := func(string, ...string) *exec.Cmd {
		cmd := exec.Command(binary, "-test.run=^TestSignalRunnerFixture$")
		cmd.Env = append(os.Environ(), "CREW_SIGNAL_RUNNER=1", "CREW_CHECK_READY="+ready, "CREW_CHECK_HEARTBEAT="+heartbeat, checktest.ProgressEnv+"="+filepath.Join(dir, "nested"), checktest.InvocationEnv+"=nested")
		closeGuard, err := testutil.GuardFixtureGroup(cmd)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(closeGuard)
		return cmd
	}
	var out, errs bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- runContext(ctx, factory, &out, &errs) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(heartbeat); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("grandchild never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if os.Getenv("CREW_OUTER_HEARTBEAT") != "" {
		select {}
	}
	cancel()
	select {
	case code := <-done:
		if code == 0 {
			t.Fatal("cancelled runner passed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nested runner did not settle")
	}
	before, err := os.ReadFile(heartbeat)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	after, err := os.ReadFile(heartbeat)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("grandchild survived cancellation")
	}
}

func TestFixtureGroupGuardian(t *testing.T) { testutil.FixtureGroupGuardian() }

func TestOuterInterruptionFixture(t *testing.T) {
	if os.Getenv("CREW_OUTER_RUNNER") != "1" {
		return
	}
	testutil.StartFixtureGroupGuardian(t)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	binary, _ := os.Executable()
	factory := func(string, ...string) *exec.Cmd {
		cmd := exec.Command(binary, "-test.run=^TestNestedInterruptionSettlesGrandchild$")
		cmd.Env = os.Environ()
		return cmd
	}
	os.Exit(runContext(ctx, factory, os.Stdout, os.Stderr))
}

func TestEnclosingInterruptionSettlesDetachedFixture(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	heartbeat := filepath.Join(t.TempDir(), "heartbeat")
	cmd := exec.Command(binary, "-test.run=^TestOuterInterruptionFixture$")
	cmd.Env = append(os.Environ(), "CREW_OUTER_RUNNER=1", "CREW_OUTER_HEARTBEAT="+heartbeat, groupEnv+"=1")
	closeGuard, err := testutil.GuardFixtureGroup(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer closeGuard()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(heartbeat); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("detached fixture never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("interrupted enclosing runner passed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("enclosing runner did not settle")
	}
	// The killed test parent cannot execute its t.Cleanup/defer. Its
	// independent fixture group must settle through parent-pipe EOF.
	time.Sleep(200 * time.Millisecond)
	before, err := os.ReadFile(heartbeat)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	after, err := os.ReadFile(heartbeat)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("detached fixture survived enclosing interruption")
	}
}
