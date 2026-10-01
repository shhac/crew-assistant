# Memberless PM chat identity — 2026-10-01

Version: nothing to pin; code-internal. Built against CA-45 design 2, superseding design 1's unresolved memberless avatar behavior.

The PM chat response supplied a display-only SVG from the existing deterministic name-based preset generator. The header and every PM reply used that same fallback when no current member filled the seat. No member was created or persisted. A current member's normal avatar took precedence immediately, and the fallback remained decorative with empty alternative text.

Waiting messages used neutral “Waiting for {name}…” wording because the dashboard could not reliably tell whether that PM was occupied across projects. Working and failed messages retained their recorded PM identity.
