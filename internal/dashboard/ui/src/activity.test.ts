import { describe, expect, it } from "vitest";
import { activityLabel, groupActivity, isRoutineActivity } from "./activity";

describe("activity presentation", () => {
  it("describes recorded kinds without leaking internal identifiers", () => {
    expect(activityLabel("memory.created")).toBe("Memory");
    expect(activityLabel("decision.opened")).toBe("Asked you");
    expect(activityLabel("task.landed")).toBe("Landed");
    expect(activityLabel("memory.corrected")).toBe("Memory");
    expect(activityLabel("some.future_kind")).toBe("Update");
    expect(activityLabel("some.future_kind")).not.toContain("future");
    expect(activityLabel(undefined)).toBe("Update");
    expect(activityLabel("")).toBe("Update");
  });

  it("describes entries from the retired delegation model neutrally", () => {
    expect(activityLabel("agent.blocked")).toBe("Earlier work");
    expect(activityLabel("work_item.accepted")).toBe("Earlier work");
    expect(activityLabel("worker.prepared")).toBe("Earlier work");
  });

  it("collapses an adjacent run of routine updates and keeps the newest entry", () => {
    const groups = groupActivity([
      {
        id: "a4",
        kind: "decision.opened",
        summary: "Question raised",
        project_id: "p1",
      },
      {
        id: "a3",
        kind: "task.writing",
        summary: "still drafting",
        project_id: "p1",
      },
      {
        id: "a2",
        kind: "task.writing",
        summary: "drafting",
        project_id: "p1",
      },
      {
        id: "a1",
        kind: "project.created",
        summary: "Project added",
        project_id: "p1",
      },
    ]);
    expect(groups).toHaveLength(3);
    expect(groups[0]).toMatchObject({
      count: 1,
      label: "Asked you",
      routine: false,
    });
    expect(groups[1]).toMatchObject({
      count: 2,
      label: "Draft",
      routine: true,
    });
    expect(groups[1].entry.id).toBe("a3");
    expect(groups[2]).toMatchObject({ count: 1, label: "Project" });
  });

  it("collapses runs of memory updates and honours a limit", () => {
    const groups = groupActivity(
      [
        { id: "m3", kind: "memory.created", summary: "c", project_id: "p1" },
        { id: "m2", kind: "memory.created", summary: "b", project_id: "p1" },
        { id: "m1", kind: "task.landed", summary: "a", project_id: "p1" },
      ],
      1,
    );
    expect(groups).toHaveLength(1);
    expect(groups[0]).toMatchObject({ count: 2, label: "Memory" });
    expect(groups[0].entry.id).toBe("m3");
  });

  it("never collapses across projects or across meaningful state changes", () => {
    const acrossProjects = groupActivity([
      { id: "b2", kind: "task.writing", summary: "update", project_id: "p1" },
      { id: "b1", kind: "task.writing", summary: "update", project_id: "p2" },
    ]);
    expect(acrossProjects).toHaveLength(2);

    const assistantUpdates = groupActivity([
      { id: "c2", kind: "assistant.update", summary: "u", project_id: "p1" },
      { id: "c1", kind: "assistant.update", summary: "u", project_id: "p1" },
    ]);
    expect(assistantUpdates).toHaveLength(2);

    const differentSteps = groupActivity([
      { id: "d2", kind: "task.reviewing", summary: "u", project_id: "p1" },
      { id: "d1", kind: "task.writing", summary: "u", project_id: "p1" },
    ]);
    expect(differentSteps).toHaveLength(2);

    expect(isRoutineActivity("decision.opened")).toBe(false);
    expect(isRoutineActivity("task.landed")).toBe(false);
    expect(isRoutineActivity("assistant.update")).toBe(false);
    expect(isRoutineActivity(undefined)).toBe(false);
  });

  it("treats task steps and memory changes as routine", () => {
    for (const kind of [
      "task.writing",
      "task.reviewing",
      "task.deciding",
      "task.started",
      "task.queued",
      "task.reordered",
      "task.landing",
      "task.awaiting",
      "memory.created",
      "memory.updated",
      "memory.corrected",
      "memory.forgotten",
    ]) {
      expect(isRoutineActivity(kind)).toBe(true);
    }
  });
});
