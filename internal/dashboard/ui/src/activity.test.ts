import { describe, expect, it } from "vitest";
import { activityLabel, groupActivity, isRoutineActivity } from "./activity";

describe("activity presentation", () => {
  it("describes recorded kinds without leaking internal identifiers", () => {
    expect(activityLabel("memory.created")).toBe("Remembered something new");
    expect(activityLabel("decision.opened")).toBe("Decision raised");
    expect(activityLabel("memory.corrected")).toBe("Memory corrected");
    expect(activityLabel("some.future_kind")).toBe("some future kind");
    expect(activityLabel(undefined)).toBe("Workspace");
  });

  it("describes entries from the retired delegation model neutrally", () => {
    expect(activityLabel("agent.blocked")).toBe("Earlier work update");
    expect(activityLabel("work_item.accepted")).toBe("Earlier work update");
    expect(activityLabel("worker.prepared")).toBe("Earlier work update");
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
        kind: "assistant.update",
        summary: "still working",
        project_id: "p1",
      },
      {
        id: "a2",
        kind: "assistant.update",
        summary: "working",
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
    expect(groups[0]).toMatchObject({ count: 1, label: "Decision raised" });
    expect(groups[1]).toMatchObject({ count: 2, label: "Assistant update" });
    expect(groups[1].entry.id).toBe("a3");
    expect(groups[2]).toMatchObject({ count: 1, label: "Project added" });
  });

  it("never collapses across projects or across meaningful state changes", () => {
    const groups = groupActivity([
      {
        id: "b2",
        kind: "assistant.update",
        summary: "update",
        project_id: "p1",
      },
      {
        id: "b1",
        kind: "assistant.update",
        summary: "update",
        project_id: "p2",
      },
    ]);
    expect(groups).toHaveLength(2);
    expect(isRoutineActivity("decision.opened")).toBe(false);
    expect(isRoutineActivity("assistant.update")).toBe(true);
  });
});
