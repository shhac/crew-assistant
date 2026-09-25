# Two-stage stop

2026-09-25. Built for v0.21.0.

## Why

Stopping the daemon cut short whatever was running. That could be a role's turn part way through, a chat reply the owner was waiting for, or a push. A restart resumed the task step, but the turn was wasted and an interrupted chat reply was lost. The owner asked for crew-code-review's behaviour: the first interrupt stops new work being taken and shuts down once the work in progress is done, and the second terminates as before.

## What was built

- **Two contexts, `lifecycle.Stop{Graceful, Force}`.** Work is taken only while Graceful lasts, and runs on Force. Before taking work, everything asks `Stopping()` outright. It doesn't rely on a `select`, which picks a ready case at random and would still start new work about half the time. The first SIGINT or SIGTERM ends Graceful and the second ends Force; SIGTERM matters because it is what launchd and systemd send.
- **What finishes and what waits.**
  - The work loop finishes its step and the chat queue finishes its reply.
  - A wake fired together with its task, and a decision notice claimed together with its send, complete as a pair.
  - Slack answers what it had already claimed but claims nothing more, so Slack delivers later messages to the next run.
  - Messages queued behind the reply in progress stay queued, and whoever was waiting for them hears so. Slack replies that the message is queued and will be answered in the dashboard.
  - Drawings stop at once, since a picture isn't worth holding up a stop for.
- **The dashboard stays up** while the work drains and shows *Stopping*. Work somebody asks for that would start a model or a CLI (an identity interview, a suggestion, a drawing, profile discovery) is refused with a 503.
- **A forced stop** cancels the work and waits at most five seconds before exiting.
- **Subprocesses in their own session.** A terminal's Ctrl-C reaches its whole foreground process group, so a git push or gh merge the daemon started died with it before the daemon could decide anything. The daemon's own subprocesses now start in a session of their own, as the model CLIs already did through the harness. Cancelling one kills its whole group, and one that would prompt fails rather than waiting on a terminal it can't read.

## Not built

- **A limit on how long a stop may drain.** A 20-minute chat reply holds the exit for up to 20 minutes, and a service manager may kill the daemon first. The README says how to give launchd and systemd room.
- **A drain test of the whole daemon with work in progress.** The work loop, the chat queue and Slack are each tested on their own; a whole daemon would need a model.
