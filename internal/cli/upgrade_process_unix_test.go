//go:build !windows

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Exercise native lifecycle primitives on an owned child. Identity output is
// synthetic: hosted sandboxes need not grant ps access to process metadata.
func TestUpgradeProcessPrimitives(t *testing.T) {
	ps := filepath.Join(t.TempDir(), "ps")
	if err := os.WriteFile(ps, []byte("#!/bin/sh\n[ \"$#\" -eq 4 ] && [ \"$1\" = '-o' ] && [ \"$2\" = 'lstart=' ] && [ \"$3\" = '-p' ] || exit 2\nkill -0 \"$4\" 2>/dev/null || exit 1\nprintf '  synthetic birth time  \\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	identityOf := func(pid int) string { return upgradeProcessIdentityWithCommand(pid, ps) }
	if got := identityOf(os.Getpid()); got != "synthetic birth time" {
		t.Fatalf("identity output not trimmed: %q", got)
	}
	if upgradePIDAlive(0) || upgradePIDAlive(-1) {
		t.Fatal("a non-positive PID was alive")
	}
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	pid := child.Process.Pid
	if !upgradePIDAlive(pid) {
		t.Fatal("a running child is not alive")
	}
	identity := identityOf(pid)
	if identity == "" || identity != identityOf(pid) {
		t.Fatalf("unstable identity %q", identity)
	}
	if err := killUpgradePID(pid); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	deadline := time.Now().Add(5 * time.Second)
	for upgradePIDAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if upgradePIDAlive(pid) {
		t.Fatal("a killed and reaped child is still alive")
	}
	if got := identityOf(pid); got != "" {
		t.Fatalf("a gone process still has an identity: %q", got)
	}
}
