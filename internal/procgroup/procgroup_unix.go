//go:build unix

package procgroup

import (
	"os/exec"
	"syscall"
)

// Detach starts cmd in a process group of its own. For a command with a
// context, cancelling it ends the whole group, so nothing it started
// outlives it; exec refuses a Cancel on a command without one.
func Detach(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	if cmd.Cancel != nil {
		cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	}
}
