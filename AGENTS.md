# crew-assistant

Go daemon, CLI and embedded dashboard for a personal assistant that runs projects through teams of agents. The dashboard is dark-mode-first; home is Overview. The assistant's display name comes from config, and its default is defined once.

## Direction

The project is mid-rebuild to the design in `design-docs/2026-09-23-project-teams.md`. Decisions made for it: `design-docs/decisions/2026-09-crew-prefix.md`, `2026-09-clean-break-state.md` and `2026-09-role-sandbox-trust.md`. Build order: (1) a writer and reviewer loop on local documents, (2) the git adapter, (3) a separate PM tier and concurrent projects. Each phase finishes with real use, not tests alone.

The owner's core complaint about v1 was that it added mental load and was over-engineered. Prefer the smallest loop that works, and add machinery only when a real run needs it.

## Architecture and boundaries

- **Any kind of work can be a project.** Code is one medium; so are documents and email. Never assume GitHub, Linear or pull requests. They are optional adapters or intake sources.
- **The owner deals with the assistant, their projects and their decisions.** Owner-facing words are project, brief, deliverable and decision. Playbook, role, round, revision and verdict belong in drill-down views. Assistant replies lead with the outcome and carry no disclaimers about the machinery.
- **Shape:** owner → assistant → project manager (PM) → team roles (implementer, reviewer, QA, …). The hierarchy governs authority and escalation, not messaging. Every role works from the project's shared record: a versioned brief, one artifact per task with numbered revisions, verdicts tied to revisions, decisions, memory and an append-only log. A role reads the original, never another role's account of it. There is no peer messaging.
- **Go owns the loop:** plan → implement → check → PM decision → delivery gate. Model calls happen only to do the work and at decision points. The PM is invoked on project start, on brief changes and at decision points; it is a one-shot decision, not a long-running session. `max_rounds` is where the PM must escalate. It is not a cap on work, and nothing stops when it is reached.
- **Playbooks are project data with a small, fixed schema.** They are not a workflow engine or a DSL; add a field only when a real project needs it. Only the assistant or the owner changes a playbook, and the PM may propose changes. A task pins the playbook revision it started with. Each revision and verdict records the brief version it was made against, and verdicts against an older brief do not count.
- **Media adapters** own workspace, snapshot, preview and delivery. For git, the workspace is a separate local clone, not a worktree. Delivery is the only outward action. It goes through a gate (the owner, by default) and is reconciled rather than blindly retried. Team roles never get a delivery capability. QA uses the adapter's preview.
- **Roles run as ordinary native Claude Code or Codex sessions** through `lib-agent-harness/session`, with their own tools, working in the adapter's workspace. Every role runs under the CLI's own OS sandbox, with writes limited to the workspace and no network, configured as `2026-09-role-sandbox-trust.md` specifies. If the sandbox cannot be confirmed for the installed CLI version, refuse to start the role and say what is missing. Never weaken this because a setting looks equivalent. Reads outside the workspace are an accepted, documented trade-off; never claim otherwise. The daemon itself never runs workspace content outside a sandbox: QA check commands, hooks, package scripts and Makefiles included. The offline-container route is an opt-in playbook setting, not the default.
- **Credentials never reach a role's environment,** a workspace, a model prompt, a log or an error. Secrets never appear in logs, config exports or the UI.
- **The assistant never writes project files and never runs a shell.** It proposes coordination actions through `lib-agent-harness/completion`, with built-in tools disabled. PM decisions use the same one-shot contract with structured output. Depend on published library versions, not local replaces.
- **Standing prohibitions for every role:** no deployment, no production-data access, no purchases. Inference and subscription use are expected operating costs. Outside what the sandbox enforces, these prohibitions hold only through the prompt; say so rather than claim otherwise.
- **Failures resolve at the lowest level that can resolve them:** role, then PM, then assistant, then owner. Failed work inside a workspace resets to the last revision and retries in the same session with bounded backoff, without notifying anyone. The owner sees a failure only when it needs their choice, described in project terms. Outward delivery is never retried without reconciling what already happened.
- **Usage:** keep the simple subscription-headroom check and show usage per project. There is no token ledger.
- **Local state owns the project registry.** Account access never implies project enrollment. Clean break: no compatibility layers for pre-rebuild state (`2026-09-clean-break-state.md`).
- **Adapters must be testable with injected dependencies.** Tests must not contact real services, start real agents, mutate Tailscale routes or use live owner data. Use fake CLIs and synthetic fixtures only.
- **Keep names, account IDs, project IDs, prompts, endpoints and credentials configurable.** Config and state live under the reverse-DNS namespace `app.paulie.crew-assistant`.
- **Use lib-agent-cli and lib-agent-output conventions,** and the family's Tailscale helpers where appropriate. The embedded dashboard bundle is built and committed; no separate frontend server runs at runtime.

## Legacy, being removed

`internal/workerbroker`, `internal/managedworkers` and `internal/integrations/worker` (the container broker and the replaced-tool sessions), plus peer messaging, steering receipts, the token ledger and work-item acceptance, are pre-rebuild. Do not extend them. Delete each one when its replacement lands. Their runtime names (the Colima profile, image and containers) still say `agent-assistant` on purpose; see the crew-prefix decision.

## Working conventions

Commit verified increments directly to main as authorized by the owner. Use git-hunk for staging, and don't commit unrelated work. Use conventional, descriptive commit messages. Run Go tests and go vet; after UI changes, build and typecheck the frontend and commit the generated assets. No release tags or real deployment unless separately requested.

Bounded subagent delegation is authorized. Agree file ownership and interfaces before editing shared files. Only the primary agent stages and commits.

Design docs are dated snapshots. See `design-docs/README.md`.
