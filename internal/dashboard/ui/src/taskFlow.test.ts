import { describe, expect, it } from "vitest";
import {
  normalizeState,
  type Project,
  type Role,
  type Task,
  type Verdict,
} from "./api";
import { stageFlow } from "./taskFlow";

const roles: Role[] = [
  { name: "Ash", kinds: ["researcher", "implementer"], engine: "claude" },
  { name: "Dee", kinds: ["designer"], engine: "codex" },
  { name: "Rune", kinds: ["reviewer"], engine: "claude" },
  { name: "Quinn", kinds: ["qa"], engine: "codex" },
  { name: "Pia", kinds: ["pm"], engine: "codex" },
];
const project: Project = {
  id: "p",
  title: "Example",
  status: "active",
  brief: { version: 1, goal: "Example", criteria: [] },
  playbook: {
    template: "code",
    medium: "git",
    roles,
    max_rounds: 3,
    deliver: "owner",
  },
};
const task = (overrides: Partial<Task> = {}): Task => ({
  id: "t",
  project_id: "p",
  objective: "Example",
  criteria: [],
  status: "queued",
  stage: "todo",
  roles,
  round: 1,
  revisions: [],
  verdicts: [],
  ...overrides,
});
const plan = { role: "Ash", summary: "Ready", at: "2026-09-29T10:00:00Z" };
const revision = (n: number) => ({
  n,
  brief_version: 1,
  files: [],
  at: plan.at,
});
const verdict = (revision: number, role = "Rune"): Verdict => ({
  revision,
  role,
  brief_version: 1,
  outcome: "pass",
  summary: "Good",
  at: plan.at,
});
const flow = (overrides: Partial<Task> = {}, extra = {}) =>
  stageFlow(task(overrides), project, normalizeState(extra));
const row = (kind: string, overrides: Partial<Task> = {}, extra = {}) =>
  flow(overrides, extra).find((r) => r.kind === kind)!;

describe("task stage progress", () => {
  it("makes every unstarted stage terminal when stopped before starting", () => {
    const rows = flow({ status: "stopped", stage: "stopped" });
    expect(
      rows.every(
        (r) =>
          r.state === "not-started" &&
          r.support === "Will not run: this request was stopped.",
      ),
    ).toBe(true);
    expect(rows.filter((r) => r.exception?.tone === "stopped")).toHaveLength(1);
  });
  it("places the stop on an interrupted claim when no stage was recorded", () => {
    const second = { ...roles[0], name: "Ash #2" };
    const rows = flow({
      status: "stopped",
      stage: "stopped",
      roles: [...roles, second],
      claims: [{ step: "writing", seat: second.name, held: "restart" }],
    });
    expect(rows.find((r) => r.kind === "implementer")).toMatchObject({
      state: "interrupted",
      role: { name: second.name },
      exception: { tone: "stopped" },
      support: "Stopped before build 1 was made",
    });
    expect(
      rows.find((r) => r.kind === "researcher")?.exception,
    ).toBeUndefined();
  });
  it.each(["reviewer", "qa"])(
    "keeps previous %s verdicts from the whole checker group",
    (kind) => {
      const first: Role = {
        name: "Checker",
        kinds: [kind],
        member: "checker",
        engine: "codex",
      };
      const second = { ...first, name: "Checker #2" };
      const other = { ...first, name: "Other", member: "other" };
      const rows = flow({
        status: "reviewing",
        stage: kind === "qa" ? "qa" : "reviewing",
        round: 2,
        checking: first.name,
        roles: [roles[0], first, second, other],
        revisions: [revision(1), revision(2)],
        verdicts: [verdict(1, second.name), verdict(1, other.name)],
        claims: [{ step: "reviewing", seat: first.name }],
      });
      expect(rows.find((r) => r.role?.name === first.name)?.previous).toEqual([
        {
          label: `${kind === "qa" ? "QA" : "Review"} · build 1`,
          outcome: "Passed",
          at: plan.at,
        },
      ]);
    },
  );
  it("does not make QA wait for a passing review", () => {
    expect(
      row("qa", {
        status: "reviewing",
        stage: "reviewing",
        revisions: [revision(1)],
        checking: "Rune",
      }),
    ).toMatchObject({
      state: "not-started",
      support: "Quinn will check build 1 when it is ready",
    });
  });
  it.each(["reviewer", "qa"])(
    "counts verdicts across %s seats in the same member group",
    (kind) => {
      const first = {
        name: "Checker",
        kinds: [kind],
        engine: "codex",
        member: "checker",
      };
      const second = { ...first, name: "Checker #2" };
      const t = task({
        status: "landing",
        stage: "ready",
        roles: [roles[0], first, second],
        revisions: [revision(1)],
        verdicts: [verdict(1, first.name)],
        messages: [
          {
            id: "m",
            to: second.name,
            from: "owner",
            kind,
            text: "Check",
            direction: 0,
            status: "waiting",
          },
        ],
      });
      const get = () =>
        stageFlow(t, project, normalizeState({})).find(
          (r) => r.role?.name === second.name,
        )!;
      expect(get().state).toBe("done");
      second.member = "other-checker";
      expect(get().state).toBe("not-started");
      second.member = "checker";
      second.kinds = [kind === "qa" ? "reviewer" : "qa"];
      expect(get().state).toBe("not-started");
    },
  );
  it("uses normalized names for checker groups without members", () => {
    const first: Role = {
      name: " Rune ",
      kinds: ["reviewer"],
      engine: "codex",
    };
    const second = { ...first, name: "rune" };
    const rows = flow({
      status: "landing",
      stage: "ready",
      roles: [first, second],
      revisions: [revision(1)],
      verdicts: [verdict(1, first.name)],
      messages: [
        {
          id: "m",
          to: second.name,
          from: "owner",
          kind: "reviewer",
          text: "Check",
          direction: 0,
          status: "waiting",
        },
      ],
    });
    expect(
      rows
        .filter((r) => r.kind === "reviewer")
        .every((r) => r.state === "done"),
    ).toBe(true);
  });
  it("keeps parallel interrupted seats started with one stopped exception", () => {
    const second = { ...roles[0], name: "Ash #2" };
    const builds = flow({
      status: "stopped",
      stage: "stopped",
      place: "implementing",
      roles: [...roles, second],
      claims: [
        { seat: "Ash", step: "writing" },
        { seat: second.name, step: "writing" },
      ],
    }).filter((r) => r.kind === "implementer");
    expect(builds).toHaveLength(2);
    expect(
      builds.every(
        (r) =>
          r.state === "interrupted" &&
          r.support === "Stopped before build 1 was made",
      ),
    ).toBe(true);
    expect(builds.filter((r) => r.exception?.tone === "stopped")).toHaveLength(
      1,
    );
  });
  it.each([
    "implementing",
    "researching",
    "reviewing",
    "qa",
    "designing",
  ] as const)("preserves the interrupted %s stage after stopping", (place) => {
    const kind = {
      implementing: "implementer",
      researching: "researcher",
      reviewing: "reviewer",
      qa: "qa",
      designing: "designer",
    }[place]!;
    const rows = flow({
      status: "stopped",
      stage: "stopped",
      place,
      round: 2,
      revisions: [revision(1)],
    });
    const interrupted = rows.find((r) => r.kind === kind)!;
    expect(interrupted).toMatchObject({
      state: "interrupted",
      exception: { tone: "stopped" },
    });
    expect(interrupted.support).toMatch(/^Stopped before /);
    if (kind === "implementer")
      expect(interrupted.support).toBe("Stopped before build 2 was made");
    expect(
      rows
        .slice(rows.indexOf(interrupted) + 1)
        .filter((r) => r.state === "not-started")
        .every((r) => r.support === "Will not run: this request was stopped."),
    ).toBe(true);
  });
  it.each(["researching", "writing"])(
    "keeps the %s asker working during design input",
    (step) => {
      const rows = flow({
        status: "designing",
        stage: "designing",
        with_designer: true,
        round: step === "writing" ? 2 : 1,
        revisions: step === "writing" ? [revision(1)] : [],
        design: [
          {
            id: "d",
            from: "Ash",
            step,
            round: 2,
            question: "Layout?",
            designer: "Dee",
          },
        ],
      });
      expect(
        rows.find(
          (r) => r.kind === (step === "writing" ? "implementer" : "researcher"),
        ),
      ).toMatchObject({
        state: "working",
        support: "Waiting for Dee to give design input",
      });
      expect(rows.find((r) => r.kind === "designer")?.state).toBe("working");
    },
  );
  it.each(["reviewer", "qa"])(
    "counts %s verdicts against the current brief and text, excluding answered work",
    (kind) => {
      const role = kind === "qa" ? "Quinn" : "Rune";
      const t = task({
        status: "landing",
        stage: "ready",
        text_version: 2,
        revisions: [revision(1)],
        verdicts: [{ ...verdict(1, role), brief_version: 2, text_version: 2 }],
      });
      const p = { ...project, brief: { ...project.brief, version: 2 } };
      const get = () =>
        stageFlow(t, p, normalizeState({})).find((r) => r.kind === kind)!;
      expect(get().state).toBe("done");
      t.verdicts![0].answered = true;
      expect(get().state).toBe("not-started");
      t.verdicts![0].answered = false;
      t.verdicts![0].text_version = 1;
      expect(get().state).toBe("not-started");
      t.verdicts![0].text_version = 2;
      t.verdicts![0].brief_version = 1;
      expect(get().state).toBe("not-started");
    },
  );
  it("does not credit an unknown builder and retains idle message recipients", () => {
    const second = { ...roles[0], name: "Ash #2", kinds: ["implementer"] };
    const rows = flow({
      status: "reviewing",
      stage: "qa",
      roles: [...roles, second],
      revisions: [revision(1)],
      messages: [
        {
          id: "m",
          to: second.name,
          kind: "implementer",
          from: "owner",
          text: "Check",
          direction: 0,
          status: "waiting",
        },
      ],
    });
    const builds = rows.filter((r) => r.kind === "implementer");
    expect(builds.map((r) => r.role?.name)).toEqual(["Ash", "Ash #2"]);
    expect(
      builds.every((r) => r.support.includes("Builder not recorded")),
    ).toBe(true);
  });
  it("orders the pinned stages and gives every queued stage a named prerequisite", () => {
    const rows = flow();
    expect(rows.map((r) => r.label)).toEqual([
      "Research",
      "Design",
      "Build",
      "Review",
      "QA",
    ]);
    expect(
      rows.every(
        (r) =>
          r.state === "not-started" &&
          r.support === `${r.role!.name} will start when this request begins`,
      ),
    ).toBe(true);
    expect(flow({ status: "triage", stage: "triage" })[0]).toMatchObject({
      label: "Triage",
      state: "working",
      role: { name: "Pia" },
    });
  });
  it("researches, records the plan, then builds with future prerequisites", () => {
    const researching = {
      status: "researching",
      stage: "researching",
    } as const;
    expect(row("researcher", researching).state).toBe("working");
    expect(row("implementer", researching)).toMatchObject({
      state: "not-started",
      support: "Ash will build when research is done",
    });
    const writing = { status: "writing", stage: "implementing", plan } as const;
    expect(row("researcher", writing).support).toMatch(/^Research recorded /);
    expect(row("researcher", writing).state).toBe("done");
    expect(row("implementer", writing).state).toBe("working");
  });
  it("keeps the current build and check visible, with previous rounds newest first", () => {
    const second = {
      status: "writing",
      stage: "implementing",
      round: 2,
      revisions: [revision(1)],
      verdicts: [verdict(1)],
    } as const;
    // Mutable arrays reflect the API record.
    const record: Partial<Task> = {
      ...second,
      revisions: [...second.revisions],
      verdicts: [...second.verdicts],
    };
    expect(row("implementer", record)).toMatchObject({
      roundLabel: "round 2",
      state: "working",
      previous: [{ label: "Build 1", outcome: "Build made" }],
    });
    expect(row("reviewer", record)).toMatchObject({
      roundLabel: "build 2",
      state: "not-started",
      support: "Rune will check build 2 when it is ready",
      previous: [{ label: "Review · build 1", outcome: "Passed" }],
    });
    expect(
      row("reviewer", {
        ...record,
        status: "reviewing",
        stage: "qa",
        checking: "Quinn",
        revisions: [revision(1), revision(2)],
        verdicts: [verdict(1), verdict(2)],
      }),
    ).toMatchObject({
      state: "done",
      support: expect.stringContaining("Passed build 2"),
    });
    expect(
      row("qa", {
        ...record,
        status: "reviewing",
        stage: "qa",
        checking: "Quinn",
        revisions: [revision(2)],
        verdicts: [verdict(2)],
      }).state,
    ).toBe("working");
  });
  it("shows only on-demand design, omitting an unstaffed stage", () => {
    expect(
      flow({ roles: roles.filter((r) => !r.kinds.includes("designer")) }).some(
        (r) => r.kind === "designer",
      ),
    ).toBe(false);
    expect(
      row("designer", { status: "writing", stage: "implementing" }).support,
    ).toBe("Dee will give design input if asked");
    expect(
      row("designer", {
        status: "reviewing",
        stage: "reviewing",
        revisions: [revision(1)],
      }).support,
    ).toContain("wasn't asked");
    const design = [
      {
        id: "d",
        from: "Ash",
        step: "writing",
        round: 1,
        question: "Layout?",
        designer: "Dee",
        answered_at: plan.at,
      },
    ];
    expect(
      row("designer", {
        status: "writing",
        stage: "designing",
        with_designer: true,
        design,
      }).state,
    ).toBe("working");
    expect(
      row("designer", { status: "writing", stage: "implementing", design })
        .state,
    ).toBe("done");
  });
  it("shows a system landing row only during landing or external checks", () => {
    for (const status of ["landing", "awaiting"] as const)
      expect(flow({ status, stage: "ready" }).at(-1)).toMatchObject({
        label: "Landing",
        state: "working",
      });
    expect(
      flow({ status: "landed", stage: "done" }).some(
        (r) => r.kind === "landing",
      ),
    ).toBe(false);
  });
  it("overlays owner decisions and blockers without hiding progress", () => {
    const waiting = {
      status: "waiting",
      stage: "implementing",
      decision_id: "d",
    } as const;
    const decision = {
      id: "d",
      kind: "question",
      title: "Question",
      context: "",
      recommendation: "",
      choices: [],
      status: "pending",
    };
    expect(
      row("implementer", waiting, { decisions: [decision] }),
    ).toMatchObject({
      state: "working",
      exception: { tone: "needs", text: "Question for you" },
    });
    expect(
      row("implementer", waiting, {
        decisions: [{ ...decision, kind: "failure" }],
      }),
    ).toMatchObject({ state: "working", exception: { tone: "block" } });
    const blockers = [
      {
        id: "b",
        kind: "manual" as const,
        description: "Need a fixture",
        by: "owner",
        at: plan.at,
      },
    ];
    expect(row("implementer", { ...waiting, blockers }).exception).toEqual({
      tone: "block",
      text: "Need a fixture",
    });
    expect(
      row("implementer", {
        status: "writing",
        stage: "implementing",
        blockers: [{ ...blockers[0], cleared_at: plan.at }],
      }).exception,
    ).toBeUndefined();
  });
  it("preserves completed work after stopping and never implies future work will resume", () => {
    const stopped = {
      status: "stopped",
      stage: "stopped",
      plan,
      revisions: [revision(1)],
    } as const;
    const rows = flow({ ...stopped, revisions: [...stopped.revisions] });
    expect(rows.find((r) => r.kind === "researcher")?.state).toBe("done");
    expect(rows.find((r) => r.kind === "implementer")?.state).toBe("done");
    expect(
      rows
        .slice(rows.findIndex((r) => r.exception?.tone === "stopped") + 1)
        .filter((r) => r.state === "not-started")
        .every((r) => r.support === "Will not run: this request was stopped."),
    ).toBe(true);
    expect(rows.some((r) => r.exception?.tone === "stopped")).toBe(true);
  });
  it("keeps partial writes and held restart claims working", () => {
    expect(
      row("implementer", {
        status: "writing",
        stage: "implementing",
        revisions: [revision(1)],
        claims: [{ step: "writing", seat: "Ash", held: "restart" }],
        detail: "Tries again shortly",
      }),
    ).toMatchObject({ state: "working", support: "Tries again shortly" });
    expect(
      row("reviewer", {
        status: "reviewing",
        stage: "reviewing",
        checking: "Rune",
        revisions: [revision(1)],
        verdicts: [verdict(1)],
      }).state,
    ).toBe("working");
    expect(
      row("implementer", { status: "reviewing", stage: "reviewing" }).state,
    ).toBe("not-started");
  });
  it("shows parallel checkers and multiple active seats of a kind", () => {
    const checked: Partial<Task> = {
      status: "reviewing",
      stage: "reviewing",
      revisions: [revision(1)],
      claims: [
        { step: "reviewing", seat: "Rune" },
        { step: "reviewing", seat: "Quinn" },
      ],
    };
    expect(
      flow(checked)
        .filter((r) => r.state === "working")
        .map((r) => r.kind),
    ).toEqual(["reviewer", "qa"]);
    const second = { ...roles[0], name: "Ash #2" };
    const rows = flow({
      status: "writing",
      stage: "implementing",
      roles: [...roles, second],
      claims: [
        { step: "writing", seat: "Ash" },
        { step: "writing", seat: "Ash #2" },
      ],
    });
    expect(
      rows
        .filter((r) => r.kind === "implementer")
        .map((r) => [r.role?.name, r.state]),
    ).toEqual([
      ["Ash", "working"],
      ["Ash #2", "working"],
    ]);
  });
  it("retains recorded research by a removed seat and repeat research only from the record", () => {
    expect(
      row("researcher", {
        status: "writing",
        stage: "implementing",
        plan: { ...plan, role: "Former researcher" },
      }),
    ).toMatchObject({ role: { name: "Former researcher" }, state: "done" });
    const research = [
      { id: "r", from: "Rune", round: 1, revision: 1, question: "Look again" },
    ];
    expect(
      row("researcher", {
        status: "researching",
        stage: "researching",
        plan,
        research,
      }),
    ).toMatchObject({ roundLabel: "pass 2", state: "working" });
    expect(row("researcher").roundLabel).toBeUndefined();
  });
  it("keeps a removed checker named from the pinned playbook and its verdict", () => {
    expect(
      row("reviewer", {
        status: "landing",
        stage: "ready",
        playbook: project.playbook,
        roles: roles.filter((r) => r.name !== "Rune"),
        revisions: [revision(1)],
        verdicts: [verdict(1)],
      }),
    ).toMatchObject({
      state: "done",
      role: { name: "Rune" },
      support: expect.stringContaining("Passed build 1"),
    });
  });
  it("keeps pickup waits working and places approval on completed checks", () => {
    expect(
      row("implementer", {
        status: "writing",
        stage: "implementing",
        waiting: { kind: "member", seat: "Ash", member: "a", on: "CA-2" },
      }),
    ).toMatchObject({
      state: "working",
      support: expect.stringContaining("Ash"),
    });
    const approval = {
      id: "d",
      kind: "delivery",
      title: "Approve",
      context: "",
      recommendation: "",
      choices: [],
      status: "pending",
    };
    expect(
      row(
        "reviewer",
        {
          status: "waiting",
          stage: "ready",
          decision_id: "d",
          revisions: [revision(1)],
          verdicts: [verdict(1)],
        },
        { decisions: [approval] },
      ),
    ).toMatchObject({
      state: "done",
      exception: { tone: "needs", text: "Waiting for your approval" },
    });
    expect(
      row(
        "designer",
        {
          status: "waiting",
          stage: "ready",
          decision_id: "d",
          revisions: [revision(1)],
          verdicts: [verdict(1)],
        },
        { decisions: [approval] },
      ).exception,
    ).toBeUndefined();
  });
  it("uses Write and draft wording for a writing team, and needs a current artifact for done", () => {
    const t = task({
      status: "deciding",
      stage: "reviewing",
      revisions: [revision(1)],
      verdicts: [{ ...verdict(1), brief_version: 0 }],
    });
    expect(row("reviewer", t).state).toBe("working");
    const writingProject = {
      ...project,
      playbook: {
        ...project.playbook!,
        template: "draft",
        medium: "documents",
      },
    };
    const rows = stageFlow(
      task({ status: "writing", stage: "implementing" }),
      writingProject,
      normalizeState({}),
    );
    expect(rows.find((r) => r.kind === "implementer")?.label).toBe("Write");
    expect(rows.find((r) => r.kind === "reviewer")?.support).toBe(
      "Rune will check draft 1 when it is ready",
    );
  });
});
