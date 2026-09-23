# crew-assistant

A personal AI assistant that remembers context, coordinates project agents, follows up on stalled work, and brings its owner prepared decisions. A Go daemon and CLI with an embedded, dark-mode-first dashboard. Private Tailscale access is optional.

## Install

```sh
brew install shhac/tap/crew-assistant
```

Homebrew installs Bash, Zsh, and Fish completions automatically. Standalone binaries for macOS, Linux, and Windows are available on the [releases page](https://github.com/shhac/crew-assistant/releases).

For a source build or standalone binary, enable completions in your shell:

```sh
# Bash: add to ~/.bashrc (requires bash-completion).
source <(crew-assistant completion bash)

# Zsh: after compinit in ~/.zshrc.
source <(crew-assistant completion zsh)

# Fish: run once.
crew-assistant completion fish > ~/.config/fish/completions/crew-assistant.fish
```

Create Fish's completions directory first if needed. PowerShell scripts are also available with `crew-assistant completion powershell`. Completions suggest config keys and supported values, assistant/worker login profiles, and configured worker projects/models. They only read local configuration; they never contact the daemon, integrations, or model providers.

## Run it

Building requires Go 1.26.4 or newer. `make build` writes the gitignored `./crew-assistant` binary. Node is only needed when developing or rebuilding the dashboard; its compiled assets are included in the repository. The default model engine requires a compatible Codex CLI installed and logged in on the daemon host.

```sh
make build
./crew-assistant serve --demo --open
```

Demo mode uses fictional data and disables external integrations. Without an explicit `--state`, its temporary state is removed on exit. It is useful for trying projects, decisions, memory, navigation, and the dashboard; it does not simulate an AI response.

For your own assistant:

```sh
./crew-assistant init
./crew-assistant model login
./crew-assistant config set assistant.name Quill
# Fresh configuration defaults to codex / gpt-6-astra / high.
./crew-assistant doctor
./crew-assistant serve --open
```

Choose engine, model and reasoning effort from the installed CLI’s live catalog in **Settings → Assistant and worker models**, independently for the assistant (`model`) and local coding workers (`worker_model`). The assistant defaults to `codex / gpt-6-astra / high`; the built-in specialist worker defaults to `codex / gpt-5.6-terra / high`. External manager brokers own their model selection. Explicit saved profiles are preserved when defaults change.

```sh
./crew-assistant config set model.engine codex
./crew-assistant config set model.model gpt-6-astra
./crew-assistant config set model.effort high
# Worker settings are independent:
./crew-assistant config set worker_model.engine codex
./crew-assistant config set worker_model.model gpt-5.6-terra
./crew-assistant config set worker_model.effort high
```

The `codex` engine uses each profile's `codex_bin` and `codex_home`. Both homes default to `~/.local/state/app.paulie.crew-assistant/codex` (respecting `XDG_STATE_HOME`). Set `model.codex_home` or `worker_model.codex_home` to an absolute path to use an existing dedicated login or separate accounts. Explicit configuration wins over the shell's `CODEX_HOME`: the daemon sets that variable only in each child process, never globally. You do not need to export it before starting the daemon or broker.

`crew-assistant model login` creates the configured directory privately and runs the selected CLI’s own interactive login. Use `--profile worker` for a separate worker home. Codex owns credential storage and refresh; this command never copies tokens. The same account and subscription can be used for both profiles. Login is an owner-invoked CLI command, not a model tool.

Codex currently loads global `AGENTS.md` / `AGENTS.override.md` even when project instructions are disabled. For the assistant, the adapter refuses a home containing those instructions before inference. This is instruction isolation, not a requirement for a second account. The assistant's Codex proposes structured actions with its built-in tools disabled; Go authorizes and executes permitted coordination tools.

A coding worker is different: it runs a full native session from a private durable home in application state, which the daemon's harness library owns and configures. Only the login is shared from the operator's home, so a worker gets their account without their settings, project instructions, plugins or unrelated MCP servers — and without anyone being asked to clean up or re-authenticate a home they use for their own work. Credentials stay in private state and never reach a workspace, a model, a tool result, a log or an error.

If you previously exported `CODEX_HOME`, point both configuration fields at that existing directory to retain its login. Configuration files written before these fields existed receive the app-owned default; the daemon does not silently adopt a shell's unrelated Codex setup.

The `claude` engine uses `claude_bin` and `claude_home`, defaults to the native `~/.claude` login, and keeps Claude’s credential storage and refresh in the CLI. Both assistant and worker profiles use this same home by default. A custom home selects a separate login; `model login --profile worker` uses Claude’s subscription login flow when that profile selects Claude. Ambient API keys are not forwarded to either CLI. Configure a managed worker’s optional `model_profile` only when it needs its own complete model/account profile; otherwise all managed workers use `worker_model`.

Normal operation uses local Codex or Claude CLI authentication and its billing arrangement. Separate per-project homes are unnecessary: project code runs in containers that cannot access the host CLI homes. No credentials are copied. API providers remain an explicit advanced option and are never selected automatically after a CLI error.

The `openai-compatible` engine uses Chat Completions with `reasoning_effort`. Configure `base_url`, `api_key_env` and an exact provider model that supports function tools and strict schemas. Remote endpoints require HTTPS; loopback HTTP is allowed for local providers. Astra's native API tool calling requires Responses, so use the Codex engine for this default. Unsupported selections fail rather than silently substituting another model.

Reasoning effort is separate from execution limits. API engines enforce `max_tokens` through `max_completion_tokens`. Codex does not expose a per-request output-token cap here; process time/output bounds and the daemon's model-turn/call limits apply instead. The configured Codex home's saved login selects the account used for inference; API credential references are not required for this engine. This integration never copies login tokens between homes.

Existing configuration with a `model` section but no `engine` keeps the previous API engine and provider. A missing `worker_model` in that legacy configuration inherits its previous assistant API profile once on load. To switch an existing setup, set the engine/model/effort explicitly using the commands above. Changes to the assistant profile apply to subsequent requests; restart a running worker broker to use its changed profile.

Configuration defaults to `~/.config/app.paulie.crew-assistant/config.json`; state defaults to `~/.local/state/app.paulie.crew-assistant/state.db`. XDG overrides and explicit `--config` / `--state` flags are supported. `config path` shows the selected location. Configuration contains credential **environment variable names**, never secret values. Run `config show` to inspect all effective defaults.

```sh
./crew-assistant chat 'What projects need my attention?'
./crew-assistant chat 'Please coordinate the export project against its acceptance criteria.'
./crew-assistant status
./crew-assistant pause
./crew-assistant resume
./crew-assistant dashboard open
```

Only one daemon can own a state file. `serve --no-dispatch` is a fixed boot-time control for observing without starting or resuming workers. Pause stops new coordination actions; it does not terminate already-running external work. Ctrl-C or SIGTERM shuts down the local daemon; saved worker identities are reconciled on the next start.

## Add an existing project

In **Projects → Add project**, choose **Existing folders**, browse the daemon host, and select one or more directories. You can also paste an absolute host path. The project name starts from the folder name and remains editable. An objective or acceptance criteria can be added later; registering a folder does not start agents. Before commissioning implementation, the assistant must establish a measurable acceptance contract.

Project details show the linked directories and let you change them. The same project can cover several repositories or folders. Paths are validated as existing directories, resolved to their canonical absolute locations, and retained across restarts. Registering a project never creates assistant files inside its source directories or grants a worker new filesystem permissions. Approved worker brokers still enforce their own workspace scope.

Each project has a private scratch directory under `<state-directory>/projects/<project-id>`. Assistant model invocations use temporary directories under `<state-directory>/model-runs`, removed after the invocation. Both follow the selected `--state` location. Linked source directories remain project references, never the assistant's working directory.

The reusable file/folder picker browses the **daemon host**, including over Tailscale. It supports single or multiple files/folders, keyboard navigation, hidden entries, and direct path entry. The server returns one directory level as paginated metadata; the browser renders only visible rows. It does not upload files or read file contents. Browsing requires the same owner authentication as the dashboard and is unavailable in demo mode.

## Dashboard

The home screen is an enduring **Overview** of outcomes, decisions, and project progress. Projects expose acceptance criteria, responsible agents, evidence, and activity. Decisions show context, a recommendation, and alternatives. Memory is visible and editable. Settings control the assistant identity, model, connection references, and execution limits.

The default palette is Graphite + sage, with Ink + soft blue and Charcoal + warm amber alternatives in Settings. Expand chat to give the conversation most of the workspace; pending agent/tool work has an animated indicator that respects reduced-motion preferences.

Use **Settings → Assistant setup** for a short conversation about working style and visual preferences. The model recommends a name, personality, theme, and geometric vector avatar. Preview the recommendation before applying it; setup never changes project permissions or connects accounts. The interview survives a daemon restart. This avatar is generated from a constrained shape and palette, not an image-model illustration.

The assistant's default name is defined once in configuration. UI labels, model instructions, and notifications use the resolved value. The older light concept art and date-based examples in the design journal are historical; they do not define the shipped navigation or theme.

Local access uses a single-use, five-minute pairing code exchanged for an HttpOnly session cookie. `--open` passes that code in a URL fragment, which the browser clears immediately. For another browser, use `dashboard open --print`. Private API reads and writes both require authentication. Treat the host account as trusted: another process running as that account can read local credentials.

On Decisions, use **Give a different answer** to record your own resolution, or
**No longer needed** to dismiss an obsolete question with a reason. Both remain in
history. Dismissing a decision does not send an answer, grant approval or resume a
worker that was waiting on it.

### Conversation queue and activity

Send another message while the assistant is working to queue it. Accepted messages are stored by the daemon and run in order, even if the browser closes. Queued messages can be cancelled before they start. The conversation displays each turn’s tool activity as it begins and finishes, using friendly labels without raw arguments or results. A daemon interruption marks the active turn as interrupted instead of replaying its actions; messages that had not started remain queued.

**Settings → Conversation** controls personalized loading phrases. By default, one small local CLI request uses the current message and the previous dialogue message: Luna with low effort for Codex, or Haiku for Claude, using the assistant’s configured login. Where the CLI reports no effort dial, the model’s native default applies. A different model can be selected from the CLI catalog. Captions have no tools, receive bounded context, and count toward the existing daily model-call limit. They are decorative text; tool cards carry actual action status. Generation is cancelled when the turn ends and failures keep the static loading message. API-only assistant configurations use static loading text.

The config keys are `chat.loading_phrases.enabled`, `chat.loading_phrases.model` (empty selects the small default), and `chat.loading_phrases.effort`. Disable captions to avoid the extra model request. Message delivery retries reuse a client-generated ID, so retrying an unconfirmed submission cannot enqueue it twice.

## Private Tailscale access

Install and log into Tailscale on the existing daemon host, and enable HTTPS for its tailnet. Configure the exact owner identities permitted to open the dashboard:

```sh
./crew-assistant model login # Once, if not already signed in.
./crew-assistant config set dashboard.allowed_users '["owner@example.test"]'
./crew-assistant serve --http 127.0.0.1:8340 --tailscale serve --tailscale-port 8443 --open
```

Port **8443 is the private Tailscale HTTPS port**; local HTTP remains on **127.0.0.1:8340**. Use your actual Tailscale login in `allowed_users`. The daemon prints the resulting `https://<machine>.<tailnet>.ts.net:8443` address.

The daemon binds loopback, derives the machine's HTTPS address using the family Tailscale helper, and checks route ownership before changing anything. It refuses occupied routes, preserves other services' configuration, and removes its route on clean shutdown only if the route still matches. Background routes left by a crash are reconciled on restart. Public Funnel is not supported. Tailnet access alone does not grant owner authority; the dashboard checks the configured user allowlist.

To make Serve persistent configuration, set `dashboard.tailscale` to `serve`. Network and Slack connection changes require a daemon restart. Routine preferences and limits can change live through Settings or `config set` against the running daemon. The host must remain running for the assistant to stay available.

## Connect your work

Projects live in crew-assistant's local state. Linear, Notion, Slack and other connections are optional resources, not the project registry. To manage a local codebase, choose **Add project → Existing folder**, select its directory, and ask the assistant to coordinate it. No Linear account, issue, or project is required. Worker execution still requires an approved broker. A connected work account does not make it relevant to a personal project; the assistant should use only resources you requested or linked to that project's context.

**Optional Linear imports:** connecting `lin` enables read-only queries without importing projects. Enable **Import assigned issues as projects** on a specific connection only if you want that account's assignments enrolled automatically (`"import_assignments": true`; default `false`). Split work and personal accounts into separate named connections when only one should import. Disabling import stops future polling/imports and preserves projects already recorded. Explicit assignment queries never enroll projects by themselves.

**CLI connections:** select existing accounts from `lin`, `agent-slack`, and `agent-fathom` in Settings. Each connection has a name and an explicit list of allowed accounts. Slack uses the workspace aliases from `agent-slack auth list`, passed to queries through `--workspace`; Fathom uses profiles. The assistant names the connection and selected account for each query. Credentials stay with the CLI. The daemon provides bounded read operations rather than arbitrary command execution. Slack search can use the broader access of your existing `agent-slack` account independently of the bot.

**Notion:** add an `agent-notion` connection without choosing a profile (`"profiles": []`, or omit the field). Search, page reads, and block reads use the CLI's current default account and native authentication, including its native environment credentials. The assistant never switches that default. Changing the default in `agent-notion` changes the account this connection reads. Named Notion profiles are rejected because the CLI cannot select them per call. Account discovery reads local metadata, not project or message content. Slack aliases with unavailable stored credentials remain visible with a re-authentication hint; configure credentials through the CLI on the daemon host.

**Slack bot:** configure `slack.owner_user_id` and supply the environment variables referenced by `slack.bot_token_env` and `slack.app_token_env`. Use a Slack app with Socket Mode and direct-message events. Only the configured owner's direct messages trigger the PA. The same conversation and decisions appear in the dashboard. The bot remains a separate connection from CLI querying. See [integration setup and protocols](internal/integrations/README.md) for scopes and delivery semantics.

**Legacy Linear API:** set `linear.import_assignments` to `true` as well as explicit `linear.team_ids` and `linear.api_key_env` to enable assignment discovery. Omitted import flags stay off on upgrade, including older configurations. New setups should use the `lin` connection; configuring one supersedes legacy API discovery. Discovery does not start agents and an issue's update time is not its assignment time.

**Workers:** add an existing folder to a project and describe what you want to do next, in chat or on the project page. The assistant can prepare its worker, register the private connection, commission the agreed outcome, and follow progress. Preparation creates no project run by itself. The project page also offers **Prepare worker**; external broker fields are under advanced settings. Its **Workers** panel separates prepared profiles from commissioned assignments, and shows the configured model, task, acceptance criteria, evidence, outstanding decisions and worker-reported progress time. An overdue check-in means progress needs checking, not proof of a blocker.

On macOS, automatic setup uses an existing local container runtime or a dedicated Colima instance. With Homebrew available it installs the free runtime tools as needed, without changing your current Docker context. On Linux, a working local Docker daemon is required. Missing host prerequisites produce a specific blocker; setup never purchases services or creates paid cloud resources. Windows can use an external broker.

The daemon builds a fixed Go/Node toolchain without project source as its build context and records the resulting image digest. Public Go and npm dependencies can be prepared from sanitized manifests without project scripts or host credentials. Private registries, local/git dependencies, npm workspaces, and install scripts need an explicitly prepared environment; automatic setup reports these limitations. Other languages need a custom toolchain through an external broker.

Workers use copied project workspaces inside non-root containers with no network, no host credentials or sockets, dropped capabilities and resource bounds. Git-ignored files and common secret files are excluded. A worker is a persistent Claude Code or Codex session running on the host, with its own agent loop and conversation, launched with its own tools removed and replaced by the daemon's — a change verified against that exact installed CLI before any credentialed process starts. Those tools are the only way it can reach anything, and they run in the container. The PA never receives coding tools. The original source remains untouched. Completion includes a patch, command results, and a bounded evidence digest for PA review. Missing or truncated evidence requires further inspection.

Managed state and recovery records live under the daemon’s state directory. The private loopback endpoint and its random token are owned by the daemon; users do not enter or share them. All managed workers share the configured worker CLI login by default. Existing external services remain supported through the [worker broker guide](docs/worker-broker.md). Use **Edit worker settings** on the project page, or ask the assistant to discover and select a model for that worker. The model and effort choices come from the selected CLI login. A saved preference alone does not change the model. Project-level edits preserve the login and workspace, require all project work and broker cleanup to be finished, and reconnect the idle broker automatically. Configured model information is not a claim that an assignment has run on that model. Changes made through global advanced settings may still require a daemon restart for an already-connected broker.

A worker's limits are the resources it consumes, not how long it thinks. There is no cap on turns, tools, commands, or how long an assignment may run: it continues while its subscription has headroom and, if one is configured, while its token budget lasts. Individual operations stay bounded — session startup, control requests, each container command, artifact collection — but the work itself is not on a clock. `--max-turns` on a manually operated broker is retired and no longer enforces anything; it once capped cumulative model calls, which stopped long assignments that were doing useful work. Nothing is retried automatically either: a native turn that failed may already have edited files and run commands, so repeating it would repeat those too. Stable dispatch keys and durable receipts prevent blind retries of uncertain effects. Tests use fake CLIs, runtimes, providers, and brokers; a full real-runtime installation and paid worker run have not been exercised during development.

External brokers remain trusted enforcement boundaries. They must provide idempotency, truthful progress timestamps, isolated workspaces and role-specific tools. Coordinators request work through the daemon, which enforces inherited scope and shared limits.

## Agents and coordination

Projects are ongoing areas of responsibility. Each requested outcome is a work item with its own objective and acceptance criteria; commissioned agents carry execution assignments within that item. The assistant coordinates the owner's commitments; a project coordinator is useful when the work needs one. Agents exchange messages through the daemon, which owns sessions, routing, recovery and execution limits.

Same-project peer messages carry daemon-supplied sender identity and do not grant permissions, change acceptance criteria or bypass a coordinator. Questions still escalate through the responsible coordinator to the assistant and, when needed, the owner. The project Workers panel shows that escalation contact by name. Existing `parent_id` records describe that authority and escalation relationship; they do not mean the assistant model directly owns another process.

The built-in specialist broker offers `send_message`. Delivery and acknowledgement have separate durable records, so a held acknowledgement does not repeat a delivered message. An unavailable recipient produces a not-delivered response so the sender can continue or escalate; uncertain delivery requires inspection. The roster is refreshed on starts, resumes and delivered messages, not continuously. Built-in workers remain specialists; project-manager runtimes still require a compatible external broker.

## Ongoing projects and work outcomes

Ask the assistant what to do next, then authorize the outcome. It can define the work item, prepare an execution profile and commission the agents needed. The project's **Work** section shows each outcome, its criteria, execution attempts, evidence and steering history. A finished outcome leaves the project available for the next request.

Direction belongs to the work item, so replacing or resuming an agent does not lose it. Ask the assistant to steer the work or add direction from the Work section. Delivery and an agent's explicit acknowledgement are distinct; neither proves the direction was implemented. New attempts receive the same durable context.

Queue follow-up outcomes explicitly by choosing **Define an outcome** and selecting **After … is accepted**, or ask the assistant to queue one. Each queued item stores its own objective, acceptance criteria and predecessor. Automatic commissioning waits for the predecessor's reviewed acceptance; a worker merely reporting completion is insufficient. **Remove from queue** withdraws permission to start while retaining the draft and its dependency. Ordinary draft work items never start automatically.

Each assignment has **Conversation and controls**: recorded assistant instructions, worker reports and questions, owner direction, and control/delivery events. You can add a steer there; it becomes durable direction for the whole outcome. Delivery, acknowledgement and implementation remain separate. The view contains application-visible exchanges recorded since this feature was installed, not private model reasoning or a reconstruction of older conversations. History is paginated and bounded, with an indicator when earlier records have expired.

**Pause** interrupts the running turn, waits for the worker's tools to actually stop, preserves the workspace and the coding session, and confirms cleanup before showing **Paused**. It can therefore take a few minutes. Cancelling a tool asks it to stop, which is not the same as knowing it has: a worker whose tools could not be confirmed stopped is held for inspection rather than reported as checkpointed. **Resume** continues the preserved assignment. **Stop** interrupts the current operation and ends the assignment after cleanup; it is not resumable. These controls apply to one worker and do not pause other projects. Pending controls remain visibly pending, and an owner pause prevents automatic recovery. External brokers must advertise each supported control before the dashboard offers it.

Acceptance records the exact revision of the contract, attempts, evidence and item decisions reviewed. Changed evidence rejects a stale acceptance request. The assistant can review and accept through its constrained tools; the dashboard offers the same check beside the criteria and evidence. Unresolved decisions, unfinished attempts, missing completion evidence and unacknowledged steering prevent acceptance. Acceptance is a review record, not a merge or deployment.

Existing assignments migrate into a compatibility work item. Historical project completions remain historical; migration does not invent review evidence. Explicit project completion remains available separately from accepting an outcome.

Worker recovery is available in conversation: ask the assistant to inspect an
assignment and pause, resume or stop it. These controls use the same daemon checks
as the dashboard, preserve the existing assignment on resume, and are unavailable
to autonomous background reasoning. A confirmed message refusal is distinct from
an unknown delivery outcome. Worker conversation details retain safe failure codes,
CLI phase and exit status when reported; older attempts may have no such detail.

## Worker subscription limits

**Settings → Capacity and supervision → Worker subscription usage** controls when to hold worker model requests. Codex and Claude default to **90% consumed**: reaching the threshold in any applicable short or weekly quota window holds work. This gate runs before starting or resuming an assignment *and* before every turn a running worker takes. A turn is the unit the daemon controls; one native turn is many upstream provider requests, so this is not a per-request meter. Because a turn can run for a long time, headroom is re-read on a clock while one is in progress: a worker that crosses the threshold mid-turn is interrupted and becomes a wait, not a failure. A running worker is measured against the account it was actually started with, so changing its model or login in Settings applies to it only after a restart; the percentage thresholds apply immediately. The gate uses the worker’s configured CLI login, including any per-worker profile, rather than assuming it shares the assistant’s account.

A held worker reports **Waiting for worker resources**. Its workspace, conversation and any direction you have queued are preserved; nothing was rejected, nothing is retried, no recovery attempt is spent, and the hold releases its execution slot so other assignments can run. The daemon looks again at the next check the hold names and continues the assignment by itself once the policy allows — including when you simply raise the threshold, which does not require waiting for a provider reset. A published reset is reported as what the provider expects, not as a promise about this assignment. Your pause or stop, a global pause and a no-dispatch boot all take precedence, and you can resume by hand at any time.

The equivalent configuration is:

```json
{
  "limits": {
    "worker_usage": {
      "codex_max_used_percent": 90,
      "claude_max_used_percent": 90,
      "on_unavailable": "allow"
    }
  }
}
```

Set an engine’s threshold to `0` to disable its gate. `on_unavailable` defaults to `allow`, so a missing or failed meter does not stop work; choose `pause` to hold work until usage can be checked. External broker accounts and engines with no local subscription meter cannot be inspected locally and follow that same unavailable-usage policy. An outage of that telemetry is treated as a measurement problem rather than a decision: it is rechecked, and work resumes by itself once readings return or you relax the policy. Usage is cached for up to one minute and the provider may also cache its report, so an already-admitted invocation can carry usage past the threshold. This is **headroom on a shared account**, not a raw token budget and not a cap on subscription charges. It does not apply to assistant chat.

The same policy governs a separately operated `worker serve` broker, from its own configuration file, using the same code. That broker holds its own runs but never resumes them by itself: admission and continuation are the connecting client's job, which for the built-in daemon is the supervision loop described above.

## Worker token budget

**Settings → Capacity and supervision → Worker token budget** optionally caps how many tokens one assignment may consume over its whole life, including context summaries and every resume.

```json
{ "limits": { "worker_token_budget": 0 } }
```

`0` is the default and disables the budget. Otherwise the count is the provider's own reported usage for each turn — fresh input, cached reads and cache creation, all of the disjoint input classes, plus output. Cache creation is what makes a first long prompt expensive, so counting it is the difference between a figure that means something and one that flatters long assignments. A report counts only when its figures are present and non-negative and the totals stay representable; anything else is unknown, never a measured zero. It is a **threshold checked before the next turn**, not a prepaid cap: a turn already running finishes, though one that crosses the threshold mid-flight is interrupted and held. This counts tokens, not money; no price is estimated and no pricing table is consulted.

When the budget is reached the assignment waits for you. Raise the number, or set it to `0`, then resume that assignment to continue with its saved context.

**Usage that cannot be established is never counted as free.** If a provider reports no usage for a turn, or reports a figure that is incomplete, negative or too large to add, or a reservation survives a crash or fails to be written, or an assignment recorded calls before this ledger existed, those are recorded as unknown consumption. While a reservation is unresolved the worker takes no further turns at all, budget or no budget; once the underlying storage problem is fixed, an explicit resume closes it as one unknown call so recovery is possible without pretending it was free. With a budget configured, an assignment holding unknown calls waits for an explicit decision; raising the budget cannot measure what was never reported, so only disabling the budget continues it. The dashboard and `inspect_agent` report unknown calls beside the token total, so a total is never read as complete when it is not.

`limits.max_model_calls_per_day` and `limits.max_model_turns` are separate and bound the **assistant's** own conversation and tool loop. They are not worker lifetime budgets and never were.

## Operating boundaries

The PA has no shell, code-writing, deployment, production-data, or purchase tool. Worker commissions carry immutable deployment, production-data-access, and purchase prohibitions. A broker must enforce those outside its prompts. Inference and approved worker execution are permitted operating usage.

The current limits bound concurrent execution, delegation depth, recovery attempts, the assistant's own model turns and its durable model-call reservations per UTC day, plus the worker resource limits described above. API output-token caps and Codex process bounds are described above. **A call limit is not a dollar budget or a provider subscription meter.** Provider-enforced monetary caps, quiet hours/digests, transcript-retention controls, raster avatar generation, WhatsApp, and phone calls remain follow-on work from the design journal.

Completion requires recorded evidence and no unfinished assignments or unresolved project decisions. The PA reviews that evidence against the recorded acceptance criteria; it does not deploy the result. Uncertain outbound actions are retained for inspection rather than silently repeated. Private state and external provider copies have separate lifetimes; deleting a memory does not erase earlier transcripts or remote copies.

## Long conversations and provider interruptions

New assignments start with fresh worker transcripts; resume continues the same
assignment. The assistant saves continuity summaries of older dialogue, and long
worker/tool conversations compact older resolved exchanges into durable checkpoints.
Full source transcripts remain in private state. Owner instructions and unresolved
operations stay exact; if those alone exceed the working budget, the run stops with
an explicit context blocker. The working budget is measured in serialized bytes,
not a model context-window percentage.

Confirmed provider overload, rate-limit and service-unavailable responses get bounded
backoff. Chat shows retries; workers show **Waiting for model provider** and the next
retry time. Worker cooldown survives restart, respects pause and admission limits,
and stops after six consecutive provider failures or its cumulative call cap.
Authentication problems, partial responses and uncertain transport failures do not
trigger blind retries. Completed tool actions are preserved. Recovery uses the same
configured model and login.

See the [context and recovery decision](design-docs/decisions/2026-09-context-and-provider-recovery.md)
for exact bounds and the distinction from native CLI session compaction.

## Worker diagnostics

`crew-assistant serve` and `crew-assistant worker serve` write structured NDJSON
errors to stderr through `lib-agent-output`. Each record includes the failing
stage, project/run IDs where available, engine, diagnostic code, observed exit
status, model-call count, context bytes, and any scheduled retry time. Capture
stderr alongside your usual launch command with `2>crew-assistant-errors.ndjson`.
No debug flag is needed. Supervision, persistence, container startup/cleanup and
artifact collection failures also emit diagnostics; a stopped supervision loop
causes the daemon to shut down rather than leave a connected but idle dashboard.

Local capability checks, context-summary validation and provider request failures
have distinct codes. Safe explanations are retained in worker state for the
dashboard and assistant. Unknown errors include their Go wrapper types, not raw
error text. Prompts, tool output, credentials and provider stderr are never copied
into these logs. Historical failures cannot acquire detail that was discarded by
an earlier version; new diagnostics apply to subsequent attempts. Logging never
resumes a worker or changes retry eligibility.

## Development

```sh
npm ci --prefix internal/dashboard/ui
make dashboard
make check
make test-race
make build
```

The Vite bundle in `internal/dashboard/assets/` is committed and embedded in Go. After UI changes, rebuild it. CI checks Go tests/races/vet, frontend tests/types, and bundle freshness. Runtime tests use temporary SQLite files, fake providers, and fake worker brokers. No test needs live Slack, Linear, Tailscale, or paid inference.

Design rationale and earlier concepts are in [design-docs](design-docs/README.md). Implementation choices and current limitations are recorded in the [implementation notes](design-docs/2026-09-14-first-implementation.md).

## Releasing

Commit the implementation and rebuilt dashboard bundle, then run:

```sh
make release VERSION=vX.Y.Z
git tag vX.Y.Z
git push origin main vX.Y.Z
```

The release check requires a clean tree and an unused local and remote tag, rebuilds the dashboard, and runs Go tests/races/vet and frontend tests/types. The tag workflow repeats CI before invoking the shared Homebrew-tap release workflow. CI builds the standalone binaries, publishes their SHA-256 checksums and GitHub release, and updates the formula with completions. Do not package or upload release artifacts manually.

The tap's write deploy key is the `TAP_DEPLOY_KEY` secret in this repository's `homebrew-tap` GitHub environment. That environment allows only tags matching `v*`; branch and PR jobs do not use it. The release workflow also checks the version format before entering the shared release job. Keep the key out of repository-level secrets, local config, and checked-in files. Its temporary provisioning files were deleted after upload.

## License

[PolyForm Perimeter 1.0.0](LICENSE), matching the agent CLI family.
