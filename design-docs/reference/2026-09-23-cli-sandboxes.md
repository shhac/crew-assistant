# CLI sandboxes for team roles

Captured on 2026-09-23 on macOS (Darwin 25.6) against **codex-cli 0.154.0** and
**Claude Code 2.1.280**. Sources: Context7 `/openai/codex` (rust-v0.154.0:
codex-rs core README, `config/permissions.rs`, `sandboxing/src/seatbelt.rs`,
the rmcp stdio launcher), Context7 `/websites/code_claude` (the sandboxing,
settings-reference, permissions and agent-sdk pages), and both CLIs' `--help`.
The probes ran without model inference. They did not change the owner's
`~/.codex` or `~/.claude`.

The question was whether a headless native session could be held to
**workspace-only writes and no network**, and whether the daemon could confirm
that before starting a role.

## Codex 0.154.0

Model shell commands run under Seatbelt (`/usr/bin/sandbox-exec`). The policy
comes from a named permission profile. The built-in `:workspace` profile was
**not sufficient**: it allowed writes to `/tmp`, `/private/tmp` and `$TMPDIR`,
and while it was active the `sandbox_workspace_write.*` overrides, including
`network_access`, were silently ignored. By default it also made `.git`
read-only, so `git commit` failed.

A custom profile passed on the command line worked:

```
-c 'permissions.crew.filesystem={":root"="read",":workspace_roots"={"."="write",".git"="write",".git/hooks"="read",".git/config"="read"}}'
-c 'permissions.crew.network.enabled=false'
-c 'default_permissions="crew"'
-c 'approval_policy="never"' -c 'web_search="disabled"'
codex exec --ignore-user-config --ignore-rules -C <workspace> ...
```

`--ignore-user-config` dropped the owner's MCP servers, plugins and approval
reviewer. Authentication still came from `CODEX_HOME`.

Probes through `codex sandbox -P crew -C <workspace> -- <cmd>`:

| Attempt | Result |
|---|---|
| Write inside the workspace | allowed |
| Write to a sibling directory, `~`, `/tmp` or `$TMPDIR` | denied |
| `curl`, `git ls-remote`, `git push`, `ssh` to GitHub, localhost | denied |
| Read a file outside the workspace; list `~/.ssh` and `~/.aws` | allowed |
| A grandchild process writing outside, or using the network | denied (the sandbox is inherited) |
| `git commit` inside the workspace | allowed |
| Write `.git/hooks`, `.git/config`, or through a symlink to `~/.zshrc` | denied |
| The same profile with `network.enabled=true` | `curl` allowed, so the switch was live |

**Not verified:** that `codex exec` itself applies the profile. `codex debug
prompt-input` with these flags rendered the matching sandbox and approval text,
but that is the prompt, not enforcement. Also not verified: that `apply_patch`
edits obey the writable roots.

**Other outward paths:**
- MCP servers run outside the sandbox. `--ignore-user-config` with no
  `mcp_servers` closed them.
- The `web_search` tool: disabled, and `--search` is never passed.
- The CLI's own model-provider connection, which cannot be closed.
- The owner's git config had commit signing on, which the sandbox blocks. Pass
  `-c commit.gpgsign=false`.

## Claude Code 2.1.280

The `sandbox` settings sandbox **only Bash and its child processes**, using
Seatbelt plus a network proxy with a domain allowlist. Read, Edit, Write,
WebFetch and WebSearch run in-process and are governed only by permission
rules. WebFetch ignores the sandbox's network lists.

A configuration that left user settings alone:

```
claude -p --setting-sources "" --strict-mcp-config --permission-mode dontAsk \
  --tools "Bash,Read,Edit,Write,Glob,Grep" --disallowedTools "WebFetch,WebSearch,mcp__*" \
  --settings '{"sandbox":{"enabled":true,"failIfUnavailable":true,"allowUnsandboxedCommands":false,
   "autoAllowBashIfSandboxed":true,"network":{"allowedDomains":[],"strictAllowlist":true}},
   "permissions":{"defaultMode":"dontAsk","allow":["Edit(./**)"],"blockReadsOutsideWorkingDirectories":true}}'
```

The owner's own settings used `defaultMode: auto`, which sends unlisted hosts
to a classifier. That is why both `dontAsk` and `--setting-sources ""` are
required. `--restricted` (file tools confined to the working directories, user
settings ignored) was noted as a stronger option but not probed.

**Pre-launch check, verified:** `claude --setting-sources "" --settings '<json>'
sandbox status` returned `enabled: true`, `strictMode: true` (source `policy`),
`unavailableReason: null`, `filesystemPolicy: "strict"`.

Probes ran through sandbox-runtime (srt 0.0.77, the same library family),
because Bash inside the CLI cannot be driven without inference. The bundled
build was not confirmed to be identical.

| Attempt | Result |
|---|---|
| Write inside the workspace | allowed |
| Write to a sibling directory, `~` or `/tmp` | denied |
| `curl` | denied (proxy 403) |
| `git ls-remote`, `git push`, `ssh` | denied |
| Read outside the workspace; list `~/.ssh` | allowed |
| Child processes | inherit the sandbox |
| `git commit` inside the workspace | allowed |
| Write `.git/hooks` or `.git/config` | denied |
| `allowedDomains: ["example.com"]` | `curl` allowed, so the switch was live |

**Not verified:**
- that a real `claude -p` Bash call receives this policy;
- that `strictAllowlist` is honoured from `--settings` (the docs list it for
  user or managed scope);
- that Edit and Write stay confined under `dontAsk` with `Edit(./**)`.

**Other outward paths:**
- WebFetch and WebSearch: removed through `--tools` and deny rules.
- MCP servers, plugins and hooks: `--strict-mcp-config` and
  `--setting-sources ""`.
- `dangerouslyDisableSandbox` retries: blocked by
  `allowUnsandboxedCommands: false`.
- The model API connection.
- The session's temp directory stayed writable.

## What followed from this

- Shell execution could be held to workspace-only writes with no network on
  both engines. For Codex this was verified on its own sandbox; for Claude
  Code, on the matching library.
- Claude Code's file tools are the weaker guarantee, because permission rules
  enforce them, not the OS.
- `lib-agent-harness` v0.2.0 (`a750e1d`) could not yet express any of this for
  an ordinary session. It had no sandbox policy type and no network or
  writable-roots options. It had no `--settings`, `--setting-sources` or
  `--strict-mcp-config` for Claude, no private home outside restricted mode,
  and no sandbox probe.
