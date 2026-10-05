# Stacking pull requests with g2g — 2026-10-05

Pins g2g 0.41.0 (`--json` schemaVersion 3). Follows
[Babysitting pull requests](2026-10-05-pull-request-babysitting.md) and
[Planning and seats](2026-09-25-planning-and-seats.md), whose
`depends_on` held a task back until what it waited for had landed.

## Why

A repository the team worked in went through code freezes: its default branch
was locked, its default became a separate staging branch, and pull requests
targeted that. Work that built on a pull request still under review could only
wait, since a task never started on work that had not landed. The owner's own
tool, g2g, records stacked branches and keeps a comment on each pull request
of a stack listing the rest. The owner asked for opt-in support, with at least
the implementer and the PM able to use it.

## What changed

- `land.stack`, valid only with pull requests, turned stacking on for a
  project. The dashboard's Landing settings gained "Stack pull requests", and
  the assistant's `set_landing` gained `stack` (yes, no, or empty to keep it).
  Turning it on was refused unless g2g was found where Settings → Tools finds
  it (Homebrew's copy first, then PATH) at 0.41.0 or later, saying to install
  or update it from Settings → Tools. One already on stayed on whatever
  happened to g2g later.
- A task could be stacked on one other task of the same project
  (`stacks_on`, kept on the task, with who set it as for other links), only
  before it had begun from a base of its own, never in a loop with its
  stacking or what it waited for, and only on a task landing by pull request.
  Stacking on a task it waited for replaced the wait. The owner and assistant
  set it as any link (dashboard Relations, `link_tasks`); the PM through
  `link_tasks` and `queue_task`'s `stacks_on`, offered only where stacking was
  on. The PM's prompt and tool guide told it to stack rather than wait when
  landing was paused for a freeze, or a task built on a pull request open but
  not merged. Other roles could not stack.
- A stacked task waited only until its parent had a pull request open with a
  head pushed. It then started from that branch as fetched from GitHub (its
  base that commit, its starting branch the parent's), and its pull request
  opened onto the parent's branch, recorded as the pull request's base.
- While its parent was open, the task followed the parent's branch with the
  existing catch-up: a clean catch-up was skipped as for any pull request, but
  a parent branch rewritten under it was always replayed onto. It was held
  from merging, with the reason on the task ("it is stacked on …, whose pull
  request has not merged"). Once the parent merged, the task was replayed onto
  the target from the parent head it had last built on, so a squash merge
  never brought the parent's commits back twice, and then its pull request
  was pointed at the target with `gh pr edit --base`, unless GitHub had
  already done so when the parent's branch was deleted. A task whose base was
  on a branch other than its target was replayed onto the target however
  cleanly it merged.
- A parent stopped, or whose pull request closed without merging, brought the
  owner a decision for the stacked task: "Rebase it onto the target", which
  unstacked it and replayed only its own change there, or Stop.
- Tasks stacked on a parent were woken from their wait when the parent
  pushed, merged, closed or was stopped.
- g2g only mirrored crew's record. After the daemon pushed to or opened a pull
  request that was part of a stack, it pointed local branches in the project's
  clone at the heads it had pushed, ran `g2g track --branch <b> --parent <p>
  --apply --json` for each branch bottom first (and `track --as-trunk` for a
  target that was not the clone's default branch), then `g2g github comment
  --apply --json`, with `GH_REPO` naming the repository and only the owner's
  own home, PATH and gh settings in the environment. A failure was a
  diagnostic and a note on the task, and never held delivery. Crew's own
  pushes, leases and merges stayed the source of truth.
- The implementer and the PM of a stacking project could read its stacks
  through a `g2g` role tool the daemon ran in the project's clone: only
  `status --json`, `doctor --json` or `github status --json`, output clipped
  to 32 KB, doctor's "found something" reported as a finding rather than a
  failure.

## Decisions not taken

- Using g2g's `push`, `submit`, `restack` or `land`: crew already pushed
  under a lease, replayed and merged, and two writers of the same branches
  would disagree.
- More than one parent, or stacking across projects: neither had a real use,
  and one parent kept the pull request's base unambiguous.
- Restacking a task already begun from its own base onto another's pull
  request: it had to be stacked before it began.
- Retargeting any pull request the loop had not stacked: one whose base the
  owner changed on GitHub was left as they set it.
