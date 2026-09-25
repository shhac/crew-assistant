import { describe, expect, it } from "vitest";
import {
  boardColumns,
  decisionFor,
  decisionKind,
  doneLabel,
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
import type { Decision, Playbook, Project, Task } from "./api";

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
  it("names its columns for the kind of work and shows QA only when there is QA", () => {
    expect(boardColumns(project(code()), []).map((c) => c.label)).toEqual([
      "To do",
      "Implementing",
      "Reviewing",
      "QA",
      "Ready to land",
    ]);
    expect(boardColumns(project(writing), []).map((c) => c.label)).toEqual([
      "To do",
      "Writing",
      "Reviewing",
      "Ready",
    ]);
    // A request that started with QA keeps its column after the team changes.
    const pinned = task({
      status: "reviewing",
      stage: "qa",
      roles: code().roles,
    });
    expect(
      boardColumns(project(writing), [pinned]).some((c) => c.stage === "qa"),
    ).toBe(true);
  });
  it("keeps finished work out of the columns, under its own name", () => {
    const stages = (p: Project) => boardColumns(p, []).map((c) => c.stage);
    expect(stages(project(code()))).not.toContain("done");
    expect(stages(project(writing))).not.toContain("done");
    expect(doneLabel(project(code()))).toBe("Landed");
    expect(doneLabel(project(writing))).toBe("Delivered");
  });
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
  it("shows research when the team researches, or while a request still is", () => {
    const planned = (): Playbook => ({
      ...code(),
      roles: [
        { name: "Researcher", kinds: ["researcher"], engine: "claude" },
        ...code().roles,
      ],
    });
    expect(boardColumns(project(planned()), []).map((c) => c.label)).toEqual([
      "To do",
      "Researching",
      "Implementing",
      "Reviewing",
      "QA",
      "Ready to land",
    ]);
    const has = (tasks: Task[]) =>
      boardColumns(project(code()), tasks).some(
        (c) => c.stage === "researching",
      );
    expect(has([])).toBe(false);
    expect(has([task({ status: "queued", roles: planned().roles })])).toBe(
      true,
    );
    expect(has([task({ status: "waiting", stage: "researching" })])).toBe(true);
    expect(
      has([task({ status: "landed", stage: "done", roles: planned().roles })]),
    ).toBe(false);
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
  it("names the designer while a request is with it, and keeps it at work", () => {
    const dee = { name: "Dee", kinds: ["designer"], engine: "claude" };
    const withDee = task({
      status: "designing",
      stage: "researching",
      checking: "Dee",
      with_designer: true,
    });
    expect(requestStep(withDee)).toBe("With Dee for design input");
    expect(
      requestStep(
        task({ status: "designing", stage: "implementing", roles: [dee] }),
      ),
    ).toBe("With Dee for design input");
    expect(requestTone(withDee)).toBe("work");
    expect(underWay(withDee)).toBe(true);
    expect(projectGroup([withDee])).toBe("working");
    // No column of its own: it stays where whoever asked left it.
    expect(
      boardColumns(project(code()), [withDee]).some(
        (c) => (c.stage as string) === "designing",
      ),
    ).toBe(false);
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
