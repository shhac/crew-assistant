---
name: lin
description: |
  Look up, search, create, and update Linear issues, projects, initiatives,
  documents, and cycles. Use when working with teams, labels, and comments,
  with customers, customer requests, and feedback, attaching files, or
  linking GitHub PRs and issues, GitLab MRs, Slack, or Discord messages to
  Linear items.
allowed-tools: Bash(lin *) Read Grep Glob
---

# Linear automation with `lin`

`lin` is a CLI binary installed on `$PATH`. Invoke it directly (e.g. `lin issue list --team ENG`).

List/search output is JSONL by default. `get <id>...` commands default to NDJSON (one line per id — the record, or `{"@unresolved":{...}}` for a missing id); pass `--format json` to get a pretty object. Use `--format json|yaml|jsonl` to override any command. Errors go to stderr as `{ "error": "...", "fixable_by": "agent|human|retry", "hint": "..." }` with non-zero exit.

## IMPORTANT: Never access the Linear API directly

**NEVER retrieve, read, or use the Linear API key directly.** Do not `curl` the Linear API. Do not use environment variables to extract credentials. `lin` handles all authentication internally.

If a specific `lin` command doesn't exist for what you need:

1. Check `lin <command> usage` — the command may exist but not be obvious from `--help`
2. Use `lin api query '<graphql>'` as a last resort — it runs raw GraphQL through `lin`'s auth

## Quick start (auth)

Set an env var (recommended):

```bash
export LINEAR_API_KEY="lin_api_..."
```

Or store it locally (supports multiple workspaces — each Linear API key is per-org):

```bash
lin auth login <api-key> --alias myorg
lin auth status
lin auth workspace list
lin auth workspace switch <alias>
lin auth logout [--all]
lin auth workspace remove <alias>
```

Generate a personal API key at **Settings > Account > Security > Personal API Keys** in the Linear app.

**Multi-user MCP:** one `lin mcp` server can serve several people. Each named principal's calls run pinned to their own workspace (`--workspace <alias>` + fail-closed `LIN_REQUIRE_IDENTITY`). Bind a principal explicitly with `mcp pair add <name> --bind workspace=<alias>`, or leave them unbound to self-enroll — they paste their own Linear API key in the browser during OAuth approval and it is stored under their name. A slot converges on one Linear org; a key for a different org is refused.

## Looking up issues

```bash
lin issue search "auth bug"
lin issue list --team ENG --status "In Progress" --assignee "alice@example.com"
lin issue get ENG-123             # NDJSON by default; includes branchName, commentCount, attachments (PR links)
lin issue get ENG-123 ENG-456     # multi-get: one NDJSON line per id (or @unresolved for missing)
lin issue comment list ENG-123
```

## Creating and updating issues

```bash
lin issue new "Fix login redirect" --team ENG --priority high --status "Todo" --labels "Bug"
lin issue update status ENG-123 "In Progress"
lin issue update assignee ENG-123 "alice@example.com"
lin issue update priority ENG-123 urgent
lin issue update project ENG-123 "Bundles self-serve"   # move to a project (name, slug, or UUID)
lin issue update estimate ENG-123 5      # validated against team's estimate scale
lin issue update due-date ENG-123 2025-03-15
lin issue update cycle ENG-123 <cycle-uuid>
lin issue update parent ENG-123 ENG-100  # make sub-issue
lin issue comment new ENG-123 "Started investigating"
lin issue comment new ENG-123 "Replying" --parent <comment-id>   # threaded reply (1 level max)
lin issue comment new ENG-123 "See attached" --file ./screenshot.png  # upload file(s)
lin issue comment get <comment-id>           # includes parent, childCount
lin issue comment edit <comment-id> "Updated" --file ./report.pdf
lin issue comment replies <comment-id>       # list replies (paginated)
```

## Files (upload and download)

```bash
# Upload files to Linear's CDN — returns asset URLs
lin file upload ./screenshot.png ./report.pdf

# Download files from Linear's CDN
lin file download https://uploads.linear.app/<org>/<uuid>/<uuid>
lin file download <uuid>/<uuid>              # org inferred from auth
lin file download <uuid>                      # single UUID
lin file download <uuid>/<uuid> --output ./report.pdf
lin file download <uuid>/<uuid> --output-dir ./downloads
lin file download <uuid>/<uuid> --stdout | cat > file.bin
lin file download <uuid>/<uuid> --force       # overwrite existing
```

`file download` defaults to the lin cache (`~/.cache/lin/downloads`) and reports
the absolute `path`; `--output`/`--output-dir`/`--stdout` override it.

**Over MCP (`lin mcp`):** a client has no filesystem, so the reported `path`
comes back as a fetchable reference (`{"@type":"file","root":"cache","path":"downloads/…"}`)
that you read with the bridge's built-in **`fs`** tool — e.g.
`fs get cache downloads/diagram.png` returns the bytes (images as image blocks),
`fs find cache -e png` / `fs ls cache downloads` discover them. No host path needed.

> **Prefer `--file` on comments** when attaching files to issues. `lin issue comment new ENG-123 "See attached" --file ./screenshot.png` uploads and embeds the file in a single step. Use `lin file upload` only when you need a standalone asset URL (e.g., for issue descriptions or documents).

## Issue history

```bash
lin issue history ENG-123                    # activity log (status, assignee, labels, etc.)
lin issue history ENG-123 --limit 10         # last 10 changes
```

## Issue relations and lifecycle

```bash
# Relations (blocks, duplicate, related)
lin issue relation list ENG-123
lin issue relation add ENG-123 --type blocks --related ENG-124
lin issue relation remove <relation-id>

# Archive and delete
lin issue archive ENG-123
lin issue unarchive ENG-123
lin issue delete ENG-123                  # moves to trash

# Attachments (link PRs, docs, Slack messages, etc.)
lin issue attachment list ENG-123
lin issue attachment add ENG-123 "https://example.com/spec.pdf" --title "Spec doc"        # default: rich link, server detects integration
lin issue attachment add ENG-123 "https://github.com/org/repo/pull/456" --github-pr        # force GitHub PR integration
lin issue attachment add ENG-123 "https://github.com/org/repo/issues/789" --github-issue
lin issue attachment add ENG-123 "https://gitlab.com/group/project/-/merge_requests/42" --gitlab-mr
lin issue attachment add ENG-123 "https://app.slack.com/client/T0/C0/p1700000000000000" --slack --sync-thread
lin issue attachment add ENG-123 "https://discord.com/channels/<guild>/<channel>/<message>" --discord
lin issue attachment remove <attachment-id>      # works for any source type
```

## Projects

Project commands accept UUID, slug ID, or name.

```bash
lin project search "migration"
lin project list --status started                  # filters by status type OR custom status name
lin project list --lead me --status started        # "projects I lead that are In Progress"
lin project get "CRM Actions"            # accepts UUID, slug, or name — includes content (markdown body)
lin project issues <id>
lin project new "New Feature" --team ENG --status planned --lead "alice@example.com"
lin project update status <id> completed
lin project update content <id> "# Updated body content"
lin project update start-date <id> 2025-01-15
lin project update target-date <id> 2025-03-31
lin project update priority <id> high
lin project update labels <id> "Discovery, Roadmap"   # replaces project labels (project labels, not issue labels)
lin project delete <id>                  # moves to trash
lin project unarchive <id>               # restore trashed/archived project
```

**Project updates (health/status posts)** — Linear's timeline posts with a health
signal, distinct from `project update <field>` which edits a project field:

```bash
lin project post new "Bundles self-serve" "🎉 Bundles is live!" --health on-track
lin project post list "Bundles self-serve"   # newest first, paginated
lin project post get <update-id>
# --health: on-track | at-risk | off-track
```

The `--project` filter on issue commands also accepts slug or name.

## Customer requests

A customer request (Linear "customer need") links a customer to an issue or
project. A request has **no status of its own** — its state is the linked
issue's state, so "in triage" and "unassigned" are filters on the linked issue.
Importance is a flag on the request itself (`important: true` ⇒ priority 1).
Requests have no labels; categorize by the linked issue's labels or the
customer's tier/status. Customers accept UUID, slug, or name.

```bash
# Triage workflow — answer "what needs attention?"
lin customer requests                          # all requests, newest first
lin customer requests --important              # flagged important
lin customer requests --unassigned             # linked issue has no assignee
lin customer requests --triage                 # linked issue still in triage
lin customer requests --customer "Acme Corp"   # everything one customer asked for
lin customer requests --team ENG --important   # important requests routed to a team

# Customers
lin customer list --tier Enterprise --status Active
lin customer search "acme"
lin customer get acme-corp                     # UUID, slug, or name; includes approximateNeedCount
lin customer statuses                          # workspace lifecycle statuses
lin customer tiers                             # workspace tiers/segments

# From the other direction
lin issue requests ENG-123                     # requests linked to one issue
lin project requests <id>                      # requests linked to one project
lin issue get ENG-123                          # includes customerRequestCount + customerImportantCount
```

## Initiatives (replaces roadmaps)

```bash
lin initiative search "migration"
lin initiative list --status active
lin initiative get <id>                  # status, health, owner, projects
lin initiative projects <id>             # projects linked to initiative
lin initiative new "Q3 Launch" --status planned --owner "alice@example.com"
lin initiative update name <id> "New Name"
lin initiative update status <id> active
lin initiative update description <id> "Updated description"
lin initiative update owner <id> "alice@example.com"
lin initiative update content <id> "# Updated body"
lin initiative update color <id> "#FF5500"
lin initiative update icon <id> "🚀"
lin initiative update target-date <id> 2025-06-30
lin initiative archive <id>
lin initiative unarchive <id>
lin initiative delete <id>
```

Initiative statuses: `planned`, `active`, `completed`.

## Documents

Document commands accept UUID or slug ID.

```bash
lin document search "onboarding"
lin document list --project "CRM Actions" --creator "alice@example.com"
lin document get <id>                    # full markdown content
lin document new "API Design Doc" --project "CRM Actions" --content "# Overview\n..."
lin document update title <id> "New Title"
lin document update content <id> "# Updated content"
lin document update project <id> "Other Project"
lin document history <id>                # content edit history (actor IDs + timestamps)
```

## Teams, users, labels, cycles

```bash
lin team list
lin team get ENG                         # includes estimate config + valid values
lin team states ENG                      # workflow states (discover valid status values)
lin user me
lin user list --team ENG
lin label list --team ENG                # issue labels under a team (incl. workspace-wide)
lin label list --name "Bug"              # exact-name match (case-insensitive); shows team for each result
lin label search "perf"                  # substring search (case- and accent-insensitive)
lin label get "Test coverage" --team ENG # single label; --team disambiguates duplicate names
lin label get <uuid>                     # always unique
lin label list --type project            # workspace-wide project labels (no --team — projects labels aren't team-scoped)
lin label search "discovery" --type project
lin label get "Discovery" --type project
lin cycle list ENG --current
lin cycle get <id>
```

## Truncation

Long text fields (`description`, `body`, `content`) are truncated to ~200 characters by default. A companion `*Length` field (e.g. `descriptionLength`) always shows the full size.

To see full content, use `--expand` or `--full`:

```bash
lin --full issue get ENG-123                             # expand all fields
lin --expand description issue get ENG-123               # expand specific field
lin --expand description,content project get <id>        # expand multiple
```

These are global flags — place them before the command or after it.

To read an entity in a terminal (not for scripting), use `--format pretty`:

```bash
lin issue get ENG-123 --format pretty                    # human-readable card
lin --full issue get ENG-123 --format pretty             # + relations and comments
lin project get <id> --format pretty --width 100         # set card width
```

## IDs

All commands accept multiple ID formats:

- Issue keys: `ENG-123`
- UUIDs: `aaaaaaaa-1111-2222-3333-444444444444`
- URL slugs: `fix-login-redirect-abc123`
- `--team` accepts team key (`ENG`) or name (`Engineering`)

## Pagination

List/search commands default to JSONL: one item per line. If another page exists, the final line is `{"@pagination":{"has_more":true,"next_cursor":"..."}}`.

Use `--format json` for an envelope: `{ "data": [...], "pagination"?: { "has_more": true, "next_cursor": "..." } }`.

Use `--limit <n>` and `--cursor <token>` to paginate.

## Global flags and defaults

- `--format json|yaml|jsonl`: override output format for this invocation
- `--format pretty` (get commands only): human-readable terminal card for reading an entity — not for scripting. Supported on `issue`, `project`, `initiative`, `document`, and `customer` get. Color is used on a terminal and dropped when piped or `NO_COLOR` is set.
- `--width <n>`: card width for `--format pretty` (0 = auto-detect terminal)
- `--workspace <alias>`: act as a specific stored workspace for this invocation, overriding the default. Resolves strictly by alias (unknown alias errors). When `LIN_REQUIRE_IDENTITY` is set, this flag is mandatory and resolution fails closed before any fallback (default workspace / legacy key / `LINEAR_API_KEY`).
- `--timeout <ms>`: request timeout in milliseconds
- `--debug`: log redacted HTTP request records to stderr
- `--expand <field,...>` / `--full`: expand truncated long text fields. With `--format pretty`, `--full` also fetches relations and comments for `issue get`.

Persist commonly-used defaults with:

```bash
lin config set output.defaultFormat jsonl
lin config set request.timeoutMS 10000
```

## Per-command usage docs

Every top-level command has a `usage` subcommand with detailed, LLM-optimized docs:

```bash
lin issue usage          # all issue subcommands, flags, valid values
lin project usage        # project commands
lin initiative usage     # initiative commands
lin document usage       # document commands
lin team usage           # team, user, label, cycle commands
lin auth usage           # auth + workspace management
lin file usage           # file upload + download commands
lin config usage         # CLI settings keys, defaults, validation
lin usage                # top-level overview (~1000 tokens)
```

Use `lin <command> usage` when you need deep detail on a specific domain before acting.

## Raw GraphQL (escape hatch)

When no structured command covers your needs, use `lin api query` instead of accessing the API directly:

```bash
lin api query '{ viewer { id name email } }'
lin api query '{ issue(id: "ENG-123") { id title createdAt completedAt } }'
lin api query 'query($id: String!) { issue(id: $id) { id title } }' --variables '{"id":"ENG-123"}'
```

Always prefer structured commands first — they handle pagination, ID resolution, and formatting.

## References

- [references/commands.md](references/commands.md): full command map + all flags
- [references/output.md](references/output.md): structured output shapes + field details
