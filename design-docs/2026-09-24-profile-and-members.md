# The assistant's profile, and team members

2026-09-24. Built.

## Why

The owner asked for two things:
- An identity setup where the assistant asks about them, suggests a name for itself and draws its own avatar, which then becomes the tab icon and appears in the conversation.
- A Team page of agents kept across projects, which a project copies when it needs someone in a role. The point is to build specialists that carry what they learn from project to project, with names and faces of their own.

## The assistant's avatar

- **An avatar is drawn, not chosen.** It used to be one of three preset shapes in two colours. Now `config.Avatar` can carry up to eight marks. Each mark is SVG path data on a 128-unit square, filled or stroked in one colour. Presets still render, so existing configs load unchanged.
- **Only validated values reach the SVG.** Path data is limited to path commands and numbers, must start with a move, and is at most 600 characters. Whitespace is collapsed first, because models put newlines in it. Colours must be `#RRGGBB` and stroke widths 0–16. `Avatar.SVG()` refuses anything `Validate` refuses. The dashboard shows the drawing only through an `<img>` data URL, never inline, so even a mistake could not run anything.
- **The interview** asks about the owner's work and taste first, then proposes a name, a personality and a drawing sized to read at 16px. A proposal that fails validation goes back to the model once, with the reason, before the owner sees an error. The retry isn't kept in the interview.
- **Where the drawing shows:** the state carries the assistant's avatar rendered on read (`assistant.avatar_svg`). The dashboard uses it as the favicon, in the sidebar, and beside the assistant in the chat. The assistant's own view drops the drawing, because it would otherwise ride along in every turn.

## Members

- **What a member is:** a named implementer, reviewer or QA, with an engine, optional model and effort, instructions of its own, an avatar and up to thirty learnings.
  - A new member gets a preset face derived from its name.
  - Names must be unique ignoring case and can't be a kind word ("reviewer").
  - In code the type is `Member`, not "agent". The old worker agents were removed in the rebuild, and this is a different thing.
- **A project's team copies a member into a role.**
  - `set_team` and the Team tab take a member for each slot.
  - The role takes the member's name, engine and model. Its instructions are the template's, then the member's, because the template's say how this kind of work is done in this medium.
  - The role records the member it came from (`role.member`).
  - Role names now have to differ by more than case, since messages find a role ignoring case.
  - Deleting a member leaves the copies in place.
- **Learnings are pinned when a task starts** (`NextTask`), newest first, within 4000 characters.
  - They are not read on every turn, because a role's instructions are part of its session's identity. A learning added mid-task would have made every writer start afresh.
  - One added mid-task reaches the member's next task, in any project.
- **Only the owner records learnings,** or the assistant with `record_learning` when the owner has said so. A learning becomes a standing instruction in every project the member joins. Letting roles write their own would let text from outside the team, such as a pull request comment, become that instruction. So agent-written learnings are not built.

## Not built

- Learnings proposed by the members themselves.
- The assistant drawing a member's avatar. Members get a preset face from their name; the model draws only its own.
- Showing members' avatars on board cards and verdict chips.
