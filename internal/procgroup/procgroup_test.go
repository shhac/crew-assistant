//go:build unix

package procgroup

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A detached command leads a group of its own, and cancelling it ends what
// it started too.
func TestDetachedCommandsAreTheirOwnGroupAndDieWhole(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child")
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "sh", "-c", "sleep 30 & echo $! > "+pidFile+"; wait")
	Detach(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if group, err := syscall.Getpgid(cmd.Process.Pid); err != nil || group != cmd.Process.Pid || group == syscall.Getpgrp() {
		t.Fatalf("group %d, err %v", group, err)
	}
	var child int
	for deadline := time.Now().Add(5 * time.Second); child == 0 && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		data, _ := os.ReadFile(pidFile)
		child, _ = strconv.Atoi(strings.TrimSpace(string(data)))
	}
	if child == 0 {
		t.Fatal("the child never started")
	}
	cancel()
	_ = cmd.Wait()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if syscall.Kill(child, 0) != nil {
			return
		}
	}
	t.Fatal("a process the command started outlived it")
}
