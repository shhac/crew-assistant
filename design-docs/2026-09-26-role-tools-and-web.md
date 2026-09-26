# Role tools and the researcher's web search

2026-09-26. Built for v0.21.0, on lib-agent-harness v0.5.0.

## Why

The owner asked two things. Every role should be able to see the project's other tasks, filtered to what bears on its own. The researcher should be able to search the web. Before this, only the researcher and the PM saw other tasks, as one-line lists in their prompts. Every role was fully offline. Roles had no tools at all, because the harness couldn't give a sandboxed session caller tools: sandboxed and restricted sessions excluded each other. The owner chose real tools over a read-only folder of task files.

## What was built

- **Two new lib-agent-harness sandbox options:**
  - `Sandbox.Web` lets Claude use WebSearch and WebFetch, and turns Codex's `web_search` to live. The shell's network stays closed.
  - `Sandbox.Tools` hosts the caller's tools beside the native sandboxed ones, through the same bridge the assistant's restricted session uses.
- **Every role turn gets tools, from a folder of its own that is removed when the turn ends:**
  - `list_tasks`, filtered by which tasks (unfinished, finished or all), what one is linked to, or words;
  - `read_task`, which gives the brief, plan, links and latest reviews;
  - `link_tasks` and `unlink_tasks` on the role's own task. Only the researcher may use depends_on or blocks; other roles may use only relates_to, and the PM uses its answer instead.
  
  The handler is built for the turn with its project, task and marker captured, so a model can't reach another project. A link is refused once the task has moved on without the turn, such as the owner stopping it.
- **The researcher searches the web**, and is asked to name the pages it relied on.
- **One tool bridge.** The `tool-bridge` command moved to `roles`, so the assistant and the team share it.

## Not built

- **A way to prove Codex offers its web tool.** Codex may offer it only when the login's provider supports web search. The harness couldn't confirm this without a real model call, so the first real research turn should be watched.
- **Shorter prompts.** The researcher's and the PM's prompts still list the other unfinished tasks. Trimming them to related tasks plus a count waits until the tools prove themselves.

