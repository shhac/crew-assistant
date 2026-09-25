// Package procgroup keeps the daemon's own subprocesses out of reach of the
// terminal it was started from.
//
// A Ctrl-C in a terminal goes to its whole foreground process group. The
// daemon catches its first one to finish the work in progress, but a git
// push or a gh merge started as its child would get the same signal and die
// mid-step, which is the loss the first Ctrl-C promises to avoid. The model
// CLIs already run in groups of their own through the harness.
package procgroup
