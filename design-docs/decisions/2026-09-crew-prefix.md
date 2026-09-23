# The `crew-` prefix for daemons that run agents

Date: 2026-09-23. Version: code-internal, at `adb6e3b` plus the
[project teams proposal](../2026-09-23-project-teams.md).

## Decision

We chose `crew-` as the family prefix for long-running daemons that run agents
for their owner. This project became **`crew-assistant`**. Its sibling
`agent-code-review`, a daemon that runs review agents on a schedule, would become
**`crew-code-review`**.

`agent-*` kept its existing meaning: a CLI that an agent uses as a tool, such as
`agent-slack` or `agent-notion`. Neither daemon fitted that meaning. Both host
agents, and a person uses them through a dashboard, chat or Slack.

The rename was decided alongside the proposed rebuild, because a rebuild was the
cheapest time to move the binary, the module and the state namespace.

## Criteria

- **It had to fit both daemons**, not just this one.
- **It had to say what the tools are for, not how they run.** `agent-` names its
  audience, and the new prefix needed to follow the same pattern.
- **It could not be code-specific.** The rebuild made projects general-purpose
  (email, documents, books as well as code).
- **It had to be short** enough for binaries, brew formulae and repository names.

## Alternatives considered

- **`daemon-`**: literal and free of brand overlap. Rejected because it names
  the process model rather than the purpose, so it would stop being true for a
  member that ran as a scheduled or on-demand job. It also reads as plumbing to
  someone drafting an email, collides with many generic `daemon` packages, and
  goes against the Unix convention of a `d` suffix (`sshd`).
- **`mill-`**: fits the software-factory metaphor. It was the runner-up;
  "run-of-the-mill" gives it a slightly negative feel.
- **`staff-`**: fits the chief-of-staff framing, but "staff-code-review" reads
  as a review by a staff engineer.
- **`forge-`**: clashes with "software forge", meaning git hosting.
- **`hq-`**: serviceable but bland.
- **A `d` suffix** (`assistantd`): conventional for daemons, but it is not a
  family prefix and names the process, not the purpose.

## Known overlaps

- CrewAI, a well-known multi-agent framework, shares the word.
- Chromebrew's package manager is invoked as `crew`. That would only matter for
  a binary named exactly `crew`.

Neither was judged a problem for `crew-assistant` or `crew-code-review`.

## What the rename touches

For this project:
- The working directory and the GitHub repository (`shhac/agent-assistant`).
- The Go module path (`github.com/shhac/agent-assistant`), and imports.
- The binary and `cmd/agent-assistant`, plus the `Makefile` `BINARY`, the CI
  build paths and the release workflow's formula name.
- The Homebrew formula in the shared tap.
- The config and state namespace would become **`app.paulie.crew-assistant`**
  (and `app.paulie.crew-code-review` for the sibling). That is the family's
  reverse-DNS convention, documented in `lib-agent-keyring` and used by
  `app.paulie.agent-slack`. This project's `agent-assistant.paulie.app`, chosen
  on 2026-09-15, was the exception.
- The owner's live install used the legacy `~/.config/agent-assistant` and
  `~/.local/state/agent-assistant` pair, with its assistant Codex login in
  `~/.local/state/agent-assistant.paulie.app/codex`. On 2026-09-23 these were
  moved by hand into `~/.config/app.paulie.crew-assistant` and
  `~/.local/state/app.paulie.crew-assistant`, with `codex/` inside the state
  directory. Every stored absolute path was rewritten, because a plain move
  would have left them pointing at the old locations: both `codex_home` values
  in `config.json`; the project's scratch directory, artifact paths and
  registered repository path in `state.db`; and the managed-worker and
  context-checkpoint JSON. The project's title changed to `crew-assistant`.
  Chat and activity history kept the old name as it was written. A tarball of
  the edited files, before the rewrite, went to
  `backups/pre-crew-rename-20260923/`.
- The code made a clean break: it dropped the legacy-pair fallback and has no
  knowledge of either former namespace. The owner confirmed it had only ever
  run locally, so no other installs needed a migration path.
- **Runtime identifiers kept the old name on purpose:** the Colima profile
  `agent-assistant` (a running VM, and a socket path recorded in the worker
  environment), the worker image tag, container names and labels, the
  in-container `/opt/agent-assistant/gomod` mount, and the setup-container
  prefix. Renaming them would have orphaned the VM and failed the broker's
  container-name check for the stored run. They belong to the worker broker,
  which the rebuild would retire.
- Shell completions, the README and `AGENTS.md`.

Renaming `agent-code-review` would be the same checklist, carried out in that
repository, plus the owner's `agent-code-review` skill that documents it.

Design docs written before this decision keep the old name, because they are
dated snapshots.
