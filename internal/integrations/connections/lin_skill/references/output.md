# Output format (reference)

## General

Commands print structured output to stdout:

- List/search commands default to JSONL (one JSON object per line).
- `get <id>...` commands default to NDJSON (one line per id — see **Get** section below).
- Use `--format json|yaml|jsonl` to override.

Errors print structured JSON to stderr with non-zero exit:

```json
{
  "error": "unknown status \"wip\" for team ENG",
  "fixable_by": "agent",
  "hint": "valid statuses: Backlog, Todo, In Progress, In Review, Done, Canceled"
}
```

`fixable_by` values: `agent` (can self-correct), `human` (needs user input), `retry` (transient failure).

Empty/null fields are pruned automatically — missing keys mean no value, not `null`.

Error messages include valid values when input is invalid (e.g., unknown status names list all valid states).

## Get (single + multi)

`get <id>...` takes one or more ids and returns one result per id, in input order. Default output is NDJSON: one line per id — the record, or `{"@unresolved":{"id":"...","reason":"...","fixable_by":"..."}}` for an id that couldn't be resolved (e.g. not found / bad id).

`--format json|yaml` collapses to one `{"data":[…],"@unresolved":[…]}` envelope.

A single `get <id>` is just the one-element case (NDJSON one line by default; was pretty JSON before — pass `--format json` for the object).

Item-level misses stay on stdout and exit 0; only a command-level failure (auth, network) goes to stderr with exit 1 and empty stdout.

## Pretty cards (`--format pretty`)

`get` commands for the rich read-and-view entities — `issue`, `project`, `initiative`, `document`, `customer` — accept `--format pretty` for a human-readable terminal card instead of JSON. It's for a person reading an entity, not for scripting (the JSON/NDJSON formats remain the machine contract).

- Multiple ids stack as cards separated by a full-width rule; an unresolved id renders as a compact `✗ <id> — <reason>` error card rather than the `@unresolved` JSON.
- `--width <n>` forces the card width (0 = auto-detect terminal; falls back to 80 when piped).
- Color is emitted only on a color-capable terminal and dropped under `NO_COLOR`, `TERM=dumb`, or when piped/redirected.
- `--full` additionally fetches and renders relations (References) and comments (Comments) for `issue get`.
- `pretty` is flag-only and per-command: it is not a valid `output.defaultFormat`, and other commands reject it with the standard unknown-format error.

```text
ENG-123  In Progress · High · 3pts                     updated 2 hours ago
──────────────────────────────────────────────────────────────────────────
Fix flaky checkout test

Assignee  Alex Rivera                Team      Engineering (ENG)
Project   Checkout Reliability       Parent    ENG-100

─ Description ────────────────────────────────────────────────────────────
The checkout test fails intermittently under load.

git branch: alex/eng-123-fix-flaky-checkout-test
https://linear.app/acme/issue/ENG-123
```

Converted get commands: `issue get`, `issue comment get`, `project get`, `project post get`, `initiative get`, `document get`, `team get`, `cycle get`, `customer get`, `label get`, `config get`.

`config get` follows the same shape over local settings: no args lists every setting as NDJSON lines; `config get <key>...` returns one `{"key":"...","value":...}` line per key (or `{"@unresolved":{…}}` for an unknown key); `--format json` collapses to the `{"data":[…]}` envelope.

Not converted (singletons or special): `user me` (singleton).

## Truncation

Fields named `description`, `body`, or `content` are truncated to ~200 characters by default. A companion `*Length` field always shows the full character count.

**Default (truncated):**

```json
{
  "description": "This is the beginning of a long project description that goes on for many paragraphs...",
  "descriptionLength": 1847
}
```

**With `--full` or `--expand description` (expanded):**

```json
{
  "description": "This is the beginning of a long project description that goes on for many paragraphs and includes detailed specifications, requirements, and implementation notes...",
  "descriptionLength": 1847
}
```

The `*Length` field is always present when the source field has content, regardless of truncation. Use it to detect whether content was truncated (`description.length < descriptionLength`).

Truncatable fields: `description`, `body`, `content`. Global flags: `--expand <field,...>` or `--full`.

## List Output

List/search commands default to JSONL:

```jsonl
{"id":"...","title":"Fix login redirect"}
{"id":"...","title":"Ship project"}
{"@pagination":{"has_more":true,"next_cursor":"abc123"}}
```

The `@pagination` line is omitted when there are no more pages.

With `--format json`, list commands return an envelope:

```json
{
  "data": [ ... ],
  "pagination": {
    "has_more": true,
    "next_cursor": "abc123"
  }
}
```

With `--format yaml`, the same envelope is emitted as YAML.

When there are no more pages, the `pagination` key is omitted entirely.

## Single item output

`get <id>` (one id) emits one NDJSON line — the record directly (not wrapped). Pass `--format json` to get a pretty JSON object instead.

```jsonl
{"id":"...","title":"...","status":"In Progress"}
```

With `--format json`:

```json
{
  "id": "...",
  "title": "...",
  "status": "In Progress"
}
```

## Document list items

Documents in list/search output:

```json
{
  "id": "...",
  "slugId": "api-design-doc-a1b2c3",
  "title": "API Design Doc",
  "url": "https://linear.app/.../document/...",
  "project": { "id": "...", "name": "CRM Actions" },
  "creator": "Alice Example",
  "updatedAt": "2025-01-15T10:30:00.000Z"
}
```

## Document detail (`document get`)

Includes full markdown content:

```json
{
  "id": "...",
  "slugId": "api-design-doc-a1b2c3",
  "title": "API Design Doc",
  "content": "# Overview\n\nThis document describes...",
  "url": "https://linear.app/.../document/...",
  "icon": "📄",
  "color": "#5e6ad2",
  "project": { "id": "...", "name": "CRM Actions", "slugId": "crm-actions-d0f9" },
  "creator": { "id": "...", "name": "Alice Example" },
  "updatedBy": { "id": "...", "name": "Bob Example" },
  "createdAt": "2025-01-10T09:00:00.000Z",
  "updatedAt": "2025-01-15T10:30:00.000Z"
}
```

## Issue list items

Issues in list output include inline context to reduce follow-up calls:

```json
{
  "id": "...",
  "identifier": "ENG-123",
  "title": "Fix login redirect",
  "branchName": "alice/eng-123-fix-login-redirect",
  "status": "In Progress",
  "priority": "high",
  "assignee": "Alice Example",
  "team": "Engineering"
}
```

## Issue overview (`issue get`)

Includes comment count, customer request counts, branch name, and attachments (e.g., linked GitHub PRs):

```json
{
  "id": "...",
  "identifier": "ENG-123",
  "title": "Fix login redirect",
  "branchName": "alice/eng-123-fix-login-redirect",
  "commentCount": 3,
  "customerRequestCount": 5,
  "customerImportantCount": 2,
  "attachments": [{ "title": "PR #456", "url": "https://github.com/...", "sourceType": "github_pr" }]
}
```

`customerRequestCount` / `customerImportantCount` count the customer requests
linked to the issue (and how many are flagged important). List them with
`lin issue requests <id>`. Counts are computed over the first 250 linked
requests.

## Customer requests (`issue requests`, `project requests`, `customer requests`)

Each item links a customer to either an issue or a project (never both):

```json
{
  "id": "...",
  "important": true,
  "createdAt": "2026-02-01T09:00:00.000Z",
  "body": "Customer asked for SSO support",
  "url": "https://intercom.example/conversations/123",
  "customer": { "id": "...", "name": "Acme Corp" },
  "issue": { "identifier": "ENG-123", "title": "Add SSO" }
}
```

`important` is derived from the request priority (`1` = important). A
project-linked request carries `project` instead of `issue`.

## Project list items

```json
{
  "id": "...",
  "slugId": "shipstation-integration-d0f95990a8f1",
  "name": "Shipstation Integration",
  "description": "One-liner project summary",
  "status": "started",
  "progress": 0.45,
  "url": "https://linear.app/.../project/..."
}
```

Use `project get <id>` with `--expand content` or `--full` for the full markdown body.

`project get <id>` also returns `labels: [{id, name}]` (project labels — distinct from issue labels) and `milestones: [{id, name, targetDate}]`.

## Project updates (`project post list` / `project post get`)

```json
{
  "id": "...",
  "url": "https://linear.app/.../projectUpdate/...",
  "health": "onTrack",
  "body": "🎉 Bundles is live!",
  "createdAt": "2026-06-19T10:00:00.000Z",
  "user": { "id": "...", "name": "Paul Somers" }
}
```

`health` is one of `onTrack`, `atRisk`, `offTrack`. `editedAt` is present only if the update was edited. `project post get` additionally returns `project: {id, slugId, name}`. `project post new` returns `{created, id, url, health, createdAt}`.

## Initiative list items

```json
{
  "id": "...",
  "slugId": "q3-launch-a1b2c3",
  "url": "https://linear.app/.../initiative/...",
  "name": "Q3 Launch",
  "description": "Platform launch for Q3",
  "status": "active",
  "owner": "Alice Example"
}
```

## Initiative overview (`initiative get`)

```json
{
  "id": "...",
  "slugId": "q3-launch-a1b2c3",
  "url": "https://linear.app/.../initiative/...",
  "name": "Q3 Launch",
  "description": "Platform launch for Q3",
  "status": "active",
  "health": "onTrack",
  "owner": { "id": "...", "name": "Alice Example" },
  "creator": { "id": "...", "name": "Bob Example" },
  "targetDate": "2025-09-30",
  "createdAt": "2025-01-01T00:00:00.000Z"
}
```

Initiative statuses: `planned`, `active`, `completed`.

## Initiative projects (`initiative projects`)

```json
{
  "id": "...",
  "slugId": "crm-actions-d0f9",
  "url": "https://linear.app/.../project/...",
  "name": "CRM Actions",
  "status": "started",
  "progress": 0.45,
  "lead": "Alice Example",
  "startDate": "2025-01-15",
  "targetDate": "2025-03-31"
}
```

## Comment list items (`issue comment list`)

```json
{
  "id": "...",
  "body": "Started investigating",
  "user": { "id": "...", "name": "Alice Example" },
  "parent": { "id": "..." },
  "childCount": 2,
  "createdAt": "2025-01-15T10:30:00.000Z",
  "updatedAt": "2025-01-15T10:30:00.000Z"
}
```

`parent` is only present for threaded replies. `childCount` is pruned when 0.

## Comment detail (`issue comment get`)

Includes parent reference (if threaded reply) and child count:

```json
{
  "id": "...",
  "body": "Started investigating",
  "user": { "id": "...", "name": "Alice Example" },
  "issue": { "id": "...", "identifier": "ENG-123" },
  "parent": { "id": "..." },
  "childCount": 2,
  "createdAt": "2025-01-15T10:30:00.000Z",
  "updatedAt": "2025-01-15T10:30:00.000Z"
}
```

`parent` is only present for threaded replies. `childCount` is pruned when 0.

## Comment replies (`issue comment replies`)

Paginated list of replies:

```json
{
  "id": "...",
  "body": "Reply text",
  "user": { "id": "...", "name": "Bob Example" },
  "createdAt": "2025-01-15T11:00:00.000Z",
  "updatedAt": "2025-01-15T11:00:00.000Z"
}
```

## Issue history (`issue history`)

Activity log entries for an issue. Empty/null fields are pruned — only changed fields appear per entry.

```json
{
  "id": "...",
  "actor": { "id": "...", "name": "Alice Example" },
  "fromState": { "id": "...", "name": "Todo" },
  "toState": { "id": "...", "name": "In Progress" },
  "fromAssignee": { "id": "...", "name": "Bob Example" },
  "toAssignee": { "id": "...", "name": "Alice Example" },
  "fromPriority": 3,
  "toPriority": 1,
  "fromEstimate": 3,
  "toEstimate": 5,
  "fromTitle": "Old title",
  "toTitle": "New title",
  "fromDueDate": "2025-01-15",
  "toDueDate": "2025-02-01",
  "fromProject": { "id": "...", "name": "Old Project" },
  "toProject": { "id": "...", "name": "New Project" },
  "addedLabels": [{ "id": "...", "name": "bug" }],
  "removedLabels": [{ "id": "...", "name": "triage" }],
  "updatedDescription": true,
  "archived": true,
  "trashed": false,
  "createdAt": "2025-01-15T10:30:00.000Z"
}
```

Priority values are numeric: 0 (none), 1 (urgent), 2 (high), 3 (medium), 4 (low).

## Document content history (`document history`)

Content edit history entries for a document:

```jsonl
{"id":"...","actorIds":["user-uuid-1","user-uuid-2"],"contentDataSnapshotAt":"2025-01-15T10:30:00.000Z","createdAt":"2025-01-15T10:30:00.000Z"}
```

Not paginated. Actor IDs are user UUIDs (resolve with `lin user list`).

## File uploads in comments

When `--file` is used with `comment new` or `comment edit`, files are uploaded to Linear's CDN and embedded in the comment body as markdown:

- Images: `![filename](https://uploads.linear.app/...)`
- Other files: `[filename](https://uploads.linear.app/...)`

Local file paths never appear in the output. The `--file` flag is repeatable for multiple attachments.

## File upload (`file upload`)

`file upload` is a single command result, so it defaults to a JSON array. Use `--format jsonl` to emit one uploaded asset per line.

```json
[
  {
    "filename": "screenshot.png",
    "assetUrl": "https://uploads.linear.app/...",
    "contentType": "image/png"
  },
  {
    "filename": "report.pdf",
    "assetUrl": "https://uploads.linear.app/...",
    "contentType": "application/pdf"
  }
]
```

## File download (`file download`)

```json
{
  "filename": "screenshot.png",
  "path": "/absolute/path/to/screenshot.png",
  "size": 45678,
  "contentType": "image/png"
}
```

With `--stdout`, binary content goes to stdout and the metadata JSON goes to stderr.

Filename is inferred from `Content-Disposition` header, then `Content-Type` MIME mapping, then falls back to `"download"`.

## Issue relations (`issue relation list`)

```jsonl
{"id":"...","type":"blocks","relatedIssue":"ENG-124"}
{"id":"...","type":"blocked_by","relatedIssue":"ENG-122"}
{"id":"...","type":"duplicate","relatedIssue":"ENG-100"}
```

`blocked_by` is displayed for inverse "blocks" relations. Relation types: `blocks`, `duplicate`, `related`.

## Workflow states (`team states`)

```jsonl
{"id":"...","name":"Todo","type":"unstarted","color":"#e2e2e2","position":0}
{"id":"...","name":"In Progress","type":"started","color":"#5e6ad2","position":1}
{"id":"...","name":"Done","type":"completed","color":"#5e6ad2","position":2}
```

State types: `triage` | `backlog` | `unstarted` | `started` | `completed` | `canceled`

## Issue attachments (`issue attachment list`)

```jsonl
{"id":"...","title":"PR #456","url":"https://github.com/...","subtitle":"Fixes login bug","sourceType":"github_pr"}
```

`sourceType` reflects the integration that created the attachment (`url`, `github_pr`, `github`, `gitlab_mr`, `slack`, `discord`, `api`, …). Rich integrations (GitHub PR, Slack message, etc.) sync metadata back to Linear automatically.

## Issue attachments (`issue attachment add`)

```json
{
  "created": true,
  "id": "...",
  "title": "PR #456",
  "url": "https://github.com/...",
  "sourceType": "github_pr"
}
```

## Labels (`label list`, `label search`, `label get`)

Issue labels (`--type issue`, default):

```json
{
  "id": "...",
  "name": "Test coverage",
  "color": "#4cb782",
  "description": "Tests added or improved",
  "isGroup": false,
  "team": { "id": "...", "key": "ENG", "name": "Engineering" },
  "parent": { "id": "...", "name": "Quality" }
}
```

`description`, `isGroup`, `team`, and `parent` are omitted when not set. Workspace-wide issue labels have no `team`.

Project labels (`--type project`):

```json
{
  "id": "...",
  "name": "Discovery",
  "color": "#4cb782",
  "description": "Discovery-phase work",
  "isGroup": false,
  "parent": { "id": "...", "name": "Phase" }
}
```

Project labels are workspace-scoped — there is no `team` field.

## Priority values

| Value    | Meaning         |
| -------- | --------------- |
| `none`   | No priority set |
| `urgent` | P0 — immediate  |
| `high`   | P1              |
| `medium` | P2              |
| `low`    | P3              |

## Team get output (`team get`)

Includes estimate configuration so callers can discover valid values before updating:

```json
{
  "id": "...",
  "name": "Engineering",
  "key": "ENG",
  "estimates": {
    "type": "fibonacci",
    "allowZero": false,
    "extended": false,
    "default": 0,
    "validValues": [1, 2, 3, 5, 8, 13],
    "display": "1 | 2 | 3 | 5 | 8 | 13"
  },
  "members": [...]
}
```

When `type` is `"notUsed"`, the `estimates` block is pruned.

## Estimate scales

| Type          | Base values                         | Extended adds  |
| ------------- | ----------------------------------- | -------------- |
| `fibonacci`   | 1, 2, 3, 5, 8, 13                   | 21, 34         |
| `linear`      | 1, 2, 3, 4, 5                       | 6, 7, 8, 9, 10 |
| `exponential` | 1, 2, 4, 8, 16                      | 32, 64         |
| `tShirt`      | 1 (XS), 2 (S), 3 (M), 4 (L), 5 (XL) | 6 (XXL)        |

When `allowZero` is true, `0` is also valid. T-shirt sizes are stored as integers.

## Project status values

`backlog` | `planned` | `started` | `paused` | `completed` | `canceled`
