import { describe, expect, it } from "vitest";
import {
  atWork,
  holds,
  kindsLabel,
  kindsProblem,
  memberOf,
  roleMember,
  taskRoles,
  workingKind,
} from "./members";
import type { Member, Playbook, Project, Role, Task } from "./api";

const ada: Member = {
  id: "m1",
  name: "Ada",
  kinds: ["implementer"],
  engine: "claude",
  learnings: [],
};
const rune: Member = { ...ada, id: "m2", name: "Rune", kinds: ["reviewer"] };
const roles: Role[] = [
  { name: "Writer", kinds: ["implementer"], engine: "claude", member: "m1" },
  { name: "Rune", kinds: ["reviewer"], engine: "codex", member: "m2" },
  { name: "Reviewer", kinds: ["reviewer"], engine: "codex" },
];
const playbook = (r: Role[]): Playbook => ({
  template: "draft",
  medium: "documents",
  roles: r,
  max_rounds: 3,
  deliver: "owner",
});
const project: Project = {
  id: "p",
  title: "P",
  status: "active",
  brief: { version: 1, goal: "g", criteria: [] },
  playbook: playbook(roles.slice(2)),
};
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

describe("a request's team", () => {
  it("is the team it started with, then its own playbook's, then the project's", () => {
    expect(taskRoles(task({ roles }), project)).toBe(roles);
    const own = playbook(roles.slice(0, 1));
    expect(taskRoles(task({ roles: [], playbook: own }), project)).toBe(
      own.roles,
    );
    expect(taskRoles(task({}), project)).toBe(project.playbook?.roles);
    expect(taskRoles(task({}), { ...project, playbook: undefined })).toEqual(
      [],
    );
  });
  it("finds the member behind a role, and who is at work now", () => {
    const members = [ada, rune];
    expect(memberOf(roles[0], members)).toBe(ada);
    expect(memberOf(roles[2], members)).toBeUndefined();
    expect(memberOf(roles[0], [])).toBeUndefined();
    expect(roleMember(roles, "Rune", members)).toBe(rune);
    expect(atWork(task({ status: "writing", roles }), members)).toBe(ada);
    expect(
      atWork(task({ status: "reviewing", roles, checking: "Rune" }), members),
    ).toBe(rune);
    expect(
      atWork(
        task({ status: "deciding", roles, checking: "Reviewer" }),
        members,
      ),
    ).toBeUndefined();
    expect(atWork(task({ status: "queued", roles }), members)).toBeUndefined();
  });
});

describe("the kinds of role a seat holds", () => {
  it("names them in the order a team works, in sentence case", () => {
    expect(kindsLabel(["reviewer"])).toBe("Reviewer");
    expect(kindsLabel(["implementer", "planner"])).toBe(
      "Planner and implementer",
    );
    expect(kindsLabel(["qa", "planner"])).toBe("Planner and QA");
    expect(kindsLabel(["planner", "implementer", "reviewer"])).toBe(
      "Planner, implementer and reviewer",
    );
    expect(kindsLabel([])).toBe("");
  });
  it("tells the work a seat does from the planning it may also do", () => {
    const both: Role = {
      name: "Ada",
      kinds: ["planner", "implementer"],
      engine: "claude",
    };
    const plans: Role = { name: "Planner", kinds: ["planner"], engine: "x" };
    expect(holds(both, "planner")).toBe(true);
    expect(holds(both, "reviewer")).toBe(false);
    expect(holds({}, "planner")).toBe(false);
    expect(workingKind(both)).toBe("implementer");
    expect(workingKind(plans)).toBe("");
  });
  it("refuses what the server refuses", () => {
    expect(kindsProblem(["planner"])).toBe("");
    expect(kindsProblem(["planner", "qa"])).toBe("");
    expect(kindsProblem([])).toBe("Pick at least one role.");
    expect(kindsProblem(["implementer", "reviewer"])).toBe(
      "Pick one of implementer, reviewer and QA, plus planning if you like.",
    );
  });
  it("finds the planner at work while a request plans", () => {
    const seats: Role[] = [
      {
        name: "Ada",
        kinds: ["implementer", "planner"],
        engine: "claude",
        member: "m1",
      },
      ...roles.slice(1),
    ];
    expect(
      atWork(task({ status: "planning", roles: seats, checking: "Ada" }), [
        ada,
      ]),
    ).toBe(ada);
    expect(atWork(task({ status: "planning", roles: seats }), [ada])).toBe(ada);
  });
});
