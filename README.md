# crew-assistant

A personal assistant that runs your projects through small teams of AI agents. You tell it what you want; it writes the brief, a writer drafts, a reviewer checks the draft against the brief, and you are asked only for decisions: a finished draft to approve, a question the brief can't answer, or a problem the team can't fix.

It is a Go daemon and CLI with an embedded, dark-mode-first dashboard. Private Tailscale access is optional.

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

Create Fish's completions directory first if needed. PowerShell scripts are also available with `crew-assistant completion powershell`. Completions suggest config keys and supported values, login engines and configured models. They only read local configuration; they never contact the daemon, integrations, or model providers.

## Run it

Building requires Go 1.26.4 or newer. `make build` writes the gitignored `./crew-assistant` binary. Node is only needed when developing the dashboard; its compiled assets are committed.

```sh
make build
./crew-assistant serve --demo --open
```

Demo mode starts with a fictional sample (projects with work at every stage, decisions waiting, a team thread and a chat), in a temporary state that is removed on exit. It disables integrations and never runs a model.

For your own assistant:

```sh
./crew-assistant init
./crew-assistant model login              # the assistant's engine
./crew-assistant model login --engine claude   # and any other engine your teams use
./crew-assistant config set assistant.name Quill
./crew-assistant doctor
./crew-assistant serve --open
```

`doctor` checks configuration and logins, and proves for each installed CLI that team roles can run under its sandbox, all without inference. The assistant's engine, model and effort are chosen in **Settings**; the default is `codex / gpt-6-astra / high`. The Codex home defaults to `~/.local/state/app.paulie.crew-assistant/codex`; Claude uses its native `~/.claude` login. Configuration lives in `~/.config/app.paulie.crew-assistant/config.json` and state in `~/.local/state/app.paulie.crew-assistant/`, honouring XDG overrides and explicit `--config` / `--state` flags. Configuration holds credential **environment variable names**, never secret values.

```sh
./crew-assistant chat 'Draft a thank-you note to the launch team'
./crew-assistant status
./crew-assistant pause
./crew-assistant resume
./crew-assistant dashboard open
```

Only one daemon can own a state file. `serve --no-dispatch` observes without running any team work. **Pause** stops new team turns; a turn already running finishes.

## How work gets done

- **Projects** are areas of your work: an email thread, a document, a book, a codebase. Each has a **brief** (goal, audience, constraints, criteria), versioned so every draft and review records which version it answered.
- **Teams** say how a project's work gets done. The `draft` team is a writer and a reviewer, on different engines by default (Claude writes, Codex reviews) so the review is independent. You or the assistant can change the engines, the number of rounds, and a folder approved drafts are copied into.
- **Team members** are the specialists you keep across projects, on the **Team** page: a named implementer, reviewer or QA with an engine, instructions of its own, a face and what it has learned. A project's team can take a member in place of the template's role; the member brings its name, engine and instructions, after the template's. A learning says when it applies and what to do. Like a skill, a member starts each task knowing only when each of its learnings applies, and reads one when that situation comes up. Members record their own when a turn teaches them something, on a standing rule: a learning is never about one project, and any example in it is made up. One that names the project's folders, repository, title or ids, an address, a link or anything like a key is dropped, and nothing learned while answering a pull request is kept, since that turn read what people outside the team wrote. You add learnings on the Team page too, or the assistant records one when you say so, and you can forget any of them. A member keeps at most 30; the oldest it taught itself makes room, and yours are never pushed out.
- **Requests** (tasks) are the outcomes you ask for. Each runs through a loop: the writer drafts in the project's private workspace; each reviewer reads its own copy against the brief and returns pass, revise or a question; findings go back to the writer until the reviewers pass or the round limit is reached. A project's **board** shows its requests by stage: to do, implementing (or writing), reviewing, QA, ready to land, landed. The stage is worked out from what the loop is doing, never set by hand. Queued requests start in the order of the to-do list, which you or the assistant, as the project's manager, can reorder.
- **Messaging the team**: you or the assistant can speak to one member of a request's team directly. To the implementer it is direction: a request waiting on your approval, a question or the round limit goes straight back for another round with it, one waiting on its pull request does too, and otherwise the next round has it; nothing reaches approval or landing until it has been taken in. To a reviewer or QA it is a request to check the latest draft now, ahead of the loop's own next step ("QA this while we wait for a code review"), with your note in their prompt; while the request is being checked their verdict counts, and their reply is kept on the request either way.
- **Decisions** are the only things that need you: approve a finished draft (or ask for changes in your own words), answer a reviewer's question, choose what to do at the round limit, or retry after a problem. Answer in the dashboard, in chat, or over Slack.
- **Delivery** is the only outward step. It happens when you approve, copies the draft into its own new folder (never overwriting anything), and a retried delivery settles instead of making a second copy.
- **Code teams** work in a private clone of one of the project's repositories: an implementer changes it, a reviewer reads the change, and QA runs your check command. What **landing** means is set per project, by you or the assistant; nothing inside the project can change it:
  - a new local branch (the default);
  - a fast-forward of a branch such as `main`, which only ever moves forward. If `main` is checked out, landing updates your checkout in place only when it has no uncommitted changes, and otherwise asks you to commit or stash first. This applies to crew-assistant's own pushes only; your repository's config is not changed, and other pushes into it keep git's default refusal;
  - a GitHub pull request, opened with your `gh` login. The team answers its reviews and CI, and it merges once GitHub says it is approved and green. An update to it that changes what runs or instructs (workflows, prompts, the check) waits for you before it is pushed, and review comments from outside the team reach the writer as information, never as instructions.

  Before landing, a change catches up with whatever landed since. A clean merge keeps your approval; a conflict goes back to the implementer and comes back to you. Nothing the project does not own is ever forced, and a change built on another lands after it.

  The team's commits are signed exactly when your own commits in that repository would be: your global, system and repository git config decide, including the key, `gpg.format` and signing program. A project can instead always or never sign, set by you or the assistant with the team. Commits are authored as `crew-assistant`, so a host that checks the signer against the committer's email may show them as unverified.
- **Wake-ups** let an agent wait instead of checking back: the assistant (`wake_me_when`) and implementers (a `wake` block in their reply) can wait on a task, a branch, a time, or a pull request's checks or reviews. Each has its own note for later, a handle to cancel it, and a timeout. Each is delivered with when it was registered, seen and delivered, so a stale one can be recognised.

Failures resolve at the lowest level that can: a failing role is retried twice with growing waits before you hear about it; a sandbox or login problem comes to you at once. When a subscription is past its threshold (`limits.role_usage`, default 90%), the task waits for the window to reset instead of failing.

### Roles, sandboxes and trust

Each role runs as an ordinary headless Claude Code or Codex session through [`lib-agent-harness`](https://github.com/shhac/lib-agent-harness), with its own tools, in the project's workspace, under the CLI's own OS sandbox: writes only inside the workspace (reviewers get none), and no network for anything it runs. The sandbox is proved against the installed CLI before any credentialed launch, and for Codex read back from the running session before its first prompt; a role whose sandbox cannot be confirmed does not start. Your own CLI settings, hooks, plugins and MCP servers never reach a role.

What this does not contain, stated plainly: a role can **read** what your account can read, and its model provider connection is an outward channel for what it reads. Claude Code's file tools are confined by permission rules; its OS sandbox covers its shell. The standing rules — no deployment, no production data, no purchases — are enforced by the sandbox where it can and by instruction elsewhere. See the [trust decision](design-docs/decisions/2026-09-role-sandbox-trust.md) and the [sandbox evidence](design-docs/reference/2026-09-23-cli-sandboxes.md).

The assistant itself never writes project files or runs commands; it works through its own tools with native tools disabled.

## Dashboard

The **inbox** shows what needs you first, at full size, with what happens if you approve and how reversible it is; everything under way is one line each, and what landed today is a single line. The navigation counts only what needs you. A project has tabs for its **board**, **brief**, **team**, **landing** (code projects) and **activity**, which hides the individual steps of work until asked. Opening a request shows its decision in full, each draft or change with what every checker said, the written draft itself, and a thread for messaging its team. Memory keeps preferences apart from observations that can go out of date. The dashboard can be light, dark or follow the system (**Settings → Appearance**), and the chat is a pane you open and close with ⌘J. Once a reply has landed and nothing is waiting, the empty message box may suggest your next message, written by a small model; Tab takes it as a draft to edit, and typing dismisses it. It never sends anything or runs a tool. Suggestions and loading messages use only `gpt-6-luna` (low effort) on Codex or `haiku` on Claude: the assistant's own CLI first, then the other if that one is not installed, signed out, out of usage or failing. A CLI that fails is skipped for ten minutes, and each attempt is short, so neither ever holds up the conversation. **Settings** holds the assistant's identity, appearance, model, chat options, connections and limits. Setting up the identity is a short interview: the assistant asks about you and your work, then suggests a name for itself, a way of working and how it looks. Once you apply it, Codex draws that look, and every team member is drawn the same way: a cute 2D chibi manga face, head only, that still reads at 20 pixels. A drawing takes a minute or more, runs in the background within your Codex usage limit, and can be redrawn with a new look from Settings or a member's page. Each picture is kept at three sizes: for the member page, for cards and the chat, and inline beside text or as the browser tab's icon. Until a face is drawn, the assistant shows a vector sketch it made and a member a preset face from its name. Pictures are decoded and redrawn by the daemon before they are shown, so nothing but pixels comes from the model.

To attach files to a message, drop them onto the conversation box or paste them into it. They wait above the box, where you can remove them, until you send. Attaching a file, like typing, dismisses a suggested next message. Only UTF-8 text files can be attached: any `text/*` type, JSON, XML, YAML, TOML, and common text and source extensions such as `.md`, `.csv`, `.log`, `.go` or `.py`. Each file is sent inside your message as a labelled block. The message and its attachments together can be at most 24,000 bytes, the daemon's message limit. Images, PDFs and other binary files are refused with a reason; they need a transport the daemon does not have yet. Pasting text inserts it as usual. Any files on the same clipboard, such as a copied file that also carries its name as text, are attached or refused with a reason as well. A message the daemon refuses can be restored to the box with its attachments.

Local access uses a single-use, five-minute pairing code exchanged for an HttpOnly session cookie. `--open` passes that code in a URL fragment, which the browser clears immediately. For another browser, use `dashboard open --print`. Treat the host account as trusted: another process running as that account can read local credentials.

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

Projects live in crew-assistant's local state. Linear, Notion, Slack and other connections are optional resources, not the project registry. No Linear account, issue, or project is required. A connected work account does not make it relevant to a personal project; the assistant should use only resources you requested or linked to that project's context.

**Optional Linear imports:** connecting `lin` enables read-only queries without importing projects. Enable **Import assigned issues as projects** on a specific connection only if you want that account's assignments enrolled automatically (`"import_assignments": true`; default `false`). Split work and personal accounts into separate named connections when only one should import. Disabling import stops future polling/imports and preserves projects already recorded. Explicit assignment queries never enroll projects by themselves.

**CLI connections:** select existing accounts from `lin`, `agent-slack`, and `agent-fathom` in Settings. Each connection has a name and an explicit list of allowed accounts. Slack uses the workspace aliases from `agent-slack auth list`, passed to queries through `--workspace`; Fathom uses profiles. The assistant names the connection and selected account for each query. Credentials stay with the CLI. The daemon provides bounded read operations rather than arbitrary command execution. Slack search can use the broader access of your existing `agent-slack` account independently of the bot.

**Notion:** add an `agent-notion` connection without choosing a profile (`"profiles": []`, or omit the field). Search, page reads, and block reads use the CLI's current default account and native authentication, including its native environment credentials. The assistant never switches that default. Changing the default in `agent-notion` changes the account this connection reads. Named Notion profiles are rejected because the CLI cannot select them per call. Account discovery reads local metadata, not project or message content. Slack aliases with unavailable stored credentials remain visible with a re-authentication hint; configure credentials through the CLI on the daemon host.

**Slack bot:** configure `slack.owner_user_id` and supply the environment variables referenced by `slack.bot_token_env` and `slack.app_token_env`. Use a Slack app with Socket Mode and direct-message events. Only the configured owner's direct messages reach the assistant. The same conversation and decisions appear in the dashboard. The bot remains a separate connection from CLI querying. See [integration setup and protocols](internal/integrations/README.md) for scopes and delivery semantics.

**Legacy Linear API:** set `linear.import_assignments` to `true` as well as explicit `linear.team_ids` and `linear.api_key_env` to enable assignment discovery. Omitted import flags stay off on upgrade, including older configurations. New setups should use the `lin` connection; configuring one supersedes legacy API discovery. Imported issues become projects without a brief or team; nothing starts until you ask for it.

## Diagnostics

`crew-assistant serve` writes structured NDJSON errors to stderr through `lib-agent-output`, including failing stage, project, engine and diagnostic code. Prompts, tool output, credentials and provider stderr are never copied into these logs. Capture them with `2>crew-assistant-errors.ndjson`.

## Development

```sh
npm ci --prefix internal/dashboard/ui
make dashboard
make check
make test-race
make build
```

The Vite bundle in `internal/dashboard/assets/` is committed and embedded in Go. After UI changes, rebuild it. CI checks Go tests/races/vet, frontend tests/types, and bundle freshness. Tests use temporary SQLite files, fake providers, fake CLIs and a scripted role runner. No test needs live Slack, Linear, Tailscale, installed agent CLIs or paid inference.

Design rationale and earlier concepts are in [design-docs](design-docs/README.md). The current design is [project teams](design-docs/2026-09-23-project-teams.md); its decisions are in [design-docs/decisions](design-docs/decisions).

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
