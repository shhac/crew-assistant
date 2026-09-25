# The assistant's context, and the order started work carries on in

2026-09-25. Built for v0.18.0.

## Why

Two problems the owner hit on the live daemon:

- **The chat refused every message** with "The remaining context cannot fit safely", and `/compact` didn't clear it. Each chat turn is a one-off model run carrying the whole state of the owner's projects plus the recent dialogue. The state had grown to about 113 KB: every finished task kept its criteria, drafts and reviews. The daemon's fixed budget was 128 KB, less the tool list. `/compact` summarizes only the dialogue, and the state can't be compacted, so no amount of compacting could make a turn fit. The turns failed in the daemon's own check, before any model ran.
- **An approved change sat at Landing for over twenty minutes.** The loop works on one task at a time and resumed whichever active task came first in the list. Approving a landing, or answering a question, makes a task active again while another is running. An earlier task going round its rounds kept the landing waiting until it stopped.

## What was built

- **Started work carries on furthest along first.** When several tasks are active, the loop picks:
  - the one furthest along: landing, then deciding, reviewing, writing, designing, researching;
  - then the one with more drafts done;
  - then the one started longest ago, from a new `started_at` set when a task first leaves the to-do list, falling back to when it was asked for.

  Work waiting to retry is passed over while anything else can move. The owner's aim: once something leaves To do, keep its time on the wall as short as possible.
- **A smaller state each turn.**
  - Unfinished tasks keep what the assistant acts on: their latest two drafts and the latest reviews, clipped.
  - Finished tasks come as the last twelve to finish, each by what was asked, how it ended and where it went.
  - A new `read_task` tool brings one task in full: its plan, recent drafts, every review and its messages.
  - On the owner's state the view went from about 113 KB to 65 KB.
- **A budget per model.**
  - A Claude reply states its model's context window; lib-agent-harness v0.3.5 reports it in `completion.Usage.ContextWindow`. The daemon keeps the latest window per engine and model (`model_windows`) and sizes each request to it: the window, less room for the reply and framing, at three bytes a token.
  - Until a model has stated a window, the old 128 KB stands. Codex's one-off runs state none, so a Codex model stays on the default.
  - When a request is refused as too long, and the refusal states a window smaller than the request was sized for (say, a smaller model just chosen in Settings), the turn is sized to it and tried once more, compacting what it must.
- **The version on startup.** The first line `serve` prints now leads with `"version"`.

## Compaction, and why it isn't the harness's here

The owner asked whether compaction should belong to lib-agent-harness. It should, for a conversation the harness holds, but the assistant chat isn't one.

- **Stateless turns:** every chat turn is a stateless `completion.Complete` call. Claude runs with `--no-session-persistence` and Codex with `exec --ephemeral`, and the daemon sends everything each time.
- **No history to compact:** with no harness-held history, crew-assistant's summarize-and-replay is the only compaction available. The real saving is sending less, which is what this change does.
- **Where native compaction applies:** it belongs to long-lived `session`s. Codex's `thread/compact/start` is already used for implementers.
- **What moving the chat onto a session would take:**
  - `Session.Compact` for Claude too, by sending `/compact` to the resumed session and waiting for `compact_boundary`. First check that `--disable-slash-commands` doesn't block it.
  - The daemon re-sending the current state after a `compaction_completed` event, the "your context just changed" handler.

  The owner hasn't decided on that move, so it isn't built.

## Not built

- The chat on a harness session, with native compaction.
- A window for Codex models, which would need the harness to read it from a Codex session or its model list.
