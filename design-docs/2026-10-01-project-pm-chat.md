# Project PM chat — 2026-10-01

Version: nothing to pin; code-internal. Built against CA-45 design 1.

The owner gained a retained conversation with each project's PM through the last project tab, **PM chat**. The PM's avatar, name and “PM · {project}” scope distinguished it from the unchanged assistant pane. Both used the shared growing composer and larger editor; PM messages accepted text only. Drafts stayed per project for the page session.

Messages were stored before dispatch and answered oldest first when the PM's existing seat was free. Owner-initiated replies ran during global or project pause, while ordinary work stayed paused. The PM received the brief, ordered tasks, team and recent conversation, with its existing task-management tools plus ordering. Replies carried grouped, linked “Changes made” receipts, including changes made before a failed reply. Later PM looks received recent exchanges to retain agreed priorities.

Client IDs made acceptance idempotent. Five pending messages per project were allowed, with a retained log capped at 200 messages. A restart failed interrupted turns rather than replaying actions; held claims kept their messages working until release. Retry was an explicit owner choice and preserved the separate draft. No PM meant no chat tab; Team offered a callout, and the retained log reappeared when a PM returned.
