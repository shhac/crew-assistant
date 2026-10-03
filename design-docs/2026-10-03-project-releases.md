# Project releases — 2026-10-03

Version: code-internal, nothing external to pin. Dashboard: design 1,
`releases-card-design.md`, supplied with this task.

As of this change, releases were an owner-controlled project setting,
available only to code projects landing by push or pull request. “When to
release” opted in; without it the project never released. The optional “How to
check a release” command substituted `{version}` and verified only. Approval
defaulted to the owner, with PM approval available separately from landing
approval. No team or assistant tool could set these fields. Release state and
history were not project-creation inputs.

Switching landing to draft or branch required removing release settings first.
Switching from push to pull request required removing the release GitHub
repository, since the landing repository became the release destination.
Validation rejected incompatible settings rather than changing publication.

On existing PM triggers, the daemon fetched the landed target and version tags
into a private tag namespace. The PM saw commits since the greatest reachable
strict semver tag, the guidance, current time, last recorded release and
pending proposal, plus only the latest declined proposal. It could propose a
greater semver version, preserving the previous tag's `v` prefix style
(default `v`), and short notes. Owner approval presented the version, notes
and included commit subjects in a decision. PM approval proceeded without that
decision. Strict semver was validated before comparison with occupied
versions. Proposals had to exceed the highest upstream version, including tags
outside the target; commit history still used the latest reachable version
tag.

Private tag namespaces were pruned on every fetch, so deleted upstream tags no
longer counted. A push project publishing to GitHub also fetched that
repository’s tags with the owner’s credentials on each PM turn to learn
occupied versions. A failure of that fetch, including a GitHub outage, blocked
release proposals even when the local history could be read. Pull-request
projects read their target and tags from their landing repository.

The durable path was proposed → approved → checking → approved → publishing →
recorded. Checking took a QA seat through a fenced project claim. It used QA's
ordinary sandbox and checkout path, including CheckInCopy and check loopback
settings, scratch directory, one JSON-format retry, and checkout verification.
QA read the member’s pinned learnings and reported a live turn, with no task
coordination or publishing tools. The command ran once on a copy of the landed
target. Its exit status and a bounded tail (40 lines / 4 KB) went to Activity
and, on failure, an owner decision. There was no free-form publishing command
and nothing team-written ran outside a sandbox. AGENTS.md was unchanged.

The proposal pinned the landed target the PM saw, so the owner's included
commit list and the PM's notes described exactly what would be tagged, even
if more work landed before approval. Checking or publishing took its claim
before marking the release started. Older unpinned runs fetched and pinned
the target only after acquiring that claim. Unavailable seats or usage
allowance therefore caused no repeated target fetches. Native git
created an annotated version tag on that commit with the notes as its message
and landing's identity and signing settings. Landings were not held for a
release: later landings could not move its pinned commit. No release step
started while landing was paused, a task was landing or Delivering, or another
project claim was held. A pause during checking held publication until
resumed. One project had at most one pending release.

For push landing, the tag was delivered into the owner's local repository with
hooks disabled. An optional GitHub owner/name selected publication using the
owner's gh login, as pull-request pushes did. The target branch was pushed
first, fast-forward only, to the checked commit, then the tag. A remote target
already containing that commit needed no update. A divergent target refused
both GitHub pushes and left the tag local. No repository meant local-only
recording, with “tag not published.” Pull-request projects pushed only the tag
to Landing's repository, where their target was already published.

Publishing steps were reconciled after a restart: existing tags at the pinned
commit counted as done, the annotation object was reused, and a branch already
containing the commit was skipped. A lookup failure or a tag at another commit
stopped publication. An abandoned clone tag could be replaced only after
checking both destinations and finding that neither held that version.
Published tags were never replaced. A check interrupted before its result was
recorded could run again after reclaiming its claim, since it had published
nothing. Any actual failure opened “Try again” / “Leave it,” with no automatic
retry. A publication retry skipped successful steps; leaving a locally tagged
release recorded it as local only. Custom answers and dismissals declined
proposals or left failed releases, just as Not now or Leave it did; an
existing tag in the owner’s repository was recorded as local only. Declining a
proposal or leaving a failed release cleared the pending run so another
proposal could be accepted. Removing settings cancelled an unstarted proposal;
changing PM approval to owner approval handed an unstarted approval to the
owner. Started work kept its policy through completion. The latest 20
successful records held version, commit, time, notes, approver and publication
destination.

The Config tab's Releases card followed the supplied RunRecipe sibling design,
immediately after Landing, with guidance, check, optional push repository and
approval in that order. Recent records showed notes and destination without
health colours. Project metadata showed only the latest successful record,
including “local only” where applicable.

Out of scope were releases requiring more than a pushed tag, such as uploads
without CI; checks needing network beyond QA's existing loopback capability;
force pushes; fetching or pulling into the owner's checkout; GitHub Releases,
changelogs, Homebrew updates and self-upgrade (CI / separate tasks); and timed
PM wake-ups. The repository's own network-dependent release-check was left
unchanged. Existing PM triggers, including landings, governed when it looked.
