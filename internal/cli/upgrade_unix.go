//go:build !windows

package cli

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const upgradeListenerEnv = "CREW_UPGRADE_LISTEN_FD"

func inheritedUpgradeListener() (net.Listener, error) {
	value := os.Getenv(upgradeListenerEnv)
	_ = os.Unsetenv(upgradeListenerEnv)
	if value == "" {
		return nil, nil
	}
	fd, err := strconv.Atoi(value)
	if err != nil || fd < 3 {
		return nil, errors.New("invalid upgrade listener descriptor")
	}
	unix.CloseOnExec(fd)
	f := os.NewFile(uintptr(fd), "upgrade-listener")
	defer f.Close()
	return net.FileListener(f)
}

func preserveUpgradeListener(listener net.Listener) (*os.File, error) {
	tcp, ok := listener.(*net.TCPListener)
	if !ok {
		return nil, errors.New("upgrade requires a TCP listener")
	}
	f, err := tcp.File()
	if err != nil {
		return nil, err
	}
	// Fd changes the shared socket to blocking mode while HTTP is serving.
	// SyscallConn preserves its pollable, nonblocking mode.
	raw, err := f.SyscallConn()
	if err == nil {
		err = raw.Control(func(fd uintptr) { unix.CloseOnExec(int(fd)) })
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func upgradeListenerDescriptor(f *os.File) string {
	var value string
	raw, err := f.SyscallConn()
	if err == nil {
		_ = raw.Control(func(fd uintptr) { value = strconv.Itoa(int(fd)) })
	}
	return value
}

func replaceUpgradeProcess(binary string, args []string) error {
	if value := os.Getenv(upgradeListenerEnv); value != "" {
		fd, err := strconv.Atoi(value)
		if err != nil {
			return err
		}
		if _, err = unix.FcntlInt(uintptr(fd), unix.F_SETFD, 0); err != nil {
			return err
		}
		defer unix.CloseOnExec(fd)
	}
	return syscall.Exec(binary, append([]string{binary}, args...), os.Environ())
}

func startUpgradeDetached(binary string, args []string, logPath string) error {
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	if err = log.Chmod(0600); err != nil {
		return err
	}
	// A short-lived launcher reparents the helper before the daemon execs.
	// Setsid alone would leave a child which the replacement cannot reap.
	if info, err := os.Stat(binary); err != nil {
		return err
	} else if info.Mode()&0111 == 0 {
		return errors.New("detached upgrade binary is not executable")
	}
	launchArgs := append([]string{"-c", `"$@" </dev/null &`, "crew-upgrade-detach", binary}, args...)
	cmd := exec.Command("/bin/sh", launchArgs...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout, cmd.Stderr = log, log
	// Inherited dashboard fds belong only to exec, not to watchdog children.
	for _, entry := range os.Environ() {
		if len(entry) >= len(upgradeListenerEnv)+1 && entry[:len(upgradeListenerEnv)+1] == upgradeListenerEnv+"=" {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	return cmd.Run()
}

func upgradePIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
func killUpgradePID(pid int) error { return syscall.Kill(pid, syscall.SIGKILL) }

func protectInheritedUpgradeListener() {
	if fd, err := strconv.Atoi(os.Getenv(upgradeListenerEnv)); err == nil && fd >= 3 {
		unix.CloseOnExec(fd)
	}
}

// Process birth time survives exec but changes when the kernel reuses a PID.
func upgradeProcessIdentity(pid int) string {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/bin/ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}
