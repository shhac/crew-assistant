import { describe, expect, it } from "vitest";
import { atWork, memberOf, roleMember, taskRoles } from "./members";
import type { Member, Playbook, Project, Role, Task } from "./api";

const ada: Member = {
  id: "m1",
  name: "Ada",
  kind: "implementer",
  engine: "claude",
  learnings: [],
};
const rune: Member = { ...ada, id: "m2", name: "Rune", kind: "reviewer" };
const roles: Role[] = [
  { name: "Writer", kind: "implementer", engine: "claude", member: "m1" },
  { name: "Rune", kind: "reviewer", engine: "codex", member: "m2" },
  { name: "Reviewer", kind: "reviewer", engine: "codex" },
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
