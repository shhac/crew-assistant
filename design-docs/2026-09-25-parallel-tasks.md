# Parallel task work: how tasks ran, and a design

2026-09-25. As of commit `735caff`, with `lib-agent-harness` v0.3.4. Written against `9d1dfb2`; `735caff` changed only the team code and dashboard ("Project Config tab and seats-style Team tab"), and the findings below were checked against it. Part 1 of 5; design only, nothing built.

## Why

The owner wanted a project's team to work on several tasks at once. They asked for this to be driven by seats, not a fixed cap: work runs in parallel only as far as a seat with the needed role is free. This doc records how tasks ran as of the commit above. It corrects the premises of the original brief, proposes the design, and rewrites parts 2 to 5 so each builds only what was missing.

## How tasks ran

Function names are starting points, not a map.

- **One step at a time for the whole daemon.** The comment on `work.Loop` said: "One task step runs at a time, across every project." `Loop.Run` called `loopStep` until nothing progressed. `loopStep` tried these in order: `settleAnswers`, `answerMessage`, `managePM`, then `core.Service.NextTask`, then one step of the task it returned. That step was one of `planTask`, `write`, `review`, `decide` or `land`.
- **Where the one-active-task limit lived.** `core.Service.NextTask` (`internal/core/tasks.go`) returned the first task in the *whole snapshot* whose `Active()` was true: planning, writing, reviewing, deciding or landing. Only when no task was active did it start one. It took the first `queued` task, in queue order, with nothing unfinished in `waitsFor`. It pinned the playbook, copied the roles with their members' learnings (`withLearnings`), set round 1, and moved the task to `planning` if the team had a planner, otherwise to `writing`. So the limit was one active task **across all projects**, not per project. `waiting` (on the owner) and `awaiting` (idle, on CI or a pull request) were not `Active()`, so a task that waited let the next queued task start.
- **Plan.** `planTask` (`planning.go`) skipped a task that already had a plan. Otherwise it ran the planner read-only in a fresh session, recorded the plan, and raised any questions as a decision.
- **Implement.** `write` refused a team without exactly one implementer (`len(writers) != 1`). It then ran `prepareWorkspace` (`begin` on round 1, then `reset` to the last revision), `holdForUsage`, and `takeInLanded`, followed by the role. `recordDraft` called the medium's `snapshot`, appended the revision and moved the task to `reviewing`.
- **Review and QA.** `review` ran one checker per step, in `Task.Checkers()` order (reviewers before QA). It skipped any checker that had already judged this revision against the current brief (`Judged`). Each verdict recorded the revision and brief version. Then `decide` asked the owner questions, handled the round limit, started the next round, or asked for delivery.
- **Messages to reviewers and QA.** `answerMessage` (`messages.go`) ran a check for one task between other tasks' steps, in the same workspace.
- **Usage.** `holdForUsage` and `usageWait` (`usage.go`) compared each engine's subscription headroom with `Limits.RoleUsage`. Nothing limited how many model runs happened at once, because only one ever did. Chat had its own one-slot channel in `app.New` (`chat: make(chan struct{}, 1)`), separate from the loop.

### Sessions

- **The implementer's session was already per task.** `Task.WriterSession` held it. `write` set `spec.Resume` from it, and `WriterNext` could ask for a fresh start or, on Codex, a compaction (`SetWriterNext`). `recordDraft` stored `result.Session` back on the same task. No other task ever resumed it.
- **Planner, reviewers and QA started a fresh session every time.** `planTask` and `runChecker` passed no `Resume`.
- **Resume fallback.** `roles.open` resumed only when the recorded ref's engine matched. When the harness returned `ErrIncompatibleResume` or `ErrRejected`, it started a fresh session. Prompts were written to stand on their own either way.
- **Learnings were member-wide.** `withLearnings` copied each member's learnings when a task started. `prepareLearnings` and `recordLearned` (`work/learnings.go`) handled them per turn.

### Clones, branches and checks

- **One clone per project, shared by every task.** `gitrepo.Open` kept a single clone at `<project dir>/clone`, and `Loop.gitMediumFor` built the medium around it for every step. `gitMedium.begin` named the branch `crew-task/<task id>`. `Repo.Begin` fetched the owner's branch. `Repo.Reset` checked out the task's branch at a commit and cleaned the tree; its comment said "Several tasks share the clone".
- **Drafts were already fixed commits.** `Repo.Snapshot` committed each draft, and `Revision.Ref` recorded that commit.
- **Checks ran in the implementer's clone.** `gitMedium.checkDir` reset the shared clone to `r.Ref`, handed that directory to the checker, and reset it again afterwards. QA got write access there (`spec.Write = checker.Holds(QA)`).
- **The documents medium already separated checks.** It kept one workspace per project, with revisions stored per task. `localdocs.ReviewCopy` gave each check its own temporary copy.

### Catch-up and landing

- `takeInLanded` (before a round) and `catchUpRound` (`landing.go`, during landing) merged in what had landed since. A clean merge was recorded by the daemon (`recordCatchUp`, carrying reviewers' passes over with `carriedOver`).
- **A conflict went to the implementer** as a writing round (`conflictMerge`), **not to the owner.** Only more than `maxCatchUps` (4) catch-ups became an owner decision.

### Restart

- `Run`'s comment: every step was recorded before the next began, so the task's status said where to resume.
- `RetryAt`, `Failures` and `ResumeStatus` carried the retry state.
- A message left `working` still counted as `Open()` and was answered again.
- A turn cut off mid-run ran again after `reset` to the last revision. The implementer resumed its recorded session. A cut-off check ran afresh, because nothing had been recorded for it.
- On start, `Run` removed any learnings a stopped turn had left behind.

### Seats

- `core.Role` had `Kinds` and `Member` (`playbook.go`).
- `fillRole` (`work/team.go`) folded a member who was already on the team into their existing seat. `Loop.SetSeat`, which the Team tab used to fill one kind of role at a time (`PUT /api/projects/{id}/team/{kind}`), did the same. One member could therefore hold several kinds, but seats like "Claudius #2" could not exist.
- `Playbook.Unseat` took a kind of role from the one seat holding it, so a team had one seat per kind.
- Seat names were already kept distinct: `Playbook.FreeName` numbered a clashing name as "Name 2", and `NameSeats` renamed a template seat that clashed with a member's seat.
- `DeleteMember` gave a deleted member's seats back to the template seats (`vacate`). Tasks under way kept the roles they had pinned.
- The project's Config tab set the workspace (`Loop.SetWorkspace`: repository, branch prefix, prepare, signing) separately from the team.
- `2026-09-25-planning-and-seats.md` had already named seats "the lever for parallel work".

## Corrections to the brief's premises

| The brief assumed | As of `735caff` |
|---|---|
| One active task per project | One task **step** at a time for the whole daemon (`Loop`, `NextTask`); a task waiting on the owner or CI let the next one start |
| Member conversations might be shared across tasks | The implementer's session was per task (`WriterSession`), and every plan and check was fresh |
| Review snapshots needed to be made immutable | Each draft was already a fixed commit (`Revision.Ref`). The gaps were **where** checks ran (the implementer's clone, reset to that commit) and that **all tasks shared one clone** |
| Catch-up conflicts reached the owner | They went to the implementer; only a target that kept moving reached the owner |

## Gaps

1. Only one step ran at a time, so a free seat could not pick up another task.
2. Only one implementer seat per team (`write`, `SetWriterNext`, `Unseat`), and no way to add a member's template twice (`fillRole`, `SetSeat`).
3. A session belonged to the task's one implementer. There was no way for another seat to carry a task's transcript into round N+1.
4. The git medium had one clone per project, and checks ran inside it.
5. No bound on model runs at once, because none was needed.
6. Nothing recorded which seat had taken a step. The loop being single-threaded made that unnecessary.
7. The board and task views assumed a single active task.

## Design

### Seats are the lever

- A team can hold several seats filled from one member template: adding Claudius three times gives *Claudius*, *Claudius #2* and *Claudius #3*. Each seat copies the template's engine, model, effort, instructions and kinds, and carries its own name. Names come from `FreeName`, which as of `735caff` wrote "Claudius 2". Whether to switch to the owner's "Claudius #2" is a wording choice for part 4; it would also rename numbered template seats.
- Verdicts, messages and activity name the seat. Learnings belong to the member, so all three seats share them.
- **A seat holds one task step at a time.** A step starts only when a free seat holds the kind it needs. The planner, implementer, reviewer or QA of a task is whichever free seat of that kind takes the step.
- A seat holding planner and implementer can plan one task, then implement another. It never does both at once.
- A playbook with one seat of each kind cannot run two steps of the same kind at once.

**A per-project cap is still needed, but only as a setting on top of seats.** Seats bound steps, not started tasks. With one implementer seat and a separate reviewer, the implementer would start task B while A was in review, then C while B was in review. Every started task holds a branch that later has to catch up, and every one can bring the owner a decision. So:

- `max_active` per project counts exactly the tasks `Task.Active()` counts: planning, writing, reviewing, deciding and landing. `waiting` (on the owner) and `awaiting` (idle on CI or a pull request) don't count, as they didn't for `NextTask`.
- It defaults to the number of implementer seats. The owner can set it.
- The cap is checked only when a queued task would start. A queued task starts only below the cap, in queue order, skipping tasks with unfinished dependencies, as `NextTask` did. A `waiting` or `awaiting` task that becomes active again (an answer, a wake, pull-request feedback) never waits on the cap. As before, that can briefly leave a project with more than one active task.
- A seat prefers the next step of an already-started task (round N+1, a pending check, landing) over starting a new one, as `NextTask` preferred an active task, so work finishes before more starts.
- **With one implementer seat and `max_active` 1, each project starts and moves its tasks as it did.** One difference remains and is a deliberate change: a project no longer waits for another project's active task. `NextTask` had one active task across the whole daemon; the cap is per project. Keeping the old daemon-wide behaviour would need a daemon-wide cap of 1 as well; the design doesn't propose one.

### A bound on model runs across projects

- `Limits.RoleRuns` sets a per-engine count of role turns (plan, implement, check, PM) running at once across all projects. It defaults to 1 for Claude and 1 for Codex, and is checked where `holdForUsage` is.
- `holdForUsage` stays as the headroom check. A step that can't get a slot stays where it is and is not counted as a failure.
- Raising the bound is the owner's choice, shown next to usage.

**Interactive use goes first.** Chat, the composer and the CLI never take a slot and never wait on one; they keep their own path (`App.chat`). That alone does not stop role turns already running from using the same subscription and the same machine, so admission to a role turn also follows these rules:
- **No new role turn starts while interactive work is in flight.** The app tells the loop while a chat turn, a composer request or a CLI request is running or queued. The loop starts no new role turn on any engine until it is done, plus a short grace period so a quick follow-up doesn't queue behind a new role turn. Role turns already running continue; they are not cancelled, because a cancelled turn reruns from the last revision and wastes the work.
- **A usage reserve for the owner.** Role turns are held at a lower headroom threshold than interactive use (`Limits.RoleUsage` below a new `Limits.InteractiveReserve`). As a subscription nears its limit, role work stops first and chat keeps working.
- **Lower host priority.** Role processes and the builds they run start at background priority (`nice`, or background QoS on macOS), so the machine favours the daemon's own interactive work.

What this can guarantee is that interactive work never waits for a slot, never waits for a role turn to start, and has usage reserved for it. It cannot guarantee that interactive use is never slower. A role turn already running keeps its share of the engine provider's rate limits, and neither engine exposed request priority. This is escalation (e).

### Per-task threads

- **A thread's identity is task, role kind and member.** `Task.Threads` replaces `WriterSession`. It is keyed by `(kind, member ID)` within the task and stores the harness session ref, plus the engine and model it ran on and the seat that last used it. A seat not copied from a member uses its seat name in place of a member ID.
- **Seats from one template continue the task.** Claudius, Claudius #2 and Claudius #3 all carry the same `Role.Member`. Whichever of them takes round N+1 resumes that member's implementer thread, so it gets the transcript of rounds 1 to N whichever of those seats did them. `WriterNext` (fresh or compact) applies to that thread.
- **Resume only when compatible.** A seat resumes a thread only when all of these hold:
  - its member ID matches the thread's key;
  - its engine and model match what the thread recorded (`roles.open` checked only the engine);
  - the harness accepts the ref.
  Otherwise the seat starts fresh. It is given the task's record: the plan, revisions with summaries, verdicts, the owner's direction and messages. As before, prompts must stand on their own.
- **Never a transcript across members.** A seat from a different member template (Ada taking over a task Claudius started) never resumes Claudius's thread. It gets a thread of its own under its own key, starting from the record. Claudius's thread is kept, so a Claudius seat that later takes the task back resumes it.
- The thread runs in the task's own clone (below). That path is the same for every seat, so a native session resumed by another seat of the same template sees the same working directory.
- **Planner, reviewer and QA stay fresh for each run.** Their independence is the point, and their verdicts already live in the record. No thread is shared between tasks, and no transcript is shared between members. Learnings remain member-wide.

### Separate checkouts

- **Git gets a clone per task**, `<project dir>/tasks/<task id>/clone`, not a worktree, as AGENTS.md requires. It is made from the project's clone as a local clone (copy-on-write where the filesystem allows, as `Prepare` copying already did on APFS). The owner's branch is fetched into the project clone once, and each task clone fetches from there.
- The project clone remains the one place that fetches from the owner, holds every recorded revision, and lands.
- **Handing a draft to the project clone.** A commit made in a task clone exists only there, so a draft is not recorded until the project clone holds it under `refs/crew/tasks/<task id>/r<N>-a<attempt>`. The attempt is part of the name, so two attempts at the same round can never write the same ref, and a recorded ref is never moved. The handoff is the two-phase protocol under "Scheduling and restart".
- **Commits made in the project clone go the other way.** A clean catch-up merge made while landing (`catchUpRound` → `recordCatchUp`) is created in the project clone and recorded through the same protocol. The task clone fetches the task's refs and the landed target from the project clone before `reset` and `takeInLanded`, so its next round starts from the recorded revision.
- **Everything after recording reads the project clone.** Review and QA checkouts, `files`, `preview`, `alreadyLanded`, `behind` and landing all resolve `Revision.Ref` there, never in a task clone. So a task clone can be removed, or rebuilt from `refs/crew/tasks/<task id>/*`, at any time without losing a revision.
- **Cleanup.** The task clone is removed when the task finishes. Its `refs/crew/tasks/<task id>/*` refs are pruned from the project clone once the task has landed, been delivered or been stopped, and no open decision refers to its revisions.
- **Checks run in their own read-only checkout of `Revision.Ref`**, never in the implementer's clone. The same applies to reviewers, QA and `answerMessage`:
  - Each check gets its own checkout, cloned from the project clone at that commit, with its files made read-only. It is outside the role's writable area; the role reaches it through `Spec.Read`.
  - **QA's writes go to a separate scratch directory**, not the checkout. It is QA's `WorkDir`, and the build caches and `TMPDIR` that `Repo.Env` put in the clone's `.crew` folder point there instead. Reviewers get no writable directory except what the harness needs.
  - After the check, the daemon confirms the checkout is still at `Revision.Ref` with a clean tree. If it isn't, the verdict is discarded and the check runs again. Then the checkout and scratch directory are deleted.
  - A project whose checks can only run by writing into the source tree is escalation (d).
- The documents medium already did this (`ReviewCopy`); its shared workspace becomes per task in the same way.

### Scheduling and restart

- **Claims.** Before a turn starts, the loop records a claim in the same store update that picks the step. The claim records the task, the step (kind, round or revision, checker), the seat, a **token** and the start time. The token is `<task id>/<attempt>`, where `attempt` is a counter on the task that only ever increases. A seat with a claim is busy, and a task with a claim isn't offered that step again. Only one daemon runs against the state (`serve` holds a file lock), so claims never compete across processes.
- **Fenced recording.** A turn carries its token to the end. Every store update that records its outcome is a compare-and-set inside `store.update`: the task's current claim must exist and carry the same token. If it does, the outcome is recorded and the claim cleared in that same update. If it doesn't, the update records nothing: no draft, verdict, session ref, learning or wake. The dropped result goes to diagnostics only. A verdict, a plan or a PM turn changes only the store, so this one fenced update is all it needs. A draft also changes git, so it uses the protocol below.
- **Two-phase draft handoff.** The rule is that a durable record of intent, fenced by the token, is written before every change outside the store, and the outcome is recorded only after that change is verified:
  1. **Snapshot.** The turn commits its draft `C` in the task clone. Nothing durable points at `C` yet; the task clone is scratch.
  2. **Prepare (fenced).** One compare-and-set on the token records `claim.pending`: revision number N, commit `C`, the ref name `r<N>-a<attempt>`, and the turn's whole outcome (reply, summary, session ref, learned and wake blocks, how much direction it saw). If the token no longer matches, the turn stops here and has changed nothing outside its own task clone. Git never moves before this record exists.
  3. **Publish.** The daemon fetches `C` into the project clone and creates the ref as a create-only update (git's `update-ref` with an all-zero old value). If the ref already exists and points at `C`, this step already happened, so it counts as done. If it points anywhere else, something is wrong, and the step fails without recording. It then reads the ref back and checks it resolves to `C`.
  4. **Commit (fenced).** One compare-and-set on the token, and on `claim.pending` still naming `C`, appends revision N from the pending outcome with `Revision.Ref = C`. The same update clears the pending intent and the claim. The update fails if revision N already exists.
  A stale turn can reach step 3 only if its claim was revoked between steps 2 and 3. Its ref carries its own attempt number, so it can't disturb a live attempt. Step 4 then fails for it, and cleanup deletes the ref.
- **Recovery transition.** On start, before scheduling anything, the loop:
  1. For each leftover claim, terminates any role process group recorded on it that is still alive, so a turn orphaned by the old daemon can't keep writing into a workspace.
  2. Resolves each leftover `claim.pending`, before any token is invalidated. Only one daemon runs, and nothing else is scheduled yet, so recovery holds the fence itself:
     - **The ref exists and resolves to the pending `C`:** roll forward. Run step 4 from the stored outcome. The turn is not run again, and its work is not lost.
     - **The ref is missing, but `C` is still in the task clone:** redo step 3, then roll forward.
     - **The ref is missing and `C` is gone** (the task clone was lost): roll back. Drop the pending intent; the round runs again.
     - **The ref points somewhere other than `C`:** roll back and record it in diagnostics. This cannot happen under create-only refs named by attempt, so it points to outside interference.
  3. In one store update, clears every remaining claim and increments each task's `attempt`, so every old token is dead. It doesn't count a failure, since a restart isn't the role's fault. The task keeps its status, which names the step it was on, or the next one after a roll-forward.
  4. Resets each affected task clone to its last recorded revision. Deletes leftover check checkouts and scratch directories. Deletes every `refs/crew/tasks/*` ref that is neither a recorded `Revision.Ref` nor named by a live pending intent.
  A step that was not rolled forward is then offered as usual and gets a fresh claim with a new token. The implementer resumes its thread; a check starts fresh, as before.
- **Landing uses the same rule.** Before a push or merge, the daemon records the landing intent (the commit and where it goes) in a fenced update. On restart it reconciles with `alreadyLanded`, as landing already did, rather than retrying blindly.
- **The same fence in a running daemon.** A claim is reissued without a restart only when its turn has ended: stopped by the owner (`StopTask`), cancelled, or failed and scheduled for retry through `RetryAt`. Each reissue increments `attempt` first, so a cancelled turn that returns late is fenced exactly like one from before a restart.
- **No duplicates, nothing lost.**
  - A crash at any point leaves one of these states, and recovery resolves each one:
    - **Before prepare:** nothing durable happened; the round runs again.
    - **After prepare, before publish:** roll forward, or roll back if `C` is gone.
    - **After publish, before commit:** roll forward.
    - **After commit:** done; recovery has nothing to resolve.
  - Recording also stays idempotent: a draft only if revision N doesn't exist, a verdict only if the checker hasn't judged that revision (`Judged`).
  - A step is cleared only in the update that records its outcome, so a step whose turn is lost runs again rather than vanishing.
  - If the task clone is missing, it is rebuilt from the project clone at the last recorded revision.
- Each step still runs as one goroutine per claim under the loop's context. `settleAnswers`, `managePM` and starting tasks stay on the single scheduling pass, so ordering decisions never race.

### Landing one change at a time

- Landing is serialized per project: one task holds the project's landing claim, and lands the `Revision.Ref` already handed to the project clone. Landing never reads a task clone.
- After a landing, other active tasks catch up before their next check, as `takeInLanded` already does. Clean merges are recorded by the daemon as before.
- **A conflict goes to the owner as a decision (a change from how it worked).** The choices:
  - "Let the implementer resolve it" (recommended);
  - "Wait for it to land first", naming the task it conflicts with;
  - "Stop".
  Escalation (b) asks whether to keep this default.

## Follow-on work, rewritten to build only what was missing

1. **Part 2: per-task member threads.**
   - Replace `WriterSession` with `Task.Threads` keyed by kind and member, recording engine and model. Resume only on a matching member, engine and model; never across members. No compatibility layer (`2026-09-clean-break-state.md`): a task in flight loses its old session, and its next round starts fresh from the record.
   - Build the fresh-start prompt from the record.
   - First, verify escalation (a) with a fake CLI test and one real run.
   - Checks stay fresh; nothing else changes.
2. **Part 3: separate checkouts.**
   - A clone per task for git, plus per-task workspaces for documents.
   - The two-phase draft handoff: record a pending intent, publish `refs/crew/tasks/<task id>/r<N>-a<attempt>` create-only, verify it, then record the revision. Add startup roll-forward and roll-back for every partial state. Record the daemon's catch-up merges the same way, fetch them back into the task clone, and prune refs once the task is finished. Steps still run one at a time in this part, so the token check on prepare and commit arrives with claims in part 4.
   - Crash tests at each boundary (before prepare, after prepare, after publish, after commit), each ending with exactly one revision N or a rerun round, and no stray refs.
   - A read-only checkout of `Revision.Ref` for every check (reviewers, QA and `answerMessage`), cloned from the project clone. QA also gets a separate scratch directory holding its caches and temporary files. The checkout is verified unchanged before the verdict counts.
   - Tests, with local repositories only:
     - a draft is recorded only once the project clone holds the same commit;
     - a check that changes its checkout has its verdict discarded;
     - checks and landing still work after the task clone is deleted;
     - a restart between snapshot and record neither loses nor duplicates a revision.
   - Drafts are already immutable commits; only where they live changes.
3. **Part 4: seats and bounded pipelined scheduling.**
   - Several seats per member template, built on what `735caff` added:
     - an "add another seat" action next to `SetSeat`, which stays for giving a kind to one seat;
     - `fillRole` and `Unseat` no longer assuming one seat per kind;
     - `vacate` removing every seat a deleted member held;
     - the seats-style Team tab showing each seat.
   - `write` taking any free implementer seat.
   - Seat claims with tokens, fenced recording and the recovery transition. First check whether the harness exposes a turn's process group. If it doesn't, killing orphaned turns needs a harness change, and until then recovery waits for them.
   - `max_active` per project, set on the project's Config tab next to the workspace. `Limits.RoleRuns` per engine, and interactive priority:
     - no new role turn while chat, the composer or the CLI is busy;
     - `Limits.InteractiveReserve`;
     - background priority for role processes.
   - Parallel steps under the loop, landing serialized per project, and catch-up conflicts as an owner decision.
   - Tests with fake CLIs:
     - a stale token records nothing;
     - a restart mid-turn neither loses nor repeats a step;
     - no role turn starts while a chat turn runs;
     - one seat and `max_active` 1 start and move a project's tasks exactly as before, including a waiting or awaiting task not holding the cap;
     - a stale turn revoked between prepare and publish leaves no revision, and its ref is cleaned up.
4. **Part 5: board and task views for several active tasks.**
   - Each seat's current task and step, several active tasks per project, and tasks queued behind the cap or behind busy seats.
   - The per-engine run bound next to usage.

Stacking dependencies with g2g, the researcher rename, the designer role, the usage sidebar and the conversation panel were separate tasks. They touch this design only where noted.

## Escalations

**(a) Can a native session be resumed by another seat, or after the working directory changes?**
- *Evidence:* `roles.open` resumed from a stored `session.Ref` and fell back to fresh on `ErrIncompatibleResume` or `ErrRejected`. Claude Code keyed its stored sessions by working directory. Whether the harness's `Ref` recorded the directory, and whether it refused a different one, was **not verified**: reading the harness source in the module cache was blocked for this task.
- *Alternatives:*
  - keep the task's clone path fixed so every seat resumes from the same directory (proposed);
  - always start fresh from the record;
  - pin a task's rounds to the seat that started it.
- *Consequences:* if resume works across seats, round N+1 gets the full transcript. If it doesn't, the fallback still carries the record, but not the implementer's own reasoning. Pinning to a seat would defeat the point of several seats.
- *Recommendation:* the fixed per-task path with the record as fallback. Part 2 starts by testing this. If resume turns out to be refused, part 2 reports it and ships the record-based fresh start.

**(b) Who resolves catch-up conflicts?**
- *Evidence:* `catchUpRound` and `takeInLanded` gave conflicts to the implementer, and the brief asked for the owner.
- *Alternatives:* the implementer, as it was; always the owner; the owner, with the implementer as the recommended choice.
- *Consequences:* always asking the owner adds the mental load the owner objected to in v1. With parallel tasks, conflicts between sibling tasks become more common.
- *Recommendation:* the owner decision with "Let the implementer resolve it" recommended. Alternatively, keep today's behaviour and raise a decision only when a conflict touches another active task's files. The owner should choose.

**(c) Disk use and first-clone time of a clone per task.**
- *Evidence:* `gitrepo.Open` cloned with `--no-hardlinks` and copied `Prepare` paths with copy-on-write on APFS.
- *Consequences:* each task clone costs roughly a working tree plus the objects, and time to first clone. Clones are removed when a task finishes, so the peak is `max_active` clones per project.
- *Recommendation:* clone locally from the project clone, with copy-on-write where available. Measure on a real project in part 3 before adding any sharing of objects.

**(d) Checks that write into the source tree.**
- *Evidence:* until now, checks ran in the implementer's writable clone, with caches in its `.crew` folder (`Repo.Env`). Some build tools write into the tree whatever their environment says, for example generated files or a local `node_modules` cache.
- *Alternatives:*
  - read-only checkout plus scratch (proposed), with such a check failing;
  - QA copies the checkout into its scratch directory and runs there, while the read-only checkout stays the reviewed snapshot;
  - a writable QA checkout (rejected by the brief).
- *Consequences:* the first can fail QA for reasons unrelated to the change. The second keeps the snapshot immutable but costs a copy per QA run (cheap with copy-on-write).
- *Recommendation:* read-only plus scratch by default. Where a project's checks need to write into the tree, use the copy-in-scratch route as a playbook setting. Either way, the verdict is tied to `Revision.Ref`, and the snapshot is verified unchanged.

**(e) "Never slows the chat, composer or CLI" can't be fully guaranteed.**
- *Evidence:* role turns ran as native Claude Code and Codex sessions on the owner's subscription. Neither engine offered request priority, so a turn already running shares the provider's rate limits and the machine with interactive use.
- *Alternatives:*
  - admission control, a usage reserve and background priority (proposed);
  - also cancel running role turns when interactive work starts;
  - run no role turn at all while the owner is active.
- *Consequences:* the first keeps interactive work from ever waiting on role work, but a turn already running can still add some latency. Cancelling wastes the turn, because it reruns from the last revision, and could keep work from finishing during a long chat. The third stalls all project work whenever the owner is in the dashboard.
- *Recommendation:* the first. Measure chat latency with one and two role turns running during part 4. If the difference is noticeable, lower `Limits.RoleRuns` rather than cancelling turns.

**(f) Criterion 2 (tests that fail without the change) cannot apply to this part.** It changes no behaviour, only this document. `make check` was run and passed. Each follow-on part carries its own tests.
