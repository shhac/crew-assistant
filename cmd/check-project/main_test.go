package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/checktest"
)

func TestCommandFixture(t *testing.T) {
	operation := os.Getenv("CREW_CHECK_FIXTURE")
	if operation == "" {
		return
	}
	switch operation {
	case "hang":
		fmt.Print("{\"Action\":\"output\",\"Package\":\"fixture\",\"Test\":\"TestRequired\",\"Output\":\"required capability loopback denied\\n\"}\n{\"Action\":\"skip\",\"Package\":\"fixture\",\"Test\":\"TestRequired\"}\n")
		os.WriteFile(os.Getenv("CREW_CHECK_READY"), []byte("ready"), 0600)
		for {
			if path := os.Getenv("CREW_CHECK_HEARTBEAT"); path != "" {
				if err := os.WriteFile(path, []byte(time.Now().String()), 0600); err != nil {
					t.Fatal(err)
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
	case "skip":
		fmt.Print("{\"Action\":\"output\",\"Package\":\"fixture\",\"Test\":\"TestRequired\",\"Output\":\"required capability fixture execution denied\\n\"}\n{\"Action\":\"skip\",\"Package\":\"fixture\",\"Test\":\"TestRequired\"}\n{\"Action\":\"pass\",\"Package\":\"fixture\"}\n")
	case "mixed":
		enc := json.NewEncoder(os.Stdout)
		for _, event := range []checktest.Event{
			{Action: "output", Package: "fixture", Test: "TestRequired", Output: "required capability fixture execution denied\n"},
			{Action: "skip", Package: "fixture", Test: "TestRequired"},
			{Action: "output", Package: "fixture", Test: "TestBad", Output: strings.Repeat("assertion failure\n", 10000)},
			{Action: "fail", Package: "fixture", Test: "TestBad"},
			{Action: "fail", Package: "fixture"},
		} {
			if err := enc.Encode(event); err != nil {
				t.Fatal(err)
			}
		}
	case "pass":
		fmt.Print("{\"Action\":\"output\",\"Package\":\"fixture\",\"Output\":\"ok\\n\"}\n{\"Action\":\"pass\",\"Package\":\"fixture\"}\n")
	case "frontend":
		fmt.Print(strings.Repeat("frontend output\n", 4000))
	case "fail":
		fmt.Fprintln(os.Stderr, "synthetic stage failure")
		os.Exit(7)
	}
	os.Exit(0)
}

func TestCheckEntryPointAndTerminalSummary(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, goResult, failStage string
		want                      int
		stage                     string
	}{
		{"required skip", "skip", "", 1, "Go tests"},
		{"skip and verbose failure", "mixed", "", 1, "Go tests"},
		{"complete", "pass", "", 0, "complete"},
		{"frontend failure", "pass", "frontend", 1, "frontend check-ui-types"},
		{"vet failure", "pass", "vet", 1, "vet"},
		{"Go failure", "fail", "", 1, "Go tests"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			factory := func(name string, args ...string) *exec.Cmd {
				operation := "frontend"
				if name == "go" {
					if args[0] == "vet" {
						operation = "vet"
					} else {
						operation = tc.goResult
					}
				}
				if operation == tc.failStage {
					operation = "fail"
				}
				cmd := exec.Command(binary, "-test.run=^TestCommandFixture$")
				cmd.Env = append(os.Environ(), "CREW_CHECK_FIXTURE="+operation)
				return cmd
			}
			var out, stderr bytes.Buffer
			if got := runWith(factory, &out, &stderr); got != tc.want {
				t.Fatalf("exit=%d: %s %s", got, &out, &stderr)
			}
			tail := out.String()
			if len(tail) > 24000 {
				tail = tail[len(tail)-24000:]
			}
			if !strings.Contains(tail, "Project check terminal stage: "+tc.stage) || !strings.Contains(tail, "Go check skip count:") {
				t.Fatal("terminal summary lost")
			}
			if (tc.goResult == "skip" || tc.goResult == "mixed") && (!strings.Contains(tail, "TestRequired") || !strings.Contains(tail, "fixture execution denied") || !strings.Contains(tail, "required-skip failures: 1")) {
				t.Fatal(tail)
			}
		})
	}
}

// Cancel while go test is active: retain observed skips and settle the child.
func TestInterruptedActiveStage(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ready := t.TempDir() + "/ready"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	factory := func(name string, args ...string) *exec.Cmd {
		operation := "vet"
		if name == "go" && args[0] == "test" {
			operation = "hang"
		}
		cmd := exec.Command(binary, "-test.run=^TestCommandFixture$")
		cmd.Env = append(os.Environ(), "CREW_CHECK_FIXTURE="+operation, "CREW_CHECK_READY="+ready)
		return cmd
	}
	var out, errs bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- runContext(ctx, factory, &out, &errs) }()
	deadline := time.After(10 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("child did not start")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	select {
	case code := <-done:
		if code == 0 {
			t.Fatal("interrupted check passed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child did not settle")
	}
	for _, want := range []string{"TestRequired", "skip count: 1", "complete: false", "terminal stage: Go tests"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %s: %s", want, &out)
		}
	}
	if !strings.Contains(errs.String(), "coverage incomplete") {
		t.Fatal(&errs)
	}
}
