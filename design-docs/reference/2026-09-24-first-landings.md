# First changes landed on main

Captured on 2026-09-24 against crew-assistant at `ff4465d`/`ed2e6d1`. The
assistant ran on Claude Opus 5.5 at high effort. Roles used Claude Code
2.1.280 and Codex 0.156.1.

## What was landed

Two changes this project's team had built itself were delivered as local
branches on 2026-09-23:
- `paul/next-message-suggestions-in-the-chat` at `176ecd3`;
- `paul/composer-asset-drop-and-paste` at `2aa0362`, which was stacked on the
  first.

After that, `main` moved on by the landing and wake-up work, to `ff4465d`.
The owner then set this project's landing to "fast-forward main", in their
words "land means fully ff-merged to main". They also set
`receive.denyCurrentBranch=updateInstead` on the repository, because `main`
is checked out there.

The owner asked the assistant, in chat, to set that policy and land the two
changes one after the other, using a wake-up rather than checking back.

## What happened

1. **The first attempt failed** before any tool ran. The table of tool labels
   still named the tools from before the rebuild, so every turn that used a
   current tool failed ("invalid chat tool event"). Fixed in `ff4465d`.
2. **The second attempt hit the context budget, after acting.** The assistant
   had set the landing policy and started landing the first change when the
   turn stopped with "context cannot be compacted safely". The whole state was
   about 70 KB of task history, and a `read_state` call added the same again.
   The loop landed the change anyway, because landing does not depend on the
   chat turn. The assistant's view of state was made compact in `ed2e6d1`.
3. **The first change landed.**
   - Landing found `main` ahead of it, so the daemon merged `main` in without
     a working tree. The merge was clean, and became draft 5 (`ce5899e`).
   - The reviewer's pass carried over and QA ran `make check` on the merged
     result.
   - The owner's approval of draft 4 still stood, so `main` was
     fast-forwarded to `ce5899e`, with the checkout clean and updated in
     place.
4. **The second change landed after a wake-up.** In a new turn, the assistant
   landed the composer change and registered two wake-ups: one for "task
   landed" and a 45-minute fallback.
   - The task caught up cleanly (draft 5, `482bf96`), and QA passed with 147
     frontend tests.
   - It landed at 09:18:23Z. The wake-up fired in the same state change and
     was delivered at 09:18:23Z, 0s after it was seen, with its handle and
     times.
   - Following its own continuation, the assistant cancelled the fallback,
     checked `main` and reported both commits.

## Evidence that nothing was lost

Each of these is an ancestor of `main`:
- `main` before the run (`e198494`, `ff4465d`);
- both delivered branch tips (`176ecd3`, `2aa0362`);
- the owner's commit made while landing was under way (`ed2e6d1`).

`ce5899e` is an ancestor of `482bf96`, so the changes landed in the intended
order. Nothing was forced. `make check` passed on the landed `main`.

## Gaps seen

- One chat turn did real work and then failed, and the owner saw only the
  error. The landing still finished, but the turn gave no account of what it
  had done before stopping.
- QA's merged-result run took about a minute. For this project, that is the
  whole cost of landing a caught-up change.
