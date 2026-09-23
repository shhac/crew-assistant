# First real runs of team roles

Captured on 2026-09-23 on macOS (Darwin 25.6), with codex-cli 0.154.0, Claude
Code 2.1.280 and `lib-agent-harness` v0.3.1. This follows
[CLI sandboxes](2026-09-23-cli-sandboxes.md), which could only check some of
these claims without inference.

## Sandbox under real turns

Each engine was asked, in both modes, to try six things:
1. create a file in the workspace with its file tool;
2. create a file outside the workspace with its file tool;
3. write outside the workspace with the shell;
4. write `/tmp` with the shell;
5. run `curl https://example.com`;
6. run `git ls-remote` against GitHub.

| | Claude, writing | Claude, read-only | Codex, writing | Codex, read-only |
|---|---|---|---|---|
| 1. File in the workspace | created | no file tools | created (`apply_patch`) | rejected |
| 2. File outside, file tool | refused by permission rules | no file tools | rejected | rejected |
| 3. Outside write, shell | operation not permitted | operation not permitted | operation not permitted | denied |
| 4. `/tmp` write, shell | operation not permitted | operation not permitted | operation not permitted | denied |
| 5. `curl` | proxy 403 | proxy 403 | DNS blocked | DNS blocked |
| 6. `git ls-remote` | proxy 403 | proxy 403 | DNS blocked | DNS blocked |

- **v0.3.0 bug:** harness v0.3.0 had also disabled Codex's `code_mode_host`
  feature, which every Codex tool call runs through. Codex could do nothing,
  including anything harmful. v0.3.1 fixed this.
- **Resume readback:** a Codex thread resumed after a completed turn reported
  its permission profile before any prompt.

## The loop end to end

The run used a throwaway state and a copy of the owner's configuration, with
Slack and connections removed. It was a `draft` project: Claude writer, Codex
reviewer, two rounds, and a delivery folder.

1. The task "Write the thank-you note" was queued. The writer produced
   `thank-you-note.md` (108 words) in one turn.
2. The reviewer passed it against the brief on its first read.
3. The loop opened a delivery decision.
4. The decision was approved from the dashboard. The draft was copied into its
   own new folder, `write-the-thank-you-note-r1/`, under the delivery folder.
5. No diagnostics were written during the run.

Two presentation issues were found and fixed before commit: the status badge
changed a path's case, and the delivery title was awkward.

## Isolation gaps found

- **Personal instructions reach Claude roles.** The draft was signed with the
  owner's first name, which the brief did not give. `--setting-sources=`
  drops settings, not instruction files. `~/.claude/CLAUDE.md` still loads for
  every Claude session. A role's real workspace sits under the owner's home
  (`~/.local/state/…`), so ancestor files such as `~/CLAUDE.local.md` would
  load too. The role then receives the owner's personal working instructions,
  which were written for their own coding sessions. This is not a sandbox
  escape; it breaks the "no inherited operator surface" rule in
  [the trust decision](../decisions/2026-09-role-sandbox-trust.md).
- **Claude role transcripts land in the owner's history.** Each Claude role
  session wrote its transcript under `~/.claude/projects/<workspace path>`,
  next to the owner's own sessions. (The test runs' entries were removed.)

Both come from Claude roles using the owner's own `CLAUDE_CONFIG_DIR`. A
likely fix is a private Claude config directory for roles that still uses
the keychain login, as Codex's runtime home already does, combined with
whatever the installed CLI offers to stop loading instruction files. The
harness is the place for both. Until then, keep role workspaces out of
directories with personal `CLAUDE.md` files where possible, and treat the
leak as known.
