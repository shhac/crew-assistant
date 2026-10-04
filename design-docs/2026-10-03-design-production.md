# Finished design assets

Captured on 2026-10-03. Nothing to pin: code-internal.

As of this change, an implementer at the writing step could request finished
assets with a `production` fenced block instead of a `design` question. Each
line named an asset and described it (`name: what it is`); notes followed a
blank line. The request accepted 1–100 unique names and counted as one design
hand-off against the existing per-step, per-round limit. Researchers and PMs
did not gain production requests.

The designer's production prompt explicitly expected image generation and
`attach_file` with `generated` when available. Attaching wrote files into the
daemon's attachment store, never the workspace or repository. “Don't deliver”
meant don't commit, land or approve. The design-input prompt also allowed
generation and attachments, to remove the earlier ambiguity.

The designer attached a file for each named asset and returned its exact
prompt, generator/model, settings and references (including reference hashes).
The daemon accepted a complete group of at most ten assets in one change,
computed SHA-256 from each kept file, and recorded its attachment and producing
turn. A malformed group was refused whole and the designer was asked again.
Partial hand-overs stayed with the designer and showed delivered and remaining
assets. The implementer's original session resumed only after completion.

Rejected variants were attached separately. The daemon built one zip per
request, rebuilding it under a new immutable attachment id on each addition,
then deleting the old file after the store update succeeded. These archives
could not be uploaded with owner notes. The final group also recorded a
daemon-generated `provenance.json` and, if nothing was rejected, an empty zip.
Later roles received each asset's readable path, turn, hash and provenance,
plus the archive and provenance attachment paths. The implementer copied the
files into its workspace as needed.

Ordinary attachments retained their limits: 5 MiB per file, ten per set,
30 files and 50 MiB per task. Production kept the 5 MiB per-file limit and ten
assets per turn, with separate task limits of 100 asset files and 200 MiB.
Each request's archive was bounded to 50 MiB (both stored and uncompressed).
Per-asset provenance was bounded to 32 KiB of JSON to keep the final record
within the per-file limit. Provenance and archive files did not consume
ordinary attachment limits.

Starting a new turn removed asset records and files from unfinished earlier
turns, while retaining completed groups and rejected history. A stale turn or
stopped task could not attach or record a group. A crash after a file write but
before its store update could still leave an unreferenced file, as before;
this change added no orphan sweep. Partial completion survived restart; final
completion returned to writing and never reran the designer.

A completed turn delivering no new assets, or more than
`ceil(wanted assets / 10) + 2` completed turns, opened an owner decision listing
what had been delivered and what remained. Crashed turns followed the existing
role-failure backoff. The designer's sandbox, authority, engine capabilities,
current-design rules and delivery gate were unchanged. No dashboard UI change
was needed: the existing attachment list offered the archive as a download,
with the existing nosniff and sandbox CSP headers.

## Review corrections captured on 2026-10-04

Nothing to pin: code-internal; the tool bridge used lib-agent-harness v0.23.3.
Inline attachment content was limited to 64 KiB, advertised in the tool schema,
description and designer instructions. Larger files used `path` or, where
available, `generated`. Before forwarding to the harness's one-MiB frame
scanner, the bridge replaced oversized inline content with a refusal marker.
The attachment handler returned the size guidance; the connection stayed open
for later calls, including after a 2,076,340-byte rejected call.

The turn bound applied only while assets remained. A completed hand-over wrote
its provenance and archive even beyond that bound, or with an explicit owner
question. Partial escalations also wrote a provenance record listing delivered
and remaining assets. An escalation with no completed assets in that group
discarded its uncompleted attachments and reached the owner without JSON retries.
Rejected archives were rebuilt outside the store lock; the atomic swap checked
the previous archive id, request and turn to refuse competing or stale writes.

Production expectations were conditional on the designer's available tools,
and the production reply prompt included the full escalation object. The
existing two-attempt writer correction remained separate from JSON retries:
writing replies used fenced blocks, not JSON. After resetting an invalid
request's workspace, the retry recreated its main integration and refreshed
both the prompt and draft metadata.

Further review on 2026-10-04 made the abandonment contract explicit: empty
delivered and provenance arrays discarded an unfinished group's attachments
only with a non-null escalation containing a recommendation. Empty arrays with
`escalate: null` still required provenance for every attached asset, and the
designer was asked to correct that reply. The dashboard's combined attachment
list reported its total without an ordinary-limit ceiling, since production
files and records used separate limits. Main's increased ordinary limit of
120 files remained unchanged.

The hosted check's npm launcher crashed through `/usr/bin/env node`, although
the same npm CLI worked when invoked directly through Node. The Makefile used
that direct launch form for checking and building, with configurable Node and
npm commands resolved from PATH. A fake-CLI regression kept every Go and
frontend check and refused direct use of the broken npm launcher.

Further review on 2026-10-04 made production reply corrections start a new
production turn before opening a fresh designer session. The daemon discarded
the unfinished group's assets and rebuilt the tools and attachment instructions;
the designer regenerated remaining assets with new provenance. Completed groups
and rejected variants remained available, and tools from the discarded turn
were refused. Ordinary attachment-limit errors reported only ordinary files,
explicitly distinguishing their limit from production files and records.
