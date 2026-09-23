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
  `~/.local/state/agent-assistant` pair. Its assistant Codex login lived in
  `~/.local/state/agent-assistant.paulie.app/codex`. These folders were **not
  moved in place**, because they store absolute paths: `config.json` holds both
  `codex_home` values, `state.db` holds project scratch directories and the
  registered repository path, and the managed-worker JSON holds its own paths.
  A move would have broken the old binary. The rebuild's migration would import
  projects, memories, decisions and chat from the old locations into the new
  namespace, rewriting the paths, and would then leave the old folders to the
  owner to delete.
- The registered project's directory pointed at the repository's old checkout
  path, which the owner renamed to `crew-assistant` on 2026-09-23. The
  migration should update it too.
- Shell completions, the README and `AGENTS.md`.

Renaming `agent-code-review` would be the same checklist, carried out in that
repository, plus the owner's `agent-code-review` skill that documents it.

Design docs written before this decision keep the old name, because they are
dated snapshots.
