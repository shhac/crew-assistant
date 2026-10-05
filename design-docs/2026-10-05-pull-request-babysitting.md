# Babysitting pull requests: drafts, CI logs, outside threads — 2026-10-05

Nothing to pin: code-internal. Follows
[Pull requests as stages of the board](2026-10-01-pull-request-stages.md).

## Why

A large repository the team was about to work in asked that pull requests
always open as drafts, with only a person marking one ready for review. Its
required checks included a fan-in summary job, whose own log said only that
other jobs failed, and an external deployment status that reported only once
a pull request was ready. Several automated reviewers left review threads.
The flow wanted was: the team opens a pull request, babysits it until it is
approved, green and has no unresolved threads, and the owner decides the
merge. Three gaps stood in the way:

- a draft's merge state never became clean, so a draft never got anywhere;
- a failing check reached the implementer, who works without the network, as
  a name and a link it could not open;
- a review thread from anyone outside the repository held the pull request
  silently, since the team never acted on it and nothing asked the owner.

## What changed

- `land.draft` (pull requests only) opened each pull request with
  `gh pr create --draft`. A draft was babysat as before but was never ready to
  merge. Once it was ready for review — every check finished and passed (one
  still pending or expected was not green; one that only reports on a ready
  pull request was simply absent), mergeable with its base, and no unresolved
  thread left for the team — the owner was asked "Mark pull request #N ready
  for review". "Mark it ready" ran `gh pr ready` for the head the owner was
  shown (an already-ready pull request was fine), then the usual approval and
  merge decision followed. "Keep it a draft" was remembered for that head, so
  the owner was asked again only after the team pushed again. Like the merge
  decision, it was withdrawn and looked at again whenever the pull request
  changed while it was open. The watch on checks now included whether the
  pull request was a draft and whether it was mergeable, since a draft's
  merge state never moves.
- A failing GitHub Actions check (one whose link was this repository's
  `actions/runs/<run>/job/<job>`) had the end of its job's log added to the
  implementer's finding: read on the daemon side with `gh api`, without
  terminal colours or GitHub's timestamps, credential-shaped strings redacted
  (the release flow's redaction, moved to `text.Redact` and widened), at most
  150 lines and 12 KB a job and three jobs, within 45 seconds in all. The other
  failed jobs of the same run were listed too, earliest started first, so a
  fan-in summary job, which starts last, gave way to the jobs whose logs said
  what failed. Any other status, or any failure to read, kept the name and
  link.
- `land.trusted_bots` named automated reviewers by GitHub login. Their
  reviews, comments and thread comments reached the implementer as advice:
  fix it if it is right, otherwise reply why and resolve the thread. GraphQL
  names an app without the `[bot]` suffix, so either spelling matched.
- Unresolved threads opened by anyone neither in the repository nor trusted
  were sorted apart. When they were all that held the pull request from being
  ready (or, for a draft, ready for review), the owner was asked once, shown
  each one's author, place, first 200 characters and link. "Let the team
  answer them" gave the implementer what had been written in those threads by
  the time the owner was asked, marked as from outside; nothing written later
  was passed on. "Leave them to me" kept them from the team; a draft could
  still be marked ready meanwhile, and anything else waited on the owner
  resolving them.
- The dashboard's Landing settings gained "Open them as drafts" and
  "Automated reviewers the team trusts", and the assistant's `set_landing`
  tool gained `draft` and `trusted_bots`; the owner and assistant set them, as
  every landing setting. Tasks under way kept the policy they started with.

## Decisions not taken

- Special-casing any check or status by name: a ready-only status is absent
  on a draft, so none was needed; one that reports pending on a draft would
  keep the draft from being offered for review.
- Letting the PM mark a draft ready: only the owner (or the assistant on the
  owner's word) answers the decision.
- Feeding the implementer later comments in a thread the owner let in: what
  the owner saw is what they agreed to.
