# Linear tools for the assistant and PM — 2026-10-02

Nothing to pin: code-internal. This decision extended the read-only connection
and project intake designs without changing their fixed query operations.

As of this change, a project's PM received `lin` only when the project had a
valid Config → Linear link. Its connection and account were bound to that link;
other team roles received no tool. The assistant received it when Settings →
Connections contained a Linear connection with an account. Each assistant call
named one approved connection and account. Both ran the CLI on the daemon side,
without adding network access to a role's turn or giving the assistant a shell.

## Owner authority

Connections were read-only by default. The owner could enable **Allow changes**
(`allow_writes`) on each Linear connection. That allowed creating and editing
issues, comments, projects, initiatives and documents, and adding relations and
URL attachments. Answer-only PM turns stayed read-only. Every call rechecked the
current link, connection, account and write permission; removing them or turning
changes off during a turn refused its next call. Demo mode never invoked lin.

Always permitting writes would have confused account access with permission to
act. Asking before every edit would have added interruptions. A per-connection
opt-in kept the permission explicit and reversible without either cost.

Deleting, archiving, unarchiving and removing (including relations and
attachments) were always refused, as were `api`, `auth`, `config`, `file`, `mcp`,
`--file`, identity overrides, debug and pretty output. Unknown command paths and
flags failed closed. The model placed the command first, with its flags after it;
the daemon’s global flags came before the command to avoid subcommand collisions. Usage requests returned the filtered shipped command map,
rather than CLI usage that could recommend refused operations.

## Execution and records

lin ran through argv execution, never through a shell. Shell syntax, whitespace
and quotes remained literal arguments. The daemon placed its selected
workspace and disabled-colour flags before the subcommand. The environment contained only native CLI
location settings and `LIN_REQUIRE_IDENTITY=1`, never inherited credential
variables. Credentials remained in lin's native account store. Output was
labelled external information, never instructions; stdout was clipped at 32KiB,
stderr at 4KiB, and parsed errors at 300 characters. Recognisable lin credential
tokens were redacted from output and errors. Raw stderr and command bodies were
never logged. Missing authentication and nonzero exits returned failed results,
including bounded partial stdout, so the model could explain the failure.

Each write recorded an Activity attempt before invocation and an outcome after
it. Entries named the seat, command path and identifier-shaped arguments, never
bodies or flag values. Failure entries deliberately kept only the outcome category:
provider errors could echo issue bodies or argument values. The bounded, redacted
error was returned to the caller instead. A restart leaving only the attempt meant outcome unknown.
There was no automatic retry or deduplication: after restarting the model was
told to inspect Linear for its earlier changes before creating again. Between
two writes the first might have a complete receipt while the next had only an
attempt, or had not started. Linear state remained the source of truth.

A 30-second deadline cancelled the process group. A timed-out write returned
“outcome unknown — check Linear before trying again”. A completed failure recorded
failed; timeout and caller cancellation recorded outcome unknown. Cancellation
reported “lin was cancelled” rather than claiming that its deadline expired. Concurrent callers used separate
processes and explicit accounts; only Activity appends shared the core store.

## Shipped guidance

The owner's original SKILL.md and its commands and output references were
supplied in the owner’s commit `afb1b03` and embedded byte-for-byte from `internal/integrations/connections/lin_skill/`.
Guidance was filtered when served to omit refused commands and, with changes
off, write commands. Only the short skill entered instructions; the two longer
references were fetched on demand with the tool. No owner's home skill was read
at runtime. Resync meant copying the three upstream files over that directory,
reviewing the plain diff, and checking guidance tests and policy together.

Fake CLI tests covered capability gating, literal argv, explicit accounts,
credential environment exclusion, clipping, partial failures and timeouts.
Live owner accounts were not used during implementation.
