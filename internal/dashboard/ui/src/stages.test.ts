import { describe, expect, it } from "vitest";
import {
  approveLabel,
  boardColumns,
  landsBy,
  leadRequest,
  projectGroup,
  requestStep,
  requestTone,
  reversibility,
  whatHappens,
} from "./stages";
import type { Decision, Playbook, Project, Task } from "./api";

const writing: Playbook = {
  template: "draft",
  medium: "documents",
  roles: [
    { name: "Writer", kind: "implementer", engine: "claude" },
    { name: "Reviewer", kind: "reviewer", engine: "codex" },
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
    { name: "Implementer", kind: "implementer", engine: "claude" },
    { name: "Reviewer", kind: "reviewer", engine: "codex" },
    { name: "QA", kind: "qa", engine: "claude" },
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
      "Landed",
    ]);
    expect(boardColumns(project(writing), []).map((c) => c.label)).toEqual([
      "To do",
      "Writing",
      "Reviewing",
      "Ready",
      "Delivered",
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
          stage: "qa",
          roles: pb.roles,
          playbook: pb,
        }),
      ),
    ).toBe("QA running make check");
    expect(requestStep(task({ status: "waiting" }), decision("delivery"))).toBe(
      "Waiting for your approval",
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
    expect(requestTone(task({ status: "writing" }))).toBe("work");
    expect(requestTone(task({ status: "awaiting" }))).toBe("wait");
    expect(requestTone(task({ status: "landed" }))).toBe("done");
    expect(requestTone(task({ status: "stopped" }))).toBe("");
  });
});

describe("projects", () => {
  it("groups a project by its most pressing request", () => {
    const p = project(writing);
    expect(
      projectGroup(p, [
        task({ status: "writing" }),
        task({ status: "waiting" }),
      ]),
    ).toBe("needs");
    expect(
      projectGroup(p, [
        task({ status: "queued" }),
        task({ status: "reviewing" }),
      ]),
    ).toBe("working");
    expect(projectGroup(p, [task({ status: "awaiting" })])).toBe("waiting");
    expect(projectGroup(p, [task({ status: "landed" })])).toBe("quiet");
    expect(
      leadRequest(p, [
        task({ id: "a", status: "queued" }),
        task({ id: "b", status: "waiting" }),
      ])?.id,
    ).toBe("b");
  });
  it("says how approved work leaves a project, and what approving does", () => {
    expect(landsBy(undefined)).toBe("No team yet");
    expect(landsBy(writing)).toBe("Stays on its page");
    expect(landsBy(code())).toBe("Fast-forward main");
    expect(
      landsBy(code({ via: "pull-request", target: "main", method: "merge" })),
    ).toBe("Pull request · merge");
    expect(landsBy(code({}))).toBe("New local branch");
    expect(approveLabel(code())).toBe("Land on main");
    expect(approveLabel(code({ via: "pull-request", target: "main" }))).toBe(
      "Open pull request",
    );
    expect(approveLabel(code({}))).toBe("Create branch");
    expect(approveLabel(writing)).toBe("Approve");
    expect(approveLabel({ ...writing, deliver_to: "/out" })).toBe(
      "Approve and copy",
    );
    expect(reversibility({ via: "push" })).toBe("Undoable with effort");
    expect(reversibility({ via: "pull-request" })).toBe(
      "Permanent once merged",
    );
    expect(reversibility({})).toBe("Undoable");
    expect(whatHappens(code())[0]).toContain("main moves forward");
    expect(
      whatHappens(
        code({ via: "pull-request", target: "main", github: "o/r" }),
      )[0],
    ).toBe("A pull request opens on o/r into main.");
    expect(whatHappens({ ...writing, deliver_to: "/out" })).toEqual([
      "The draft is copied into /out.",
    ]);
  });
});
