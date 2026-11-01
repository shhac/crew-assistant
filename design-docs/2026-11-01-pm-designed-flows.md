# PM-designed flows

Date: 2026-11-01. Version: `crew-assistant` v0.71.0 (`9861b5f`). A proposal,
revised the same day after an adversarial review against the engine. Extends
[PM-designed teams](2026-10-31-pm-designed-teams.md), whose phase 1 (lane
labels from seats, seat names in prompts) is built and stays.

## Why

The owner wants a project's flow to suit the work as a real team's does: a
book going Muse, Outline, Draft, First Reader, Editor, Publisher's read; a
designer only when designs are missing; a second QA run skipped when nothing it
covers changed; an implementer sending a task to design; the PM signing off a
move back to To-do or on to Ready to land. The PM knows the project best, so it
designs the flow and the owner says yes.

Today the flow is fixed in Go. Its steps are task statuses, switched on in
about 180 places across claims, recovery, routing, stage limits and owner
decisions, and the dashboard's columns are fixed to match. Statuses, what runs,
which column a card shows in and whose capacity it holds are separate things
today (`stage_limits.go`), and the redesign keeps them separate.

## Shape

A **flow** is an ordered list of **stages** a task works through between being
admitted from the queue and asking to land. A project's playbook carries one;
a task pins the flow it started with, as it pins its playbook today, and a
pinned flow never changes under a running task. The board's work columns are
the flow's stages; To-do and the delivery rows stay as they are.

A stage has a stable **id** (verdicts, claims and moves name it, never its
label), a **name** for its column, and one of these kinds:

- **advise**: reads, and leaves notes and fixed structured facts (research).
- **make**: writes the deliverable. The task has one artifact and its
  revisions are task-wide, as today; a make stage produces the next revision.
- **checks**: one or more checks run **together** on the same revision, each
  with its own seat and capability: **check** (reads, gives a verdict) or
  **check by running** (runs the check, the app and the browser, as QA does,
  for code and for documents with writable scratch as today). Today's review
  and QA is one checks stage of two. A stage with one check is an ordered
  reader, such as First Reader.
- **on call**: a stage nothing passes through, which a turn can ask, then
  return to: design today. The asking turn's session is kept and its unsent
  edits are discarded, as today; a call is not a round. Assets it produces
  must still be taken in by a new revision before delivery, as today.

Each check in a checks stage either **gates** (default) or only **advises**.

## What moving through a flow means

Go owns this; no flow changes it.

- After admission, a task enters the first stage whose conditions admit it,
  and after each stage the next one that does.
- **The only loop is revision.** A gating check that fails, or asks for
  research, sends the task where today's decide step would: the latest make
  stage, or an advise stage for research, with the PM choosing among
  conflicting recommendations, as today. Questions go first to whoever can
  answer them.
- **Before asking to land, every gating check in the flow must be satisfied on
  the final revision**: passed on it, or carried over from an earlier pass
  because nothing in that check's scope changed since that pass. Scope is a
  set of path globs on the check, defaulting to everything. Changes are
  counted since the qualifying pass, not since the previous revision, and
  include renames, deletions and dependency or configuration files; when they
  cannot be known, the check is due again. A check placed before a later make
  stage whose scope that make stage then changed is due again, in flow order,
  before landing.
- **Deferred is not satisfied.** A check skipped on an intermediate round by
  its condition has not passed; it must still be satisfied before landing.
- **Owner acceptance stays an exception.** The owner may accept a draft
  despite findings, as today; the rules above are what the team must meet, not
  what the owner may waive.
- Rounds escalate at the round limit, as today; they do not end the task.
- Every branch taken, stage skipped, check carried over and move made is
  recorded in the task's activity with the fact or request that decided it.
  Replaying a task's record gives the same path.

## Conditions: a fixed vocabulary

A stage may have **enter when** and **skip when** conditions, each a list of
facts combined with `and`. Facts come from the record or from fixed fields
earlier stages report; there are no expressions, scripts or readings of prose.

| Fact | True when |
|---|---|
| `has_design` | the task has a current design |
| `needs_design` | an advise stage reported `needs_design` |
| `passed_before(stage)` | that stage was satisfied on an earlier revision |
| `unchanged_since_pass(stage)` | nothing in that stage's scope changed since its last pass |
| `changed(glob…)` | the latest revision changed a path matching one of the globs |
| `medium(code)` / `medium(documents)` | the project's medium |

`skip when` gives negation where it is needed: the motivating QA case is
`skip when passed_before(qa) and unchanged_since_pass(qa)`. New facts are added
only when a real flow needs one, as code with tests.

## Moves

A teammate or the PM can ask for a task to move out of order with
`move_to(stage, why)`. Each stage says who may move a task into it, set by the
PM when it designs the flow: `anyone`, `pm` (the request waits for the PM's
next look) or `none`. Today's design request from the implementer is the
first move and keeps its current behaviour.

- To-do is not a stage: moving a task back to it is a request to re-queue,
  always `pm`. Ready to land is not a stage either: a move there is a request
  to judge readiness by the rules above, always `pm`, and it never grants
  approval or delivery.
- A move takes effect only between turns. One that arrives during a running
  turn waits for it to end; a verdict, owner answer or hand-off already
  recorded for that turn wins.
- A move counts as a round. Repeated moves between the same stages are a loop
  signal for the PM's loop policies (CA-131), not a new mechanism.

## Sessions

Each stage says whether its turns **resume** the seat's own native session
from one round to the next or start **fresh**, set by the PM when it designs
the flow (owner, 2026-11-01, replacing CA-77):

- Defaults keep today's behaviour: make stages resume, which keeps the
  implementer's working memory and is where the prompt cache saves most;
  checks and advise stages start fresh, so each review is independent of
  the last. A First Reader meant to read as a newcomer stays fresh; an Editor
  tracking a manuscript across drafts may resume.
- A stage whose instructions change starts fresh, whatever it says: a
  member's changed learnings or attachments cost one cache miss rather than
  risking a session working from stale instructions.
- A resumed session starts fresh after a bound on turns or context, with a
  hand-over drawn from the record, so a long task's context does not degrade.

Before building this, measure what resuming saves today from the per-turn
cache-read share the daemon already records.

## Done, and delivery

What "done" means differs by repository, and only the owner knows it: a pull
request opened; a pull request merged, with its review and CI babysat; a tag
pushed; a commit on main. The repository's own conventions decide tags and
release steps, and crew-assistant follows them rather than prescribing any
(owner, 2026-11-01). A flow ends where delivery begins, and delivery keeps its
own authorities: opening, readying and merging pull requests, blockers,
landing pauses and stack order. A narrow **releaser** role that knows a
repository's release steps and definition of done may come later.

## Who designs a flow

The PM proposes a flow with `propose_flow` on project start or a brief change:
stages, seats, conditions, scopes and move rules, and why. It is one owner
decision showing the flow before and after; the assistant may accept it when
the owner asked it to. Validation refuses a flow with no make stage, a checks
stage with no gating check before landing, check by running where the medium
cannot run, a seat the team lacks, or an unknown fact.

## Build order

1. **Describe and shadow (1a).** Express today's code and draft flows as
   immutable data, with the checks batch and design call modelled as such, and
   write a read-only evaluator that predicts, for a task's record, which stage
   it is in, what may run next and what it waits for. Statuses, scheduler,
   recovery and delivery are unchanged. The daemon runs the evaluator beside
   the engine and records every disagreement as a diagnostic; tests compare
   the two across scripted scenarios, including crashes around hand-offs,
   late completions, design return, brief edits, accepted drafts, clean,
   foreign and conflicting catch-ups, stacks and document QA.
2. **Take over one transition family at a time (1b)**, starting with the
   board's columns, then admission to research and design, then the checks
   batch, each only once shadow runs agree.
3. **Ordered and repeated stages, conditions and scopes.**
4. **Moves**, beyond today's design request.
5. **`propose_flow`** and its decision, with each stage's session setting;
   then custom roles.

Phase 1 is done outside the crew: it changes the engine the crew builds itself
with. Phases 3 to 5 can be crew tasks once 1b has landed.

## Invariants to keep

Stable stage ids; revisions task-wide, stage attempts per stage; each verdict
names the revision and stage it judged; pinned flows immutable;
`UseProjectTeam` repinning only waiting tasks, as today; facts carry where
they came from and when; unknown scope fails closed; moves never bypass
delivery reconciliation or a recorded result.

## Not doing

PM-defined loops, expressions, scripts, conditions that read prose, stages
after delivery begins, or per-stage tool or sandbox choices.

## What step 1a found

Step 1a (`internal/core/flow.go`, `flow_shadow.go`; the daemon's shadow in
`internal/work/flow_shadow.go`) agreed with the engine on every pass of every
work test. Writing it found these differences between this design and the
engine, each to settle before the evaluator takes over a transition:

1. **Research is skipped when a task already has a plan**
   (`next_task.go` `startTask`, `claims.go` `startQueued`). Entering stages
   needs a `has_plan` fact, or an implicit rule.
2. **Researcher and designer stages follow role kinds on every medium**, not
   only on code: a documents team with a researcher researches. The default
   flows are derived from kinds; the medium names only the make stage.
3. **A checks stage with no checkers passes straight through**
   (`review.go`). The default flow keeps an empty checks stage; `propose_flow`
   validation must decide whether to allow one.
4. **Carry-over copies reviewer verdicts by role kind on a clean catch-up**
   (`handoff.go`), and QA is always due again. Scopes must replace that
   copying, not add to it.
5. **Owner acceptance waives the reading checks only**; checks by running
   stay due on merged revisions (`acceptance.go` `AcceptanceCheckers`).
6. **Delivery sends tasks back to the make stage**: answering a pull request
   and integrating landing assets move a task from delivery or deciding to
   writing. The evaluator places these at the landing position, within the
   make stage; delivery keeps its own columns.
7. **The engine has two notions of outstanding checkers** while an accepted
   draft's merge check is pending (`seat_preference.go` `nextTakers` drops QA;
   `nextChecker` and `offer` keep it). One must win before 1b.
8. **Some owner decisions show in the last check's column** before anything
   is written (`stage.go`, escalation and similar). A board quirk, left as is.
9. **A card's column can lag its flow position for stage capacity**
   (`stage.go` `Place`). Flow position and capacity stay separate in 1b.
10. **The board shows the checks batch in sequence** although its checks run
    together; the flow expresses that as a rule over the batch.
