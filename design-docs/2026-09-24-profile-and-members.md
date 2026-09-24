# The assistant's profile, and team members

2026-09-24. Built.

## Why

The owner asked for two things:
- An identity setup where the assistant asks about them, suggests a name for itself and draws its own avatar, which then becomes the tab icon and appears in the conversation.
- A Team page of agents kept across projects, which a project copies when it needs someone in a role. The point is to build specialists that carry what they learn from project to project, with names and faces of their own.

## The assistant's avatar

- **An avatar is drawn, not chosen.** It used to be one of three preset shapes in two colours. Now `config.Avatar` can carry up to eight marks, which since the revision below are the stand-in shown until Codex has drawn the face. Each mark is SVG path data on a 128-unit square, filled or stroked in one colour. Presets still render, so existing configs load unchanged.
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
- **Learnings are pinned when a task starts** (`NextTask`), not read on every turn, because what a role is told at the start is part of its session's identity: a learning added mid-task would have made every writer start afresh. One added mid-task reaches the member's next task, in any project. How they are shown to the role changed the same day; see below.
- **At first only the owner recorded learnings,** or the assistant on the owner's word, for fear that text from outside the team could become a standing instruction. The owner chose to let members record their own under a standing rule instead; see below for how that risk is kept down.

## Revised the same day: learnings like skills, members learning, faces drawn by Codex

After using it, the owner changed three things.

- **Members record their own learnings.** The owner's standing rule is that a shared learning is never about one project and any data in it is made up.
  - A member's reply, or a checker's after its verdict, may end with a ```learned block holding at most two, each with a `when` and a `learning`.
  - A learning is dropped if it names the project's title, ids, branch, folders or their names, the repository or the owner's home, or holds an address, a link or something shaped like a key. The check ignores case.
  - Nothing learned while answering a pull request is kept, because that turn read what people outside the team wrote.
  - The same situation isn't learned twice.
  - When a member is full, the oldest learning it taught itself makes room; the owner's are never pushed out.
  - Each learning is noted in the project's activity, not the inbox.
- **Learnings are disclosed like skills.** Each has a `when`. A task pins a copy of its members' learnings when it starts.
  - Each role begins with an index only: when each learning applies, and a file under the state directory to read it from.
  - The index says these are notes that never override the brief or the owner.
  - The files are never in the workspace, and they exist only while a turn that can read them runs. A stopped or landed task drops its copy.
  - A `when` is one line: the index goes into every later task's instructions, so a line break in one could forge another entry. A learning saved without one gets its heading from its text, once, and the dashboard shows the same heading the member reads.
  - Because the index comes from the pinned copy, every turn is told the same thing and the writer's session resumes.
- **Faces are drawn by Codex**, the assistant's and every member's, in one style: a cute 2D chibi manga face, head only, that still reads at 20 pixels.
  - The painter runs one read-only Codex turn in its own Codex home, and takes the picture from where Codex saves what it generates, keyed by that turn's session.
  - The daemon decodes only PNGs from 256 to 2048 pixels a side, crops them square and redraws them at 512, 128 and 48 pixels, named by a hash.
  - Drawing takes a minute or more (44 s to 4 min in the first runs). It runs in the background, one at a time, within the owner's Codex usage limit, and stops with the daemon. Its status lives in memory, so a restart forgets an unfinished drawing.
  - A new member is drawn when created. The assistant is drawn once the owner applies an identity; the interview now asks how it looks, and its vector sketch stands in until then. Either can be redrawn with a new look, and the assistant can draw a member with `draw_member`.
  - Tests can never reach Codex.
- **Faces show everywhere:** in the sidebar, the chat and the tab icon, on board cards (who is at work), on verdict chips and in the team thread.
