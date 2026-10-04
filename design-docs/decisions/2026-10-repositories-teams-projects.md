# Repositories, teams and projects — 2026-10-04

Version: proposal against crew-assistant v0.56.9 (state schema as of that
release). Status: proposed by the owner and the operator session; not yet
built.

## Goal

A person, or an external agent acting for them, said what needed doing.
crew-assistant orchestrated the doing. Everything below served that: the
owner directed outcomes, and crew-assistant chose who worked, where, in
what order, and how the result reached the codebase.

As of v0.56.9 a crew-assistant project bundled four things with different
lifetimes: the code (repository, check, landing and release policy, all on
the project's playbook), the staffing (seats on the same playbook), the
backlog (tasks), and an optional Linear intake link. That worked for one
person running one project per repository. It didn't fit larger work: a
monorepo with several streams of work in flight, teams that work through
their projects one at a time, more than one tracker, and other people
landing into the same repository.

## Principles

1. **One human per instance.** A crew-assistant instance acted for exactly
   one owner, or for no human at all when an external agent drove it. It
   kept no roster of other people, no roles and no multi-user login. Teams,
   seats and members were internal organisation. From outside (a tracker,
   GitHub, a chat), every action looked like the owner, or like a bot
   account the owner configured. Several people meant several instances.
   They coordinated the way people already did, through the repository and
   the tracker.
2. **Direction in, orchestration out.** Direction came through any front
   door: the dashboard, chat with the assistant, the CLI, the HTTP API, a
   tracker, or an external agent using the CLI or API with the owner's
   token. All of them produced the same internal objects: projects, briefs,
   tasks, answers to decisions. The owner's decisions and owner steps were
   the only places a human was required. The PM's and the assistant's
   policies (CA-102, CA-98) handled the rest.
3. **crew-assistant's model is the source of truth.** Trackers were optional
   adapters that mirrored it. No concept depended on a particular tracker.
4. **Agents never appear outside.** Seats and members had no identity in a
   tracker or on GitHub. Where an outside system wanted an assignee or an
   author, it got the owner, or the bot account, optionally with a line
   naming the seat that drafted the work.

## Entities

```
Owner (the one human) ── decisions, owner steps, outward identity
Repository ── code areas, checks, release units, landing strategies on offer
Team ── default seats, PM, policies, ordered project queue, WIP limit
Project ── scope (repositories and areas), brief, backlog, dependencies,
           optional team, seat overrides, delivery choice, optional tracker link
Task ── as now
Member (agent) ── fills seats
Thread correspondent ── who to reply to in an outside thread; nothing more
```

### Repository

A codebase the instance could work in: its local path, default branch, and
how its changes reach the outside. It owned:

- **Checks.** The full check (today's `Playbook.Check`), plus optional
  checks scoped to code areas.
- **Code areas.** Named path sets. Each could carry a scoped check, and
  optionally the owners its outside review process expected (as in
  CODEOWNERS); crew-assistant only read those, so a pull request could ask
  the right people. Projects declared the areas they touched.
- **Landing strategies on offer.** See "Delivery" below.
- **Release units.** What could be released, and how: today's
  `ReleasePolicy`. A small repository had one unit. A monorepo could have
  one per package or app, each tied to the areas whose changes it ships.
- **Workspace preparation.** Today's `Prepare` and `CheckInCopy`, plus the
  run recipe for the app (`RunRecipe`).
- **Sandbox needs.** Check loopback and similar settings, which belong to
  the code rather than to who works on it.

Within one instance, landings into a repository were serialised (one at a
time per target branch), so the instance's own teams didn't race each
other. Other people's landings were normal. When main moved, a clean catch-
up reran only the checks it needed, never a new review (CA-94).

### Team

A durable group of seats doing work in a known way:

- **Default seats** by role kind (researcher, designer, implementer,
  reviewer, QA), and a **PM**.
- **Policies:** the PM's loop guards (CA-102), autopilot modes (CA-98),
  engine budget and preferences, max rounds.
- **A project queue.** An ordered list of the team's projects, with a WIP
  limit (default 1). By default a team worked through its projects one at
  a time, the PM keeping the order. A limit of 2 or more allowed overlap.

A team didn't own repositories. Its projects declared where they worked.

### Project

A body of work with an outcome:

- **Brief and objective**, as now.
- **Scope:** the repositories and code areas it expected to touch. Scope
  drove which checks ran, which conflicts were watched, and which release
  units a landing affected.
- **Backlog:** its tasks and their dependencies, including dependencies on
  other projects' tasks.
- **Team, optional.** With a team, the project took the team's seats, PM
  and policies, and sat in the team's queue. Without one, it stood alone:
  exactly today's model, so existing projects migrated without change.
- **Seat overrides,** layered on the team's seats per role kind:
  - *add*: an extra seat, such as a designer only for this project, or a
    second, security-minded reviewer;
  - *replace*: this project's QA is a different member from the team's;
  - *exclude*: no researcher on this small project.

  The scheduler resolved the effective seats as the team's defaults with
  the project's overrides applied. A member busy anywhere was busy
  everywhere, as now.
- **Delivery choice** (see "Delivery").
- **Tracker link, optional** (see "Trackers").

### Members and correspondents

Members were agents (engine, model, effort, instructions, learnings,
skills) and filled seats, as now. Correspondents were people met in
outside threads: an issue comment, a pull request review, a mention.
crew-assistant recorded only who said what in which thread, and whether it
had been answered.

## Delivery: landing strategies between repositories and projects

`LandPolicy` already held the landing strategies (2026-10): a new local
branch, a fast-forward push to a target, or a GitHub pull request, with
`Open` (who decides a PR opens), `Approve` (who approves landing or
merging) and `Merge` (squash, merge or rebase). `ReleasePolicy` held
checked tagging. Both lived on the project's playbook.

They split across the new layers like this:

- **The repository offered strategies.** It said which ways of landing
  were allowed and how each was configured: the target branches, whether
  pull requests were required (a protected main), the merge methods
  allowed, whether a merge queue existed outside, and which strategy was
  the default. For a work monorepo that was typically "pull requests only,
  into main, squash, through the repository's merge queue". For a personal
  repository it might be "push to main".
- **The project chose within that offer.** It picked one of the offered
  strategies and its own approval settings: who opened the PR (PM, owner
  or implementer) and who approved landing (the owner, the PM, or nobody).
  A team could set a default choice for its projects.
- **The repository owned release units.** A landing recorded which release
  units its changed areas belonged to. The release proposal (PM-decided
  when, as now) was per unit.
- **Outside processes stayed outside.** With pull requests, crew-assistant
  opened the PR as the owner, answered review comments through the
  correspondence flow, and waited for the repository's own CI, required
  reviews and merge queue. It never bypassed them. Without pull requests,
  it landed directly, as today.

So "delivery" was a repository-level menu plus a project-level choice.
Today's `Playbook.Land` became the project's choice, and validation moved
to the repository's offer.

## Trackers

A tracker adapter (Linear, Jira, GitHub Issues, or none) attached to a team
or a project. Through it:

- **Intake:** issues could become projects or tasks, as the Linear intake
  did then.
- **Mirroring:** task status and key events (draft ready, landed, blocked
  on owner) were reflected back, as the owner.
- **Correspondence:** comments and mentions arrived as thread events.

Mapping was configured per adapter. A tracker team or board could map to a
crew-assistant team, a tracker project to a project, an issue to a task.
Nothing in the core model assumed any of them.

## Correspondence: replying to people outside

When someone outside wrote in a thread crew-assistant was part of (an
issue, a PR review, a mention):

1. The event was routed to the task or project owning that thread.
2. It was classified: a question, a change request, an approval or
   blocker, or FYI.
3. The team handled what it could: answering from the code, or revising
   the draft for a requested change. Anything needing the owner's
   judgement became a decision.
4. The reply went into the same thread, addressed to that person, as the
   owner.

A thread carried the correspondent's handle, the messages, and whether
each had been answered. There was no model of who these people were.

## Repository awareness across people

Other people, or their crew-assistant instances, worked in the same
repository. crew-assistant watched open pull requests that touched the
same code areas as its in-flight projects, and told the project's PM about
likely conflicts early. A draft whose areas were changed by someone else's
landing was caught up before its next check rather than at landing.

## What changes for the owner

- Creating work: say what you want. It becomes a project (with an optional
  team and scope) or tasks on an existing project. The PM orders it.
- Staffing: set up teams once; adjust single projects with overrides.
- Code: register repositories once, with their delivery offer, checks,
  areas and release units.
- Outside world: optional tracker adapters. Replies and status go out as
  you.

## Alternatives considered

- **Keep one project per repository and per stream** (the status quo,
  using several projects pointed at the same monorepo). Rejected: staffing,
  delivery and checks were duplicated per project, landings raced, and
  there was no sequential team queue.
- **Model all human contributors with roles and logins.** Rejected: one
  instance acts for one person. Several people run several instances and
  coordinate through the repository and tracker. That kept auth, data and
  responsibility simple.
- **Make the tracker the source of truth.** Rejected: not every owner uses
  one, trackers differ, and seats would have to be represented there.
- **Own the repository's merge queue.** Rejected: other people land too.
  crew-assistant serialised only its own landings and used the
  repository's real process otherwise.

## Phasing

1. **Repositories and teams as entities.** Introduce both. Move code
   settings (checks, prepare, run recipe, sandbox needs, release units) to
   repositories, and seats, PM and policies to teams. Projects gain an
   optional team, scope, and seat overrides. Migrate each existing project
   to one repository, one team and the same project, with identical
   behaviour.
2. **Delivery split.** The repository's offer of landing strategies plus
   the project's choice within it. Per-repository serialisation of this
   instance's landings. Clean catch-ups rerun checks only (CA-94).
   Release units per repository.
3. **Team queues.** An ordered project queue per team, a WIP limit, the
   PM ordering across the team's projects, and cross-project dependencies.
4. **Tracker adapters and correspondence.** An adapter interface (the
   Linear intake becomes the first adapter), team and project mapping,
   status mirroring as the owner, and the correspondence flow.
5. **Repository awareness.** Area conflict warnings from others' open pull
   requests, proactive catch-up, and code-area scoped checks.
