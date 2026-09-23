# Integration contracts

These adapters contact external services only when the operator configures and starts them. Tests use synthetic fixtures and local HTTP servers. Credential values are read from environment variables and never returned in adapter errors. Configure services before starting the daemon; the application owns configuration, retries, durable receipts and authorization.

## PA model

`engine.New` accepts a full HTTPS Chat Completions endpoint (for example the configured base URL followed by `/chat/completions`), model identifier, credential environment reference, assistant name and personality. Loopback HTTP supports local compatible providers. The provider must support Chat Completions function tools and `max_completion_tokens`; model compatibility is checked by the first requested conversation, not by spending inference during setup.

The engine sends only coordination tools. The application bridge validates every argument and checks current authority. A model response cannot introduce new tools or increase allowances. `BeforeRequest` reserves a durable model-call allowance before each request. Per-call output tokens, model turns and serialized context size are bounded. The implementation does not automatically retry model HTTP requests: a lost response can still represent billable inference. Usage reports are token counts with an explicit `known` flag, not dollar charges or subscription remaining capacity. The model-call cap is not a dollar budget.

History is server-owned user/assistant dialogue. Tool results and current records are supplied as evidence, not policy. The prompt prohibits deployment, production data access and purchases through all descendants. Team roles are held to them by their sandbox where it can, and by instruction elsewhere.

Reference: [OpenAI function calling](https://developers.openai.com/api/docs/guides/function-calling). This adapter uses the compatible Chat Completions protocol deliberately; it does not require the OpenAI agent runtime or give a model arbitrary built-in tools.

## Existing CLI accounts

The preferred connection path uses installed `lin`, `agent-slack`, `agent-notion` and `agent-fathom` tools. `connections` contains `{id, name, tool, profiles, import_assignments}` entries. Connections are optional resources; the local assistant database owns project records. Account access does not automatically enroll projects or establish relevance to personal work. Account discovery returns only known profile names. The PA receives `list_connections` and `query_connection`; each query names one approved connection/profile and a supported read operation. Arguments are assembled by Go, never interpreted by a shell. Output and duration are bounded; keys and auth defaults remain owned by the external CLI.

`lin` supports assignments, issue search/details, and project listing; `agent-slack` supports message search and history; `agent-fathom` supports meetings, summaries, and open action items. Linear assignment import runs only for connections with `import_assignments: true` (default false), using their selected accounts and retaining connection/profile provenance. Read-only queries remain available when import is off and never create local projects. It imports at most 50 issues per account per sweep and does not claim exhaustive coverage.

Notion reads use the CLI's default account with an empty profile list. The daemon never changes that default to impersonate per-request selection.

Slack Socket Mode below remains separate for owner messages and replies. The direct Linear API below is retained for legacy configurations.

## Linear

Explicitly enable `linear.import_assignments` (default false), set `linear.api_key_env` to an environment variable containing a Linear personal API key and configure explicit `linear.team_ids`. `Assigned` discovers the key owner's `viewer.id`, then reads active assigned issues in those teams with cursor pagination. OAuth access tokens require the value's `Bearer ` prefix; personal API keys use their raw value. No write scope or write operation is used. The adapter returns an error on partial GraphQL errors or incomplete pagination; callers must not interpret a failed read as an empty assignment list.

`updatedAt` indicates record freshness. It does **not** establish when an issue was assigned. Assignment-time queries require actual assignment history, which this adapter does not yet fetch. Current discovery is assigned issues, not lead-owned projects with no assigned issue.

Reference: [Linear GraphQL API](https://linear.app/developers/graphql).

## Slack

Create a Slack app, enable Socket Mode, create an app-level token with `connections:write`, and grant its bot `chat:write`, `im:history`, and `im:write`. Subscribe to the `message.im` bot event, install the app in the intended workspace, then set the configured bot-token and app-token environment variables. Set `slack.owner_user_id` to the owner's Slack user ID. Reinstall the Slack app after adding scopes.

Only original messages from that owner in a DM enter the PA. Channel messages, bot messages, edits and shared-channel events are ignored. Replies stay in the initiating thread. The SDK manages Socket Mode reconnects; no public webhook ingress is needed. SDK debug logging is disabled. The application must persist event claims before acknowledgement, record completion and surface pending claims after a crash. Pending requests must not be automatically re-run when prior coordination effects are uncertain. Notification calls target only the configured owner; external recipients are not part of this adapter.

Reference: [Slack Socket Mode](https://docs.slack.dev/apis/events-api/using-socket-mode/).
