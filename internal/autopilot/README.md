# Autopilot core contracts

CA-98 owns saved modes, checked execution and durable history. CA-103 consumes
these interfaces for event delivery, recovery and summaries; CA-104 presents
them. Actual function policies remain in CA-99/100/101. There is no external watch loop, frontend or host-command tool here.

The compiled catalog has eleven stable IDs. Routine and health functions default
to `suggest`; `landing-release-operator` defaults to `off`. Empty/unset modes use
those defaults. All production functions remain unavailable until trusted Go
code registers their implementation. A saved `act` does not register anything.

## Configuration and permission

`config.Config.Autopilot` persists explicit modes, future function settings and
per-row revisions. `config.SetAutopilotMode` takes an expected row revision and
holds the document lock across load, validation and save. Different rows merge;
the same stale row conflicts. Whole-config saves cannot replace changed saved
autopilot settings. The daemon serializes mode saves/application with execution.
An unresolved persistence/application error holds autonomous admission until a
successful reload or save reconciles it. Invalid modes, unknown IDs and stale
row revisions reject before writing and retain the previous admission state.
Narrow mode edits apply only autopilot settings, retaining effective runtime
HTTP/Tailscale overrides during subsequent file-watcher reloads too. Actual
disk connection edits still require a restart. Slack project validation reads
state before acquiring the app lock; unchanged narrow mode edits do not read
state under that lock. Restart loads saved settings before admission.

`Project.OperatorPermission` defaults false and records the owner's revision,
identity and timestamp. Only the authenticated owner route/CLI changes it; no
assistant/PM tool exposes that operation. Every change increments the revision,
so revoke/re-enable invalidates old proposals. It changes neither modes nor
landing/release policy. The operator additionally checks the gate, its revision
and landing pause. Each registered action must check its own release policy,
target and commit through the existing checked action path.

## Registration and execution

The daemon's `App.Autopilot` coordinator starts with no function registrations.
`Register` binds a compiled function ID, policy version, allowed action kinds
and a trusted policy checker, and returns an `AutopilotFunction` capability.
Unknown functions, duplicate registrations and unknown actions are refused.
No HTTP request or assistant argument can register or select a rule/actor.

`AutopilotFunction.Submit(ctx, source, reason, concreteAction)` is the CA-103
delivery seam. Source identities must be stable and globally unique (include
function ID in an event's identity). Reusing a source with different function,
policy, reason or canonical arguments conflicts. Off creates nothing. Suggest
records the exact proposal; Act admits and executes through the registered
checked adapter. Replaying an existing suggestion does not execute it when its
mode later becomes Act. Completed, refused and cancelled records never repeat.

Admission checks current mode, availability, policy, project/task scope, pauses,
stopping, no-dispatch, upgrade admission and operator permission. The adapter
checks target versions and any function-specific authority. Pure checks must
not perform I/O or mutate state. Local `Apply`/`Undo` callbacks may change only
the supplied snapshot and must never publish, invoke host commands or perform
post-commit effects. They commit through the same SQLite transaction as receipts
and append-only audit entries. Failed local application discards partial state.

Built-in local adapters are `rename-project` and ordinary `resolve-choice`.
Rename uses title revisions, preserving the previous title and rename flag for
undo; both ordinary owner renames and source refreshes increment that revision.
Decision choices fence a digest of the decision, brief, playbook and task authority,
excluding scheduler counters, claims and derived presentation fields, use the
existing checked resolver, and retain decision evaluations atomically. Special
release/upgrade/prerequisite/owner-step decisions require dedicated policies and
are refused by the ordinary choice adapter. There is no universal inverse.

Local adapters receive a trusted `AutopilotActorKind`, separately from the
configurable profile identity recorded in `Executor`. Audit entries retain
`ActorKind`; a profile ID equal to `owner` confers no owner authority.
`OnPerformed` observes new local commits after coordinator and transaction locks
are released. Rollbacks and replay never notify. The app nudges work after a
committed ordinary decision resolution.

External adapters explicitly register separate `Check`, `Execute`, and
read-only `Reconcile` callbacks. Only the operator can bind them. No actual
external adapter is registered in production. Intent commits as `uncertain`
before dispatch; checks run again before the callback. The committing caller
alone invokes Execute. Replay never repeats it, even if completion persistence
fails or its response is lost. Reconciliation records bounded evidence without
repeating an effect, even after mode/gate revocation. Errors retain uncertainty;
arbitrary host error text is excluded. CA-101 owns actual execution/reconciliation
evidence and checked host/release authority.

`Reconcile` records owner inspection; `ReconcileAutomatic` records assistant
recovery attribution. Neither grants execution or approval authority.

## Owner API

All routes use the dashboard's existing owner authentication and strict bounded
JSON decoding. No body accepts an actor. CA-78 still owns transport hardening.

| Route | Contract |
| --- | --- |
| `GET /api/autopilot` | Catalog, runtime availability, saved settings/revisions |
| `PUT /api/autopilot/modes/{function}` | `{mode, revision}`; empty mode resets |
| `PUT /api/projects/{id}/operator-permission` | `{allowed, revision}` |
| `GET /api/autopilot/actions/{id}` | Durable current result |
| `GET /api/autopilot/pending?after=&limit=` | Pending proposals, ascending ID |
| `POST /api/autopilot/actions/{id}/{verb}` | `{revision}`; approve/cancel/undo/reconcile |
| `POST /api/autopilot/actions/{id}/override` | `{revision, replacement: ConcreteAction}` |
| `GET /api/autopilot/history` | Bounded immutable audit transitions |

Approval names the exact proposal revision; arguments cannot be substituted.
Override executes through ordinary owner action authority, cancels the original
only on success, and records a separate owner-attributed replacement. Failed
replacement attempts retain the pending suggestion and record their failure.
Only explicitly owner-allowed local adapters support replacement; external
operations do not. Cancellation does not answer the underlying decision.
Undo requires a supported inverse and unchanged target revision. Duplicate
approval/undo reads the recorded outcome without another effect or audit entry.
Inspect the returned status rather than interpreting HTTP success as performance.

History accepts `project_id`, `task_id` (requires its project), `limit` (default
50, maximum 200), `cursor`, `forward`, and `after` (forward initial reads only).
Each entry has an immutable sequence, time, actor and complete action copy.
Newest-first pages retain a fixed high-water boundary; later transitions never
shift earlier pages. Forward reads support bounded summary catch-up. Continue
with `next`, then use `boundary` as the next initial forward `after`. Invalid
bounds, scope/cursor mismatches and database errors fail the whole read.
Malformed URL escapes and query separators are rejected rather than discarded.
Invalid pagination returns 400; storage/decode failures return a generic 500
without exposing database diagnostics.
History stays outside `/api/state`. IDs, rule/version, concrete arguments,
target/digest/commit, why, assistant identity at proposal time, approval/executor,
outcome and supported inverse survive restart and assistant renaming.

Outcomes are `proposed`, `performed`, `failed`, `cancelled`, `refused`, `undone`
and `uncertain`.

## Durable events and summaries (CA-103)

State transactions capture `decision.opened:<id>`, `decision.resolved:<id>`,
`task.landed:<task>:<revision>` and `release.completed:<project>:<version>` in
`autopilot_events`. A failed capture rolls back the state mutation. After commit,
a buffered nudge wakes the daemon consumer; startup and its minute ticker recover
lost nudges. There is no external watch loop. The existing PR-check watcher feeds
`Service.RecordCIEvent`; this is ingestion, not additional monitoring. CI identity
hashes the provider, repository, ref, full commit, check and state, with bounded
required fields. Independent checks cannot mask one another. Heartbeats use UTC
30-minute buckets; recording the current bucket expires older pending heartbeats.
Event-only ingestion, delivery receipts, completion marks, digest boundaries and
presentation cursors use small SQLite transactions without rewriting project state.
Transactions that also record Activity retain the atomic state-update path.
Duplicate heartbeat ticks make no changes. Heartbeats and their delivery rows older
than seven days are pruned; the newest heartbeat remains a watermark against replay.
Other terminal events retain compact identity tombstones for durable deduplication;
their delivery rows are pruned after seven days.
CI capture failures are recorded in Activity and never prevent existing wakes firing.

Trusted registered functions subscribe with `OnEvents(kinds, handler)`. Handlers
return bounded proposals with unique stable keys, reasons and concrete actions.
The consumer derives the source from the event ID, function ID and key and calls
`SubmitEvent`. Current modes, policy and target authority are checked there. Each consumer pass
caches a snapshot by the durable state revision, checking that revision before
each handler and refreshing after committed effects or owner edits. Unchanged
state is decoded once; submission revalidates authority inside its transaction.
Temporary admission closures defer without writing a refusal. Ordinary `Submit`
keeps its original semantics. Off consumes a wake without an effect; later mode
changes do not replay it. No production function is registered by this task.

Delivery marking follows submission. If marking fails or the process stops,
recovery resubmits the same source and reads the durable receipt without another
effect or audit transition. Changed content conflicts rather than overwriting a
receipt. A conflicting proposal key is recorded in delivery detail; other keys
are still submitted. Handler failures back off exponentially in minutes and terminate after
five attempts, with Activity explaining failure or conflict. Storage failures
leave delivery pending. Each pass takes at most 200 events in chronological keyset pages. The consumer
rotates past paused and backoff rows and wraps after the last page, so held
events cannot starve newer projects. Restart begins scanning from the oldest
page again. It
expires inputs over seven days old with an Activity record. Graceful stop takes
no new event; force cancellation interrupts in-flight work. Pending suggestions
remain in the existing action table across deferral, replay and restart.

`Summary(SummaryQuery)` reduces immutable audit transitions at a fixed boundary,
groups proposed, performed, failed (including uncertain), refused, undone and
cancelled outcomes, and includes pending proposals and open owner decisions.
It decodes at most 1000 audit copies, with a bounded outstanding-proposal scan;
current summaries query indexed proposed actions rather than regrouping the full
audit history. Historical boundaries still reduce immutable audit copies;
truncation is explicit. An audit-truncated response advances only to the last
scanned sequence. Histories remain `autopilot_actions` and `autopilot_audit`;
events, cursors and digest boundaries are delivery state, not a second history.
Text presentations show counts, three short examples per outcome and five owner
decisions, retaining full details in the query and history. Unconfirmed effects
keep their uncertain label. Operator landing pauses defer event proposals without
changing ordinary submission or owner-action refusal semantics.

`GET /api/autopilot/summary` defaults to the shared `owner_seen` cursor and accepts
`after` and `project_id`. Reading, including the read-only `autopilot_summary`
assistant tool and `autopilot summary` CLI command, does not acknowledge anything.
`POST /api/autopilot/summary/ack {"boundary": n}` monotonically advances the shared
cursor after successful presentation. Future boundaries are rejected. Concurrent
presenters cannot regress the cursor or acknowledge newer entries accidentally.
Only unfiltered summaries may advance this global cursor. Filtered responses
have `acknowledgeable: false`; presenters must not acknowledge them. The ack
route rejects a nonempty `project_id`.
Dispatcher, summary-read, acknowledgement and notification errors appear in one
Autopilot row in the owner snapshot. Each clears after its operation succeeds;
unrelated successes do not hide remaining errors. With no errors, the row is
omitted rather than advertising registered or ready functions.
Summary read failures report status but do not block the owner's chat or advance
progress. Owner-origin assistant turns include unseen summaries and acknowledge only after
a completed, uncancelled turn and successful session save. Wake turns do not.

The existing Slack notification channel batches actions using a separate durable
`notified` cursor. A single `autopilot_notifications` row for action delivery
reserves retries across restarts, including when the covering range grows. Retry
delays are 2, 4, 8, 16, 32 and then 60 minutes, capped at an hour. Attempts do not
create interrupted-operation claims; one Activity failure is recorded per outage.
Successful presentation resets the backoff and commits its receipt with the
notified cursor atomically. Failed sends and failed receipt commits cannot
advance progress. Recovery reads only the durable notification reservation and
its atomically committed cursor; no Events-map claims are created or read.
If Slack accepts a message but its durable receipt cannot commit, recovery can
send a duplicate covering message after backoff. Without Slack, the dashboard
and assistant share the summary API.
No model writes these summaries.

Daily digests are disabled unless the owner enables `daily_digest`. The default
time is 08:00 in the owner's local timezone. `PUT /api/autopilot/digest` takes
`enabled`, `at` (HH:MM) and the expected `revision`; the CLI offers
`autopilot digest on|off|at HH:MM` and the narrow JSON config key
`autopilot.daily_digest`. Whole-config saves cannot overwrite a changed digest.
There is no assistant setter. `RecordDigest` accepts an injected clock value and
location, creates only today's digest at or after its configured time, and uses a
unique local-calendar-date key across restarts, concurrency, DST and backwards
clock changes. Repeated ticks read the existing date without taking the writer
lock; concurrent first inserts still deduplicate transactionally. Its boundary starts at the previous digest's high-water mark.
`GET /api/autopilot/digests?before=YYYY-MM-DD&limit=50` lists dated records with
summaries computed from audit copies. Slack delivery uses one durable `digest:<date>` reservation and the same bounded
backoff; a failed or interrupted send never regenerates the digest. Only today's record is sent;
configuring Slack later never sends a backlog. Missed days are not backfilled.

## CLI

`crew-assistant autopilot catalog|get <function>|set <function> <off|suggest|act>|unset <function>`
supports the same saved modes. Offline changes hold the state/document locks;
`config get|set|unset autopilot.modes.<function>` uses the same narrow contract.
when a daemon owns the state lock, mode edits use its narrow owner route.
Offline catalog availability is always false because no daemon registration is
known. `permission <project> <true|false> <revision>`, `action <id>`, `pending`,
`history`, and `approve|cancel|undo|reconcile <id> <revision>` require the daemon.
`override <id> <revision> <replacement-json>` sends an exact local replacement.
History offers `--project`, `--task`, `--cursor`, `--limit`, `--forward`, `--after`.
