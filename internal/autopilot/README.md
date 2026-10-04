# Autopilot core contracts

CA-98 owns saved modes, checked execution and durable history. CA-103 consumes
these interfaces for event delivery, recovery and summaries; CA-104 presents
them. Actual function policies remain in CA-99/100/101. There is no new event
loop, frontend or host-command tool here.

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
and `uncertain`. Delivery, notifications, return/daily summaries and presentation
acknowledgements belong to CA-103, not this coordinator.

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
