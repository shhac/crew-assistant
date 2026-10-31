# The owner reaches the daemon through a private socket

Date: 2026-10-31. Version: `crew-assistant` v0.66.0 (`6936f53`) plus this
change, `lib-agent-harness` v0.24.0, Claude Code 2.1.316.

## Problem

The CLI authenticated to the daemon with an admin bearer token over loopback
TCP, read from `<state>.runtime/admin-token`, and a browser paired with a code
the CLI wrote to `<state>.runtime/pairing-code`. Both files were owner-only
(0600), but team roles run as the owner's account, and their sandboxes allow
reads outside the workspace (an accepted trade-off of
[role sandbox trust](2026-09-role-sandbox-trust.md)). A role given localhost,
which implementers and QA can have, could read the token and call the API as
the owner: resolve decisions, change settings, land work. With Tailscale Serve
on, any loopback caller could also claim owner identity with a
`Tailscale-User-Login` header (CA-78).

## Decision

- The daemon serves its API on a Unix socket, `owner.sock`, in the runtime
  directory (0700), or in a fresh private temporary directory when that path
  is too long for a socket. `daemon.json` records where. Every connection
  through it is the owner's; nothing else is.
- The CLI and the upgrade health check talk to the daemon only through it.
  There is no admin token; starting removes one an earlier version left.
- Pairing codes are issued over the socket (`POST /api/pairing`) and held only
  in the daemon's memory, single use, five minutes. Nothing on disk is a
  credential.
- Loopback TCP serves browsers, recognised only by a paired session cookie.

## Why a socket is a boundary

Role sandboxes refuse Unix-domain sockets, while all of them can be given
loopback TCP:

- The harness's command sandbox (run_check, QA's app) allows loopback but
  "never Unix-domain sockets"; its canary proves the profile.
- Codex sessions run with network disabled.
- Claude Code sessions set no `allowUnixSockets`. Checked by hand on
  2026-10-31 with Claude Code 2.1.316 and the harness's settings, loopback on:
  a sandboxed `curl --unix-socket` to a listening socket failed and the
  listener saw no connection; the same command outside the sandbox reached it.

This holds only while that stays true for each engine. A harness canary for
Unix sockets in session sandboxes would make it checked rather than observed.

## Not covered

- A process running as the owner outside any sandbox can still reach the
  socket. The host account remains trusted.
- Browser sessions are written to `upgrade-sessions.json` during a
  self-upgrade handover and removed once the new version is healthy; a role
  reading that file in that window could take a session.
- Tailscale identity: loopback callers are still trusted with a
  `Tailscale-User-Login` header when Tailscale Serve is on. Moving Serve onto
  its own socket was paused on 2026-10-31 pending the sandboxing review.
