//go:build !windows

package testutil

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"testing"
)

const fixtureParentFD = "CREW_FIXTURE_PARENT_FD"

// GuardFixtureGroup gives a deliberately isolated fixture a parent-liveness
// pipe. Only its test parent holds the writer, so even SIGKILL closes it.
// Call StartFixtureGroupGuardian in the fixture before spawning descendants.
func GuardFixtureGroup(cmd *exec.Cmd) (func(), error) {
	read, write, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.ExtraFiles = append(cmd.ExtraFiles, read)
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%d", fixtureParentFD, 2+len(cmd.ExtraFiles)))
	return func() { _ = write.Close(); _ = read.Close() }, nil
}

// A separate guardian survives the fixture leader exiting. It inherits only
// the pipe's reader and the fixture's group; it inspects no other processes.
func StartFixtureGroupGuardian(t *testing.T) {
	t.Helper()
	fd := os.Getenv(fixtureParentFD)
	if fd == "" {
		return
	}
	if syscall.Getpgrp() != os.Getpid() {
		t.Fatal("fixture guardian requires an owned process group")
	}
	var number int
	if _, err := fmt.Sscan(fd, &number); err != nil {
		t.Fatal(err)
	}
	read := os.NewFile(uintptr(number), "fixture-parent")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "-test.run=^TestFixtureGroupGuardian$")
	cmd.Env = append(os.Environ(), "CREW_FIXTURE_GUARDIAN=1")
	cmd.ExtraFiles = []*os.File{read}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = read.Close()
}

// Each fixture's test package provides TestFixtureGroupGuardian, which calls
// this function. EOF settles the entire owned group, including this guardian.
func FixtureGroupGuardian() {
	if os.Getenv("CREW_FIXTURE_GUARDIAN") != "1" {
		return
	}
	read := os.NewFile(3, "fixture-parent")
	_, _ = io.Copy(io.Discard, read)
	_ = syscall.Kill(-syscall.Getpgrp(), syscall.SIGKILL)
	os.Exit(1)
}
