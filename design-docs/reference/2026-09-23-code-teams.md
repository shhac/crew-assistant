# Code teams: what the sandbox changed

Captured on 2026-09-23 against lib-agent-harness v0.3.3, at commit 2955cd8.

## A correction to the first real runs

[The first real runs](2026-09-23-first-real-runs.md) said personal
instructions reach Claude roles, because a draft was signed with the owner's
first name. That diagnosis was wrong. A re-test with the role's flags showed
that with `--setting-sources=` empty, Claude loads no `CLAUDE.md` at all, not
even the workspace's own. The name came from the account's email address,
which the session can see. It was not an instruction file.

The transcript finding in that note was not re-tested here.

## Consequences for code work

- **Roles must be told to read the repository's instructions.** Because no
  instruction file loads, a code role would otherwise never see the
  repository's `AGENTS.md` or `CLAUDE.md`. The implementer and reviewer
  prompts now ask for them explicitly. QA only runs the check.
- **The sandbox forbids listening on loopback.** `make check` failed under
  the role sandbox on tests that start a local HTTP server. They now skip
  when the listen is refused (`internal/testutil`). With that change,
  `make check` for this repository passes under the Codex role sandbox.
- **Builds must stay offline and inside the workspace.** The clone gives
  roles Go, npm and XDG caches under `.crew/`, with `GOPROXY=off` and
  `GOTOOLCHAIN=local`. Ignored dependency folders the check needs (here
  `internal/dashboard/ui/node_modules`) are copied in once, when the clone
  is made. Playbooks cannot add environment variables; the fixed set is
  enough so far.

## The daemon's own git

The daemon runs git in a clone that roles have written to. Its git ignores
global and system config, hooks, fsmonitor, attribute and exclude files, and
configured remotes. It fetches only from the owner's repository path. Tests
plant a hook, an fsmonitor and a global-config filter, and check that none of
them runs. The filter test was confirmed to fail when the protection is
removed.

Delivery only creates a branch in the owner's repository. It uses
`update-ref` with a zero old value, and adds a `-N` suffix when the name is
taken. A retry that finds its own branch already at the approved commit
counts as done.
