# Hidden acceptance scenarios, flow recordings and task budgets

Date: 2026-10-31. Version: `crew-assistant` v0.66.0 plus `7535dc7`. A
proposal, not yet built.

## Why

Three ideas from the 2026-10-31 software-factory research fit a small,
owner-in-the-loop system:

- **Hidden scenarios.** StrongDM's factory keeps end-to-end scenarios outside
  what the implementer sees, so the implementer can't write to the test and the
  judge's verdict means more. The owner should not have to write them: the team
  can, from the brief.
- **Flow recordings.** QA already keeps up to `core.MaxScreenshots` screenshots
  and a short evidence list with its verdict. A recording of each flow shows
  what worked, or what broke, without the owner rerunning it.
- **Budgets and a stuck-work watchdog.** Runaway cost and loops were the most
  reported failure in agent systems. A task should say what it has spent and
  stop asking for more when it passes a limit the owner set.

## Hidden scenarios

- **Who writes them.** The researcher drafts a few scenarios per task from the
  brief and the task's criteria while it researches: short end-to-end stories
  ("a new user signs up with an email already taken and sees …"). The PM may
  add or settle disagreements about them. The owner can read and edit them on
  the task but never has to.
- **Who sees them.** QA and reviewers judge each draft against them. The
  implementer never sees them: they are kept out of its prompt, notes and the
  task text it reads, and verdict findings name the scenario's outcome, not
  its script ("signing up with a taken email shows no error"), so a revision
  fixes the behaviour rather than the test.
- **What they gate.** A failed scenario is a finding like any other, with the
  same rounds and escalation. No new verdict kind.
- **Limits.** A handful per task, a few sentences each. They are judged, not
  executed; QA turns them into actions in the app as it would any criterion.

## Flow recordings (owner's ask, 2026-10-31)

- Where a project has an app and QA has a browser, the daemon keeps every
  screenshot QA takes while it uses the app (bounded, scaled down) and builds
  one animated GIF of the turn from them, in order. QA is asked to take a
  screenshot at each step of each flow it checks.
- Not the browser extension's own recorder: as of Claude in Chrome on
  2026-10-31, `gif_creator` exports only by downloading into the owner's
  Downloads folder or dropping onto a page, never in the tool result, so the
  daemon could not keep it reliably. Building from screenshots also works for
  any engine whose browser returns them.
- Recordings are verdict evidence beside screenshots: bounded in count and
  size, kept with the verdict, shown on the task's drafts in the dashboard,
  and attached to the pull request description when it lands.
- A recording that fails to capture never fails the verdict; QA says so in a
  finding.

## Budgets and the watchdog

- Each task already records team turns with provider token counts. Show the
  task's total on its board card and page.
- A per-project limit (turns or tokens per task) set by the owner. Past it, the
  task stops taking new turns and the owner gets one decision: continue with a
  new limit, accept as is, or stop. No limit by default.
- The autopilot's runaway-work detection (CA-100) is the watchdog: the same
  finding recurring across rounds, or rounds without a new revision, pauses
  the task rather than spending another round.

## Order

Recordings first (smallest, and asked for), then scenarios, then budgets with
CA-100.
