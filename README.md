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

Create Fish's completions directory first if needed. PowerShell scripts are also available with `crew-assistant completion powershell`. Completions suggest config keys and supported values, login engines and configured assistants. They only read local configuration; they never contact the daemon, integrations, or model providers.

## Staying up to date

Settings → Updates offers **Ask me** (the default) or **Off**. Checks run every six hours and sooner after this project's own release. Ask me opens a decision with the versions and release notes. On a Homebrew install, its recommended choice is **Upgrade to vX**: it drains at once, finishes running work, then upgrades. Off keeps the version display without starting upgrades or opening decisions. Skipping a version hides its notice until a newer release appears. Development and demo builds do not check. Demo mode refuses a state file with an upgrade record; use a fresh state file for a demo.

Automatic mode is available through the CLI (its dashboard option is a follow-up):

```sh
crew-assistant config set upgrade.mode automatic
```

It checks each minute for a moment with no role turn or assistant reply running. Work continues while it waits. After six hours it drains anyway: nothing new starts, and running work finishes. Once draining, Activity names the turns and replies it is waiting on, their running durations, and changes to that set. A version that failed is never retried automatically; choose **Try vX again** or clear its rollback explicitly.

Before installation, the daemon saves its running binary and backs up SQLite and configuration under the state directory's `upgrade/<state identity>/attempts/`. It runs the Homebrew prefix's own `bin/brew upgrade <configured formula>`, finding that prefix from the executable's Cellar path rather than PATH. It removes `HOMEBREW_NO_INSTALL_CLEANUP` from the installer environment. After installation it replaces itself, handing over the listening socket and browser sessions, so the dashboard address and your paired browser stay the same. The new version holds all work and refuses mutations while it opens and migrates state and answers its authenticated API health check, within two minutes.

If startup fails, state and config are restored before the saved previous version starts. SQLite WAL and SHM files are removed. A saved-binary watchdog covers crashes and hangs: for a terminal start it brings the previous daemon back **detached**, logging to `<state directory>/upgrade/<state identity>/rollback-serve.log`; its own log is `upgrade/<state identity>/watchdog.log`. The next terminal start explains the rollback and names the log, including when that detached daemon is already running. A health-check failure with the process still alive replaces it directly with the previous binary. Recovery stays pending until that restored daemon opens its config and state and answers its API; the watchdog retries failed restarts. The same protection covers restarting the running version after a backup or installer failure.

Upgrade startup requires a confirmed process identity. If inspection keeps failing, admission is refused; the watchdog logs the inspection failure and retries without killing an unconfirmed PID, even after the health deadline. Check local process inspection permissions. After a completed upgrade, moving the config is allowed; a pending recovery or rollback pin still requires its recorded config destination.

Under launchd, the watchdog asks `launchctl` to restart your job. Point the owner's plist at the stable path `$(brew --prefix)/bin/crew-assistant`, with the expanded absolute path in `ProgramArguments`; the formula does not supply a service block. The job label is accepted only when launchd is the daemon’s direct parent, excluding terminal application labels and the terminal sentinel 0. While rollback is in force, that Homebrew binary execs the saved previous one before reading state, on terminal starts too. An upgrade-failed decision explains the failure and how to clear it. The dedicated dashboard rollback banner is a follow-up.

```sh
crew-assistant upgrade status          # reads the record even with no daemon
crew-assistant upgrade clear-rollback  # asks the running daemon to retry safely
```

Keep your usual `--state` and `--config` flags when using these commands. Clearing a rollback takes fresh backups and runs probation again; it does not simply remove the pin. The ordinary command refuses when no daemon is running. If the saved executable is missing, startup names the exceptional `upgrade clear-rollback --offline` recovery: with the daemon stopped, it restores both backups and removes the unusable pin without starting a process. This discards state and config changes since that backup. Install a working Homebrew binary and then start `serve`; the command refuses this rescue when the saved executable still exists.

A signal during upgrade draining abandons the upgrade without installing. The first signal during installation lets Homebrew finish and leaves a durable record for the next start; a second signal cancels it. The dashboard remains available during installation. A stop during probation stands down the watchdog; a second probation start rolls back. A stop during restoration lets recovery finish restoring the backups without restarting the daemon. The record is read before config migration or opening state on every start, including through state-file aliases, and recovery can repeat an interrupted restore. If the recovery watchdog cannot start, the previous daemon reports this and still attempts direct recovery. Only the most recent successful attempt's backups are kept. Cleanup errors are reported and retried at startup without stopping a healthy daemon. An incomplete backup prevents installation, restarts the running version with a failure decision, and retains any existing rollback protection. Installer failures give an exit status and manual retry guidance; arbitrary installer stderr is deliberately excluded because hooks can print credentials.

To upgrade by hand, run `brew upgrade shhac/tap/crew-assistant`, then restart. Standalone installs keep the manual release-download instructions and cannot self-install.

## Run it

Team turns are recorded prospectively in SQLite, one record per runner attempt,
including PM turns, release QA and retries. Records retain project and member
attribution, optional task attribution, actual resumed or fresh opening outcomes,
fresh reasons and provider token counts across restarts. An unspecified CLI model
means **provider default**, rather than a guessed resolved model.

Missing accounting stays unknown; reported zero stays measured zero. Terminal
usage, partial response observations and compaction accounting are retained
separately and are never added together. Interrupted attempts whose launches
cannot be confirmed gone stay held until recovery can settle them. Existing
transcripts are not reconstructed; task and member dashboard histories are a
separate follow-up.

Terminal accounting writes retry without rerunning inference. If those writes
remain unavailable, the admitted claim stays held instead of scheduling the
work again, including when the assistant asks the PM directly. No-tools CLI
and API turns have no durable harness process identity, so restart recovery
preserves uncertain cleanup even after terminal accounting is recorded and
when the launch marker is absent. Later cleanup confirmation is recorded
separately without changing the terminal outcome or provider counts.

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
./crew-assistant doctor
./crew-assistant serve --open
```

`doctor` checks configuration and logins, and proves for each installed CLI that team roles can run under its sandbox, all without inference. Assistants are set up on the **Team** page, each with its own name, personality, look, engine, model and effort; **Settings → Assistant** chooses which one you work with. The first is Milo on `codex / gpt-6-astra / high`, and a config from before assistants becomes that first one, in the seat. Each engine has its own section under `engines`: `engines.codex.home` defaults to `~/.local/state/app.paulie.crew-assistant/codex`, and Claude uses its native `~/.claude` login unless `engines.claude.home` says otherwise. Configuration lives in `~/.config/app.paulie.crew-assistant/config.json` and state in `~/.local/state/app.paulie.crew-assistant/`, honouring XDG overrides and explicit `--config` / `--state` flags. Configuration holds credential **environment variable names**, never secret values. [`config.example.json`](config.example.json) shows every setting.

```sh
./crew-assistant config list                                       # every setting, with what it does
./crew-assistant config get engines.claude.usage_floor.1w_percent
./crew-assistant config set engines.claude.usage_floor.1w_percent 2
./crew-assistant config unset engines.claude.usage_floor.1w_percent  # back to the default
```

A file from an earlier version still loads, and is rewritten in the current layout when the daemon starts. `doctor` and `serve` name any key that has no effect, and where a renamed one went; `config unset` removes it.

```sh
./crew-assistant chat 'Draft a thank-you note to the launch team'
./crew-assistant status
./crew-assistant pause
./crew-assistant resume
./crew-assistant engine pause claude --for 1h
./crew-assistant engine resume claude
./crew-assistant engine status
./crew-assistant dashboard open
```

Only one daemon can own a state file. `serve --no-dispatch` observes without running any team work. **Pause** stops new team turns; a turn already running finishes. Use the pause control beside an engine's sidebar usage, or `engine pause <engine>`, to reserve that engine for your own work. Choose an open pause, `--for 4h`, or `--until 18:00` (the next local time; RFC 3339 also works). Timed pauses lift automatically, including after a restart. Other engines carry on, and your own assistant messages still send.

## How work gets done

- **Projects** are areas of your work: an email thread, a document, a book, a codebase. Each has a **brief** (goal, audience, constraints, criteria), versioned so every draft and review records which version it answered.
- **Teams** say how a project's work gets done. The `draft` team is a writer and a reviewer, on different engines by default (Claude writes, Codex reviews) so the review is independent. You or the assistant can change the engines, the number of rounds, and a folder approved drafts are copied into.
- **Team members** are the specialists you keep across projects, on the **Team** page: a named researcher, designer, implementer, reviewer, QA or PM, or one who holds several of those (at most one of implementing, reviewing and QA), with an engine, instructions of its own, a face and what it has learned. A project's team can take a member in place of the template's role; the member brings its name, engine and instructions, after the template's. A learning says when it applies and what to do. Like a skill, a member starts each task knowing only when each of its learnings applies, and reads one when that situation comes up. Members record their own when a turn teaches them something, on a standing rule: a learning is never about one project, and any example in it is made up. One that names the project's folders, repository, title or ids, an address, a link or anything like a key is dropped, and nothing learned while answering a pull request is kept, since that turn read what people outside the team wrote. You add learnings on the Team page too, or the assistant records one when you say so, and you can forget any of them. A member keeps at most 30; the oldest it taught itself makes room, and yours are never pushed out.
- **Requests** (tasks) are the outcomes you ask for. Each runs through a loop: the writer drafts in the project's private workspace; each reviewer reads its own copy against the brief and returns pass, revise or a question; findings go back to the writer until the reviewers pass or the round limit is reached. A project's **board** shows its requests by stage, in columns of equal width: to do; research, with designing below researching; implementing (or writing); and checks, with QA above reviewing. A team without research, design or QA has no lane for it. Requests ready to land, or waiting for your approval, sit in a strip above the board, one row each, which is usually empty; landed and stopped ones fold away below it. The stage is worked out from what the loop is doing, never set by hand. Queued requests start in the order of the to-do list, which you or the assistant, as the project's manager, can reorder. Once started, work already under way comes first: when several requests are active (an approval arrived, or an answer came in, while another was running), the loop carries on with the one furthest along, landing before deciding before reviewing before writing, then the one with more drafts done, then the one started longest ago. Started work spends as little time as it can between the to-do list and landing.
- **Research** comes first for a code team, which has a researcher seat (called the planner before; existing teams and members were renamed where they stood, keeping everything else). Before anything is written, the researcher reads the repository without changing it, and may search the web for what the repository can't tell it (nothing its shell runs reaches the network), and leaves a plan on the request: what already exists, what will change, what is out of scope, what is unclear and what the request has to wait for. The implementer and the reviewers work from that plan, and reviewers flag work beyond it. Questions come to you before any code is written, and your answer starts the first round. A request that depends on others, whoever linked them, waits in the to-do list until they have landed, then is researched again on top of them. Seats are how a team is shaped: one member can research and implement from one seat, working on one thing at a time, and a seat never both implements and reviews. Choose a team without research when you would rather start writing at once.
- **A designer** gives design input, if you give the team one: a member holding the designer role, in a seat of its own or beside another role. No template has one, so a team without it works as before. While researching or implementing, the researcher or the implementer can hand the request to the designer with a question; the board shows it in the designing lane, and the request comes back to them with the designer's answer, which is kept on the request for everyone who works on it afterwards. The designer only reads: it never changes the draft, commits, lands or approves, and a message can't reach it. When a question needs more than design input, it brings it to you with the evidence, the alternatives, their consequences and its recommendation. Each step can hand a request over twice per round; past that the question comes to you instead.
- **A PM** keeps a project's to-do list, if you give the team one: a member holding the PM role, in a seat of its own or beside another role. When work is queued, researched or finished, it looks across the unfinished requests and their plans, then sets the order they start in and what waits for what. It rewrites only the links the team set; yours and the assistant's stay. It asks you what it can't settle. It never directs the others. The PM may reorder the list even after you or the assistant ordered it. It treats your order as your priorities and keeps it unless it has a stated reason to change it, never simply moving back what you moved. Every change shows in Activity with the previous order and the reason, so you can drag it back. It asks only for decisions you must make, never approval of an order.
- **Seeing work move.** A request with a role at work shows it live on its card and panel: how long it has run, its tool calls, the files it has changed and when it last did anything, with the tool running now in the panel. A turn that has gone quiet for a couple of minutes says so. A request that is with a role but not being worked on says it is waiting for that role to pick it up, and why when it can tell: the team is on another request, teams are paused, crew-assistant is stopping, or when it tries again.
- **Changing a draft by hand.** `crew-assistant task list` lists requests, and `task show <id>` says where one's work is: its branch, latest draft and the daemon's own workspace (`task path <id>` prints just that; look, but don't change it, since every step resets it). Any unique start of an id will do. To change a code request's draft yourself, `task checkout <id>` puts it in your repository as its branch (or `--worktree <dir>`), without touching the branch you're on or moving one you've committed to. Commit your change there, then `task adopt <id>` takes it as the next draft, marked as yours: the reviewers and QA check it like any other, and the implementer builds on it if another round follows. `--approve` approves it as you hand it over, so the reviewers are skipped but QA still runs before it lands. A draft can't be taken while a role is at work on the request.
- **External conditions** hold a request’s start and landing, or only its landing: add and clear them in the request panel or by asking the assistant. Manual conditions wait for you or the team; “daemon includes request” clears when the running build includes that request’s landed change. Cleared conditions stay on the record. A research plan can name a prerequisite no task tracks, such as a library release. It stays in To do until you choose "It's ready" or "Don't wait for it"; confirmed and dropped outcomes stay in later plans. Answering in your own words or dismissing the question leaves the condition open, for you to clear on the request page. Owner-confirmed external prerequisites, including “Don't wait for it”, stay on their task across replans and restarts with the original condition, identity and exact answer. Research sees those settlements; Crew reconciles repeated wording before asking again. Saved planning questions are checked again just before their decision opens, including after a restart. Materially changed versions, scope or capabilities still need a new decision. Only an explicit owner instruction can reopen a settled condition. In chat or a task decision, explicitly say `Prerequisite <blocker id> no longer holds: <original condition>`. Chat reopening is checked against your recorded message and the settlement current when you sent it; delayed answers and retries cannot undo a newer settlement. Complete conditions are preserved up to 3000 bytes; larger proposals are rejected rather than truncated. An invalid plan retries research without starting implementation. Previous settlements remain in the task's history. If reopening interrupts the first implementation turn, its claim loses write authority but holds replacement work until that turn ends; after a restart, an unreclaimed launch keeps the request held. Reopening also discards any prepared first-draft handoff. Other owner answers retain their continuation, but first implementation waits until the reopened condition is settled.
- **Relations** link one request to another of its project: it depends on the other (it doesn't start, or land, before the other finishes), blocks it (the same, the other way round), or relates to it (worth reading together; nothing waits). A pair has one link. You set and remove them in the request's panel, the assistant with `link_tasks` and `unlink_tasks`, and the team as it works: the researcher from its plan or its tools, the PM from its list, and any role can mark related work. Every role can look up its project's other requests while it works (list them filtered by status, link or words, and read one). The researcher is also shown unfinished tasks from your other projects and can make its plan depend on one; the board and request page link to it and name its project. Roles still cannot read another project's records. Only a plan creates these cross-project links; you or the assistant can remove them, and the PM keeps them. Board cards show both same-project and cross-project waits. Each link records who set it. Yours and the assistant's hold against the team, which can add links but only take away its own, and can make a request wait only before its work has begun. The board card of a request others wait for says how many.
- **Messaging the team**: you or the assistant can speak to one member of a request's team directly. To the implementer it is direction: a request waiting on your approval, a question or the round limit goes straight back for another round with it, one waiting on its pull request does too, and otherwise the next round has it; nothing reaches approval or landing until it has been taken in. To a reviewer or QA it is a request to check the latest draft now, ahead of the loop's own next step ("QA this while we wait for a code review"), with your note in their prompt; while the request is being checked their verdict counts, and their reply is kept on the request either way.
- **Decisions** are the only things that need you: approve a finished draft (or ask for changes in your own words), answer a reviewer's question, choose what to do at the round limit, or retry after a problem. Answer in the dashboard, in chat, or over Slack.
  Answered decisions are kept separately from project state, including their context, choices, recommendation, answer, respondent and time. Download the evaluation record as JSONL from **Settings → Advanced → Decision record**, or run `crew-assistant decisions export --output decisions.jsonl`. Older answers have an unknown respondent. Dismissed decisions are excluded, and the record never enters an agent prompt.

  A new task can include your checks after landing, separately from team criteria. The researcher also moves task criteria that can only be checked after landing into those owner checks as one undoable change. They join the delivery’s **After it lands, check** list and are never team requirements.

- **Delivery** is the only outward step besides the team's replies on a pull request. It happens when you approve, copies the draft into its own new folder (never overwriting anything), and a retried delivery settles instead of making a second copy.
- **Code teams** work in a private clone of one of the project's repositories: an implementer changes it, a reviewer reads the change, and implementers and QA use `run_check` to run your check in a disposable copy inside a daemon-hosted sandbox, on any engine. Checks have a ten-minute limit in the current harness; a longer check returns a timed-out result. **Let the check use this machine's own network** allows localhost for tests that start servers, without access beyond this machine. With a **run recipe** (Config → Running the app), the daemon starts the app in that sandbox on macOS and stops it when QA's turn ends. QA can try it through your allowed Chrome browser, Codex included, and keeps screenshots and what it saw with its verdict; without a browser, QA needs an engine whose session offers localhost access. An unavailable sandbox or app is reported rather than given wider network access. What **landing** means is set per project, by you or the assistant; nothing inside the project can change it:
  - a new local branch (the default);
  - a fast-forward of a branch such as `main`, which only ever moves forward. Each change lands as one commit, worded from the request, on top of what `main` holds; the team's drafts stay on the task's own branch. If `main` is checked out, landing updates your checkout in place only when it has no uncommitted changes, and otherwise asks you to commit or stash first. This applies to crew-assistant's own pushes only; your repository's config is not changed, and other pushes into it keep git's default refusal;
  - with **Use pull requests** on, a GitHub pull request, opened with your `gh` login. Its board grows two rows above Ready to land: **PR to open**, once its checks pass, and **PR open**, until it is ready, which means approved where the repository asks for review, every check green, every review thread resolved and no conflicts. Who opens it (the PM by default, you, or the implementer) and who approves a ready one merging (the PM by default, you, or no one) are set with it. The implementer writes its title and description. When it gets feedback, the implementer revises, replies in its threads or conversation, resolves threads a pushed draft fixed, or hands the question to a reviewer, QA or the PM, whose answer is posted there too; the PM can merge, hold, send it back or ask you. Only feedback from the repository's owner, members and collaborators is acted on, and it reaches the team as information, never as instructions. An update that changes what runs or instructs (workflows, prompts, the check) waits for you before it is pushed. Turning pull requests off has the PM, or you, choose for each task that started with them whether it keeps its pull request.

  A project can also **pause landing**, as for a code freeze, on its Landing settings: nothing lands until you resume, and with pull requests they still open and are answered, and only merging waits.

  Before landing, a change catches up with whatever landed since. A clean merge keeps your approval; a conflict goes back to the implementer and comes back to you, except a conflict with another task of the project built at the same time, which comes to you first. A project lands one change at a time. Nothing the project does not own is ever forced, and a change built on another lands after it. If `main`'s history was rewritten since the change last joined it, only the change's own work is replayed onto the new `main`, so nothing you dropped comes back.

  The team's commits are signed exactly when your own commits in that repository would be: your global, system and repository git config decide, including the key, `gpg.format` and signing program. A project can instead always or never sign, set by you or the assistant with the team. Commits are authored as `crew-assistant`, so a host that checks the signer against the committer's email may show them as unverified.
- **Wake-ups** let an agent wait instead of checking back: the assistant (`wake_me_when`) and implementers (a `wake` block in their reply) can wait on a task, a branch, a time, or a pull request's checks or reviews. Each has its own note for later, a handle to cancel it, and a timeout. Each is delivered with when it was registered, seen and delivered, so a stale one can be recognised.

Failures resolve at the lowest level that can: a failing role is retried twice with growing waits before you hear about it; a sandbox or login problem comes to you at once. Team roles leave part of each Codex and Claude usage window unused, 10% of the 5-hour and of the weekly window by default (`engines.<engine>.usage_floor.5h_percent` and `1w_percent`; 0 turns one off). When less than that is left, the task waits for the window to reset instead of failing, and starts again by itself once it has. `engines.<engine>.on_unknown_usage` set to `pause` holds roles while usage can't be read.

Work runs in parallel as far as people are free. Each seat is one person who works on one step at a time, across every project and every role they hold: a member seated in two projects, or holding two roles in one, does those steps one after another, in the order they were asked for, while different people work at once whatever engine they run on. Adding another seat filled like one (Claudius gains Claudius #2) adds another person, so that work runs twice at once, and seats filled from one member give a draft one verdict between them. A free teammate takes the next step whenever they are free. Researching through QA each hold up to their column capacity (10 by default), set on Config. Finished tasks wait in their column until the next has room. To do, PR to open, PR open and Ready have no limit unless set; triage is never limited. There is no overall limit unless you set a number (`set_parallel`, or `PUT /api/projects/{id}/parallel`); tasks past an overall limit wait on the to-do list, in order, and tasks waiting on you or outside checks count towards column capacity but not the overall limit. Returning tasks keep their place even if its stage is full. A step that is ready but waits says who or what for, on the board and the task. `engines.<engine>.role_runs` is an optional safety cap on team turns running at once on an engine across every project; it is off unless you set it. No new team turn starts while you chat or the composer is busy, and team turns run at background priority where the CLI allows it.

### Roles, sandboxes and trust

Each role runs as an ordinary headless Claude Code or Codex session through [`lib-agent-harness`](https://github.com/shhac/lib-agent-harness), with its own tools, in the project's workspace, under the CLI's own OS sandbox: writes only inside the workspace (reviewers get none), and no network for anything it runs, except for an implementer's individual tests or QA's access to a daemon-hosted app where the engine offers a network limited to this machine. The project's check and QA's app commands run in separate daemon-hosted command sandboxes, proved by the harness before launch; the project's localhost setting applies to the check independently of the role's engine. The sandbox is proved against the installed CLI before any credentialed launch, and for Codex read back from the running session before its first prompt; a role whose sandbox cannot be confirmed does not start. Your own CLI settings, hooks, plugins and MCP servers never reach a role.

What this does not contain, stated plainly: a role can **read** what your account can read, and its model provider connection is an outward channel for what it reads. Claude Code's file tools are confined by permission rules; its OS sandbox covers its shell. The standing rules — no deployment, no production data, no purchases — are enforced by the sandbox where it can and by instruction elsewhere. See the [trust decision](design-docs/decisions/2026-09-role-sandbox-trust.md) and the [sandbox evidence](design-docs/reference/2026-09-23-cli-sandboxes.md).

The assistant itself never writes project files or runs commands; it works through its own tools with native tools disabled.

## Dashboard

The **inbox** shows what needs you first, at full size, with what happens if you approve and how reversible it is; everything under way is one line each, and what landed today is a single line. The navigation counts only what needs you. A project has tabs for its **board**, **brief**, **team** (who fills each role, and each member's roles on it), **config** (how the team works: the kind of work, engines for roles no member fills, rounds, the QA check and where approved drafts go; for code projects also how changes land and the workspace; and the project's folders) and **activity**, which hides the individual steps of work until asked. Opening a request shows its decision in full, each draft or change with what every checker said, the written draft itself, its relations to other requests, and a thread for messaging its team. Memory keeps preferences apart from observations that can go out of date. The dashboard can be light, dark or follow the system (**Settings → Appearance**), and the chat is a pane you open and close with ⌘J. Once a reply has landed and nothing is waiting, the empty message box may suggest your next message, written by a small model; Tab takes it as a draft to edit, and typing dismisses it. It never sends anything or runs a tool. Suggestions and loading messages use `gpt-6-luna` (low effort) on Codex or `haiku` on Claude: the assistant's own CLI first, then the other if that one is not installed, signed out, out of usage or failing. **Settings → Models** can choose any other model for them instead, on a CLI login or the API, each call kept to a short reply, and holds how each engine is reached. A CLI that fails is skipped for ten minutes, and each attempt is short, so neither ever holds up the conversation. Under **Running** in the navigation, each of Codex and Claude shows what its login reports it has left in its tightest window, with when that window resets if the CLI says, or plainly why there is no figure (not installed, not signed in, not reported, or the check failed). It is highlighted as low below that window's usage floor, when team work waits, and as out of usage or rate-limited when it has none left or refused a suggestion for its rate limit; suggestions and loading messages skip a CLI whose login was last measured with none left, and only look at its usage again, in the background every five minutes, until a check measures usage left; a failed check doesn't count. Usage is read every five minutes without a model call, so looking spends none; while crew-assistant is stopping, only the last reading is shown. **Settings** holds who your assistant is, appearance, models, chat options, connections and limits. The **Team** page lists your assistants first, then your members, and each is set up the same way: a name, a personality (how they write), a model and a look. A member can also be allowed **browser use**, whatever their roles: every turn they take then gets your real Chrome, signed in as you, told it is only for looking and never for signing in, submitting forms or acting on an account. It is offered on Claude and on Codex, whose browser bridge lib-agent-harness proves is confined by the session's sandbox before each launch; a turn whose Chrome isn't connected goes on without it. Memories about you are shared by every assistant; what an assistant remembers about itself is kept with it, and goes when it is deleted. Adding an assistant or a member can start with **Suggest a name and personality**, a short interview: your assistant asks what you want of them, then suggests a name, a personality and how they look, which fills in the form. Once they are added, Codex draws that look, and everyone on the team is drawn the same way: a cute 2D chibi manga face, head only, that still reads at 20 pixels. A drawing takes a minute or more, runs in the background within your Codex usage limit, and can be redrawn with a new look from an assistant's or a member's page. Each picture is kept at three sizes: for the member page, for cards and the chat, and inline beside text or as the browser tab's icon. Until a face is drawn, the assistant shows a vector sketch it made and a member a preset face from its name. Pictures are decoded and redrawn by the daemon before they are shown, so nothing but pixels comes from the model.

To attach files to a message, drop them onto the conversation box or paste them into it. They wait above the box, where you can remove them, until you send. Attaching a file, like typing, dismisses a suggested next message. Only UTF-8 text files can be attached: any `text/*` type, JSON, XML, YAML, TOML, and common text and source extensions such as `.md`, `.csv`, `.log`, `.go` or `.py`. Each file is sent inside your message as a labelled block. The message and its attachments together can be at most 24,000 bytes, the daemon's message limit. Images, PDFs and other binary files are refused with a reason; they need a transport the daemon does not have yet. Pasting text inserts it as usual. Any files on the same clipboard, such as a copied file that also carries its name as text, are attached or refused with a reason as well. A message the daemon refuses can be restored to the box with its attachments.

With Claude or Codex as its model, the assistant's conversation runs on a session the CLI keeps open. The CLI holds the conversation, so each turn sends only your message and a short note of what changed at your level since the last one (a project set up, a request landed, a decision opened), and the provider serves the rest from its cache. A new conversation, or one whose history was just compacted, is given the overview of your projects again; details of a request come from `read_task` and `ask_pm`. The session's only tools are the assistant's own, and the CLI's native tools are proven absent before it starts. Allow the assistant browser use in its settings and its session is sandboxed instead: the browser and the assistant's tools sit beside the CLI's own, which can read files on this machine but never change them, with a shell that reaches no network. It is told the browser is only for controlling a browser, never for signing in or acting on an account; while Chrome isn't connected, the chat goes on restricted as before. It survives a restart: the daemon resumes the same conversation in the CLI, or, when it can't, starts a new session from the conversation's record, summary and latest exchanges. Changing the model or the assistant's name or personality, or seating another assistant, starts a new one the same way. A line under the chat's header shows the session's model, how much of its context is used, how much of the last turn came from cache, and whether it was picked up where it left off, with Compact and Start fresh beside it. With an OpenAI-compatible endpoint, or a CLI that can't run a restricted session, turns run one at a time as before.

Three commands can be typed as a whole message; anything else, such as `/new plan`, is an ordinary message. They wait in the queue like any message and are never sent to the model. `/compact` summarizes the conversation so far and shows the summary where it was made. A Codex session then compacts its own history; a Claude session can't be asked to from outside, so it is set aside and the next turn starts a new one from that summary and the last two exchanges, word for word. Claude compacts on its own as its context fills. Without a session, every turn carries an overview of your projects. That overview is where each request stands and what it waits on, open decisions, and the last dozen finished requests by how they ended. How a request is being built and checked stays with its team. The assistant reads a request in full with `read_task` when you ask about it, and asks a project's PM with `ask_pm` about its order and how its requests fit together; the PM answers and changes nothing. How much a turn may send follows the model: the window its provider states, learned from its replies, or 128 KB until it has said. `/new` and `/clear` start a fresh conversation that opens with an overview of your projects, running tasks and open decisions. The old conversation is archived, not deleted: the clock button in the chat's header lists past conversations, and you can open one and continue it. A command you type with attachments is not sent, and the attachments stay in the box. An unknown command is refused and says which commands exist. The assistant can do the same itself through its `manage_conversation` tool, and it takes effect once its reply is finished. It can also have a task's implementer compact its working session or start afresh at its next round. Only a Codex implementer can compact; Claude Code offers no compaction to a session driven from outside. Reviewers, QA and project managers start fresh every time, so they keep no conversation to act on.

**Project PM chat.** A project's **PM chat** tab opens its retained conversation with its PM, alongside the assistant pane. The PM’s next look receives recent completed exchanges from the shared record to keep agreed priorities in mind. Ask about tasks, priorities and dependencies; the PM can tidy, link, queue and reorder work within that project. Replies show **Changes made** with links. Messages wait while the PM is busy and can be retried after a failed turn. Chat replies continue while work is paused; changes start no work until it resumes. Without a PM, the Team tab explains how to add one.

**Stopping.** The first Ctrl-C (or SIGTERM) stops new work being taken and lets the daemon exit once the work in progress is done: a role's turn, a chat reply, a landing. The dashboard stays up meanwhile and shows *Stopping*. Messages sent while it stops are queued for the next run. A second Ctrl-C cancels the work in progress and exits within a few seconds; a cancelled task step resumes on restart. A service manager waits only so long before it kills the process outright, so give it room to drain: raise `ExitTimeOut` in a launchd plist (the default is 20 seconds) or `TimeoutStopSec` in a systemd unit.

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

**Linear tools:** the assistant can use `lin` with configured accounts, and a project’s PM can use its linked account. Reads are the default; **Allow changes** on each connection lets them edit Linear, with attempts and outcomes in Activity. Deletes, archives, raw API and file operations remain unavailable.

**CLI connections:** select existing accounts from `lin`, `agent-slack`, and `agent-fathom` in Settings. Each connection has a name and an explicit list of allowed accounts. Slack uses the workspace aliases from `agent-slack auth list`, passed to queries through `--workspace`; Fathom uses profiles. The assistant names the connection and selected account for each query. Credentials stay with the CLI. The daemon provides bounded read operations rather than arbitrary command execution. Slack search can use the broader access of your existing `agent-slack` account independently of the bot.

**Notion:** add an `agent-notion` connection without choosing a profile (`"profiles": []`, or omit the field). Search, page reads, and block reads use the CLI's current default account and native authentication, including its native environment credentials. The assistant never switches that default. Changing the default in `agent-notion` changes the account this connection reads. Named Notion profiles are rejected because the CLI cannot select them per call. Account discovery reads local metadata, not project or message content. Slack aliases with unavailable stored credentials remain visible with a re-authentication hint; configure credentials through the CLI on the daemon host.

**Slack bot:** edit **Settings → Connections → Slack bot messaging**. Set the workspace ID, your Slack user ID and the environment variable names holding the bot and app tokens. Choose the assistant, or a project whose PM will answer. Project messages use that PM’s existing tools and appear in the project conversation, with separate history for each Slack thread; only that project’s decisions are sent as notifications. A project can be selected before its team is chosen, but needs a PM to answer. Saved connection changes take effect after a restart; the current connection stays in use until then. Use a Slack Agent app with Socket Mode and `message.im` events. Only your original DMs in the configured workspace are accepted. The bot is separate from CLI querying. See [integration setup and protocols](internal/integrations/README.md) for scopes and delivery semantics.

For credentials in a private dotenv file, pass its path explicitly at startup:

```sh
crew-assistant --env-file /path/to/.env.local doctor
crew-assistant --env-file /path/to/.env.local serve --open
```

`--env-file` is supported by `serve` and `doctor`. Existing environment variables take precedence. Files are never discovered automatically in project folders, and demo mode refuses the flag. Values stay out of config and errors; sandboxed team sessions do not inherit these credential variables. Keep the file private and outside version control. `.env` and `.env.*` are already ignored in this repository.

**Legacy Linear API:** set `linear.import_assignments` to `true` as well as explicit `linear.team_ids` and `linear.api_key_env` to enable assignment discovery. Omitted import flags stay off on upgrade, including older configurations. New setups should use the `lin` connection; configuring one supersedes legacy API discovery. Imported issues become projects without a brief or team; nothing starts until you ask for it.

**Project Linear pick-up:** in a project's Config tab, optionally choose a Linear team or project through your existing `lin` connection, then status and assignee rules. Matching issues become tasks on the five-minute sweep; each task shows its source issue and keeps a bounded description snapshot for the team. Unlinking stops pick-up and keeps imported tasks. Nothing is written to Linear.

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
