# Work you can see moving, and change by hand

2026-09-26. Built for v0.21.0.

## Why

The owner couldn't tell whether a long implementer turn was working or had died. A task handed to a role looked the same whether the role had picked it up or not. When a draft needed a small fix after the team's rounds ran out, the only ways to finish it were to land it past the daemon or to edit the daemon's records.

## What was built

- **Live turns.** The roles runner reports a turn from when it is picked up to when it ends, with each event its session sends. The loop keeps the running turns on show and never stores them.
  - Each turn shows who is working and since when, its tool calls and edits, the tool running now, its output tokens, and when it last did anything.
  - For a turn that writes, it also shows how many files differ from where the turn began. A repository asks git; other work compares each file's timestamp with the one it had when the turn began, rather than with the clock, which a filesystem may stamp coarsely.
  - A turn that has to ask again counts its second run afresh.
  - The dashboard shows the counts on the request's card and panel from the state it already polls, and says when a turn has gone quiet.
  - A request with a role but no turn running says it is waiting to be picked up, and why when it can tell.
- **Drafts by hand.**
  - `task list`, `show` and `path` say where a request's work is.
  - `task checkout` puts its latest draft in the owner's repository as a branch or worktree. It never moves a branch past the owner's own commits unless forced.
  - `task adopt` takes a commit from there as the next draft, marked as the owner's.
  - The daemon fetches the commit without touching the working copy other tasks share, and requires it to build on where the task started.
  - The draft is reviewed and checked like any other, and the team is told it is the owner's. `--approve` skips the reviewers but not QA.
- **One owner for a task at a time.** A loop step and an adopt each claim the task for their whole run, and recording a draft refuses one that is no longer the next. So a draft by hand never gets the same number as the implementer's, and is never replaced unseen.

## Not built

- **The daemon doing a checkout.** `task checkout` runs in the CLI against the owner's repository, reading the daemon's workspace for the draft, rather than asking the daemon to hand it over.
- **Owner drafts for written work.** A document task's draft can't be adopted by hand yet.
