# Self-upgrade recovery boundaries — 2026-10-04

Version pin: nothing to pin: code-internal. This snapshot described the recovery
boundary corrections following the self-upgrade recovery snapshot.

Backup and installation failures committed their restart obligation in the same
durable transition as the failure outcome. The restored receiver retained that
obligation until its API answered, even when its watchdog could not be launched;
it reported the missing helper rather than stopping otherwise usable recovery.
Recovery starts used the recorded dashboard address, including unpinned failures
restarted without an inherited socket.

Listener preservation used SyscallConn throughout, retaining nonblocking socket
mode while the dashboard continued accepting connections during installation.
State and configuration destinations were canonicalized before state locking,
journal lookup and restoration. File and directory aliases therefore selected
the same protected state, journal and lock, and restoration replaced the real
destination instead of its symlink.

An owner stop during incomplete restoration left the restoration obligation
journaled, but durably suppressed process restart. The watchdog could finish
restoring once it acquired exclusive state ownership without killing or restarting
the stopped daemon. A later explicit start still enforced the rollback pin.
An unavailable process identity was treated as unknown while the PID was alive;
the watchdog retried inspection instead of claiming a premature rollback.

Regression tests covered the first failure transition, partial restoration with
an owner stop, transient identity lookup failure, aliases during pinned and
rolling-back startup, listener accept/shutdown, and restored API startup with a
persistent helper launch failure. Dashboard progress and styling remained in the
separate dashboard task.
