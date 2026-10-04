# Self-upgrade recovery boundaries — 2026-10-04

Version: nothing to pin: code-internal.

This snapshot recorded the recovery corrections to the 2026-10-03 self-upgrade
implementation. The dashboard progress, banner and Automatic setting remained
in the agreed follow-up.

Incomplete backups had a distinct backup-failed outcome. They could not admit
the target into probation, and a failed retry retained the original rollback
binary and backups. Owner signals took precedence over failure restart. Browser
sessions were persisted before installation and refreshed at handover.

Every rollback route journaled a pending restart before replacement. Recovery
remained pending through config loading and SQLite open, until the restored API
answered under a fresh bounded deadline. A failed exec, a crash before exec or
a failed receiver startup therefore left the watchdog responsible. Recovery was
armed before fallible HTTP/session shutdown. Watchdog leases belonged to an
attempt, so a retiring helper could not suppress coverage for the next upgrade.
Recovery errors produced fixed, rate-limited diagnostics without command output
or arbitrary error strings.

Drain progress errors, including a missing or unreadable journal, never bypassed
waiting for admitted turns. Installation was abandoned after the drain instead.
Backup cleanup errors did not block dispatch after health or prevent healthy
startup; startup retried cleanup.

Upgrade choices committed durable intent alongside decision resolution.
Startup reoffered a committed choice that never reached a matching attempt.
Hook compensation revalidated the target, leaving superseded notices closed.
PM reply observers carried the message identity, so a working chat and its
model turn described one waiting operation; claimed replies remained visible.

The production version probe bounded both the process and descendant-held pipe
completion. Synthetic tests exercised these boundaries, pending recovery API
failure, retained pins, concurrent supersession, older leases, shutdown failure
and interrupted offline restore after each destination replacement. Offline
rescue kept rolling-back and the pin durable until both destinations were safe.

The next review extended the restart obligation to backup-failed and
install-failed outcomes without restoring incomplete or unnecessary backups.
Receiver PID, process identity and arguments were published together under the
journal lock. A helper launch failure was reported without blocking usable
direct rollback. Owner stops won over handover and replacement errors.
Every PM session entry point reported its live turn, including landing, PR
answers, routing, escalation and owner-step judgments. Demo refused any state
with an upgrade journal before SQLite open. Session handover synced its runtime
directory, and Unix script/process tests were constrained to Unix builds.
