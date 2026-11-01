# A project's flow is data, with fixed meaning

Date: 2026-11-01. Version: `crew-assistant` v0.71.0 (`9861b5f`). Amends the
AGENTS.md rule that playbooks are "not a workflow engine or a DSL".

## Decision

The owner chose to let the PM design a project's flow: an ordered list of
stages with conditions and move rules, described in
[PM-designed flows](../2026-11-01-pm-designed-flows.md). This is a deliberate,
bounded exception to "not a workflow engine or a DSL":

- A flow is data with a small fixed schema. Its stages choose a capability from
  a fixed set; tools and sandbox follow the capability, never the flow.
- Conditions come from a fixed vocabulary of facts Go evaluates, combined with
  `and`. No expressions, scripts or readings of prose.
- The only loop is revision, which escalates at the round limit as today. Explicit moves are recorded, count as rounds and obey each stage's
  move rule.
- A flow ends where delivery begins, and delivery keeps its own authorities.
  Before asking to land, every gating check must be satisfied on the final
  revision, passed or carried over; the owner may still accept despite
  findings.
- What counts as done is the owner's, per repository, and crew-assistant
  follows each repository's own release conventions rather than prescribing
  any.
- Routing is deterministic from the task's record, and every decision says
  which fact made it.

## Why

A fixed flow could not express the owner's real cases: a book's ordered
readers, a designer only when designs are missing, QA once when nothing it
covers changed, an implementer sending a task to design. The bounds above keep
the over-engineering the owner disliked in v1 out: nothing in a flow can run
code, choose tools, loop on its own or skip delivery.

## Consequence

AGENTS.md's playbook rule is reworded to say playbooks carry a flow of this
kind and nothing beyond it. Adding a capability, a condition fact or a move
rule is a code change with tests, not a flow change.
