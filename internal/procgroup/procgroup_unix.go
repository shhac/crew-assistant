//go:build unix

package procgroup

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// Detach starts cmd in a session of its own, which also makes it the leader
// of its own process group. With no terminal, anything it runs that would
// prompt, such as ssh-keygen asking for a key's passphrase, fails at once
// rather than stopping to wait for a terminal it can't read. For a command
// with a context, cancelling it ends the whole group, so nothing it started
// outlives it; exec refuses a Cancel on a command without one.
func Detach(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
	if cmd.Cancel == nil {
		return
	}
	cmd.Cancel = func() error {
		// A group already gone is a command that finished as it was
		// cancelled, which Wait should report as its own exit.
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
