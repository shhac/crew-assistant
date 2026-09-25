# The assistant's conversation on a model session

2026-09-25. Built for v0.19.0, on lib-agent-harness v0.4.0.

## Why

Each chat turn was a one-off, locked-down model run. The whole prompt went in as one message: the standing instructions, the overview of the owner's projects, the conversation's summary and its latest exchanges. So:
- **Almost no cache reuse.** Only the harness's fixed system prompt could be served from the provider's cache; the rest was paid for on every turn.
- **Nothing native to compact.** The CLI held no history of its own.

The owner asked for:
- the conversation held by the CLI, for both Claude and Codex;
- a restart that resumes it, or starts afresh when it can't;
- sessions that can be managed and picked back up, both as an escape hatch and as a fresh start;
- the assistant told only what changed at its level, with details from tools;
- each concern on the right side of the boundary with lib-agent-harness.

## Ownership

**lib-agent-harness (v0.4.0) owns the mechanism any caller on a long-lived session needs:**
- **`session.Open`:**
  - reclaims a crashed launch;
  - resumes a saved conversation when the CLI still has it and it was opened the same way;
  - otherwise starts afresh and says why.
  Claude's transcript is checked before `--resume`; a Codex refusal counts as the conversation being gone.
- **A resume reference that survives a tool-surface change,** since the restriction is proven again on every launch.
- **The caller-context handler (`Options.Context`).** The harness sees compaction on both engines' streams, and records it in a marker that survives a restart. At the next turn it asks the caller for its current context, once, instead of anything stale being replayed. It asks the same when a conversation is new.
- **Protocol knowledge:** where Claude keeps transcripts, that Claude's compaction trigger is nested, and what Codex returns for a missing thread.

**crew-assistant owns its domain:**
- what the assistant is told (the overview, the "since your last message" note, which activity is owner-level);
- the conversation's record, summary and archive;
- the tools and their authorization;
- when to compact or start afresh;
- the dashboard.

The implementer's session now resumes through the same `session.Open`.

## What was built

- **One restricted session per conversation, open while the daemon runs.**
  - The assistant's tools are its whole tool surface, reached through `crew-assistant tool-bridge`, which the CLI starts.
  - Its folders are its own, under `state/chat`, apart from the teams' sessions.
  - It closes after 30 idle minutes, on `/new` or `/clear`, when the conversation, model or the assistant's persona changes, and on shutdown.
- **What a turn carries.**
  - A new or just-compacted conversation gets the overview through the context handler. A new one also gets the summary and the latest exchanges.
  - Every other turn is the owner's message, after "Since your last message:" and up to 20 owner-level changes from the activity log. Wake-ups say what happened themselves, so they get no note.
- **The session record.**
  - Each conversation keeps its session's reference, how it was opened, compactions, context used and window, and the last turn's cached share.
  - It is archived with the conversation. Picking a conversation up from History resumes its session, or rebuilds it from the record.
- **Tools behave as before.**
  - The same checks and dashboard events apply.
  - An error's own words never reach the model.
  - Each action counts against the day's allowance, and a reply has a cap on its actions.
  - A session turn is never retried automatically.
- **`/compact`.** It makes the summary as before. Then a Codex session compacts its own history; a Claude session is set aside, and the next turn starts a new one from the summary. The harness can't ask Claude to compact in a restricted session, where slash commands are off. Claude also compacts on its own as it fills.
- **The dashboard.** A line under the chat's header shows the session, with Compact and Start fresh beside it.
- **The fallback.** The OpenAI-compatible engine, and a CLI or platform that can't run a restricted session, keep the one-at-a-time turns.

## Not built

- Replacing the CLI's base instructions in a restricted session, so the assistant doesn't also carry the coding agent's prompt. Needs a security review in the harness.
- A private Claude home for chat transcripts, which would keep them out of the owner's `~/.claude` resume list.
- Codex `thread/inject_items`, for context as a developer note rather than text ahead of the message.
- Showing cache savings over time rather than just the last turn.
