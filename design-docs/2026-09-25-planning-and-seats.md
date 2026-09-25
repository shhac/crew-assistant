# Planning, seats and dependencies

2026-09-25. Built for v0.16.0.

## Why

Two problems with how work started:
- **Tasks were often wrong about the code.** Many were written by the assistant from a chat, and some described as missing what already existed. The implementer's first act was to write code, so nothing checked the task against the repository first.
- **Nothing tracked ordering between tasks.** A task could start before the one it built on had landed.

The owner asked for:
- a stage that works out what needs doing, gathers the context and raises questions, before implementing;
- the planner as a named role;
- team members who can hold several roles;
- the plan kept on the task, not carried in a conversation;
- dependencies that block a task until they land, as native git behaviour, with stacking through g2g later.

## What was built

- **Seats hold several roles.**
  - A seat on a project team is a `Role` with `kinds`: planner, implementer, reviewer, QA. A seat holds at most one of implementer, reviewer and QA, plus optionally planner. Verdicts and messages name the seat, so a seat that reviewed its own work, or both reviewed and ran QA, would be ambiguous.
  - A member holds kinds too. Choosing the same member for planner and implementer gives one seat, "Ada", holding both.
  - A seat does one thing at a time. That will be the lever for parallel work, since how much runs at once follows from how many seats there are and what each holds.
  - Older state with a single `kind` is read into the list.
- **The planner** is an ordinary role, not a special slot, so more than one member could plan later. The code template has a Planner seat; `planner_member: "none"` leaves it out.
- **The planning stage.** A task whose team has a planner starts in `planning`.
  - The planner reads the task's branch without changing it, starting afresh each time. It returns a plan: summary, what exists, what will change, what is out of scope, questions, and which of the project's unfinished tasks it depends on.
  - The plan is kept on the task (`task.plan`), and the implementer's and reviewers' prompts include it.
  - A plan that can't be parsed is kept as written rather than stopping the task.
- **Outcomes, in order:**
  - **Unlanded dependencies:** the task goes back to the queue with no plan and no branch point, and plans again once they land, so the plan sees the landed work.
  - **Questions:** these come to the owner as a question decision before any code, and the answer starts round one. A round no longer counts up before anything is written.
  - **Otherwise:** the implementer starts.
- **Dependencies.** `task.depends_on` comes from the planner or the assistant's `queue_task`, and must name tasks in the same project that don't form a loop.
  - The queue never starts a task while a dependency is unfinished; a stopped dependency no longer holds it back.
  - `waits_for` is derived for the board.
  - Without stacking, a branch-landing task builds only on the project's most recent landing, so two dependencies on separate branches are not both included. Stacking with g2g is the follow-up.

## Not built

- PM, a flat role owning order, priorities and dependencies across tasks, is next (v0.17.0).
- Parallel work: seats, member availability and more than one seat of a kind are directions for the parallelism tasks.
