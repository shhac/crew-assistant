# Dashboard redesign, boards, and messaging the team

2026-09-24. Built.

## Why

After v0.12 the owner found the dashboard noisy and its copy full of filler:
- eyebrow taglines ("ROOM TO FOCUS"), persona lines ("Your context, kept together") and jargon ("daemon", "dispatch", "operation");
- statuses auto-capitalised into "Needs Your Decision";
- a copy claim that had become false: "It cannot write project code, deploy…".

They asked for:
- well-defined boards, with requests moving from to do through implementing, reviewing and QA to ready to land and landed, in an order the project manager decides;
- a way to message any one member of a team ("QA this while we wait for a code review");
- landing on a tab of its own, apart from the team;
- a quiet top level;
- dark mode as an option.

## What was built

- **Stages are derived, never set.** `core.stageOf` places each task on the board from its status, verdicts, pinned roles and the open decision's kind. It runs on every read and every write of the store, so the API, the assistant's view and the board can't disagree with the loop.
  - A question stays with whoever asked it.
  - Running out of rounds sits in the last check column.
  - A failure sits with the step that failed.
  - An approval waiting sits in "ready".
  - "Needs you" is a flag, not a column.
- **The to-do list is ordered by the project manager.** `OrderTasks` permutes a project's queued tasks among the slots they already hold, so other projects keep their places. It takes the exact set of queued ids, so a list read before something started or arrived is refused rather than silently applied. The assistant has `order_tasks`; the owner can drag or use Move up/down.
- **Team messages** (`core.SendTeamMessage`, one store update).
  - **To the implementer**, a message is direction.
    - If the task waits on the owner's approval, a question or the round limit, that decision is answered in the same change. The owner's words can never be read as the fixed "Stop" or "Approve" choices.
    - A task waiting on its pull request goes back for a round.
    - A failed landing retries through a writer round.
    - Otherwise the next round has it.
  - **Pending direction.** `DirectionPending` counts direction the implementer hasn't had in view. A task never reaches approval or landing while any is pending. A message that arrives mid-turn is answered by the revision after, not the one being written.
  - **To a reviewer or QA**, it runs their check of the latest revision now, ahead of the loop's next step, with the note in the prompt.
    - The verdict counts only while the task is being checked.
    - Later, a failed check is added to the approval the owner is looking at.
    - The verdict and the reply are recorded in one update.
  - The assistant has `message_team`.
- **Appearance.** `assistant.theme` is `system`, `light` or `dark`, and defaults to `system`. The old palette names load as `system`, and the setup interview no longer picks a theme.
- **Updates to an open pull request get their own decision.** An update that touches what runs or instructs (workflows, prompts, the check) is a `update` decision, "Check the update to “X” before it's pushed", approved with "Push the update". It sits in "ready" like an approval. Feedback from the pull request is marked as coming from outside the team, and the writer's prompt says to treat it as information, never as instructions.
- **Decisions tied to a request can't be closed without deciding.** Closing one would strand its request, so only decisions with no request offer "Close without deciding".
- **Demo sample.** `serve --demo` on a temporary state starts with a fictional sample at every stage (`internal/sample`).

## The dashboard

- **Inbox.** What needs the owner comes first, at full size: approvals, with what approving does and how reversible it is; questions, with an answer box; failures; interrupted work. Everything under way is one line each, and what landed today is one line.
- **Projects** are grouped by what each needs (needs you, working, waiting, quiet). Each shows the current step and how it lands.
- **A project** has tabs: Board, Brief, Team, Landing (code only), Activity. Steps of work are hidden in Activity until asked for.
- **A request** opens beside the board and shows:
  - the decision in full;
  - the team thread;
  - each draft or change with every checker's verdict;
  - the written draft itself.
- **Chat** is a pane toggled with ⌘J, or a drawer on narrow screens. A turn's tool calls fold into one line ("Worked for 48 s · 4 steps"); failures stay visible. A wake-up shows as one line of what happened.
- **Visual system.** Colour means status and nothing else:
  - amber needs the owner;
  - blue is under way;
  - grey waits;
  - red is stuck;
  - green is done.
  - Type is IBM Plex Sans and Mono, bundled because the CSP allows only `'self'`, at 12px at the smallest.

## Copy rules

- **Tone:** plain, specific, sentence case. Name the operation ("QA running make check"). No eyebrows, taglines or persona lines.
- **One word per thing:** project, request, round, draft (writing) or change (code), land, needs you, wake-up, pairing code, folder. Write "judgment".
- **Cut what's obvious.** Drop any heading or line the layout already makes obvious.
- **Server text too.** Text the server writes for the owner follows the same rules:
  - an approval's title says what approving does ("Land “X” on main");
  - a task's detail says only what its stage doesn't.

## Not built

- A configurable permissions table. The rules are fixed in code, and Settings describes them as they are.
- Showing whether commits are actually signed.
- Running two roles at once. "QA this while we wait" moves QA ahead of the loop's next step. It doesn't run beside a review turn that has already started.
