# Asset routing — 2026-10-04

Version: nothing to pin: code-internal.

As of this change, implementers owned code, including animation behaviour,
while designers owned generated assets. A team with a designer required an
explicit asset_creation classification of blocked work rather than guessing
from words such as image or sprite. Asset reports required a production spec
before another draft could be recorded. PM asset findings overrode owner-step
recommendations. Missing classification returned to the implementer for correction.

Recorded reports returned atomically to writing, preserving the draft and
session. The report, reporter and routing reason remained durable until a
production request settled the report and linked its criterion in the same
update. A restart before that request resumed writing. A failed update left
the previous record intact; stopped, edited and stale turns were fenced out.

Production reused the finished-assets protocol, complete groups, hashes,
provenance and rejected-variant archive. Production revisions no longer counted
towards the two-request ordinary design-input limit. Actual designer capability
failures and owner-only choices still used the designer's escalation contract.
Teams without designers retained their existing escalation behaviour.


## Review corrections, 2026-10-04

As of the reviewed implementation, production requests named their covered
reports explicitly. Updated classifications replaced prior ones while omitted
reports retained their origin. A production-only correction for a single
unclassified obstacle acknowledged that obstacle as asset work; several
unclassified obstacles required the reporter to disambiguate. Specs covering
selected durable routes named each with a Requirement: note.

Classification-only turns updated reports and the implementer session against
the existing draft, then returned to deciding. Review findings retained their
source and original finding, so a code-only classification resumed the original
round-limit decision rather than becoming an owner step. Combined implementer
and designer seats could request a separate production turn. Removed or rewritten
requirements were reconciled before writing; a disappeared designer retained the
report and recorded the missing capability.

Transactional rejection and revoked-claim tests covered the redirect,
classification and production request. Restart tests retained the original draft,
session, routing reason and finished assets without running completed groups again.

## Finding identity and brief fences, 2026-10-04

As of the next review, each review finding had its own identity, including its
draft and requirement versions. Corrections preserved that identity even when
several findings quoted one criterion or the original note had no criterion.
Production specs with explicit Requirement notes settled only their named
reports. Completed rewrites replaced ordinary omitted obstacles; partial
hand-off corrections continued to preserve them.

Redirects, classification corrections and production requests checked the
brief version seen by their turn, as well as task evidence. Deleted requirements
were reconciled before recording a route. Losing a designer ended an unknown
review finding's classification request without turning it into an implementer
owner step. Task-edit undo also preserved distinct finding identities.

PM landing holds could report a structured blocked_asset with requirement,
why and asset_creation. Confirmed asset creation returned to the implementer
for a production spec before an owner hold was recorded, with or without an
open pull request. Ordinary holds kept their existing behaviour. No-designer
teams retained the existing single-object owner-step reply contract.

## Effective requirements and partial production, 2026-10-04

Brief updates retired removed requirements' pending reports atomically, including
reports recorded before requirement bindings existed. Routing reconciled against
both current task and brief criteria, preserving duplicated requirements and
routes restored by undo rather than relying only on historical removals.

Partial production specs classified and settled only their covered reports.
Unrelated unknown reports remained unknown; several durable routes required
explicit coverage even when the reply contained only a production block.

PM landing validation detected structured asset-report fields independently of
their field types. Unsuccessful bounded correction became a role failure,
and a confirmed asset obstacle took precedence over a contradictory approval.


## Production lifecycle recovery, 2026-10-04

As of the lifecycle fixes, an explicit PM asset classification survived an
unsuccessful correction of other judgement fields. Removed brief requirements
were retired from prepared draft outcomes and linked production reports while
the outgoing brief still established their origin.

Uncovered unknown reports retained their classification obligation through
partial hand-backs and restarts. Requirement notes had to resolve to pending
reports; empty and unmatched coverage used bounded reply correction.

Delivered groups held writing open until a new draft recorded their integration.
Classification-only corrections and unchanged pull-request replies could not
approve the pre-production draft. A disappeared designer restored an unfinished
request's linked reports, recorded the missing capability, and retained completed
groups, provenance and rejected variants while discarding unfinished-turn files.

## Malformed siblings and owner assignments, 2026-10-04

As of this review, PM finding classifications were retained independently when
a sibling finding could not be decoded. Writer correction retained readable
requirements and reasons from malformed report lists, treating invalid
classifications as unknown until explicitly corrected. Production coverage used
the same normalized report identity as unmet-report recording.

Designer-loss recovery retained confirmed asset obstacles before the first draft,
even on teams without designers. Existing owner assignments superseded inherited
brief requirements during recovery, using the task-edit authority rule without
discarding its undo evidence. Git-backed PM and restart tests lived behind the
repository's non-Windows build constraint.


## Coverage and recovery follow-up, 2026-10-04

As of this review, shortened production quotes resolved against the complete
candidate set. Ambiguous quotes required correction; report IDs distinguished
findings sharing a criterion, including classification plus production replies.
PM landing corrections could not omit an already reported asset obstacle.

Designer-loss recovery kept owner-transferred reports on the transfer edit for
undo. Removing all linked requirements ended the integration obligation while
retaining historical attachments, provenance and reports; unchanged pull requests
could then proceed against the amended requirements.
