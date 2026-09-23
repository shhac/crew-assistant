# Project teams: a general-purpose work factory

Proposal date: 2026-09-23. Status: proposed direction, not implemented.
Pinned to `agent-assistant` at `adb6e3b` and `lib-agent-harness`
`v0.1.1-0.20260918183126-e5c3ddbeaac8`. External sources checked on 2026-09-23.

This revisits the product shape in
[the original proposal](2026-09-14-personal-assistant.md). If adopted, it would
supersede the peer-messaging model in
[scoped peers](decisions/2026-09-peer-agents.md) and the host trust boundary in
[native worker sessions](2026-09-18-native-worker-sessions.md). Nothing here was
built as of the pinned commit.

## Why

After a week of use, the owner's verdict was that the assistant did not work,
and that it added mental load instead of removing it.

**The evidence on "did not work" was weaker than it looked.** The owner's only
recorded worker run changed zero files, compacted seven times and ended
`deadline_exceeded`. That run happened under the action-envelope loop, and the
native-sessions doc had already blamed that loop for the failure; its last
recorded progress was at 13:16 UTC on 2026-09-18. Native sessions replaced the
loop in `ec0b2e1`, at 18:13 UTC the same day. As of the pinned commit, **the
current design had no recorded real run**, so it was unproven, not proven
broken. Over the same period (09-14 to 09-18), all 82 commits in the repository
were written by the owner driving Claude Code and Codex directly. Those tools,
used directly, had worked.

**The evidence on mental load was direct:**
- The owner's last four chat messages were operations: checking whether the
  worker had been interrupted, asking why it stopped, and asking for a retry.
- The assistant's replies described its own machinery: "Resume accepted… This
  confirms the resume, not that the earlier blocker is resolved."
- To read the dashboard, the owner needed to understand projects, work items,
  assignments, agents, profiles, brokers, steering, receipts, resource holds,
  unknown calls and revision-pinned acceptance.

The original proposal said "a dashboard that merely made unfinished
coordination visible would fail this product goal."

### What the rebuild addressed

- **Too many handoffs, even after native sessions.** A request still passed
  through assistant model → constrained tools → daemon → broker → native session
  with its tools replaced → offline container on a copied workspace → patch →
  evidence digest → assistant review → acceptance.
- **Every failure went to the owner.** The rule was "preserve, never retry, a
  person decides", so each failure became the owner's to handle.
- **Nothing checked the artifact itself.** The assistant reviewed an evidence
  digest, and acceptance "is not a merge", so finished work never landed
  anywhere.
- **It was only ever pointed at code.** The owner wanted the same pattern for
  email drafts, long-form writing, or any other outcome.

## Outside context

Software-factory writing from August–September 2026 converged on a common
shape:

- artifacts, connected by agent-driven transformations;
- verification as the bottleneck, with cheap checks first and an independent
  model judge;
- the smallest isolation that works;
- short loops of 3–10 steps;
- human gates at specification and at shipping.

It also warned that overbuilt pipelines "became undebuggable and
uncontrollable". Nearly all of it assumed code and pull requests. This proposal
keeps the shape and drops that assumption. See [Sources](#sources).

## Goals

1. **Any kind of work can be a project.** Code is one medium. Issue → PR is one
   supported pattern, and neither Linear nor GitHub is required.
2. **Agents check agents.** Implementing, reviewing and exercising an artifact
   would be separate roles in separate sessions. The owner could read diffs or
   reviews but would never have to.
3. **The owner deals with three things:** the assistant, their projects and
   their decisions.
4. **Failures are resolved at the lowest level that can resolve them.** The
   owner would see only failures that need their choice.
5. **The owner approves only briefs and outward actions** by default.

Non-goals: a fully automated "dark factory", large parallel fleets, and a
workflow engine or DSL.

## Shape

```
Human ─1:1─ Assistant ─1:N─ Project manager ─1:N─ Team roles
                                                  (implementer, reviewer, QA, …)
```

| Tier | Would own | Could change | Escalates to |
|---|---|---|---|
| **Assistant** | The owner relationship, intake, cross-project priorities, memory | Briefs (the owner approves acceptance criteria), playbooks, which projects exist | Owner |
| **Project manager (PM)** | One project against its brief: plans tasks, then decides at each decision point in the loop | Tasks and their order; can propose playbook changes | Assistant |
| **Team role** | One task's artifact, from one angle | The artifact (implementer only) | PM, through its verdict |

The PM is **the project's context scope**. It sees one project, while the
assistant sees the owner and every project. The PM would be invoked when a
project starts, when its brief changes, and at decision points in the loop. It
would not be a long-running process. Until phase 3, the assistant would play
this role itself.

**Communication.** The hierarchy would govern authority, not messaging.
Same-project peer messaging, from the scoped-peers decision, would be dropped.
Instead, every project would have one **shared record**:
- a versioned brief;
- one artifact per task, with numbered revisions;
- verdicts, each tied to a revision;
- the project's decisions, memory and an append-only log.

**A role would read the original, never another role's account of it.** A
reviewer would read the brief and the revision, not the implementer's report on
what it did. The v1 evidence digest was exactly that kind of second-hand
account.

## The loop

The loop would be deterministic Go code, configured by the project's playbook.

```
          ┌──────────────── revise (round < max) ─────────────────┐
          v                                                       │
Task ─> implement ─> revision N ─> review ─> QA ─> verdicts ─────┤
                                                       │    PM: revise / rethink /
                                                       │        answer / escalate
                                                       v
                                            all pass ─> delivery gate ─> deliver
```

- **Plan.** The PM would split the brief into tasks, each with one artifact.
  Work that needs several artifacts would become several tasks, linked by
  `After`.
- **Implement.** The implementer produces a new revision in the workspace.
- **Check.** Checking roles return `pass`, `revise` or `question`, with findings
  tied to specific acceptance criteria. QA would exercise the revision through
  the adapter's **preview**: run the check command, render the document, or
  render the email. It would never deliver it.
- **Decide.** If any check fails, the PM chooses to revise, rethink, answer a
  question from the brief, or escalate to the assistant.
- **Rounds.** `max_rounds` (default 3) would set **when the PM must escalate**,
  not a limit on the work. Reaching it would put a decision in front of the
  assistant; nothing would stop. This differs deliberately from the retired
  cumulative caps, which ended work.
- **Pinning.** Each revision and verdict would record the brief version it was
  made against. A verdict against an older brief would not count at the gate.
  Each task would pin the playbook revision it started with, and playbook
  changes would apply to new tasks only.

## Playbooks

A playbook would be project data, picked from a template by the assistant and
editable by the assistant or the owner. The PM could propose changes; team
roles could not.

```yaml
medium: git                     # the adapter to use
workspace:                      # adapter settings
  source: /path/to/repo
  branch_prefix: paul/
  check: make check
roles:
  - { name: implementer, kind: implementer, engine: claude, access: write }
  - { name: reviewer,    kind: reviewer,    engine: codex,  access: read }
  - { name: qa,          kind: qa,          engine: claude, access: read }
max_rounds: 3
deliver: owner                  # owner | assistant
```

Suggested templates: **draft an email** (writer and reviewer), **code change**
(implementer, a reviewer from a different model family, and QA running the
check command), and **long-form writing** (writer, editor and continuity
checker). Every template would deliver through the owner until the owner
changes that.

## Media adapters

An adapter would supply everything that differs by medium:
- opening a workspace;
- snapshotting a revision;
- previewing it for reviewers and the owner;
- delivering it.

The first two adapters would be **local documents** (a project folder, with
revisions as copies) and **git**. For git, the workspace would be a separate
local clone, not a worktree, because worktrees share hooks and config with the
source repository. GitHub, Linear, Notion, Gmail and Google Docs would be later
adapters or intake sources.

Delivery would be the only outward step. It would be **reconciled, not
idempotent**: before retrying, the adapter would check whether the email was
already sent or the PR already exists. The existing machinery for uncertain
effects would apply here.

## Execution and trust

Each role would run as an ordinary headless Claude Code or Codex session
through `lib-agent-harness/session`, **with its own native tools**, working
inside the adapter's workspace. The trade-off would need stating plainly:

- **A host session can read whatever the owner's account can read**, and it
  could inherit the host's git credential helpers, `gh` login, ssh-agent and
  keychain access.
- **Only the CLI's own OS-level sandbox would stop it from writing outside the
  workspace or reaching the network.** Both CLIs offered filesystem-write and
  network restrictions for the commands they run. Phase 0 would need to confirm
  what the installed versions actually enforce. Without those restrictions, the
  standing prohibitions (no deployment, production access or purchases) and
  "roles never deliver" would be **enforced by prompt only**.
- **Resetting to the last revision would undo only writes inside the
  workspace.** It would not undo effects the sandbox failed to prevent, or
  processes still running.

The recommendation would be to require the CLI sandbox (workspace-only writes,
no network) for every role by default, and to refuse to start a role whose
sandbox cannot be confirmed. The current container route would remain as an
opt-in playbook setting for work that must not touch the host at all.

Given that sandbox, a failed implement step would be reset to the last revision
and retried in the same session with bounded backoff, and nobody would be
notified. Failures would climb one tier at a time: role, then PM, then
assistant, then owner. The owner would see a failure only when it needs a
choice, described in the project's terms.

## What the owner would see

- **Chat** with the assistant. Replies would lead with the outcome and carry no
  disclaimers about the machinery.
- **Projects:** each would show its brief, its latest deliverable, a one-line
  status, and any gate waiting on the owner.
- **Decisions:** real choices, brief approvals and delivery approvals.

Everything else would be one click deeper: team, rounds, revisions, verdicts,
the log and usage.

## Mapping from the current code

| Keep | Reshape | Retire, or park behind the container option |
|---|---|---|
| config, CLI, server, access, file picker, state paths, diagnostics | `core` projects, work items, agents and steering → project, brief, task, revision, verdict, role session | `workerbroker`, `managedworkers`, `integrations/worker` |
| `integrations/{connections,linear,slack}` as intake sources | `Decision`: `AgentID` and `WorkItemID` → task and role session | the token ledger and unknown-call accounting |
| chat and its queue, memory, `quota`'s headroom check | `engine` assistant tools → brief, playbook, task and decision tools | peer messaging |
| dashboard shell, chat, decisions, memory, settings | `app` supervision → the loop runner; the project pages | |

**Migration.** Carry over the owner's projects, memories, decision history and
chat. Archive the old agents and work items rather than migrate them.

## Rules in `AGENTS.md` that adoption would change

- "Approved workers may implement within an isolated environment", "never ship
  a worker with a shell", and the whole native-tool-replacement boundary.
- "Nothing a worker does is retried automatically."
- "Never reintroduce a cumulative … cap as work authority". `max_rounds` would
  be an escalation point, not a cap, but that needs saying.
- "Unknown costs are not free" and "unresolved accounting blocks turns", given
  the ledger is retired.
- Agents as peers, and the `parent_id` compatibility rule.
- "Stopping a turn is not stopping its tools". The rule would still hold, but
  it would now be the CLI's to enforce.
- "Preserve compatibility migration" (see Migration above).

## Build order

0. **Decide.** Settle the trust trade-off, confirm the CLI sandboxes, the
   migration, the `AGENTS.md` rewrites and the project name (see Open
   questions).
1. **The loop on local documents.** One project, a writer and a reviewer, with
   the assistant acting as PM and owner-gated delivery. This adapter is thin, so
   this phase would test the part that matters: whether agent review produces
   good work with little owner effort. *Exit:* three real drafts, with owner
   interventions counted.
2. **The git adapter.** An implementer, a reviewer from a different model
   family, and QA running the check command. *Exit:* the Tab-to-accept
   suggestion ships through it.
3. **A separate PM and concurrent projects.** *Exit:* two projects run for a
   week with the owner only answering decisions.

## How to tell it's working

Weekly counts, from the original proposal:
- how often the owner repeated context;
- how often they chased an agent themselves;
- how often they had to clarify an assistant question;
- how often they investigated a supposed blocker.

Added here:
- how often the owner stepped in during a delivery;
- rounds per task;
- time to first deliverable.

Messages sent, agents launched and tokens used would not be measures of
success.

## Risks

- **Reviewers rubber-stamp or ping-pong.** Mitigated by a different model
  family, findings tied to criteria, the escalation point and the PM.
- **Costs multiply** with roles × rounds. Show usage per project and keep the
  headroom check.
- **Weaker isolation.** See [Execution and trust](#execution-and-trust).
- **Weak verification outside code.** Non-code media have few deterministic
  checks, so owner-gated delivery stays the default.

## Open questions

1. Does the owner accept sandboxed host sessions as the default, with the
   container route opt-in?
2. For local documents, what would "deliver" mean: copy to a chosen folder, or
   mark a revision as final?
3. Should long projects, such as a book, have a persistent PM session?
4. Should the rebuild ship under a new name? `agent-*` means "a tool an agent
   uses", while this is a daemon that runs agents. The new prefix also had to
   fit `agent-code-review`, a sibling daemon that runs review agents.
   `crew-assistant` and `crew-code-review` were suggested. Renaming would touch the binary, the Go
   module, the config and state namespace, the brew formula and the repository.
   The existing legacy-namespace detection gives the state files a migration
   path.

## Sources

- [Software Factories in September 2026 — Igor Ostrovsky](https://igoro.com/archive/software-factories/)
- [How to Build an AI Software Factory — Firecrawl](https://www.firecrawl.dev/blog/ai-software-factory)
- [Inside a Software Factory — O'Reilly Radar](https://www.oreilly.com/radar/inside-a-software-factory/)
- [Software Factories Shift to Lit Models — Zetik](https://www.zetik.com/news/article/story_id-p008-216452)
- [What Is a Software Factory? — Augment Code](https://www.augmentcode.com/guides/what-is-a-software-factory)
- [StrongDM's software factory — Simon Willison](https://simonwillison.net/2026/Feb/7/software-factory/) (background, 2026-02)
