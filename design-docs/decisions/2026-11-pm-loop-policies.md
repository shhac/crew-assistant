# PM loop policies, part 1 (2026-11-01)

Version: nothing to pin, code-internal. This recorded the first part of the
three-part PM loop-policy split.

## Evidence came from the existing record

The pure `core.LoopSignals` report used the snapshot, not a new history store.
Each signal retained a count, a threshold, and dated sources. Task counters
pointed to the task; decisions, checks, drafts, attachments, research requests
and prerequisite settlements pointed to their original records.

Rounds used versus the pinned limit and handoff attempts were supporting
history. Neither alone diagnosed a loop. Decision-kind counts also supported
judgement without treating normal choices, delivery, updates, or release shapes
as failed progress. Two consecutive same-shaped escalation or failure titles,
or two same-shaped owner questions with the same context, tripped recurrence.
Whitespace, case and round/attempt numbers were normalised; unrelated question
contexts stayed separate. Generated loop explanations were excluded from
question matching.

Findings counted once per distinct draft, using each checker's latest,
unanswered assessment of that draft and the current requirement version.
Two drafts repeating a still-current finding tripped recurrence. A later pass
retired that checker's finding. File-count growth or evidence growth alongside
a recurring unresolved finding was shown as possible growth without core
progress, rather than a claim that more files alone meant failure.

Two clean-merge drafts since the last substantive draft tripped catch-up
recurrence. Accepted-draft catch-ups still in checked landing were excluded
from failed progress, including owner holds that resumed landing and a stamped
validation failure of the same accepted merge. Reports were computed before a
new owner decision changed the task to waiting; stale validation evidence and
retired acceptance did not suppress loops. More than four landing catch-up attempts also surfaced
the task counter and clean-merge sources, including accepted drafts: this was a mechanical target-moving loop, not failed draft progress. Three same-shaped research requests, or two
settlements of the same prerequisite, surfaced those histories. These were
diagnostics; catch-up and prerequisite root-cause fixes stayed with their
existing owners.

The queue look showed only tripped signals, their counts and latest dated sources,
plus one recommendation. Escalation judgement retained tripped evidence and policy recommendations; untripped context kept counts and the latest three dated sources, with full sources available in the report. Relevant owner
decisions led with one plain observation and preserved the producer’s recommended choice and reason, adding only the counted observation and authority note. Risk stayed with the owner. Raw source
IDs, timestamps and workflow terminology moved to an optional Loop evidence
disclosure in the request's full decision view, absent from inbox cards. The
decision retained a derived report snapshot for that historical drill-down;
the detector still counted the original records, not stored report counts.
The report's precise presentation prefix was stripped when matching original
question content and identical decision retries. Open escalation, failure and question decisions
were reused only when their
title, original context and choices matched; generated loop annotations did not
make identical retries appear different.
Ordinary choices and delivery decisions kept their replacement semantics.

## Authority stayed with the owner

Five independent settings defaulted On:

| Key | Response |
| --- | --- |
| rescope | Keep the core purpose and split secondary requirements |
| accept_leftovers | Accept only safe narrow leftovers at the limit, with a follow-up |
| pause_loop | Consolidate loop evidence and propose a smaller scope |
| change_approach | Propose an alternative after two failures; owner confirms any stop |
| close_duplicate | Close only queued, never-started duplicates |

Only explicit On/Off choices were stored. Default removed the explicit choice.
A revision fenced simultaneous owner changes. Teams owned shared policies;
teamless projects owned theirs. CLI, owner API and dashboard controls exposed
the same operations. PM and answering tools could not change these settings.
Geoffrey's modes conferred no PM authority, and were left untouched.

The first four settings explained and recommended only in this part, even when
On. Off recommendations said no action was executed. P1, security, data loss
and irreversible work stayed with the owner.

## Duplicate closure and undo shared the store transaction

`PMAction` recorded a stable key, policy, PM seat, reason, source evidence,
effects, timestamp and performed/undone status on the task. `CloseDuplicate`
checked live authority, the record captured at admission, and owner overrides
under the store lock. It refused self/missing/cross-project targets, finished targets or targets
that were themselves duplicates, and tasks already carrying other duplicate
requests (so later closures could not create chains). It also refused started or
nonqueued work, decision history, unfinished dependants (including stacks and
blockers), owner links and holds, project pause and high-risk work. Pending
messages, waiting or fired wakes, and prior role learnings also prevented closure,
so the ordinary stop cleanup would have had no live work to cancel. It recorded
the target, stopped detail, note, action and Activity in that same transaction.
The target's readable reference (or objective) was saved as a display label;
owner history, notes and Activity used readable labels rather than policy keys
or canonical IDs. The dashboard resolved current references when available and
kept the saved label after undo or target removal.
The PM tool admitted at most three closures per look, separately from queuing,
and PM chat recorded a receipt. Routing and landing turns had no duplicate-closure capability; queue looks and PM chat could close queued duplicates, as their instructions explicitly allowed.

A repeated performed key returned its existing action. Undo was owner-only.
It required the task still to be stopped by that action, with no later task
edit, and restored its status, detail and clamped prior queue position. Target
renames/stops and later incoming links did not block undo. Both undo and its
Activity entry committed together. An owner-undone duplicate closure vetoed any
further duplicate closure on that task, including against a different target,
across restarts. Other policies retained their per-key replay fence. Undo did
not alter policy settings.

Errors rolled back the entire transaction. Restarts therefore saw either the
original queue record or a fully logged closure. Concurrent PM calls, owner
changes and Geoffrey actions serialized on the existing store transaction.
Closure changed task status, which invalidated Geoffrey's existing
`DecisionActionDigest`; a new decision or changed task state invalidated PM
admission in the reverse direction.

## Follow-on parts reused these pieces

The next parts owned execution of re-scope/acceptance and consolidated/failed-
approach pauses. They could extend the action effects and checked undo without
creating a second audit history. Cross-project loop diagnosis could reuse the
exported pure report. A later policy could add an independent Off default to
the defaults table. Nothing in this part changed landing or delivery authority.
