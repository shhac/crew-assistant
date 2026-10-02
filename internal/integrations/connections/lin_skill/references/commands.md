# `lin` command map (reference)

Run `lin --help` (or `lin <command> --help`) for the full option list.
Run `lin usage` for concise LLM-optimized docs.
Run `lin <command> usage` for detailed per-command docs (e.g., `lin issue usage`).

## Auth

- `lin auth login <api-key> [--alias <name>]` — validate and store API key (auto-detects workspace)
- `lin auth logout [--all]` — remove active workspace credentials (--all: clear all workspaces)
- `lin auth status` — show auth state, active workspace, and other stored workspaces
- `lin auth workspace list` — list all stored workspaces
- `lin auth workspace switch <alias>` — set default workspace
- `lin auth workspace remove <alias>` — remove a stored workspace
- `--workspace <alias>` (global) — act as a specific stored workspace for one command, overriding the default (unknown alias errors)
- `LIN_REQUIRE_IDENTITY` (env) — make `--workspace` mandatory, failing closed before any fallback; set by `lin mcp` per named principal. Bind principals with `mcp pair add <name> --bind workspace=<alias>`, or let unbound principals self-enroll their own API key in the browser during OAuth approval.

## Projects

- `lin project search <text>` — search projects by name
- `lin project list [--team] [--status] [--lead] [--limit] [--cursor]` — list projects. `--status` matches a status type (started, planned, …) or a custom status name; `--lead` accepts me, name, email, or user ID
- `lin project get <id>...` — project summary with lead, labels, milestones, url, and content (markdown body, truncated by default)
- `lin project issues <id> [filters]` — issues in a project
- `lin project requests <id> [--important] [--limit] [--cursor]` — customer requests linked to the project
- `lin project update title <id> <value>`
- `lin project update status <id> <value>` — backlog | planned | started | paused | completed | canceled
- `lin project update description <id> <value>`
- `lin project update content <id> <value>` — update the project's markdown body
- `lin project update lead <id> <user-id>`
- `lin project update start-date <id> <YYYY-MM-DD>`
- `lin project update target-date <id> <YYYY-MM-DD>`
- `lin project update priority <id> <value>` — none | urgent | high | medium | low
- `lin project update icon <id> <emoji>`
- `lin project update color <id> <hex>`
- `lin project update labels <id> <labels>` — replace project labels (comma-separated names or UUIDs; resolved via `lin label list --type project`). Replace semantics: previously-set labels not listed are removed.
- `lin project new <name> [--team <keys>] [--description <text>] [--lead <user>] [--start-date <YYYY-MM-DD>] [--target-date <YYYY-MM-DD>] [--status <status>] [--content <markdown>]`
- `lin project delete <id>` — delete project (move to trash)
- `lin project unarchive <id>` — restore trashed/archived project

### Project updates (health/status posts)

Linear "project updates" are timeline posts carrying a health signal — distinct from `lin project update <field>`, which edits a project field.

- `lin project post new <project> <body> [--health <health>]` — post a project update. `--health`: on-track | at-risk | off-track
- `lin project post list <project> [--limit] [--cursor]` — list project updates (newest first)
- `lin project post get <update-id>...` — get one or more project updates (NDJSON by default)

## Initiatives

- `lin initiative search <text> [--limit] [--cursor]` — search initiatives by name
- `lin initiative list [--status] [--limit] [--cursor]` — list initiatives (status: planned | active | completed)
- `lin initiative get <id>...` — initiative summary with status, health, owner, projects
- `lin initiative projects <id> [--limit] [--cursor]` — projects linked to an initiative
- `lin initiative new <name> [--status <status>] [--owner <user>] [--target-date <YYYY-MM-DD>]`
- `lin initiative update name <id> <value>`
- `lin initiative update status <id> <value>` — planned | active | completed
- `lin initiative update description <id> <value>`
- `lin initiative update owner <id> <user>` — user name, email, or ID
- `lin initiative update content <id> <value>` — markdown body
- `lin initiative update color <id> <hex>`
- `lin initiative update icon <id> <emoji>`
- `lin initiative update target-date <id> <YYYY-MM-DD>`
- `lin initiative archive <id>` — archive an initiative
- `lin initiative unarchive <id>` — unarchive an initiative
- `lin initiative delete <id>` — delete an initiative

## Documents

- `lin document search <text> [--include-comments] [--include-archived] [--limit] [--cursor]` — full-text search
- `lin document list [--project <name|slug|id>] [--creator <name|email|id>] [--include-archived] [--limit] [--cursor]` — list documents
- `lin document get <id>...` — full document details + markdown content (accepts UUID or slug ID)
- `lin document new <title> [--project <name|slug|id>] [--content <markdown>] [--icon <emoji>] [--color <hex>]`
- `lin document update title <id> <value>`
- `lin document update content <id> <value>`
- `lin document update project <id> <project>` — project ID, slug, or name
- `lin document update icon <id> <emoji>`
- `lin document update color <id> <hex>`

## Document History

- `lin document history <id>` — content edit history (actor IDs + timestamps, not paginated)

## Files

- `lin file upload <paths...>` — upload one or more files to Linear's CDN, returns `[{ filename, assetUrl, contentType }]`
- `lin file download <url-or-path> [--output <path>] [--output-dir <dir>] [--stdout] [--force]` — download a file from Linear's CDN

URL formats for download (all equivalent):

- Full URL: `https://uploads.linear.app/<org>/<uuid>/<uuid>`
- Domain + path: `uploads.linear.app/<org>/<uuid>/<uuid>`
- With org: `<org>/<uuid>/<uuid>`
- Without org: `<uuid>/<uuid>` (org inferred from auth)
- Single UUID: `<uuid>` (org inferred from auth)

Flags `--output`, `--output-dir`, and `--stdout` are mutually exclusive. Without `--force`, download refuses to overwrite existing files. By default (no `--output`/`--output-dir`) the file is saved to the lin cache (`~/.cache/lin/downloads`) and the absolute `path` is reported.

Over MCP (`lin mcp`) the reported `path` is rewritten to a fetchable reference `{"@type":"file","root":"cache","path":"downloads/…"}`; read it with the built-in **`fs`** tool — `fs get cache downloads/<name>` (images return as image blocks), `fs find cache -e png`, `fs ls cache downloads`. The host path is never exposed.

> **Prefer `--file` on comments** when attaching files to issues — `lin issue comment new ENG-123 "text" --file ./image.png` uploads and embeds in one step. Use `lin file upload` when you need a standalone asset URL.

## Issues

- `lin issue search <text> [filters]` — full-text search
- `lin issue list [filters]` — list issues (returns status, assignee, team, branchName)
  - Date filters: `--updated-after`, `--updated-before`, `--created-after`, `--created-before` (YYYY-MM-DD)
- `lin issue get <id>...` — full issue details with commentCount, customerRequestCount, customerImportantCount, branchName, attachments (PR links)
- `lin issue requests <id> [--important] [--limit] [--cursor]` — customer requests linked to the issue
- `lin issue new <title> --team <team> [--priority <p>] [--status <s>] [--assignee <name|email|id>] [--project <p>] [--labels <names|ids>]`
- `lin issue update title <id> <value>`
- `lin issue update status <id> <value>` — team-specific workflow state names
- `lin issue update assignee <id> <user>` — accepts name, email, or user ID
- `lin issue update priority <id> <priority>` — none | urgent | high | medium | low
- `lin issue update project <id> <project>` — project ID, slug, or name
- `lin issue update estimate <id> <value>` — validated against team estimate scale
- `lin issue update labels <id> <name1,name2,...>` — accepts label names or IDs
- `lin issue update description <id> <value>`
- `lin issue update due-date <id> <YYYY-MM-DD>`
- `lin issue update cycle <id> <cycle-id>` — move issue to a cycle (UUID)
- `lin issue update parent <id> <parent-id>` — set parent issue (make sub-issue)

## Comments

- `lin issue comment list <issue-id> [--limit] [--cursor]` — list comments with authors, parent ref, childCount (paginated)
- `lin issue comment new <issue-id> <body> [--parent <comment-id>] [--file <path>]` — add comment (--parent for threaded reply, 1 level max; --file repeatable for uploads)
- `lin issue comment get <comment-id>...` — get comment(s) with author, issue ref, parent ref, and childCount
- `lin issue comment edit <comment-id> <body> [--file <path>]` — edit a comment (--file repeatable)
- `lin issue comment replies <comment-id> [--limit] [--cursor]` — list replies to a comment (paginated)

## Issue Relations

- `lin issue relation list <issue-id>` — list all relations (both directions, includes blocked_by)
- `lin issue relation add <issue-id> --type <type> --related <related-id>` — type: blocks | duplicate | related
- `lin issue relation remove <relation-id>` — delete a relation

## Issue History

- `lin issue history <issue-id> [--limit] [--cursor]` — activity log: status changes, assignee changes, priority, labels, estimate, title, project, due date, archive/trash (paginated)

## Issue Lifecycle

- `lin issue archive <id>` — archive an issue
- `lin issue unarchive <id>` — unarchive an issue
- `lin issue delete <id>` — delete issue (move to trash)

## Issue Attachments

- `lin issue attachment list <issue-id>` — list attachments (any source type)
- `lin issue attachment add <issue-id> <url> [--title <text>]` — link a URL (default: rich link via `attachmentLinkURL` — server detects integrations)
  - `--github-pr` — force GitHub pull request integration (PR status sync)
  - `--github-issue` — force GitHub issue integration
  - `--gitlab-mr` — force GitLab merge request integration (project path + number derived from URL)
  - `--slack [--sync-thread]` — force Slack message integration; `--sync-thread` mirrors the Slack thread to a comment thread
  - `--discord` — force Discord message integration (channel + message IDs derived from URL)
- `lin issue attachment remove <attachment-id>` — remove any attachment, regardless of source type

## Customers

- `lin customer list [--tier] [--status] [--owner] [--domain] [--revenue] [--limit] [--cursor]` — list customers (filter by tier display name, status name, owner, email domain, or minimum revenue)
- `lin customer search <text>` — match customers by name substring
- `lin customer get <id|slug>...` — customer detail: tier, status, owner, domains, externalIds, revenue, size, approximateNeedCount
- `lin customer statuses` — workspace customer lifecycle statuses (e.g. Active, Churned)
- `lin customer tiers` — workspace customer tiers/segments (e.g. Enterprise, Pro, Free)

## Customer Requests

A customer request (Linear "customer need") links a customer to an issue or project; it has no status of its own, so triage/assignment filters apply to the linked issue.

- `lin customer requests [filters] [--limit] [--cursor]` — list customer requests across the workspace
  - `--customer <id|slug|name>` — scope to one customer
  - `--project <id|slug|name>` — scope to one project
  - `--important` — only important requests (priority = 1)
  - `--unassigned` — linked issue has no assignee
  - `--triage` — linked issue is in a triage state
  - `--status <name>` — linked issue status name
  - `--label <name>` — linked issue label
  - `--team <key|name>` — linked issue team
  - `--created-after`, `--created-before` (YYYY-MM-DD)
- `lin issue requests <id> [--important]` — requests linked to a specific issue
- `lin project requests <id> [--important]` — requests linked to a specific project

## Teams

- `lin team list` — list teams (id, name, key)
- `lin team get <id>...` — team details + members + estimate config (type, valid values)
- `lin team states <team>` — list workflow states (id, name, type, color, position)

## Users

- `lin user search <text>` — search users by name, email, or display name
- `lin user list [--team]` — list users
- `lin user me` — current authenticated user

## Labels

`IssueLabel` and `ProjectLabel` are distinct Linear entities; the same name can exist in both. Pass `--type project` to operate on project labels; the default is `issue`.

- `lin label list [--type issue|project] [--team] [--name <text>] [--is-group[=false]]` — list labels (filterable). Issue-label output includes `team{id,key,name}` and `parent{id,name}` when present; project-label output omits `team` (project labels are workspace-only).
- `lin label search <text> [--type issue|project] [--team]` — substring search by name (case- and accent-insensitive).
- `lin label get <id|name>... [--type issue|project] [--team]` — one or more labels by UUID or exact name. Use `--team` (or a UUID) to disambiguate when a name is shared across teams (issue labels only).
- `--team` is rejected with `--type=project` (project labels are workspace-scoped).

## Cycles

- `lin cycle list <team> [--current] [--next] [--previous]` — list cycles
- `lin cycle get <id>...` — cycle details + issues

## Usage

- `lin usage` — print concise LLM-optimized top-level docs (~1000 tokens)
- `lin <command> usage` — print detailed docs for a specific command domain:
  - `lin issue usage` — issue search, list, create, update, comment, relation, archive, attachment
  - `lin project usage` — project commands
  - `lin initiative usage` — initiative commands
  - `lin document usage` — document search, list, create, update
  - `lin team usage` — team, user, label, cycle commands
  - `lin auth usage` — authentication + workspace management
  - `lin file usage` — file upload + download commands
  - `lin config usage` — CLI settings (keys, defaults, validation rules)

## API (raw GraphQL)

- `lin api query <graphql> [--variables <json>]` — execute raw GraphQL query (escape hatch for unsupported operations)

## Get contract (single + multi)

`get <id>...` takes one or more ids and returns one result per id, in input order. Default output is NDJSON: one line per id — the record, or `{"@unresolved":{"id":"...","reason":"...","fixable_by":"..."}}` for an id that couldn't be resolved. `--format json|yaml` collapses to one `{"data":[…],"@unresolved":[…]}` envelope. Item-level misses stay on stdout with exit 0; only a command-level failure (auth, network) goes to stderr with exit 1.

Converted: `issue get`, `issue comment get`, `project get`, `project post get`, `initiative get`, `document get`, `team get`, `cycle get`, `customer get`, `label get`, `config get` (over local settings: no args lists all as NDJSON, `config get <key>...` is one `{key,value}` line per key, `--format json` gives the `{data:[…]}` envelope). Not converted (singletons/special): `user me`.

## Common filters (list/search commands)

| Flag                           | Description                           |
| ------------------------------ | ------------------------------------- |
| `--team <key\|name>`           | Filter by team                        |
| `--status <name>`              | Filter by workflow state              |
| `--assignee <name\|email\|id>` | Filter by assignee                    |
| `--priority <level>`           | none, urgent, high, medium, low       |
| `--label <name>`               | Filter by label                       |
| `--cycle <id>`                 | Filter by cycle                       |
| `--project <name\|slug\|id>`   | Filter by project (ID, slug, or name) |
| `--limit <n>`                  | Max results per page                  |
| `--cursor <token>`             | Pagination cursor                     |

## Global flags

| Flag                   | Description                                                         |
| ---------------------- | ------------------------------------------------------------------- |
| `--format <fmt>`       | Output format: json, yaml, jsonl (get commands also accept `pretty`) |
| `--width <n>`          | Card width for `--format pretty` (0 = auto-detect terminal)         |
| `--timeout <ms>`       | Request timeout in milliseconds                                     |
| `--debug`              | Log redacted HTTP request records to stderr                         |
| `--expand <field,...>` | Expand specific truncated fields (e.g. `--expand description,body`) |
| `--full`               | Expand all truncated fields; with `--format pretty`, also fetches relations + comments for `issue get` |

## Persistent config keys

Use `lin config set <key> <value>` to persist defaults:

| Key                          | Values / meaning                         |
| ---------------------------- | ---------------------------------------- |
| `output.defaultFormat`       | `json`, `yaml`, or `jsonl`               |
| `request.timeoutMS`          | Request timeout in milliseconds          |
| `pagination.defaultPageSize` | Default page size for paginated commands |
| `truncation.maxLength`       | Default truncation length for long text  |
