# Pull requests as stages of the board — 2026-10-01

Version: nothing to pin: code-internal. Supersedes the pull-request way of
`2026-09-24-landing-and-wakes.md`, whose mechanics it keeps.

## Why

The owner found the pull-request way hard to follow: one Ready stage covered
waiting for approval, opening the pull request, waiting on its reviews and
CI, and merging, and nobody but the loop decided anything once the owner had
approved the opening. They asked for the flow to be shown and run as stages,
with the team, not only the loop, deciding what each change on the pull
request needs.

## What changed

- Pull requests became a landing toggle (`pull_requests`), apart from the way
  a change lands without them (`via`: branch or push), so later parts of the
  flow can be switched on and off on their own. `Way()` still answered
  pull-request whenever the toggle was on, so nothing asking it could miss it.
- Two board stages joined Ready, as rows above the columns, each with a limit
  of its own (unlimited unless set): **PR to open**, a change past its checks
  whose pull request was still to open; and **PR open**, open but not ready.
  **Ready to land** came to mean ready to merge: approved where the
  repository asks for review, every check green, every review thread
  resolved, and no conflicts. Checks that had not started just after a push
  were not taken for none. Opening a pull request needed room in PR open.
- The stages were derived from what the loop recorded of the pull request
  (`Proposal.Observed`), never from new task statuses: landing, awaiting,
  deciding and waiting kept their meanings, so every resume path stayed.
- Two gates: who decides a pull request opens (`open`: the PM by default, the
  owner, or the implementer), and who approves a ready one merging
  (`approve`: the PM by default, the owner, or none). The PM could merge,
  hold, or send a ready pull request back to the implementer with what still
  needed doing. A merge approval was kept apart from the opening approval,
  and one open while the pull request changed was withdrawn and decided again.
- The implementer wrote the pull request's title and description with its
  draft (a `pr` block), and a later draft rewriting them updated it.
- Feedback that asked for no change no longer always cost a round: the
  implementer could reply in a thread or the conversation, resolve threads
  its pushed drafts fixed, or hand the question to a reviewer, QA or the PM
  (a `pr-reply` block). A reviewer or QA answered with a check; the PM could
  reply, direct the implementer or ask the owner. Replies went up signed and
  marked as the team's own, only after the draft they came with was pushed.
- Only feedback from the repository's owner, members and collaborators was
  acted on; anyone else's was counted for the owner and never acted on.
- A project could pause landing, as for a code freeze. Without pull requests
  nothing landed; with them, pull requests still opened and were answered,
  and only merging waited.
- Turning pull requests off had the PM (or, without one, the owner) choose
  for each task that started with them whether it carried on with them or
  landed the project's way, its pull request closed with a word on why.
- State moved to schema 3, upgraded once on open with a backup of the
  database, and refused by older builds. Existing pull-request projects kept
  their behaviour: the owner approved opening, and it merged once ready.

## Decisions not taken

- Opening the pull request earlier in the flow, before QA: deferred.
- A teammate answering on the pull request without the implementer handing
  it over: the implementer stays the one who reads what comes in.
- Linking stacks of pull requests: out of scope.
