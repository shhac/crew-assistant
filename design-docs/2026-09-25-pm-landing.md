# The PM decides what lands (push only)

Date: 2026-09-25. Nothing to pin: code-internal.

This note adds to `2026-09-25-pm-role.md`, which said the PM doesn't land. With this change, a project could let its PM decide whether a signed-off change landed.

## What was built

- **Opt-in, per project.** The landing policy's `approve` gained `pm`, next to `before` (the default) and `none`. Only the owner or the assistant set it, on the Config tab's Landing card or through `set_landing`. Existing projects kept their setting and behaved as before.
- **Push only.** `pm` was accepted only with `via: push` (a fast-forward onto the target). A pull request merges as GitHub's reviews say, and a new branch lands nothing, so neither offered it. Without a PM seat on the team, the owner was asked as before.
- **Signed off first.** The PM was asked only when every reviewer and QA had passed the latest draft against the current brief, no decision about the task was open, no owner direction was pending, and every task it depended on had landed (a stopped one hadn't). Otherwise the owner got the usual approval, saying why the PM couldn't land it. The same check ran again when the PM's "land" was recorded.
- **One read-only turn.** The PM answered `{"land": bool, "how": "squash" | "fast-forward", "reason": "…"}`, with one retry. An unreadable reply or an engine error brought the owner the usual approval. The decision, its reason and what followed were written in one store change, as `land_decision` on the task and a `task.pm_landing` activity entry ("The PM approved … to land" or "The PM held …"). "The PM landed … on main" was recorded only once the change was actually there, so a landing that failed and went back to the implementer never read as landed. Because of that, a restart never asked again, and the existing already-landed check stopped a second push.
- **Land** went through the owner's landing path unchanged: catch up, QA on the merged result, fast-forward only, one step at a time.
- **Squash or keep the commits.** Before this, every push landing already squashed: one new commit with the approved tree on top of the target, which the target fast-forwarded to. The PM chose per change between that (`squash`, the default when it named nothing) and fast-forwarding the target onto the task's own commits as they were (`fast-forward`, through the existing `PushFastForward`). Neither was ever forced. The landing policy's method was left alone, so owner-approved and approve-none landings kept squashing.
- **Clean-up.** After a change the PM approved had landed, by either way, the task's branch was deleted from the project's clone. That included a restart that found the change already there. The delete happened only while the branch was still at the landed commit. The owner's repository never held the branch, so nothing there was touched. A failed clean-up was logged and never undid the landing. **Hold** opened the owner's usual approval, titled as the PM's hold, so they could land it themselves. A later landing in the project caught it up, which brought it back to the PM.
- **Failures.** A catch-up conflict, or checks failing on the merged result, sent the task back to the implementer and was counted in `landing_failures`. After two such failures, or one at the round limit, the owner got the approval instead, with the failures listed. Any answer from the owner reset the count.
- **The owner kept control.** A signed-off change still waiting on the PM (derived as `pm_deciding`) offered the owner "Land on main", which approved it as theirs through `land_task`. A PM answer arriving after that was refused. Switching the project back to "Ask me first" applied at once, to tasks already under way too, and dropped any PM approval that hadn't landed yet. Stop, the owner's own approval and messages to the team worked as before.

## Not built

- Landing through `g2g` where it is installed, which the owner mentioned for later. The git commands stayed the daemon's own.
- Cleaning up branches after landings the owner approved. Only the PM's landings cleaned up, so owner-approved landing was unchanged.
