# PM-designed teams

Date: 2026-10-31. Version: `crew-assistant` v0.66.0 plus `7535dc7`. Nothing to
pin beyond that: this is a proposal, not yet built.

## Why

The owner wants a project's team and board to suit any kind of work, not only
software. Writing a book might want a muse, a novelist, an editor and a
publisher; a report a researcher, a writer, a fact-checker and an editor. The PM
knows the project best, so it should be able to propose that team, and the
owner should only have to say yes.

## What was found

As of `7535dc7`, most of this is already data:

- Seat names, charters (instructions), engines and members are per seat
  (`core.Role`, `internal/core/playbook.go`). The draft template's implementer
  seat is already called "Writer".
- What stays closed is the six kinds of role a seat can hold: PM, researcher,
  designer, implementer, reviewer and QA. Go switches on them for what a turn
  may do (write the artifact, give verdicts, run the check, use the web, hand
  off for design). In effect they are capability classes:

  | Class | Kind | Can |
  |---|---|---|
  | maker | implementer | writes the deliverable; runs the check on code |
  | checker | reviewer | reads and gives verdicts |
  | checker with the app | QA | runs the check, the app and the browser; code only |
  | advisor before work | researcher | reads, plans, uses the web |
  | advisor on call | designer | design hand-off, images |
  | keeps the list | PM | orders and triages |

- Board stages derive from task status, and the dashboard's columns are fixed
  (`boardLanes.ts`), with "Writer" special-cased in a few places.
- About 260 places in prompts name kinds directly ("the implementer", "QA"),
  so a book team's PM would still be told about implementers.
- The PM has one propose-then-approve path already: the run recipe
  (`propose_run_recipe`, `core.ProposeRunRecipe`, accepted on the owner's
  choice).

## Proposal

Keep the loop's shape fixed in Go: advisor, then maker, then checkers (in
parallel, as now), then the PM's routing, then the delivery gate. Make
everything a person sees about the team into data. No flow definitions, stages
or transitions: that would be the workflow engine AGENTS.md rules out.

- **A seat's lane label.** `Role.Lane` names the board lane for its class
  ("Musing", "Drafting", "Editing"); empty keeps the class's default. The
  board labels lanes from the seats, so a book team's board reads Musing →
  Drafting → Editing → Ready. The checks column gets a sub-lane per checker
  seat when there is more than one.
- **Seat names in prompts.** Every prompt names seats, not kinds ("Editor and
  Publisher check each draft"), so a muse is never told it is a researcher of
  files and functions. The researcher's prompt gets a written-work variant.
- **Templates.** Beside `draft` and `code`: `book` (Muse as researcher,
  Novelist as implementer, Editor and Publisher as reviewers), `report`
  (Researcher, Writer, Fact-checker and Editor as reviewers) and `design`
  (Designer as maker, Critic as reviewer). A written-work team may have a
  researcher. The publisher judges readiness to publish; publishing itself is
  delivery, an adapter behind the gate, never a seat's capability.
- **The PM proposes a team.** A `propose_team` tool, offered on project start
  and when the brief changes, opens one decision: each seat before and after,
  with its charter and lane, and why. It may set seat names, kinds from the
  table above, charters, lanes, engines, members and max rounds. It may not set
  the medium, the check, tools, web or browser access, landing or delivery.
  Accepting edits the project's playbook through the existing path; if the
  project shares a team, it gets its own copy first, so no other project
  changes. The assistant may apply a proposal directly when the owner asked it
  to. Running tasks keep the team they started with.
- **Kinds stay as they are.** No new kinds unless a real run needs one, and no
  migration: existing seats already fit.

## Phases, each ending with real use

1. Lane labels from seats, seat names in prompts, and the "Writer" special
   cases gone. Real use: relabel an existing writing project.
2. The book, report and design templates. Real use: start a book project.
3. `propose_team` and its decision. Real use: the book project's PM proposes a
   continuity checker and the owner accepts it.
4. Only if a run needs it: a checker whose verdict advises rather than gates,
   or an ordered check (editor before publisher).

## Not doing

User-defined stages or transitions, per-seat tool or sandbox switches, delivery
by any seat, or the PM changing a playbook without the owner or the assistant.
