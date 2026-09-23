# A clean break for pre-rebuild state

Date: 2026-09-23. Version: `crew-assistant` v0.11.0 (`ac7be39`), before the
[project teams](../2026-09-23-project-teams.md) rebuild.

## Decision

The rebuild keeps no compatibility layer for state written by the old design.
The owner's existing state is converted to the new model once, at cutover, and
the new code reads only the new model. The tool had only ever run locally for
one owner, so no other installs needed a migration path.

This reverses the earlier convention of preserving compatibility migrations
(`parent_id`, legacy work items, and the historical-acceptance rules).

## What the conversion does

| Old record | At cutover |
|---|---|
| Project | Kept. Its description and acceptance criteria become brief version 1. |
| Work item | Becomes a task if the outcome still applies. "Next-message suggestions" becomes the phase 2 git task. "Composer asset drop and paste" stays queued behind it. "Assistant worker lifecycle controls and diagnostics" is dropped, because the rebuild removes what it described. |
| Decision | Kept, with references to agents and work items replaced by task references, or cleared where the target is gone. |
| Memory, chat messages, activity | Kept as written. |
| Agents, steering, steering receipts, agent conversation, pending operations, usage ledger | Dropped from live state. |

Before conversion, the pre-cutover `state.db` is copied into the state
directory's `backups/`. That copy is the historical record; nothing reads it.

The old worker runtime is removed once the new loop has replaced it:
- `managed-workers/`, `context-checkpoint-*` and `model-runs/` move to
  `backups/`;
- the Colima profile `agent-assistant` and its worker image are deleted, after
  telling the owner.

## Alternatives considered

- **Import from the old store on first start.** Rejected: the importer would be
  code kept alive to run exactly once, for one owner, and would pin the old
  schema in the new codebase.
- **Keep the old records in place beside the new ones.** Rejected: it would
  bring back the vocabulary the rebuild exists to remove.
