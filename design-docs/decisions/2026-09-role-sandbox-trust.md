# Team roles run as sandboxed native sessions

Date: 2026-09-23. Version: `crew-assistant` v0.11.0 (`ac7be39`). The evidence
is in [CLI sandboxes](../reference/2026-09-23-cli-sandboxes.md).

## Decision

The owner accepted the trust model proposed in
[project teams](../2026-09-23-project-teams.md). Each team role runs as an
ordinary native Codex or Claude Code session, with its own tools, working in
the adapter's workspace, under that CLI's own OS sandbox. This replaced the
2026-09-18 boundary, in which native tools were removed and replaced by daemon
tools running in an offline container. That container route remains available
as an opt-in playbook setting.

The rules a role launch must satisfy:

1. **Workspace-only writes, no network, for everything the role executes.**
   - Codex: a dedicated permission profile (filesystem `:root` read, workspace
     write, `.git/hooks` and `.git/config` read-only, network disabled), with
     `approval_policy=never`, `web_search` disabled, `--ignore-user-config`
     and `--ignore-rules`. The built-in `:workspace` profile is not acceptable,
     because it allows writes to temp directories and ignores network
     overrides.
   - Claude Code: `sandbox.enabled` with `failIfUnavailable`,
     `allowUnsandboxedCommands: false`, an empty network allowlist,
     `--setting-sources ""`, `--strict-mcp-config`, `dontAsk` permissions,
     file tools confined to the workspace, and WebFetch, WebSearch and MCP tools
     removed.
2. **Fail closed.** Before a role starts, the daemon confirms the sandbox for
   that exact CLI binary and configuration. For Codex, it runs a canary
   through `codex sandbox` with the same profile: a write outside the
   workspace and a network call must both fail, and a write inside it must
   succeed. For Claude Code, it requires `claude sandbox status` to report
   the sandbox enabled and strict, with no unavailable reason. The result is
   cached per binary version and configuration. If any check fails, the role
   does not start, and the blocker names the engine and what failed.
3. **No inherited operator surface.** No user settings, rules, hooks, plugins
   or MCP servers reach a role. No credentials reach its environment.
4. **The daemon never runs workspace content outside a sandbox.** That covers
   QA check commands, hooks, package scripts and Makefiles.

## Accepted trade-offs

- **Reads are not restricted.** A role can read what the owner's account can
  read, including listing `~/.ssh`. The model provider connection is an
  unavoidable outward channel, so anything a role reads could leave through
  the model's context.
- **Claude Code's file tools are not OS-sandboxed.** They are confined by
  permission rules. Its shell is covered by the OS sandbox.
- **Two claims could not be verified without real inference.** That a real
  `codex exec` or `claude -p` session applies exactly the probed policy, and
  that Codex `apply_patch` obeys the writable roots, stay unverified until the
  first real run exercises them. That run should try both deliberately.
- **Everything the sandbox does not enforce is prompt-level only.** That
  includes the standing prohibitions on deployment, production data and
  purchases.

## Consequence for the harness

`lib-agent-harness` v0.2.0 could not express these launches for an ordinary
session: it had no sandbox policy, no Claude `--settings`, `--setting-sources`
or `--strict-mcp-config`, no private home outside restricted mode, and no
sandbox probe. Either the library gains them, or the rebuild launches the CLIs
directly for phase 1.
