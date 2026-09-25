# The researcher, and a designer to hand work to

2026-09-25. Nothing to pin: code-internal, built on top of commit 735caff.

## Why

The planner's name undersold what it did, which was reading the repository and finding out what a task needed before anything was written; the owner renamed it the researcher. The owner also wanted a designer on some teams: a member the researcher or the implementer could hand a task to for design input, and get it back from, without any new authority.

## What was built

- **The researcher is the planner, renamed.** Role kind `researcher`, task status `researching`, board stage and column "Researching", the code template's seat "Researcher", and `set_team`'s `researcher_member`, which still takes `none` to leave the step out. What a researcher produces is still the task's plan: `Task.Plan` and its JSON kept their names.
- **Older state was read as the researcher where it was loaded,** beside the reading of single-kind members and seats. Members, project seats and task seats that held `planner` hold `researcher`; a template seat called "Planner" that held it was renamed (a member's seat keeps the member's name), with the plan's author renamed to match; task and retry statuses of `planning` became `researching`, as did task wakes that watched for it. Settings, assignments, learnings and tasks under way were left as they were, and the template's instructions for the seat were not reworded, so a seat's own instructions still separate from them.
- **The designer is a side role, like the researcher and the PM.** A member can hold `designer` alone or beside one kind of work. No template has a designer seat, so a team has one only when a member is seated as one (`designer_member`, or the Team tab's Designer seat), and a team without one behaves as before: no role is told about a designer, and nothing in a reply is read as a hand-off.
- **The hand-off.** While researching, the researcher can reply with `{"design": "question"}` instead of a plan. While implementing, the implementer can reply with a fenced `design` block; asking ends its turn without a draft, what it changed is set aside by the usual reset, and its session is kept so it resumes. Either way the task moves to a persisted `designing` status, with a design request on the task recording who asked, from which step and round, and the question. A seat that is its own designer is never offered a hand-off to itself.
- **The designer's turn** is read-only, fresh each time, in the workspace at the last revision, with the brief, the plan, earlier design input and the question. It gives input only, and says so to itself: no commits, delivery, landing or approval. Its input is recorded on the request in the same change that sends the task back to the step that asked, so a restart re-runs it only if it never finished, and never runs a step twice. The returning role, and later the reviewers, read the input in their prompts.
- **Escalation.** When a question needs more than design input, the designer replies with evidence, alternatives, consequences and a recommendation. That becomes a question decision for the owner, opened in the same change; the owner's answer joins the task's direction and the task returns to the step that asked, in the same round.
- **The bound.** Each step can hand a task over twice per round. The prompt says when that is used up, and an ask past it goes to the owner as a question, recorded as a request with no designer. A task can't pass back and forth on its own.
- **Around it.** Stopping a task with the designer leaves it stopped, and a late answer isn't recorded. A failed designer retries, then comes to the owner, and a retry asks it again. Messages reach the implementer as direction while the designer works; a designer-only seat can't be messaged. The PM and round limits were not changed.
- **Where it shows.** A task with the designer stays in the column of whoever asked, and its card and panel say "With <designer> for design input", with the designer's face. The panel lists each hand-off with the question and what came back.

## Not built

- A designer in the templates, a board column for design, or hand-offs started by the owner or the assistant.
- Any way for the designer to write, produce revisions, deliver, land or approve.
