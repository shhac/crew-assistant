# Integration contracts

These adapters contact external services only when the operator configures and starts them. Tests use synthetic fixtures and local HTTP servers. Credential values are read from environment variables and never returned in adapter errors. Configure services before starting the daemon; the application owns configuration, retries, durable receipts and authorization.

## PA model

`engine.New` accepts a full HTTPS Chat Completions endpoint (for example the configured base URL followed by `/chat/completions`), model identifier, credential environment reference, assistant name and personality. Loopback HTTP supports local compatible providers. The provider must support Chat Completions function tools and `max_completion_tokens`; model compatibility is checked by the first requested conversation, not by spending inference during setup.

The engine sends only coordination tools. The application bridge validates every argument and checks current authority. A model response cannot introduce new tools or increase allowances. `BeforeRequest` reserves a durable model-call allowance before each request. Per-call output tokens, model turns and serialized context size are bounded. The implementation does not automatically retry model HTTP requests: a lost response can still represent billable inference. Usage reports are token counts with an explicit `known` flag, not dollar charges or subscription remaining capacity. The model-call cap is not a dollar budget.

History is server-owned user/assistant dialogue. Tool results and current records are supplied as evidence, not policy. The prompt prohibits deployment, production data access and purchases through all descendants. Team roles are held to them by their sandbox where it can, and by instruction elsewhere.

Reference: [OpenAI function calling](https://developers.openai.com/api/docs/guides/function-calling). This adapter uses the compatible Chat Completions protocol deliberately; it does not require the OpenAI agent runtime or give a model arbitrary built-in tools.

## Existing CLI accounts

The preferred connection path uses installed `lin`, `agent-slack`, `agent-notion` and `agent-fathom` tools. `connections` contains `{id, name, tool, profiles, import_assignments, allow_writes}` entries. Connections are optional resources; the local assistant database owns project records. Account access does not automatically enroll projects or establish relevance to personal work. Account discovery returns only known profile names. The PA receives `list_connections` and `query_connection`; each query names one approved connection/profile and a supported read operation. Arguments are assembled by Go, never interpreted by a shell. Output and duration are bounded; keys and auth defaults remain owned by the external CLI.

`lin` supports assignments, issue search/details, and project listing; `agent-slack` supports message search and history; `agent-fathom` supports meetings, summaries, and open action items. Linear assignment import runs only for connections with `import_assignments: true` (default false), using their selected accounts and retaining connection/profile provenance. Read-only queries remain available when import is off and never create local projects. It imports at most 50 issues per account per sweep and does not claim exhaustive coverage.

Notion reads use the CLI's default account with an empty profile list. The daemon never changes that default to impersonate per-request selection.

Slack Socket Mode below remains separate for owner messages and replies. The direct Linear API below is retained for legacy configurations.

## Linear

The assistant has a `lin` tool when a Linear connection has selected accounts;
a project's PM has it only through that project's Linear link. It runs approved
CLI commands as argv on the daemon, with the selected account and no inherited
credentials. Changes are off by default; **Allow changes** (`allow_writes`) opts
in each connection. Writes record an Activity attempt and outcome without bodies.
Deleting, archiving, removal, raw API, auth/config, files and MCP are unavailable.
The embedded lin skill is filtered to permitted commands; references are fetched
on demand. Output is clipped external data; failures return information and
writes are never retried automatically. See the [decision](../../design-docs/decisions/2026-10-lin-tool.md).

Project Config → Linear optionally links one local project to a team (board) or Linear project through a configured `lin` connection and account. Status names and assignee rules (anyone, unassigned, me, or selected users) define pick-up. The existing startup, five-minute and manual sweeps import matching issues as tasks, at most 50 new tasks and five pages per project per sweep. The source identifier, title, URL and bounded description snapshot remain on each task. Metadata pages contain ten issues; descriptions are fetched separately for new issues, truncated at 8KiB with a marker, or explicitly omitted if the individual read exceeds 128KiB. Other read failures abort the buffered import. The team's brief includes the description as external context; the URL is not an acceptance criterion. No writes to Linear occur. Unlinked projects and existing assignment-to-project imports are unchanged.

Reads use fixed `lin team list`, `project list`, `team states`, `user list` arguments and a constant `api query` with JSON variables. GraphQL IssueFilter supports team/project IDs, state names, assignee IDs, null assignees and isMe; cursor pagination bounds each read. Credentials stay in the CLI, with the configured workspace selected explicitly. Failed or incomplete reads import nothing from that read. Project errors are visible in Config; another project's sweep continues.

Option lists follow cursors up to five pages and 500 records; incomplete or failed lists are refused. Unchanged cursor/error updates and duplicate receipts do not rewrite state.

Imported issue IDs survive unlinking, rule changes, account/connection renames and task completion. Task creation and its import receipt share a transaction, including concurrent sweeps. A saved scan cursor resumes from the last fully processed page, then resets at the end of the matching list. Receipts stay local rather than growing a GraphQL exclusion filter; a crash before saving the cursor only repeats deduplicated reads. When resuming, each sweep checks the newest ten matches first, then reads up to four pages from the saved cursor. New issues in that top page are picked up on the next successful sweep; other newly matching issues wait for the scan to reach them. Changing or removing the link invalidates pending reads and resets the cursor. Later Linear edits or deletions do not change local tasks. Adding/removing other task links is a separate feature (CA-52).

The Config form caches teams, projects, users and workflow states for its selected account. A fixed read-only project-teams query limits state discovery to the selected project's teams; state reads run sequentially and are reused when switching targets. No workspace-wide state scan occurs. A recovered pick-up error records a readable recovery activity.

Explicitly enable `linear.import_assignments` (default false), set `linear.api_key_env` to an environment variable containing a Linear personal API key and configure explicit `linear.team_ids`. `Assigned` discovers the key owner's `viewer.id`, then reads active assigned issues in those teams with cursor pagination. OAuth access tokens require the value's `Bearer ` prefix; personal API keys use their raw value. No write scope or write operation is used. The adapter returns an error on partial GraphQL errors or incomplete pagination; callers must not interpret a failed read as an empty assignment list.

`updatedAt` indicates record freshness. It does **not** establish when an issue was assigned. Assignment-time queries require actual assignment history, which this adapter does not yet fetch. Current discovery is assigned issues, not lead-owned projects with no assigned issue.

Reference: [Linear GraphQL API](https://linear.app/developers/graphql).

CA-52 added task-level Linear links alongside imported sources. The owner or
assistant can validate an issue identifier or pasted Linear issue URL, or select
a project UUID, through any configured lin account (defaulting to the project's
connection). Fixed read-only queries store identifier/name, title and URL.
Added issue links create an atomic import receipt, retained on removal so pick-up
never imports the issue later. Imported sources stay read-only; added projects
replace the previous project link, and tasks allow up to 50 added issue links.
Nothing writes to Linear, and added links never enter team prompts.

## Slack

Create a Slack app with the Agent experience (`features.agent_view`), enable Socket Mode, create an app-level token with `connections:write`, and grant its bot `assistant:write`, `chat:write`, `im:history`, and `im:write`. Subscribe to the `message.im` bot event, install the app in the intended workspace, then set the configured bot-token and app-token environment variables. Set `slack.workspace_id` to the workspace ID and `slack.owner_user_id` to the owner’s Slack user ID. Reinstall the app after adding scopes. Connection settings are under Settings → Connections → Slack bot messaging → Edit; they can be saved while running and take effect after restart. The active transport, message destination and notification scope all retain their boot configuration until then.

The bot token’s workspace is verified with `auth.test` before the connector starts or sends notifications. Incoming events must name the configured workspace. Only original messages from that owner in a DM enter the connection. Channel messages, bot messages, edits and shared-channel events are ignored. Replies stay in the initiating thread. The SDK manages Socket Mode reconnects; no public webhook ingress is needed. SDK debug logging is disabled. The application must persist event claims before acknowledgement, record completion and surface pending claims after a crash. Pending requests must not be automatically re-run when prior coordination effects are uncertain. Notification calls target only the configured owner; external recipients are not part of this adapter.

With `slack.project_id` empty, messages enter the assistant’s global chat. With a project ID, messages enter its existing PM chat queue and use the actual PM seat, never the assistant’s global tools or history. The connection may be prepared before the project has a PM; a DM then explains how to choose one. A removed or unknown project never falls back to the assistant. Workspace, channel and thread identify each saved conversation; Slack event IDs identify questions deterministically, so repeating an event cannot create another PM question. Both questions and replies appear in the project’s dashboard conversation. Chat turns use only their thread’s history; a scheduled PM look can read every conversation of its own project to retain the owner’s priorities. Notifications are filtered to the selected project, while existing claim-before-send semantics remain unchanged.

`crew-assistant --env-file /path/to/private.env serve` (or `doctor`) loads explicit startup credentials with existing environment values taking precedence. There is no project-folder autodiscovery. Parsing errors omit file contents; the sandboxed harness environment allowlist excludes inherited credentials from team roles. No environment-file values are added to config or state. The flag is refused in demo mode.

Basic Agent DMs work through `message.im` and threaded `chat.postMessage` replies. Native processing status, session titles and stop controls are not yet implemented.

References: [Slack Socket Mode](https://docs.slack.dev/apis/events-api/using-socket-mode/), [Slack Agent messaging](https://docs.slack.dev/ai/developing-agents/).
