import { describe, expect, it } from "vitest";
import {
  prWords,
  activeCap,
  capLine,
  stageLimit,
  decisionFor,
  decisionKind,
  isOpenMessage,
  latestFirst,
  leadRequest,
  projectGroup,
  projectKind,
  projectTasks,
  requestStep,
  needsYou,
  orderLine,
  requestTone,
  taskPlaybook,
  underWay,
} from "./stages";
import type { Decision, Playbook, Project, Stage, Task } from "./api";

const writing: Playbook = {
  template: "draft",
  medium: "documents",
  roles: [
    { name: "Writer", kinds: ["implementer"], engine: "claude" },
    { name: "Reviewer", kinds: ["reviewer"], engine: "codex" },
  ],
  max_rounds: 3,
  deliver: "owner",
};
const code = (
  land: Playbook["land"] = { via: "push", target: "main" },
): Playbook => ({
  ...writing,
  template: "code",
  medium: "git",
  roles: [
    { name: "Implementer", kinds: ["implementer"], engine: "claude" },
    { name: "Reviewer", kinds: ["reviewer"], engine: "codex" },
    { name: "QA", kinds: ["qa"], engine: "claude" },
  ],
  check: "make check",
  branch_prefix: "crew/",
  land,
});
const project = (playbook?: Playbook): Project => ({
  id: "p",
  title: "P",
  status: "active",
  brief: { version: 1, goal: "g", criteria: [] },
  playbook,
});
const task = (t: Partial<Task>): Task => ({
  id: "t",
  project_id: "p",
  objective: "o",
  criteria: [],
  status: "queued",
  stage: "todo",
  round: 1,
  revisions: [],
  verdicts: [],
  ...t,
});
const decision = (kind: string): Decision => ({
  id: "d",
  kind,
  title: "",
  context: "",
  recommendation: "",
  choices: [],
  status: "open",
});

describe("the board", () => {
  it("lists finished work the most recently finished first", () => {
    const ids = (tasks: Task[]) => latestFirst(tasks).map((t) => t.id);
    expect(
      ids([
        task({ id: "a", updated_at: "2026-09-20T10:00:00Z" }),
        task({ id: "b", updated_at: "2026-09-22T10:00:00.5Z" }),
        task({ id: "c", updated_at: "2026-09-22T10:00:00Z" }),
      ]),
    ).toEqual(["b", "c", "a"]);
    // Ties fall back to when it was asked for, then the server's order reversed.
    expect(
      ids([
        task({ id: "a", created_at: "2026-09-19T10:00:00Z" }),
        task({ id: "b", created_at: "2026-09-18T10:00:00Z" }),
        task({ id: "c" }),
        task({ id: "d" }),
      ]),
    ).toEqual(["a", "b", "d", "c"]);
  });
  it("says what a request is doing in a few words", () => {
    const pb = code();
    expect(requestStep(task({ status: "queued" }))).toBe("Waiting to start");
    expect(
      requestStep(
        task({ status: "writing", round: 2, roles: pb.roles, playbook: pb }),
      ),
    ).toBe("Round 2 · Implementer working");
    expect(
      requestStep(
        task({
          status: "reviewing",
          stage: "reviewing",
          roles: pb.roles,
          playbook: pb,
          revisions: [{ n: 1, brief_version: 1, files: [] }],
        }),
      ),
    ).toBe("Reviewer reviewing");
    expect(
      requestStep(
        task({
          status: "reviewing",
          stage: "reviewing",
          checking: "Rune",
          roles: pb.roles,
          playbook: pb,
        }),
      ),
    ).toBe("Rune reviewing");
    expect(
      requestStep(
        task({
          status: "reviewing",
          stage: "qa",
          roles: pb.roles,
          playbook: pb,
        }),
      ),
    ).toBe("QA running make check");
    expect(requestStep(task({ status: "waiting" }), decision("delivery"))).toBe(
      "Waiting for your approval",
    );
    expect(requestStep(task({ status: "waiting" }), decision("update"))).toBe(
      "Update waiting for you",
    );
    expect(requestStep(task({ status: "waiting" }), decision("question"))).toBe(
      "Question for you",
    );
    expect(
      requestStep(
        task({ status: "waiting", round: 3 }),
        decision("escalation"),
      ),
    ).toBe("Still has review points after 3 rounds");
    expect(requestStep(task({ status: "waiting" }), decision("failure"))).toBe(
      "Stuck until you decide",
    );
    expect(requestStep(task({ status: "landing", playbook: pb }))).toBe(
      "Landing on main",
    );
    expect(
      requestStep(
        task({ status: "awaiting", proposal: { branch: "b", number: 7 } }),
      ),
    ).toBe("Pull request #7: waiting on checks and reviews");
    expect(requestStep(task({ status: "landed", delivered_to: "main" }))).toBe(
      "Landed on main",
    );
    expect(
      requestStep(
        task({ status: "delivered", delivered_to: "crew/x", playbook: pb }),
      ),
    ).toBe("On branch crew/x");
    expect(requestStep(task({ status: "delivered", playbook: writing }))).toBe(
      "Approved",
    );
    const later = new Date(Date.now() + 60_000).toISOString();
    expect(
      requestStep(
        task({
          status: "reviewing",
          retry_at: later,
          detail: "Waiting for codex subscription headroom",
        }),
      ),
    ).toBe("Waiting for codex subscription headroom");
  });
  it("says a request in triage is with the PM, or waits on the owner's answer", () => {
    const triage = (t: Partial<Task> = {}) =>
      task({ status: "triage", stage: "triage", ...t });
    expect(requestStep(triage({ checking: "Pim" }))).toBe(
      "With Pim for triage",
    );
    expect(requestStep(triage())).toBe("With the PM for triage");
    expect(
      requestStep(
        triage({ checking: "Pim", decision_id: "d" }),
        decision("pm-question"),
      ),
    ).toBe("Waiting on your answer for Pim");
    expect(
      requestStep(
        triage({ checking: "Pim", decision_id: "d", answered: true }),
      ),
    ).toBe("Your answer is in; Pim looks again next");
    expect(requestTone(triage())).toBe("wait");
    expect(underWay(triage())).toBe(false);
    expect(needsYou(triage())).toBe(false);
  });
  it("colours a request by what it needs", () => {
    expect(requestTone(task({ status: "waiting" }))).toBe("needs");
    // An answered task waits only for the loop, not for the owner.
    expect(requestTone(task({ status: "waiting", answered: true }))).toBe(
      "wait",
    );
    expect(needsYou(task({ status: "waiting", answered: true }))).toBe(false);
    expect(needsYou(task({ status: "waiting" }))).toBe(true);
    expect(
      requestStep(
        task({ status: "waiting", answered: true }),
        decision("escalation"),
      ),
    ).toBe("Your answer is in; it carries on next");
    expect(requestTone(task({ status: "writing" }))).toBe("work");
    expect(requestTone(task({ status: "awaiting" }))).toBe("wait");
    expect(requestTone(task({ status: "landed" }))).toBe("done");
    expect(requestTone(task({ status: "stopped" }))).toBe("");
  });
  it("counts as under way only what has started and isn't back with the owner", () => {
    expect(underWay(task({ status: "writing" }))).toBe(true);
    expect(underWay(task({ status: "awaiting" }))).toBe(true);
    expect(underWay(task({ status: "queued" }))).toBe(false);
    expect(underWay(task({ status: "waiting" }))).toBe(false);
    expect(underWay(task({ status: "landed" }))).toBe(false);
  });
  it("finds the open decision a request waits on", () => {
    const open = { ...decision("delivery"), id: "d1" };
    const closed = { ...decision("question"), id: "d2", status: "resolved" };
    expect(decisionFor(task({ decision_id: "d1" }), [open, closed])).toBe(open);
    expect(
      decisionFor(task({ decision_id: "d2" }), [open, closed]),
    ).toBeUndefined();
    expect(decisionFor(task({}), [open])).toBeUndefined();
  });
  it("shows and answers each kind of decision in its own way", () => {
    expect(decisionKind(decision("question")).answering).toBe("open");
    expect(decisionKind(decision("delivery")).prompt).toBe(
      "What should change?",
    );
    expect(decisionKind(decision("update")).approve?.()).toBe(
      "Push the update",
    );
    expect(
      [
        "delivery",
        "update",
        "question",
        "escalation",
        "failure",
        "choice",
      ].filter((k) => decisionKind(decision(k)).recommend),
    ).toEqual(["update", "escalation", "choice"]);
    // A kind this dashboard doesn't know is shown as a plain choice.
    const other = decisionKind(decision("constructor"));
    expect(other.badge).toBe("Decision");
    expect(other.answering).toBe("offered");
    expect(decisionKind(undefined).step(task({}))).toBe("Waiting for you");
    // The PM's questions are ordinary choices, badged as the PM's.
    const pm = decisionKind(decision("pm-question"));
    expect(pm).toMatchObject({ ...other, badge: "Question from the PM" });
  });
  it("says who set the to-do order, or that the PM is looking at it", () => {
    const pia = { name: "Pia", kinds: ["pm"], engine: "claude" };
    const kept = {
      ...project(code()),
      playbook: { ...code(), roles: [...code().roles, pia] },
    };
    expect(orderLine(project(code()), 0)).toBe("");
    expect(orderLine(project(code()), 1)).toBe("");
    expect(orderLine(project(code()), 2)).toBe("In the order asked for");
    expect(orderLine({ ...project(code()), ordered_by: "owner" }, 1)).toBe(
      "Ordered by you",
    );
    expect(orderLine({ ...project(code()), ordered_by: "assistant" }, 2)).toBe(
      "Ordered by the assistant",
    );
    expect(orderLine({ ...kept, ordered_by: "pm" }, 2)).toBe("Ordered by Pia");
    expect(orderLine({ ...project(code()), ordered_by: "pm" }, 2)).toBe(
      "Ordered by the PM",
    );
    expect(orderLine({ ...kept, ordered_by: "owner", pm_due: true }, 1)).toBe(
      "Pia is looking at the order",
    );
    // Without a PM on the team, nobody is about to look.
    expect(
      orderLine({ ...project(code()), ordered_by: "owner", pm_due: true }, 2),
    ).toBe("Ordered by you");
    expect(orderLine({ ...kept, pm_due: true }, 0)).toBe("");
  });
  it("treats a message as open until it is answered", () => {
    const message = (status: "waiting" | "working" | "answered") => ({
      id: "m",
      to: "Reviewer",
      kind: "reviewer",
      from: "owner",
      text: "",
      status,
      direction: 0,
    });
    expect(isOpenMessage(message("waiting"))).toBe(true);
    expect(isOpenMessage(message("working"))).toBe(true);
    expect(isOpenMessage(message("answered"))).toBe(false);
  });
  it("says a role is at work only while its turn runs, and who has the request otherwise", () => {
    const turn = {
      project_id: "p",
      task_id: "t",
      role: "implementer" as const,
      seat: "Ada",
      started_at: "",
      last_activity_at: "",
      tool_calls: 0,
      edits: 0,
      output_tokens: 0,
    };
    const ada = {
      name: "Ada",
      kinds: ["implementer"],
      engine: "claude",
      member: "m1",
    };
    const writing = task({ status: "writing", round: 2, roles: [ada] });
    expect(requestStep(writing, undefined, [turn])).toBe(
      "Round 2 · Ada working",
    );
    expect(requestStep(writing, undefined, [])).toBe("Round 2 · With Ada");
    // Without the turns, as on pages that don't know them, the words stay.
    expect(requestStep(writing)).toBe("Round 2 · Ada working");
    const pb = code();
    const reviewing = task({
      status: "reviewing",
      stage: "reviewing",
      playbook: pb,
      roles: pb.roles,
      checking: "Reviewer",
    });
    expect(requestStep(reviewing, undefined, [])).toBe("With the reviewer");
    expect(
      requestStep({ ...reviewing, stage: "qa", checking: "QA" }, undefined, []),
    ).toBe("With QA");
    expect(
      requestStep(
        task({ status: "researching", stage: "researching" }),
        undefined,
        [],
      ),
    ).toBe("With the researcher");
    expect(
      requestStep(
        task({ status: "designing", stage: "designing", checking: "Dee" }),
        undefined,
        [],
      ),
    ).toBe("With Dee for design input");
  });
  it("names the designer while a request is with it, and keeps it at work", () => {
    const dee = { name: "Dee", kinds: ["designer"], engine: "claude" };
    const withDee = task({
      status: "designing",
      stage: "designing",
      checking: "Dee",
      with_designer: true,
    });
    expect(requestStep(withDee)).toBe("With Dee for design input");
    expect(
      requestStep(
        task({ status: "designing", stage: "designing", roles: [dee] }),
      ),
    ).toBe("With Dee for design input");
    expect(requestTone(withDee)).toBe("work");
    expect(underWay(withDee)).toBe(true);
    expect(projectGroup([withDee])).toBe("working");
  });
  it("names the researcher while a request is researched", () => {
    expect(
      requestStep(
        task({ status: "researching", stage: "researching", checking: "Ada" }),
      ),
    ).toBe("Ada researching");
    expect(
      requestStep(task({ status: "researching", stage: "researching" })),
    ).toBe("Researching");
    expect(requestTone(task({ status: "researching" }))).toBe("work");
    expect(underWay(task({ status: "researching" }))).toBe(true);
  });
  it("ignores a hold whose time was never recorded", () => {
    expect(
      requestStep(
        task({
          status: "queued",
          retry_at: "0001-01-01T00:00:00Z",
          detail: "Held",
        }),
      ),
    ).toBe("Waiting to start");
  });
  it("says who or what a ready step waits for", () => {
    const queued = (waiting: Task["waiting"]) =>
      requestStep(task({ status: "queued", waiting }));
    expect(
      queued({ kind: "member", seat: "Lucius", member: "m1", on: "CA-27" }),
    ).toBe("Waiting for Lucius (busy on CA-27)");
    expect(queued({ kind: "member", seat: "Implementer", on: "CA-3" })).toBe(
      "Waiting for the implementer (busy on CA-3)",
    );
    expect(
      queued({ kind: "member", seat: "Pim", member: "m2", list: "Notes" }),
    ).toBe("Waiting for Pim (busy with the Notes to-do list)");
    expect(queued({ kind: "project_cap", active: 2, cap: 2 })).toBe(
      "Waiting for this project's cap (2 of 2 active)",
    );
    expect(queued({ kind: "engine_cap", engine: "claude" })).toBe(
      "Waiting for the Claude safety cap",
    );
    expect(queued({ kind: "owner" })).toBe("Waiting while you chat");
    // A started task's next round says it too, and a task back with the
    // owner never does.
    const waiting = { kind: "member" as const, seat: "Ada", member: "m1" };
    expect(requestStep(task({ status: "writing", round: 2, waiting }))).toBe(
      "Round 2 · Waiting for Ada",
    );
    expect(
      requestStep(task({ status: "waiting", waiting }), decision("delivery")),
    ).toBe("Waiting for your approval");
  });
  it("says which stage a request waits for room in, as its board names it", () => {
    const full = (stage: Stage, from?: Stage) => ({
      kind: "stage" as const,
      stage,
      from,
      count: 2,
      limit: 2,
    });
    expect(
      requestStep(
        task({
          status: "reviewing",
          playbook: code(),
          waiting: full("qa", "reviewing"),
        }),
      ),
    ).toBe("Done, waiting for room in QA");
    expect(
      requestStep(
        task({
          status: "writing",
          round: 2,
          playbook: writing,
          waiting: full("reviewing", "implementing"),
        }),
      ),
    ).toBe("Round 2 · Done, waiting for room in Reviewing");
    expect(
      requestStep(
        task({
          status: "deciding",
          playbook: code(),
          waiting: full("ready", "qa"),
        }),
      ),
    ).toBe("Done, waiting for room in Ready to land");
    // A request not yet started takes its project's names for the stages.
    expect(
      requestStep(
        task({ status: "queued", waiting: full("implementing") }),
        undefined,
        undefined,
        project(writing),
      ),
    ).toBe("Waiting for room in Writing (2 of 2)");
  });
});

describe("projects", () => {
  it("groups a project by its most pressing request", () => {
    expect(
      projectGroup([task({ status: "writing" }), task({ status: "waiting" })]),
    ).toBe("needs");
    expect(
      projectGroup([task({ status: "queued" }), task({ status: "reviewing" })]),
    ).toBe("working");
    expect(projectGroup([task({ status: "awaiting" })])).toBe("waiting");
    expect(projectGroup([task({ status: "researching" })])).toBe("working");
    expect(projectGroup([task({ status: "landed" })])).toBe("quiet");
    expect(
      leadRequest([
        task({ id: "a", status: "queued" }),
        task({ id: "b", status: "waiting" }),
        task({ id: "c", status: "landed" }),
      ])?.id,
    ).toBe("b");
    expect(leadRequest([task({ status: "stopped" })])).toBeUndefined();
  });
  it("keeps only a project's own requests", () => {
    const own = task({ id: "a" });
    expect(
      projectTasks(project(writing), [own, task({ id: "b", project_id: "q" })]),
    ).toEqual([own]);
  });
  it("names a project's kind of work from its team", () => {
    expect(projectKind(undefined)).toBe("Tracking only");
    expect(projectKind(code())).toBe("Code");
    expect(projectKind(writing)).toBe("Writing");
  });
  it("prefers the team a request started with over the project's", () => {
    const pb = code();
    expect(taskPlaybook(task({ playbook: pb }), project(writing))).toBe(pb);
    expect(taskPlaybook(task({}), project(writing))).toBe(writing);
    expect(taskPlaybook(undefined, undefined)).toBeUndefined();
  });
});

describe("several requests under way", () => {
  const two: Playbook = {
    ...code(),
    roles: [
      {
        name: "Claudius",
        kinds: ["implementer"],
        engine: "claude",
        member: "m1",
      },
      {
        name: "Claudius #2",
        kinds: ["implementer"],
        engine: "claude",
        member: "m1",
      },
      ...code().roles.slice(1),
    ],
  };
  it("caps them as the server does: the optional overall setting", () => {
    expect(activeCap(code())).toBe(0);
    expect(activeCap(two)).toBe(0);
    expect(activeCap({ ...two, max_active: 5 })).toBe(5);
    expect(activeCap({ ...two, roles: [] })).toBe(0);
  });
  it("counts exactly the requests the cap counts", () => {
    const counted = (
      [
        "researching",
        "designing",
        "writing",
        "reviewing",
        "deciding",
        "landing",
      ] as const
    ).map((status, i) => task({ id: `a${i}`, status }));
    const not = (
      [
        "queued",
        "triage",
        "waiting",
        "awaiting",
        "landed",
        "delivered",
        "stopped",
      ] as const
    ).map((status, i) => task({ id: `b${i}`, status }));
    const other = task({ id: "c", project_id: "q", status: "writing" });
    expect(capLine(project(two), [...counted, ...not, other])).toBe(
      "6 under way",
    );
    expect(capLine(project({ ...two, max_active: 2 }), counted)).toBe(
      "6 of 2 under way",
    );
    expect(capLine(project(), counted)).toBe("");
  });
  it("names the seat that took the step, and every checker of one draft", () => {
    const pb = code();
    const writer = task({
      status: "writing",
      roles: two.roles,
      playbook: two,
      claims: [{ step: "writing", seat: "Claudius #2" }],
    });
    expect(requestStep(writer)).toBe("Claudius #2 working");
    expect(requestStep(writer, undefined, [])).toBe("With Claudius #2");
    const checked = task({
      status: "reviewing",
      stage: "reviewing",
      checking: "Reviewer",
      roles: pb.roles,
      playbook: pb,
      claims: [
        { step: "reviewing", seat: "Reviewer", shared: true },
        { step: "reviewing", seat: "QA", shared: true },
      ],
    });
    expect(requestStep(checked)).toBe(
      "Reviewer reviewing · QA running make check",
    );
    // Before their turns show, every checker that took a check is named.
    expect(requestStep(checked, undefined, [])).toBe(
      "With the reviewer and QA",
    );
    // A turn a stopped daemon left behind is held, not at work.
    expect(
      requestStep({
        ...checked,
        claims: [{ step: "reviewing", seat: "QA", held: "still running" }],
      }),
    ).toBe("Reviewer reviewing");
  });
});

describe("stage defaults", () => {
  it("counts seats for each role, including seats from one member", () => {
    const roles = [
      "researcher",
      "designer",
      "implementer",
      "reviewer",
      "qa",
    ].flatMap((kind) =>
      [1, 2].map((n) => ({
        name: kind + n,
        kinds: [kind],
        member: kind,
        engine: "codex",
      })),
    );
    const pb = { ...code(), roles };
    for (const stage of [
      "researching",
      "designing",
      "implementing",
      "reviewing",
      "qa",
    ] as const) {
      expect(stageLimit(pb, stage)).toBe(2);
      expect(stageLimit({ ...pb, stage_limits: { [stage]: 1 } }, stage)).toBe(
        1,
      );
      expect(stageLimit({ ...pb, stage_limits: { [stage]: 5 } }, stage)).toBe(
        5,
      );
      expect(stageLimit({ ...pb, stage_limits: { [stage]: 0 } }, stage)).toBe(
        2,
      );
    }
    expect(stageLimit(pb, "ready")).toBe(0);
    expect(stageLimit({ ...pb, roles: [] }, "qa")).toBe(0);
    expect(stageLimit(undefined, "implementing")).toBe(0);
  });
});

describe("a task landing through a pull request", () => {
  const prs = {
    template: "code",
    medium: "git",
    roles: [{ name: "Implementer", kinds: ["implementer"], engine: "claude" }],
    max_rounds: 3,
    deliver: "owner",
    land: { pull_requests: true, target: "main", github: "o/r" },
  } satisfies Playbook;
  const pr = (t: Partial<Task>): Task => ({
    id: "t",
    project_id: "p",
    objective: "o",
    criteria: [],
    status: "landing",
    stage: "pr_opening",
    round: 1,
    revisions: [],
    verdicts: [],
    playbook: prs,
    ...t,
  });
  it("says how far its pull request has got", () => {
    expect(requestStep(pr({}))).toBe("Opening a pull request");
    expect(requestStep(pr({ proposal: { branch: "b", number: 7 } }))).toBe(
      "Updating pull request #7",
    );
    expect(
      requestStep(
        pr({
          status: "awaiting",
          proposal: {
            branch: "b",
            number: 7,
            observed: {
              checks: "FAILURE",
              review: "CHANGES_REQUESTED",
              unresolved: 2,
              at: "",
            },
          },
        }),
      ),
    ).toBe(
      "Pull request #7: checks failed, changes requested, 2 unresolved threads",
    );
    expect(
      requestStep(
        pr({
          status: "writing",
          proposal: { branch: "b", number: 7, answering: true },
        }),
      ),
    ).toBe("Implementer answering pull request #7");
    const delivery = decisionKind(decision("delivery"));
    expect(delivery.approve?.(prs, pr({}))).toBe("Open pull request");
    expect(
      delivery.approve?.(prs, pr({ proposal: { branch: "b", number: 7 } })),
    ).toBe("Merge pull request");
    expect(prWords({ checks: "SUCCESS", ready: true, at: "" })).toBe(
      "ready to land",
    );
    expect(prWords({ checks: "NONE", conflicting: true, at: "" })).toBe(
      "no checks, conflicts with its base",
    );
  });
});
