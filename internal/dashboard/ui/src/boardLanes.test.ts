import { describe, expect, it } from "vitest";
import { boardColumns, doneLabel, readyLabel } from "./boardLanes";
import type { Playbook, Project, Task } from "./api";

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
const lanes = (p: Project, tasks: Task[] = []) =>
  boardColumns(p, tasks).map((c) => c.lanes.map((l) => l.label));
const hasLane = (p: Project, tasks: Task[], stage: string) =>
  boardColumns(p, tasks).some((c) => c.lanes.some((l) => l.stage === stage));

describe("the board's lanes", () => {
  it("names its columns for the kind of work and shows QA only when there is QA", () => {
    expect(lanes(project(code()))).toEqual([
      ["To do"],
      ["Implementing"],
      ["QA", "Reviewing"],
    ]);
    expect(lanes(project(writing))).toEqual([
      ["To do"],
      ["Writing"],
      ["Reviewing"],
    ]);
    // A request that started with QA keeps its lane after the team changes.
    const pinned = task({
      status: "reviewing",
      stage: "qa",
      roles: code().roles,
    });
    expect(hasLane(project(writing), [pinned], "qa")).toBe(true);
  });
  it("keeps work ready to go out above the columns, under its own name", () => {
    expect(hasLane(project(code()), [], "ready")).toBe(false);
    expect(readyLabel(project(code()))).toBe("Ready to land");
    expect(readyLabel(project(writing))).toBe("Ready");
  });
  it("keeps finished work out of the columns, under its own name", () => {
    expect(hasLane(project(code()), [], "done")).toBe(false);
    expect(hasLane(project(writing), [], "done")).toBe(false);
    expect(doneLabel(project(code()))).toBe("Landed");
    expect(doneLabel(project(writing))).toBe("Delivered");
  });
  it("shows research when the team researches, or while a request still is", () => {
    const planned = (): Playbook => ({
      ...code(),
      roles: [
        { name: "Researcher", kinds: ["researcher"], engine: "claude" },
        ...code().roles,
      ],
    });
    expect(lanes(project(planned()))).toEqual([
      ["To do"],
      ["Researching"],
      ["Implementing"],
      ["QA", "Reviewing"],
    ]);
    const has = (tasks: Task[]) =>
      hasLane(project(code()), tasks, "researching");
    expect(has([])).toBe(false);
    expect(has([task({ status: "queued", roles: planned().roles })])).toBe(
      true,
    );
    expect(has([task({ status: "waiting", stage: "researching" })])).toBe(true);
    expect(
      has([task({ status: "landed", stage: "done", roles: planned().roles })]),
    ).toBe(false);
  });
  it("shows design below research, when the team designs or a request is with the designer", () => {
    const dee = { name: "Dee", kinds: ["designer"], engine: "claude" };
    const designs = { ...code(), roles: [...code().roles, dee] };
    expect(lanes(project(designs))).toEqual([
      ["To do"],
      ["Designing"],
      ["Implementing"],
      ["QA", "Reviewing"],
    ]);
    const researcher = { name: "Ada", kinds: ["researcher"], engine: "claude" };
    expect(
      lanes(project({ ...designs, roles: [researcher, ...designs.roles] }))[1],
    ).toEqual(["Researching", "Designing"]);
    const withDee = task({ status: "designing", stage: "designing" });
    expect(hasLane(project(code()), [withDee], "designing")).toBe(true);
    expect(hasLane(project(code()), [], "designing")).toBe(false);
  });
});
