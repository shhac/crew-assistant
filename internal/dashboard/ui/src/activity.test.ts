import { describe, expect, it } from "vitest";
import {
  activityLabel,
  foldActivity,
  pageActivity,
  matchesFilter,
  isRoutineActivity,
} from "./activity";
import type { Activity } from "./api";

const now = new Date(2026, 8, 30, 12);
const entry = (
  id: string,
  day = 30,
  task_id?: string,
  kind = "task.landed",
): Activity => ({
  id,
  task_id,
  kind,
  summary: id,
  created_at: new Date(2026, 8, day, 10).toISOString(),
});

describe("activity folding and paging", () => {
  it("uses seven calendar days for a sparse first page and updates labels at midnight", () => {
    const days = foldActivity(
      Array.from({ length: 10 }, (_, i) => entry(String(i), 30 - i)),
      "all",
      now,
    );
    expect(pageActivity(days, now).oldestKey).toBe(days[6].key);
    expect(pageActivity(days, now).hasOlder).toBe(true);
    expect(
      foldActivity([entry("today")], "all", new Date(2026, 9, 1, 0))[0].label,
    ).toBe("Yesterday");
  });
  it("uses the newest matching event and splits an expanded run without changing the older key", () => {
    const entries = [
      entry("writing", 30, "t1", "task.writing"),
      entry("landed", 30, "t1"),
      entry("delivered", 30, "t1", "task.delivered"),
    ];
    expect(foldActivity(entries, "outcomes", now)[0].rows[0]).toMatchObject({
      entry: entries[1],
      others: 1,
    });
    const split = foldActivity(
      [entry("new", 30, "t1"), entry("project", 30), ...entries],
      "all",
      now,
    )[0].rows;
    expect(split.map((row) => row.key)).toEqual([
      "new",
      "project",
      "delivered",
    ]);
  });
  it("keeps the filter families precise", () => {
    for (const kind of ["decision.opened", "task.waiting"])
      expect(matchesFilter(kind, "needs")).toBe(true);
    for (const kind of [
      "task.landed",
      "task.delivered",
      "task.stopped",
      "task.pm_landing",
    ])
      expect(matchesFilter(kind, "outcomes")).toBe(true);
    expect(matchesFilter("task.writing", "steps")).toBe(true);
    for (const kind of [
      "decision.resolved",
      "pm.asked",
      "project.created",
      "unknown",
      undefined,
    ]) {
      expect(matchesFilter(kind, "all")).toBe(true);
      for (const filter of ["needs", "outcomes", "steps"] as const)
        expect(matchesFilter(kind, filter)).toBe(false);
    }
  });
  it("folds six task events with stable oldest keys and chronological details", () => {
    const entries = [
      "task.landed",
      "task.pm_landing",
      "task.reviewing",
      "task.writing",
      "task.started",
      "task.queued",
    ].map((kind, i) => entry(String(i), 30, "t1", kind));
    const row = foldActivity(entries, "all", now)[0].rows[0];
    expect(row).toMatchObject({ key: "5", others: 5, entry: entries[0] });
    expect(row.entries.map((e) => e.id)).toEqual([
      "5",
      "4",
      "3",
      "2",
      "1",
      "0",
    ]);
    expect(foldActivity(entries, "outcomes", now)[0].rows[0]).toMatchObject({
      others: 1,
      entry: entries[0],
    });
    expect(
      foldActivity([entry("new", 30, "t1"), ...entries], "all", now)[0].rows[0]
        .key,
    ).toBe("5");
  });
  it("splits tasks at project events and day boundaries, retaining undated entries", () => {
    const days = foldActivity(
      [
        entry("a", 30, "deleted"),
        entry("rename", 30, undefined, "project.renamed"),
        entry("brief", 30, undefined, "brief.updated"),
        entry("b", 30, "deleted"),
        entry("c", 29, "deleted"),
        entry("d", 21),
        { id: "bad", summary: "undated", created_at: "bad" },
        { id: "zero", summary: "unset", created_at: "0001-01-01T00:00:00Z" },
      ],
      "all",
      now,
    );
    expect(days.map((d) => d.label)).toEqual([
      "Today",
      "Yesterday",
      new Date(2026, 8, 21).toLocaleDateString(undefined, {
        month: "short",
        day: "numeric",
      }),
      "Undated",
    ]);
    expect(days[0].rows).toHaveLength(4);
    expect(days[3].rows).toHaveLength(2);
  });
  it("pages 300 entries by whole days and reaches every row once", () => {
    const days = foldActivity(
      Array.from({ length: 300 }, (_, i) =>
        entry(String(i), 30 - Math.floor(i / 10)),
      ),
      "all",
      now,
    );
    let page = pageActivity(days, now);
    let end = days.findIndex((d) => d.key === page.oldestKey);
    expect(days.slice(0, end + 1).flatMap((d) => d.rows)).toHaveLength(50);
    expect(end).toBeLessThan(7);
    const seen = days
      .slice(0, end + 1)
      .flatMap((d) => d.rows.map((r) => r.key));
    while (page.hasOlder) {
      page = pageActivity(days, now, page.oldestKey);
      const next = days.findIndex((d) => d.key === page.oldestKey);
      seen.push(
        ...days
          .slice(end + 1, next + 1)
          .flatMap((d) => d.rows.map((r) => r.key)),
      );
      end = next;
    }
    expect(seen).toHaveLength(300);
    expect(new Set(seen).size).toBe(300);
  });
  it("keeps an oversized day whole and shows old history when nothing is recent", () => {
    const days = foldActivity(
      Array.from({ length: 60 }, (_, i) => entry(String(i), 10)),
      "all",
      now,
    );
    expect(pageActivity(days, now)).toEqual({
      oldestKey: days[0].key,
      hasOlder: false,
    });
    expect(days[0].rows).toHaveLength(60);
  });
});

describe("activity presentation", () => {
  it("shows Linear arrivals as owner-visible task steps", () => {
    expect(activityLabel("task.picked-up")).toBe("Asked");
    expect(matchesFilter("task.picked-up", "steps")).toBe(true);
    expect(isRoutineActivity("task.picked-up")).toBe(true);
  });
  it("describes recorded kinds without leaking internal identifiers", () => {
    expect(activityLabel("memory.created")).toBe("Memory");
    expect(activityLabel("decision.opened")).toBe("Asked you");
    expect(activityLabel("task.landed")).toBe("Landed");
    expect(activityLabel("task.planned")).toBe("Planned");
    expect(activityLabel("task.ordered")).toBe("Order");
    expect(activityLabel("pm.asked")).toBe("PM asked");
    // What the PM decided about landing is shown, not folded away.
    expect(activityLabel("task.pm_landing")).toBe("PM decided");
    expect(isRoutineActivity("task.pm_landing")).toBe(false);
    // Arriving in triage is asking, like queuing; where the PM sent it is shown.
    expect(activityLabel("task.triage")).toBe("Asked");
    expect(isRoutineActivity("task.triage")).toBe(true);
    expect(activityLabel("task.triaged")).toBe("Triage");
    expect(isRoutineActivity("task.triaged")).toBe(false);
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

  it("treats task steps and memory changes as routine", () => {
    for (const kind of [
      "task.writing",
      "task.reviewing",
      "task.deciding",
      "task.started",
      "task.queued",
      "task.planned",
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
