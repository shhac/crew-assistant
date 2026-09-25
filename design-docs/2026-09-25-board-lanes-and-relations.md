# Board lanes, a Team/Config split, and relations between tasks

2026-09-25. Built for v0.21.0.

## Why

The board had grown a column per role. Columns took their width from their cards, so an empty one shrank to a sliver and the rest shifted about. A task with the designer borrowed the column of whoever asked, so the board never showed that the designer had it. "Ready to land" held work that waited on the owner or on checks rather than on the team, yet it took a full column on every board.

The Team tab mixed two things: who fills each role, which is saved as soon as it is chosen, and how the team works, which sat behind an Edit form.

Tasks could wait for one another only in the way the researcher or the PM said, as they were queued or planned. Nobody could change a link afterwards, or say that two tasks belong together without one waiting. The owner wanted to see and manage these links, and so did the assistant and the team.

## What was built

### The board

- **A designing stage.** A task with the designer is at stage `designing`, and so is one waiting after the designer failed. A design question sent to the owner stays with the step that asked, because the answer goes back to that step.
- **Columns made of lanes.** The columns are To do; Research, with Researching above Designing; Implementing (Writing for a writing team); and Checks, with QA above Reviewing. Each lane has its own header and list. A lane shows when the team has the role, or when an open task started with it or is in its stage. A column shows when any of its lanes does. Every column takes an equal share of the width, with a common minimum. An empty lane keeps its width and shows only its header, dashed and muted.
- **Ready above the board.** Tasks at stage `ready` (landing, awaiting checks, or waiting for approval of a delivery or update) sit in a strip above the columns, one row each, only while there are any. Each row keeps what its card showed: the step, the needs-you mark, who is at work, the PM's landing note and waiting messages.

### Team and Config

- **The Team tab** shows each role with who fills it, as before, and each member on the team with every role they hold on it. It has no Edit button. A project with no team yet points to Config.
- **Config** opens with **Team settings**, with its own view and edit, like Landing and Workspace. It holds what the Team tab's Edit held: the kind of work (when the project has folders), engines for roles no member fills, rounds before asking, and the QA check or where approved drafts go. The team is still saved whole, so who fills each role and where a code team works are sent again as they were.

### Relations

- **Three relations.** A task depends on another (it doesn't start before the other finishes, and doesn't land before it lands), blocks another (the same link seen from the other end), or relates to another (a pointer for whoever works on either; nothing waits). A pair has at most one link, which keeps what the owner sees unambiguous. Both tasks are in the same project, a task can't be linked to itself, and a dependency can't make a loop.
- **The model.**
  - `DependsOn` stays stored on the waiting task, as before, so everything that already read it kept working.
  - `RelatesTo` is stored on both tasks.
  - `LinkedBy` marks who set each link and when, keyed `depends_on:<id>` or `relates_to:<id>`. A depends-on link is marked on the waiting task, and a relates-to link on both. A link with no mark was set before marks existed, by the researcher or the PM, and counts as the team's.
  - `Blocks` is derived whenever state is read, from a reverse index built once per snapshot. `WaitsFor`, the objectives of unfinished dependencies, is now derived for every unfinished task, not only queued ones.
- **Who may change what.** Links are set by the owner (from the dashboard), the assistant (`link_tasks` and `unlink_tasks`), or the team: the researcher from its plan and the PM from its list. A member is recorded as `member:<id>`, and a seat with no member as `role:<kind>`.
  - The owner and the assistant may link any two tasks at any status, except that a finished task can't be made to wait. They may remove any link.
  - The team may add links, but may make a task wait only while it is queued or researching, and can't change the links of a task that has finished. It may remove only links the team set.
  - A new or removed link makes the PM due to look again, unless the PM made it.
- **The PM's list.** The PM used to replace the whole list of what each task waits for. Now it replaces only the team's part; the owner's and the assistant's links stay, and its prompt marks them "(set by the owner)".
- **In the dashboard.** A request's panel has a Relations section: Depends on, Blocks and Relates to, each linking to the other task, saying who set it, with a Remove button. Beneath, a relation and any other task of the project not yet linked, finished or not, can be added. A refused link says why. A board card whose task others wait for says "Blocks N" beside the "Waits for" it already showed.
- **The API.** `POST /api/projects/{id}/tasks/{task}/links` with a relation and the other task, and `DELETE /api/projects/{id}/tasks/{task}/links/{other}`, which removes whatever links the pair. Both return the task. Changes are recorded in the project's activity as `task.linked` and `task.unlinked`.

## Not built

- Dragging a card between lanes, or onto another card to link them.
- More than one relation per pair, and relations across projects.
- A view of a project's links as a graph.
- Showing who set a link anywhere but the request's panel.
