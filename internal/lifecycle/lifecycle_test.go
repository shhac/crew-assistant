package lifecycle

import (
	"context"
	"os"
	"slices"
	"syscall"
	"testing"
	"time"
)

func ended(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	case <-time.After(time.Second):
		return false
	}
}

func TestTheFirstSignalStopsNewWorkAndTheSecondEndsIt(t *testing.T) {
	signals := make(chan os.Signal, 2)
	var said []string
	sayings := make(chan string, 2)
	stop, cancelAll := Watch(context.Background(), signals, func(s string) { sayings <- s })
	defer cancelAll()
	if stop.Stopping() {
		t.Fatal("stopping before any signal")
	}
	signals <- os.Interrupt
	if !ended(stop.Graceful) || !stop.Stopping() {
		t.Fatal("the first signal didn't stop new work")
	}
	said = append(said, <-sayings)
	select {
	case <-stop.Force.Done():
		t.Fatal("the first signal ended the work in progress")
	case <-time.After(50 * time.Millisecond):
	}
	signals <- syscall.SIGTERM
	if !ended(stop.Force) {
		t.Fatal("the second signal didn't end the work in progress")
	}
	said = append(said, <-sayings)
	if len(said) != 2 || said[0] == said[1] {
		t.Fatalf("said %q", said)
	}
}

func TestTheParentEndingEndsBoth(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stop, cancelAll := Watch(ctx, make(chan os.Signal), func(string) {})
	defer cancelAll()
	cancel()
	if !ended(stop.Graceful) || !ended(stop.Force) {
		t.Fatal("a cancelled parent left the daemon running")
	}
	stop, cancelAll = Watch(context.Background(), make(chan os.Signal), func(string) {})
	cancelAll()
	if !ended(stop.Graceful) || !ended(stop.Force) {
		t.Fatal("cancelAll left the daemon running")
	}
}

// launchd, systemd and a reboot send SIGTERM; it must drain like Ctrl-C.
func TestTerminationDrainsLikeAnInterrupt(t *testing.T) {
	has := func(want os.Signal) bool {
		return slices.ContainsFunc(Signals, func(s os.Signal) bool { return s == want })
	}
	if !has(os.Interrupt) || !has(syscall.SIGTERM) {
		t.Fatalf("signals %v", Signals)
	}
}
