# A team member's panel on a request

Written 2026-09-28, against lib-agent-harness v0.9.0. Nothing else to pin: code-internal.

## What changed

From a request's view, the team was shown as one entry per seat instead of a "Message the team" box with a "To" choice. One click on an entry opened that member's panel over the request: what the member was asked, what it wrote and what it ran on this request, with the messages sent to it, their replies and the box to send another. The panel showed and sent messages; it offered no other action. The open member lived in the address (`…/requests/<task>/team/<seat>`), so a poll or a reload kept it open, and an unsent message was kept in the browser tab's session storage. The request stayed mounted underneath, so closing the panel came back to it as it was left. Sending a message went through the same endpoint as before, with the same hints and effects.

## What is kept now, superseding part of 2026-09-26

The 2026-09-26 note said running turns were kept on show and never stored. The counts shown on a running turn still weren't stored. What a turn did on a task was: before this there was nothing the panel could show, since a turn kept only counts in memory and a thread kept only a session reference for resuming.

- The roles runner told its observer the prompt the session was actually given (the fresh prompt when a conversation couldn't be resumed), and every event a finished turn reported reached the observer before the turn ended.
- Each turn on a task kept steps: its prompt, its replies (a streaming reply was written at most about once a second while it grew, and in full once another step began or the turn ended), and each tool with its input, output, status and exit code as lib-agent-harness v0.9.0 reported them. A tool the turn never saw finish was marked as stopped with its outcome unknown. A turn on no task, such as the PM ordering the list, kept nothing.
- Steps lived in their own table beside the state document, not in it, so the state every page polls didn't grow with them.
- Bounds: a step's text was cut to 16 KiB, a tool's input to 4 KiB and its output to 8 KiB, marked as cut; the harness had already capped each payload at 64 KiB. A task kept its newest 600 steps and at most 2 MiB of them across all its seats, the oldest going first.

## Visibility

Prompts and tool payloads can be private. They were served only by one signed-in dashboard endpoint for one seat on one task, and went nowhere else: no prompt, log, diagnostic or other role was given them, and they were not part of the polled state. The harness redacted only its own tool-channel credential; anything else a role read into its output was kept as the role saw it, for the owner alone.
