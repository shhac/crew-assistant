//go:build !windows

package cli

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// The real process primitives behind the upgrade watchdog: a live child is
// alive with a stable identity until it is killed, and then it is gone.
func TestUpgradeProcessPrimitives(t *testing.T) {
	// A sandbox may refuse to let ps inspect even this process; that is the
	// hosted check's limit (LAH-29), not a defect here.
	if upgradeProcessIdentity(os.Getpid()) == "" {
		t.Skip("required capability process-inspection unavailable: ps cannot inspect processes here")
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
	identity := upgradeProcessIdentity(pid)
	if identity == "" || identity != upgradeProcessIdentity(pid) {
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
	if got := upgradeProcessIdentity(pid); got != "" {
		t.Fatalf("a gone process still has an identity: %q", got)
	}
}
