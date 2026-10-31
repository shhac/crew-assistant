# Sandboxing stays coarse, and stops growing

Date: 2026-10-31. Version: `crew-assistant` v0.66.0 plus `0eb9d29`,
`lib-agent-harness` v0.29.0. Follows the
[sandboxing review](../2026-10-31-sandboxing-review.md); amends
[role sandbox trust](2026-09-role-sandbox-trust.md).

## Decision

The owner chose to relax the role sandbox rather than build a stronger
boundary. Agent teammates must be able to do their work; the owner trusts what
they are asked to do, and the review found that the fine-grained layer cost far
more in refused work and owner decisions than the risk it removed.

Kept, because they are cheap and stop the mistakes that matter:

- every task works in a disposable clone;
- every outward action (push, pull requests, tracker and chat writes) goes
  through the daemon's delivery gate; roles have no delivery capability;
- no owner credentials in a role's environment or prompt;
- each role runs under its CLI's own sandbox, with writes limited to its
  workspace;
- the owner's daemon channel is a private socket roles cannot reach
  ([owner socket](2026-10-owner-socket.md)).

Relaxed:

- every role may use this machine's own addresses (localhost) wherever its
  engine's sandbox offers it, and the daemon-hosted check always may; this is
  no longer a per-project setting;
- subagents per member, once the harness can switch them on, without a proof of
  policy inheritance beyond the CLI's own sandbox applying to them.

Not done:

- No separate operating-system user for roles. The tool may run on a managed
  machine where creating users is not possible, so it cannot depend on one.
- No new fine-grained proofs: selected-port and interface canaries,
  process-inspection proofs, read allow-lists by request, deny-read lists or a
  harness "sandbox edge". Queued work for those is dropped or reworked.

## What this accepts

Roles still run as the owner's account, so a role can read what the owner can
read, including credentials on disk. The defence is that nothing a role can do
leaves the machine except through the delivery gate, and roles have no network
beyond localhost unless the owner grants web access. Say so wherever access is
explained; never claim more.
