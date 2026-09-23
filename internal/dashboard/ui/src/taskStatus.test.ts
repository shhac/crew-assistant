import { describe, expect, it } from "vitest";
import { projectStatusLine, taskStatusLine } from "./taskStatus";
import type { Project, Task } from "./api";

const task = (overrides: Partial<Task>): Task => ({
  id: "t",
  project_id: "p1",
  objective: "Note",
  criteria: [],
  status: "queued",
  round: 0,
  revisions: [],
  verdicts: [],
  ...overrides,
});
const project: Project = {
  id: "p1",
  title: "Launch note",
  status: "active",
  brief: { version: 1, goal: "Explain the launch", criteria: [] },
};
const draft = (n: number) => ({ n, brief_version: 1, files: [] });

describe("task status in plain words", () => {
  it.each<[Partial<Task>, string]>([
    [{ status: "queued" }, "Waiting to start"],
    [{ status: "writing" }, "Writing draft 1"],
    [{ status: "writing", revisions: [draft(1), draft(2)] }, "Writing draft 3"],
    [{ status: "reviewing", revisions: [draft(1)] }, "Reviewing draft 1"],
    [{ status: "deciding" }, "Weighing the reviews"],
    [{ status: "waiting" }, "Needs your decision"],
    [{ status: "delivered" }, "Delivered"],
    [{ status: "delivered", delivered_to: "/out/note.md" }, "Delivered"],
    [{ status: "stopped", detail: "the owner stopped it" }, "Stopped"],
    [{ status: "stopped" }, "Stopped"],
  ])("%j reads %s", (overrides, label) => {
    expect(taskStatusLine(task(overrides)).label).toBe(label);
  });

  it("tolerates revisions the daemon sent as null", () => {
    expect(
      taskStatusLine(task({ status: "writing", revisions: null })).label,
    ).toBe("Writing draft 1");
  });
});

describe("project status", () => {
  it("is idle with nothing open, including finished and other projects' work", () => {
    expect(
      projectStatusLine(project, [
        task({ status: "delivered" }),
        task({ project_id: "other", status: "writing" }),
      ]).label,
    ).toBe("Idle");
  });

  it("puts a waiting decision ahead of work in progress and queued work", () => {
    const tasks = [
      task({ id: "a", status: "queued" }),
      task({ id: "b", status: "reviewing", revisions: [draft(1)] }),
      task({ id: "c", status: "waiting" }),
    ];
    expect(projectStatusLine(project, tasks)).toEqual({
      label: "Needs your decision",
      tone: "amber",
    });
    expect(projectStatusLine(project, tasks.slice(0, 2)).label).toBe(
      "Reviewing draft 1",
    );
  });
});
