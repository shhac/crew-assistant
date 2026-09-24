# Landing and wake-ups

Proposal date: 2026-09-24. Status: built the same day; see "As built" at the end.
Pinned to `crew-assistant` at `fadf0a2` and `lib-agent-harness` `v0.3.4`.

Extends [project teams](2026-09-23-project-teams.md). It changes what happens
after a code change is approved, and gives agents a way to wait on things that
take time.

## Why

The first two real code tasks were approved and "delivered" as local branches
(`paul/next-message-suggestions-in-the-chat` and
`paul/composer-asset-drop-and-paste`). For this project, the owner does not
count that as done. Here, done means the change is fast-forwarded onto `main`.
For another project, done could mean a squash-merged pull request. That merge
needs approval from other people, and the team has to work through several
rounds of review comments and CI failures to get there.

The owner asked for three things:
- **What "land" means is set per project.** Only the owner or the assistant can
  set it; teams inside the project cannot.
- **Nothing that is not ours is ever overwritten.** A branch the project does
  not own is never force-pushed without an explicit decision.
- **Agents can wait on slow things** such as CI, reviews or another task
  finishing, and pick up where they left off. They should not have to poll,
  and they should not be kept waiting forever.

## Landing

### What landing means is project data

Each code team's setup gains a landing policy:

| Field | Meaning |
|---|---|
| `means` | Prose: what landing means for this project, in the owner's words. It is shown to the team and on the project page. |
| `via` | `branch` (the default, and today's behaviour): approval creates a new local branch. `push`: approval pushes onto a target branch in the owner's repository. `pull-request`: approval opens a pull request on GitHub, and the change lands when the platform merges it. |
| `target` | The branch the change lands on (`push`), or the pull request's base (`pull-request`). It is never inferred. |
| `method` | `push`: `fast-forward`, the only method a push path offers. `pull-request`: `squash`, `merge` or `rebase`. |
| `github` | `pull-request` only: the `owner/name` repository. |
| `approve` | `before` (the default): the owner approves before landing, or before a pull request opens. `none`: a change that passes its checks lands without asking. |

For a code team, only the owner or the assistant sets these, through the
dashboard's team editor or the assistant's `set_team` tool. No role inside a
project can reach them.

### One flow, three ways

Our loop is already a local pull request, so the three ways share one flow:

| Step | `branch` / `push` | `pull-request` |
|---|---|---|
| Propose | revisions on the task branch in the clone | the task branch pushed to GitHub, and a pull request opened |
| Review | the team's reviewer | the team's reviewer, then reviews on the pull request |
| Checks | the team's QA | the team's QA, then CI |
| Approval | the owner (`approve: before`) | the owner before it opens, then the repository's own rules |
| Base moved | catch-up round | catch-up round |
| Land | a new branch, or a fast-forward push to `target` | the platform merges with `method` |

Outside feedback reaches the implementer in the same form as the team's own
verdicts. A failing CI check, or a review or comment on the pull request,
becomes "the checks said: …", with its source named. Text written by people
outside the team is marked as theirs. It is a request to consider, never an
instruction to run commands, fetch URLs or reveal anything. The implementer
never sees push, pull-request or CI mechanics. It only gets another round.

### Mechanics belong to the loop

Pushing, opening and merging pull requests, and watching for events, are
deterministic steps with credentials. The Go loop does them, as the rest of
the project's design already does. Agents own the judgement: the fixes, what
to change in response to a reviewer, and when to escalate. They never hold
push credentials or network access.

### Never losing work

- **Owned branches.** A branch the project created (a `branch` delivery, or a
  pull request's head) is recorded with the last commit it pushed there. It is
  only ever updated with `--force-with-lease` against that commit. If anyone
  else pushed in between, the lease fails. Their commits are then merged into
  the task, never overwritten.
- **Branches the project does not own**, such as `target`, only move forward:
  a plain fast-forward push, or a merge done by the platform. If one has
  moved, the task catches up (below) and tries again. No code path
  force-pushes a branch the project does not own. Adding one would need a
  decision the assistant can pass on to the owner.
- **Catch-up.** When `target` (or, for `branch`, the project's last landing)
  has moved past the task, the daemon merges it into the task branch in the
  clone.
  - **A clean merge** becomes a new revision straight away. The reviewer's
    passes carry over, because the task's own change did not change. QA runs
    again, and an approval already given still stands.
  - **A merge with conflicts** goes to the implementer, who resolves it. Every
    check runs again, and the owner is asked again. A revision that still has
    conflict markers is never recorded.
- **A checked-out `target`.** A push into a branch that is checked out follows
  git's own rule on the receiving side. By default git refuses, and the task
  asks the owner, explaining the choice. If the owner has set
  `receive.denyCurrentBranch=updateInstead` for that repository, git updates
  the checkout only when it has no uncommitted changes, and refuses otherwise.
  The owner's hooks and file-system monitor never run for these pushes.

### Landing work that was already delivered

A task delivered under an older policy, such as the two branches above, can
be landed later with "Land it". That action is on the dashboard and is also
the assistant's `land_task` tool. It moves the task into landing under the
project's current policy, starting from its approved revision. The earlier
approval stands, subject to the catch-up rules above.

## Wake-ups: `wake_me_when`

An agent asks to be woken when something changes, and says what to do then.

### Who can ask

- **The assistant**, through its tools `wake_me_when`, `list_wakes` and
  `cancel_wake`. A wake-up arrives as a new turn in the conversation, marked as
  a wake-up rather than a message from the owner.
- **A task's implementer**, by ending its reply with a `wake` block (see
  below). Its wake-ups arrive in its next round. A sandboxed role has no tool
  bridge, so a block in the reply is its way in.
- **The loop itself**, on a task's behalf, while a pull request lands. These
  wake-ups only make the loop look again. They are not shown to an agent
  unless they carry feedback.

### What can be waited on

| `on` | Target | Fires when |
|---|---|---|
| `task` | a task id | its status changes, or reaches `match` (such as `landed`) |
| `branch` | a branch in a project's repository | its tip moves |
| `pr_checks` | `owner/name#N` (or `this` for a task's own pull request) | the pull request's combined check state changes |
| `pr_review` | `owner/name#N` or `this` | a new review of any state, or a new comment, arrives |
| `time` | an RFC 3339 time, or a duration such as `30m` | the time is reached |

The daemon records what it saw when the wake-up was registered (the
baseline). A condition that already holds, such as waiting for a task to land
that already has, fires on the next check rather than never.

### Handles, parallel waits, cancelling

- Every wake-up gets a handle such as `wake-3f9a2c7d`. An agent can hold many
  at once.
- An agent can cancel any of its own wake-ups by handle. The assistant, like
  the owner, can cancel any wake-up.
- The assistant sees its open wake-ups through `list_wakes`. The implementer
  sees its task's open wake-ups listed in every round's prompt.
- Wake-ups end with their owner: a task's wake-ups are cancelled when it lands
  or stops.
- Each has a timeout: 24 hours by default, at most 7 days. When it expires it
  is delivered as a wake-up that says it timed out, so no agent waits forever
  without knowing.
- An owner can have at most 20 waiting at once.

### What a wake-up says

Every delivered wake-up carries:
- the handle, and what was waited on;
- when it was registered, when the change was seen, and when it was delivered;
- what was seen before and after;
- the continuation prompt the agent wrote for itself.

The gap between "seen" and "delivered" tells the agent whether it is reading
something stale. That happens if its own long turn, or a busy queue, held the
wake-up back. Several wake-ups that fire while the assistant is busy go into
a single turn, each with its own times, so the agent can tell which is
newest. Every wake-up ends with the same reminder: things may have changed
since it was seen, so check the current state before acting.

### The implementer's `wake` block

```wake
{"wake_me_when": [{"on": "pr_checks", "target": "this", "match": "", "prompt": "If e2e failed again, it is the flaky upload test; see the note in draft 3.", "timeout": "2h"}],
 "cancel": ["wake-3f9a2c7d"]}
```

The daemon reads this block from the end of the implementer's reply and
leaves it out of the revision summary. A block that is malformed or over the
limits is reported back in the next round rather than silently dropped.

## Trust

- Credentials never leave the daemon. It pushes with the owner's `gh` login as
  its credential helper, and git's global configuration stays ignored as
  before.
- Roles keep their sandbox: no network, no push, and writes only inside their
  own clone.
- Text from outside the team (reviews, comments, CI output) is data. It is
  quoted as someone else's text, never followed as instructions.
- A push into the owner's repository runs the receiving side with hooks and
  file-system monitor turned off.

## Alternatives considered

- **A fixed list of landing modes and nothing else.** Rejected: the
  projects differ in too many ways. Prose is kept alongside the structured
  fields for exactly that reason.
- **Agents push and merge with a real shell.** Rejected: that needs network
  access and credentials inside the sandbox, next to text from other people.
- **Agents call narrow landing tools.** Rejected for now: it needs a tool
  bridge for sandboxed sessions, and it adds nothing the deterministic loop
  does not already do. `wake_me_when` is the exception, because it carries the
  agent's own judgement: what to wait for, and what to do then.
- **Fast-forwarding the owner's checkout directly.** Rejected: it touches a
  working tree the daemon does not own. Git's receiving rules already
  express the owner's choice.

## Not yet verified

- The `pull-request` way has only been tested against a stand-in for GitHub
  (a local repository and a scripted `gh`), not a real one.

## As built (2026-09-24)

A review before building, and the build itself, changed a few things. The
text above is the design as proposed; this section records how the built
code differs.

- **Catch-up compares against a fresh tip.** For `push` and `pull-request`,
  what a task must include is the target's current tip, fetched each time.
  It is never the project's record of what landed: that record had pointed
  at a stacked branch, and would have merged the second change into the
  first.
- **Clean merges need no working tree.** The daemon builds them with
  `git merge-tree` and `commit-tree`. Only a conflict goes through the clone's
  working tree, to the implementer. A merge that changes no files is still
  recorded, or the task would try to catch up forever.
- **An approval stands only through clean merges.** Walking back from the
  latest draft, every step must be a clean merge, ending at the approved
  draft. Reviewer passes carry over only if they were given against the
  current brief. Commits someone else pushed to a pull request's branch never
  carry anything over.
- **Order is enforced.** A change built on another that has not landed is
  refused. A change already on the target is recorded as landed, with no
  push.
- **A moving target has a limit.** After four catch-ups the owner is asked.
  Landing failures become decisions, never retry timers, because a timer on
  the running task holds up every other project.
- **Pushes into the owner's repository** run the receiving side with the full
  safety list, plus `receive.autogc=false`. Local transport drops `-c`
  settings, so they are passed through `--receive-pack`. Git's messages are
  read with `LC_ALL=C`.
- **Wake-ups.** A wake-up on a task fires inside the state change that
  matches it. An assistant's wake-ups count as delivered only when its turn
  completes; a failed turn offers them again, up to three attempts.
  Cancelling a wake-up turn cancels its wake-ups.
- **Landing is its own setting.** Choosing a team no longer changes where it
  lands. Landing is set through `set_landing`, `PUT
  /api/projects/{id}/landing`, or the team card.
- **Landing work delivered earlier:** the dashboard's "Land on main" button
  and the assistant's `land_task`.
- **What the assistant reads each turn is now compact:** finished tasks show
  only their outcome. The first live run failed on the full state; see
  [the first landings](reference/2026-09-24-first-landings.md).
- **Tool labels.** The table of labels shown for assistant tool calls was
  still the table from before the rebuild. It is now tested against the
  tools the assistant is offered.
