# What would make crew-assistant as good as an interactive agent

Date: 2026-10-31. Version: `crew-assistant` v0.67.0 (`3b9635c`) plus the
check-timeout fix. Evidence comes from one day in which an interactive Claude
Code session, with subagents and one Codex second opinion, worked on this
repository directly while the daemon was paused.

## The question

The owner asked which is better placed to build crew-assistant: crew-assistant
itself, or an interactive agent. On 2026-10-31 the honest answer was "the
interactive agent, for anything touching the engine or needing judgement on
scope; crew-assistant, for well-scoped work no one wants to watch". This note
records why, and what would change that answer to "equal, or crew-assistant".

## Where the interactive session did better, and why

| Observed on 2026-10-31 | Why crew-assistant falls short | What would close it |
|---|---|---|
| About 15 verified changes landed in one day, each with focused tests during work and one full check before landing. | Every round runs the whole project check: 8 to 12 minutes for this repository, which outgrew the hosted limit and timed out repeatedly (CA-126). | **Scoped checks per round.** Run the tests for what the draft changed during rounds; run the full check once, before landing. This is the "scoped checks" half of CA-111, and is worth taking out and doing first. |
| Queued work that no longer made sense was found and stopped: CA-107 was already built; six crew-code-review tasks targeted an atlas that had been replaced; several sandbox tasks contradicted a decision taken that day. | A queued task keeps its original brief however long it waits. Nothing asks whether it is still worth doing before it starts. | **A "still worth doing?" look before a queued task starts.** The PM sees the commits since the task was written and the current state of what it touches, and keeps, rewrites or closes it. Closing for "already done" or "superseded" names the commits as evidence and needs no owner decision under a policy the owner enables. |
| Most of the planned sandbox work was dropped after a short review, and the requirements change was kept to visibility only. | The team works to the task as written. The researcher can say what is unclear but has no way to say "this is not worth building" or "a much smaller change gets most of it". | **A smaller-scope proposal from the researcher**, judged by the PM and routed to the assistant before it reaches the owner: the assistant answers from the owner's recorded decisions where it can. |
| The engine's own faults were fixed directly: owner identity over loopback, the check timeout, stale engine names after a harness upgrade. | A fault in the engine stops the team that would fix it: a 10-hour accounting hold (CA-125), a 7-hour cleanup stall (LAH-44), checks that never finish (CA-126). | **The reliability tasks first** (CA-126, CA-125, CA-122), plus the watchdog in CA-100: a stalled member or task is noticed within minutes, not hours. |
| Branches landed the same day they were written. | Four tasks sat "in review" on branches 3 to 4 weeks behind main. | **Work in progress limits and early catch-up** (CA-109, CA-111), and a review that times out into a decision rather than waiting indefinitely. |

## Where crew-assistant is already better

- It runs unattended and in parallel on the owner's subscription.
- Its reviewer and QA judge each draft independently, from the record rather
  than from the author's account. The interactive session's work was checked
  mainly by tests and by agents it briefed itself, which is a weaker check.
- It records everything: revisions, verdicts, decisions and costs per task.

## How to tell

Opinion settles nothing here. Give the same small set of tasks to both, a mix
of a bug fix, a feature with UI, a refactor and a written document, and
compare:

- owner interventions per task (decisions answered, messages sent);
- wall time to landed;
- rounds per task, and findings per round;
- tokens or subscription share used;
- defects found after landing.

crew-assistant already records most of these per task; the decision exporter
covers interventions. The interactive side needs the same five numbers kept by
hand for a few tasks.

## Order

1. CA-126 (done), CA-125, CA-122: the engine must not stall the builder.
2. Scoped checks per round.
3. The "still worth doing?" look before a queued task starts.
4. Researcher smaller-scope proposals, routed to the assistant first.
5. The comparison above, on four tasks, before deciding what comes next.
