# Run a separate coding worker (advanced)

For normal local use, add a project folder and ask the assistant to prepare a worker. It manages setup, names and private authentication; see the [managed workflow](../README.md#connect-your-work). This guide is for operating a separate broker with a custom toolchain.

The assistant coordinates work; the separate `worker serve` process performs implementation inside an offline Docker container. This repository includes both components. You do not need to write your own execution service to try direct worker delegation. Manager agents can still use an external approved broker; the built-in broker accepts direct `worker` roles with `implement` or `review` capabilities only.

## Prepare the boundary

Use macOS or Linux with a local Docker daemon. Prepare a **dedicated, stable, nonsecret source workspace** and a locally installed Linux image containing `/bin/sh`, basic Unix utilities, the required compilers and preinstalled dependencies. The image must support running as your numeric user ID. Network access is disabled during work, so dependencies must already be in the image or approved source snapshot. The broker never builds or pulls an image, installs dependencies, logs in to a registry, or buys anything.

Supply an immutable image identifier: `sha256:<64 hex digits>` or `repository@sha256:<64 hex digits>`. Startup inspects the local image and resolves it to its immutable image ID. The broker uses `--pull=never` for every container start.

Known sensitive files and directories are excluded from the source copy: `.env*`, `.git`, credential/config directories, key/certificate files, `.npmrc`, `.netrc`, and similar names. Symlinks, device files and sockets are not copied. Source reads use `os.OpenRoot` confinement and do not follow final symlinks. File-name exclusions cannot recognize a secret embedded in ordinary source code; prepare the dedicated source accordingly and keep it stable while it is copied. The original workspace is never mounted or modified.

## Start the broker

Configure the independent `worker_model` profile in Settings or with `config set worker_model.<field>`. Fresh worker profiles use `codex / gpt-5.6-terra / high` with the login in `worker_model.codex_home`. `--engine`, `--model` and `--effort` override that profile for this broker process. Implementation workers require a local Codex or Claude CLI. Codex uses `worker_model.codex_bin`; Claude uses `worker_model.claude_bin` and `worker_model.claude_home` with its native CLI login. An API model profile cannot run an implementation worker. Changing the PA's model does not change a running worker broker.

Set an independently generated broker API token in the environment variable `CREW_ASSISTANT_WORKER_TOKEN` in both the broker and assistant processes. Select the worker login with `worker_model.codex_home` or `worker_model.claude_home`; an existing authenticated CLI home can be shared. Use `crew-assistant model login --profile worker` if that selected profile needs login. Codex shares file-backed login material into a private per-assignment runtime home; Claude uses the selected native login directly in restricted mode. Neither engine inherits the selected home’s project instructions, hooks, plugins or unrelated MCP tools. Keep actual tokens out of configuration files, command arguments and version control.

```sh
crew-assistant worker serve \
  --workspace /path/to/dedicated-source \
  --project <assistant-project-id> \
  --image <locally-installed-image@sha256:digest> \
  --http 127.0.0.1:8350 \
  --max-output-tokens 4096 \
  --max-concurrent 1
```

`--docker-socket` selects a local Unix socket when Docker is not available at `/var/run/docker.sock`. Remote TCP Docker daemons are not supported. `--worker-state` chooses the private state/artifact directory, which must be outside the dedicated source tree. Its default is beside the assistant state file.

Register the endpoint as a worker profile in the assistant configuration, preserving existing profiles:

```json
{
  "id": "local-builder",
  "name": "Isolated builder",
  "project_id": "<assistant-project-id>",
  "endpoint": "http://127.0.0.1:8350",
  "api_key_env": "CREW_ASSISTANT_WORKER_TOKEN",
  "capabilities": ["implement", "review"]
}
```

The profile lives in the configuration's `workers` array. Its broker accepts only the exact `--project` ID. Use a separate broker/state directory for another project. `crew-assistant status` includes project IDs. The PA can then commission a direct worker when you ask it to coordinate that project.

## What is isolated

Each run gets its own copied workspace. The container has:

- No network, host sockets, host home directory, provider credentials or cloud credentials.
- A read-only root filesystem, dropped Linux capabilities and `no-new-privileges`.
- A non-root numeric user, 1 GiB memory limit, two CPUs and 128-process limit.
- One copied workspace mount and a bounded 256 MiB temporary filesystem. Review-only workers receive a read-only workspace mount; managed npm workers also have a writable per-run dependency mount for temporary test bundles.

A worker is a persistent native coding session: the locally installed Claude Code or Codex CLI, running on the host, with its own agent loop, its own conversation and its own compaction. The daemon does not drive that loop. What it owns is everything a coding agent has no business deciding — whether a turn may start, what the worker is being asked to do, what counts as evidence, and when the owner has taken the assignment away.

The CLI is launched restricted, and the restriction is verified against that exact binary and configuration **before** a credentialed process starts. The check runs against a disposable home with a dummy credential and a provider that refuses every request, so it establishes what the installed CLI actually does rather than what its flags are supposed to mean. If the installed CLI cannot be restricted, the assignment stops and says which tools were the problem; it does not start a worker with a shell.

The session's own tools are removed and replaced by the daemon's, served over a private local channel that only this daemon's own bridge process can reach: `read_file`, `write_file`, `run_command`, `send_message`, `acknowledge_steering`, `ask_decision`, and `finish`. They are the only way a worker can reach anything. File writes and test commands are sent through `docker exec`; command strings are interpreted only by the container's shell. The Docker CLI receives a clean environment without inherited credentials. The PA's model does not receive these implementation tools.

Codex uses a private durable runtime home in application state, with configuration owned by the library and login material shared from the selected CLI home. Claude uses its selected native login directly, with restricted mode disabling inherited customization. Both keep durable conversations for resumption. Credentials never reach the workspace, the model, a tool result, a log or an error.

`send_message` requests daemon-mediated communication with another active agent in the same project. It yields until the daemon acknowledges delivery, then the sender can continue. The daemon supplies sender identity and marks peer content as untrusted; it never grants new authority. The project roster arrives in the task and subsequent daemon messages. Requests outside the project are rejected.

The owner-supplied image and local Docker daemon are trusted infrastructure. This is container isolation, not a claim of a hardware security boundary against kernel or Docker vulnerabilities.

## Bounds and recovery

A worker's limits are the resources it consumes. There is no cap on model calls, turns, tools or elapsed time: an assignment continues while its account has headroom and, where one is configured, while its token budget lasts. Its lifetime belongs to the broker process, and the owner's pause and stop are honoured between operations.

`--max-turns` is **retired**. It capped cumulative model requests for a worker session and stopped long assignments that were doing useful work, so passing it now prints a notice and changes nothing. Configure `limits.worker_usage` (subscription headroom, 90% by default) and `limits.worker_token_budget` (per-assignment tokens, 0 disables) instead; a standalone broker reads both from its own configuration file and applies the same policy the daemon does. A running broker measures headroom against the engine, binary and login it was started with, so editing a worker profile changes thresholds for it immediately but does not move it to another account until it is restarted.

Both gates run before **every turn**, and a turn is the unit this daemon controls. One native turn is many upstream provider requests, so neither gate is a per-request meter. A refused turn is not started another way: nothing is sent, nothing is accounted, the session is unchanged, and the run reports `usage_wait` with its reason.

Because a turn can run for a long time, headroom is also re-read on a clock while one is in progress, not only between them. A turn that crosses a limit mid-flight is interrupted, its tools are settled, and the assignment becomes a wait rather than a failure. The hold keeps its own kind: a subscription wait that clears by itself is never reported as a budget decision that needs a person. Consumption can still exceed either figure by the part of a turn already in progress. Neither is a pre-reserved guarantee, and neither is a currency limit.

Consumption is the provider's own reported usage for the turn: fresh input, cached reads and cache creation — the disjoint input classes, all of them — plus output. Cache creation is what makes a first long prompt expensive, so leaving it out made exactly the expensive turns look cheap. A report is accepted only when its counts are present and non-negative and their totals stay representable, so an absent or unusable figure is unknown rather than a measured zero. A reservation is persisted before each turn, so a crash records an unmeasured turn as *unknown* rather than as free work; the same applies to turns reported without usage, to figures that are incomplete, negative or too large to add, and to a session's history from before this accounting existed. With a token budget configured, an assignment carrying unknown turns waits for an explicit owner decision.

Alongside the charged ledger, the daemon publishes what a turn was *observed* to consume from its event stream. That is evidence of activity, not a measurement, and it is reported separately so an unmeasured turn does not look like an idle one.

While a reservation is unresolved — because its settlement could not be written — the worker makes no further requests at all, with or without a budget, and does not act on the reply whose cost went unrecorded. That state needs owner inspection rather than a timer. Once the storage problem is fixed, an explicit resume closes the reservation as one unknown call: the run is not executing, so nothing can still be billing against it. A configured budget then holds on that unknown until the owner decides; a disabled budget continues, with the uncertainty still recorded.

There is no whole-turn deadline. Stopping useful work at an arbitrary elapsed time was never a resource policy, and the operations that can genuinely run away are bounded individually: session startup, control requests, a 45-second per-command limit inside the container, and artifact collection. There is no limit on how many commands a session may run — a long build-and-test loop is the work, not a symptom — but each one's time and captured output are contained and every one is recorded as evidence. Source/artifact collection is limited to 10,000 source files, 64 MiB total and 2 MiB per file, and command output to 64 KiB. Repeated failing commands are not treated as a reason to stop: a red test run is ordinary progress. The daemon's own stalled-progress check, which escalates an assignment whose substantive status, summary and evidence have not changed across several check-in windows, is what notices work that has genuinely stopped moving.

The command limit is the container's, not the client's. Cancelling `docker exec` ends the local client; the process it started may still be running inside the container, still writing. So the limit is enforced by the shell that runs the command, and a client-side cancellation is recorded as an unknown outcome. While one is outstanding, the worker may read but not write or run anything else, and it cannot report the work as done — a write landing beside an unknown process is what makes the evidence afterwards undescribable. Confirmed removal of the container settles it: the processes are gone, whatever they were doing. What they did stays recorded as unestablished, and the next attempt is told to re-read and re-run rather than inheriting the assumption that it was fine.

These are resource bounds, not a dollar estimate or a promise of free model inference. Model calls can cost money under the configured provider.

**Nothing is retried automatically.** The previous contract drove workers through a request-per-proposal transport, where repeating a refused request was a safe no-op. A native turn is not that: by the time one fails, the worker may have edited files and run commands, and starting it again would do all of it a second time on top of itself. Every failure stops the assignment with its work preserved, carrying the harness's own diagnostic code, and waits for a person.

Launch and control requests use durable idempotency keys. Interrupted execution requires the explicit resume endpoint; an ordinary message cannot bypass recovery limits. Resuming keeps the existing copied workspace, baseline and coding session. On broker restart, recorded containers must be verified as owned and stopped before any run becomes recoverable, and a harness process left behind by a crashed daemon must be confirmed gone before a second worker is started beside it.

Direction is recorded as handed over *before* it is handed over. A missing acknowledgement is not proof a worker never received something: a CLI that took a prompt, ran tools and then lost its process leaves exactly as little behind as one that never heard it. So a refusal the daemon saw happen before anything was sent is said again, and anything else stops the assignment and asks. An explicit resume re-sends it marked as a possible repeat, so the worker reconciles it against what it already did.

A resource wait is not a recovery: it consumes no recovery allowance, schedules no retry, carries no provider failure classification, and releases the daemon's execution slot. Each hold states its kind and when to look again. A daemon may re-admit `subscription_quota` and `telemetry_unavailable` holds automatically once their stated next check is due, re-reading both policy and account first; a published `resets_at` is the provider's expectation, not a promise, and is never used as the earliest eligible moment. `token_budget` and `usage_unknown` holds are owner decisions and wait for an explicit resume. A hold with no kind this daemon recognizes, or with no stated next check, also waits for the owner rather than being restarted on an invented schedule.

This broker holds its own runs but never continues them by itself. Admission and continuation belong to the connecting client: the built-in daemon supervises and resumes, and an external client operating this broker directly owns that job.

## Review results

A worker's `completed` report is published only after its container has stopped and actual artifacts have been collected under:

```text
<worker-state>/runs/<run-id>/
  workspace/                 # resulting isolated copy
  artifacts/changes.patch    # content changes against the initial copy
  artifacts/commands.json    # commands, success flags and captured output
  artifacts/summary.txt      # changed paths and command count
```

Evidence includes these concrete paths plus a broker-generated digest: changed paths, actual command success/failure counts, failure-first command/output excerpts, and a content-patch hash and excerpt. The PA evaluates those supplied excerpts without receiving arbitrary host-file access. Truncation and omissions are explicit; when an acceptance criterion needs omitted information, the PA must obtain that evidence or escalate for inspection rather than infer success. The patch records text content changes; binary changes are identified by content hashes. It is not a full Git commit and does not encode permission-bit or symlink changes. The resulting workspace remains available for inspection and manual transfer. Nothing is committed, pushed, merged or deployed automatically.

An explicit `finish` tool call supplies the worker's acceptance summary. The PA still compares it with the actual evidence before accepting the work item; accepting an outcome does not close its project. A successful command is recorded as such; missing tools, failed checks and unresolved questions must remain visible rather than being described as passing tests.

The broker persists at most 1,000 runs in one state directory. Preserve or archive completed state deliberately; automatic artifact deletion is not currently implemented.


## Durable steering receipts

As of v0.6, the daemon appends work-item direction to start/resume context and routes new direction to live attempts. The built-in broker exposes `acknowledge_steering` with a `message_ids` array. It persists cumulative, unique IDs in the run response's optional `steering_acknowledgements` array, including the final report and after restarts. The daemon validates each ID against the assignment's work item before recording a receipt.

External brokers can implement that optional response field using explicit agent acknowledgements. A successful message HTTP response is delivery only: do not synthesize read receipts from it. Older brokers continue to run, but work with steering cannot be accepted until a completed attempt explicitly acknowledges that direction. Receipts do not grant permissions or demonstrate implementation. The daemon stores up to 200 messages and 24 KiB of encoded steering data per work item; broker receipts are bounded to 200 unique IDs per run.

## Worker controls and communication history

A run can advertise `control_capabilities: ["pause", "resume", "stop"]`. The built-in broker advertises all three; external brokers may advertise only those they implement. The daemon does not infer support from an endpoint's existence.

`POST /runs/{id}/pause` accepts an empty JSON object and the normal idempotency key. An executing run remains `running` with `pause_requested: true` until its turn is interrupted, its tools are confirmed stopped and cleanup succeeds; it then becomes `paused`. Interrupting reaches the CLI's conversation and nothing else, so the daemon closes its own tool channel as part of stopping a turn and waits for the handlers to actually return — cancelling a tool asks it to stop, which is not the same as knowing it has. A queued or already-idle run can pause immediately. Resuming uses the existing `/resume` endpoint, workspace, coding session and cumulative allowances. Pending peer messages survive the pause.

A worker whose tools could not be confirmed stopped is not reported as checkpointed. It stops for inspection instead, because a checkpoint is a claim about the workspace and not about the conversation.

`POST /runs/{id}/cancel` interrupts the running operation. A running session reports `stop_requested: true` until cleanup confirms cancellation. If cleanup cannot be confirmed, execution capacity stays reserved and the run requires reconciliation. A cancelled session cannot resume. The daemon persists owner control intent before contacting the broker, preventing automatic recovery from undoing an owner pause or stop.

The dashboard's conversation records application-level assignment text, instructions, reports, questions, owner direction, and delivery/control events. It does not expose private model reasoning, provider credentials or raw implementation-tool payloads. Retention is bounded to 500 entries per assignment and 10,000 overall, with 8 KiB message excerpts and explicit truncation indicators. Exchanges from before recording was installed cannot be reconstructed.

Beside it, the dashboard shows what the worker was observed doing — which tools it called, how each turn ended, when direction was delivered — because a worker can spend an hour inside one turn and a single status line cannot tell working from stuck. That record is bounded and sanitized, and it is not the evidence acceptance rests on. It also reports how full the coding session's conversation has become, separating a provider's own figure from a local estimate, and counts the compactions the CLI performed itself. Nothing here rewrites a worker's conversation.

## Assignments from before this change

An assignment saved under the previous contract has no coding session to resume. Its workspace, changed files, recorded commands, steering receipts and usage ledger are all still valid; only the model conversation is not portable, and it is kept for inspection rather than described as a session.

Such an assignment is never continued silently. The first time the daemon reaches it, it stops and says what carries over, so an owner who would rather accept what it already produced is not overtaken by a new attempt. An explicit resume performs the handover once: the preserved evidence — the files that actually differ from the baseline, the commands that actually ran and whether they passed — becomes the brief the new session begins with. Where that comparison cannot be made, the brief says so rather than reporting that nothing changed.
