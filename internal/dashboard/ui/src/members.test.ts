import { describe, expect, it } from "vitest";
import {
  atWork,
  holds,
  kindsLabel,
  kindsProblem,
  memberOf,
  roleMember,
  taskRoles,
  teamChoice,
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
    expect(kindsLabel(["implementer", "researcher"])).toBe(
      "Researcher and implementer",
    );
    expect(kindsLabel(["qa", "researcher"])).toBe("Researcher and QA");
    expect(kindsLabel(["researcher", "implementer", "reviewer"])).toBe(
      "Researcher, implementer and reviewer",
    );
    expect(kindsLabel([])).toBe("");
  });
  it("names the PM last, beside the work", () => {
    expect(kindsLabel(["pm"])).toBe("PM");
    expect(kindsLabel(["pm", "reviewer"])).toBe("Reviewer and PM");
    expect(kindsLabel(["pm", "implementer", "researcher"])).toBe(
      "Researcher, implementer and PM",
    );
  });
  it("names the designer after research and before the work", () => {
    expect(kindsLabel(["implementer", "designer", "researcher"])).toBe(
      "Researcher, designer and implementer",
    );
    expect(kindsLabel(["designer"])).toBe("Designer");
  });
  it("holds a designer alone or beside one kind of work", () => {
    expect(kindsProblem(["designer"])).toBe("");
    expect(kindsProblem(["designer", "implementer", "researcher", "pm"])).toBe(
      "",
    );
    expect(kindsProblem(["designer", "implementer", "reviewer"])).not.toBe("");
    expect(
      workingKind({ name: "Dee", kinds: ["designer"], engine: "claude" }),
    ).toBe("");
    expect(
      workingKind({
        name: "Ada",
        kinds: ["designer", "reviewer"],
        engine: "x",
      }),
    ).toBe("reviewer");
  });
  it("finds the designer at work while a request is with it", () => {
    const dee: Member = { ...ada, id: "m5", name: "Dee", kinds: ["designer"] };
    const seats: Role[] = [
      ...roles,
      { name: "Dee", kinds: ["designer"], engine: "claude", member: "m5" },
    ];
    expect(
      atWork(task({ status: "designing", roles: seats, checking: "Dee" }), [
        ada,
        dee,
      ]),
    ).toBe(dee);
    expect(atWork(task({ status: "designing", roles: seats }), [dee])).toBe(
      dee,
    );
  });
  it("saves a team with its designer, or without one", () => {
    const dee: Member = { ...ada, id: "m5", name: "Dee", kinds: ["designer"] };
    const seated = playbook([
      ...roles.slice(0, 1),
      { name: "Dee", kinds: ["designer"], engine: "claude", member: "m5" },
    ]);
    expect(teamChoice(seated, [ada, dee]).designer_member).toBe("m5");
    expect(teamChoice(playbook(roles), [ada]).designer_member).toBe("");
  });
  it("tells the work a seat does from the research it may also do", () => {
    const both: Role = {
      name: "Ada",
      kinds: ["researcher", "implementer"],
      engine: "claude",
    };
    const plans: Role = {
      name: "Researcher",
      kinds: ["researcher"],
      engine: "x",
    };
    expect(holds(both, "researcher")).toBe(true);
    expect(holds(both, "reviewer")).toBe(false);
    expect(holds({}, "researcher")).toBe(false);
    expect(workingKind(both)).toBe("implementer");
    expect(workingKind(plans)).toBe("");
    const keeps: Role = { name: "Pia", kinds: ["pm"], engine: "claude" };
    expect(workingKind(keeps)).toBe("");
    expect(workingKind({ ...keeps, kinds: ["pm", "researcher"] })).toBe("");
    expect(workingKind({ ...keeps, kinds: ["pm", "qa"] })).toBe("qa");
  });
  it("refuses what the server refuses", () => {
    expect(kindsProblem(["researcher"])).toBe("");
    expect(kindsProblem(["researcher", "qa"])).toBe("");
    expect(kindsProblem([])).toBe("Pick at least one role.");
    expect(kindsProblem(["pm"])).toBe("");
    expect(kindsProblem(["researcher", "implementer", "pm"])).toBe("");
    expect(kindsProblem(["implementer", "reviewer"])).toBe(
      "Pick one of implementer, reviewer and QA, plus researcher, designer and PM if you like.",
    );
    expect(kindsProblem(["pm", "reviewer", "qa"])).not.toBe("");
  });
  it("finds the researcher at work while a request is researched", () => {
    const seats: Role[] = [
      {
        name: "Ada",
        kinds: ["implementer", "researcher"],
        engine: "claude",
        member: "m1",
      },
      ...roles.slice(1),
    ];
    expect(
      atWork(task({ status: "researching", roles: seats, checking: "Ada" }), [
        ada,
      ]),
    ).toBe(ada);
    expect(atWork(task({ status: "researching", roles: seats }), [ada])).toBe(
      ada,
    );
  });
});
