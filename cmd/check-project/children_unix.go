//go:build !windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

const groupEnv = "CREW_CHECK_PROCESS_GROUP"

// Nested runners stay in the enclosing check's group, so cancellation settles
// their descendants too without inspecting unrelated processes.
func settleChildren(cmd *exec.Cmd) {
	if os.Getenv(groupEnv) == "1" {
		cmd.Cancel = func() error { return cmd.Process.Kill() }
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Env = append(cmd.Env, groupEnv+"=1")
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
