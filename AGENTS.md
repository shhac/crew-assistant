# crew-assistant

Go CLI and daemon for personal-assistant coordination. Dashboard is dark-mode-first; home is Overview, not Today/Tomorrow. Assistant display name comes from config; its default is defined once.

## Pending direction (2026-09-23)

A near-full rebuild is proposed in `design-docs/2026-09-23-project-teams.md`: general-purpose projects (code is one medium), Human → Assistant → Project manager → team roles (implementer, reviewer, QA) working from a shared project record, and playbooks plus media adapters in place of the worker broker. The project was renamed from `agent-assistant` to `crew-assistant` on 2026-09-23; `agent-code-review` is to become `crew-code-review` (`design-docs/decisions/2026-09-crew-prefix.md`). The owner's pending phase 0 decisions come first: trust, CLI sandboxes, migration and the rewrites of rules below. Until then, the rules below still apply; do not start rebuild code before the owner confirms phase 0.

## Architecture and boundaries

- Go owns deterministic policy, durable state, retries, scheduling, adapters and APIs. The model chooses coordination actions through constrained tools.
- Local state owns the project registry. Linear and other connections are optional resources; local projects must work without them. Account access never implies project enrollment or relevance to personal work. Assignment imports require explicit opt-in.
- Projects hold ongoing context; work items hold individual outcome contracts. Agent assignments belong to work items. Acceptance is pinned to the reviewed revision and leaves the project open. Steering belongs to the work item; explicit agent receipts are distinct from transport delivery and implementation evidence. Preserve compatibility migration without inventing historical acceptance.
- Agents are peers with scoped outcome ownership. The daemon owns assignments, runtime lifecycle, routing and recovery. Reporting relationships constrain delegated authority and route escalation; peer messages never grant authority. Keep legacy parent_id wire/state compatibility.
- The PA never writes project code or runs a general shell. Approved workers may implement within an isolated environment. No deployment, production-data access, or purchases, including through descendants.
- All adapters must be testable using injected dependencies. Tests must not contact real Slack/Linear, start real agents, mutate Tailscale routes, or use live owner data.
- Authority is scoped and inherited; retries are idempotent and uncertain external effects are reconciled before repeating. Unknown costs are not free.
- The PA proposes coordination actions through `lib-agent-harness/completion`, with
  its built-in tools disabled and fail-closed probes intact. Implementation workers
  use `lib-agent-harness/session` instead: a persistent native Claude Code or Codex
  session that owns its own agent loop, conversation and compaction. These are two
  different execution contracts and neither may silently become the other. Depend on
  published library versions, not local replaces.
- A worker's own tools are removed and replaced by the daemon's, verified against
  that exact installed binary and configuration before a credentialed process
  starts. If the installed CLI cannot be restricted, fail closed and say which
  tools were the problem. Never ship a worker with a shell, and never weaken the
  boundary because a sandbox setting looks equivalent — a read-only sandbox is not
  a read restriction. A worker runs from a private durable home the library owns,
  sharing only the operator's login; credentials never reach a workspace, a model,
  a tool result, a log or an error.
- Worker limits are resource limits: shared subscription headroom and an optional
  per-assignment token budget, both admitted before every turn, and headroom
  re-read on a clock while a long turn runs. Never reintroduce a cumulative turn,
  call, command or wall-clock cap as work authority, and do not add no-progress
  heuristics that cannot distinguish repeated red tests from stuck work. A resource
  hold is a wait, not a failure: it preserves work, keeps its own kind, spends no
  recovery allowance, carries no provider classification and releases execution
  capacity. Count every disjoint token class, cache creation included. Usage that
  cannot be established is never counted as zero, and unresolved accounting blocks
  further turns rather than being assumed free.
- Nothing a worker does is retried automatically. A failed native turn may already
  have edited files and run commands, so its diagnostic is preserved with the
  harness's own code and a person decides. Keep typed library failures typed all
  the way to the operator's log, inspect output and dashboard; never re-derive a
  classification the library already made, and never parse its prose.
- Stopping a turn is not stopping its tools. Close tool admission as part of every
  interrupt, checkpoint and terminal transition, then wait for handlers to return
  before describing a workspace or starting anything new. Cancelling a container
  command does not stop the process inside it: hold further changes until confirmed
  cleanup proves nothing is still writing, and keep the command's outcome recorded
  as unestablished afterwards.
- Direction is recorded as handed over before it is handed over, for both the
  prompt that opens a turn and a steer into a running one. A missing
  acknowledgement is not proof of non-delivery; replay only what this process saw
  refused before it was sent, and stop for an owner otherwise.
- Use lib-agent-cli/lib-agent-output conventions and the family Tailscale helpers when appropriate. Embedded dashboard bundle is built and committed. No separate frontend server needed at runtime.
- Keep names, account IDs, project IDs, prompts, endpoints, and credentials configurable. Synthetic fixtures only. Secrets never appear in logs, config exports, or the UI.

## Working conventions

Commit verified increments directly to main as authorized by the owner. Use git-hunk for staging. Do not commit unrelated work. Use conventional descriptive commit messages. Run Go tests and go vet; build/typecheck frontend after changes and commit generated assets. No release tags or real deployment unless separately requested.

Bounded subagent delegation is authorized. Coordinate file ownership and interfaces before editing shared files. Only the primary agent stages and commits.

Design docs are dated snapshots. See design-docs/README.md.
