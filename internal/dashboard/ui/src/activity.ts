import type { Activity } from "./api";
import { recordedTime } from "./ui";

/**
 * Readable descriptions for recorded activity kinds. Unknown kinds fall back to
 * a readable phrase rather than leaking the internal identifier.
 */
const labels: Record<string, string> = {
  "release.proposed": "Release proposed",
  "release.refused": "Release deferred",
  "release.waiting": "Release waiting",
  "release.checked": "Release check",
  "release.published": "Release published",
  "release.failed": "Release stopped",
  "release.recorded": "Released",
  "assistant.review": "Assistant",
  "assistant.theme": "Appearance",
  "assistant.update": "Assistant",
  "brief.updated": "Brief",
  "project.paused": "Pause",
  "coordination.paused": "Pause",
  "decision.dismissed": "Closed",
  "decision.opened": "Asked you",
  "decision.resolved": "You decided",
  "memory.corrected": "Memory",
  "memory.created": "Memory",
  "memory.forgotten": "Memory",
  "memory.updated": "Memory",
  "operation.acknowledged": "Checked",
  "operation.interrupted": "Interrupted",
  "playbook.set": "Team",
  "project.completed": "Project",
  "project.created": "Project",
  "project.directories_updated": "Folders",
  "project.refined": "Brief",
  "project.renamed": "Project",
  "recovery.pending": "Interrupted",
  "task.awaiting": "Waiting",
  "task.deciding": "Checks",
  "task.delivered": "Delivered",
  "task.landed": "Landed",
  "task.landing": "Landing",
  "task.message": "Message",
  "task.message_answered": "Reply",
  "task.message_failed": "No reply",
  "task.ordered": "Order",
  "pm.asked": "PM asked",
  "task.planned": "Planned",
  "task.pm_landing": "PM decided",
  "task.queued": "Asked",
  "task.picked-up": "Asked",
  "task.triage": "Asked",
  "task.triaged": "Triage",
  "task.reordered": "To do",
  "task.reviewing": "Checks",
  "task.started": "Started",
  "task.stopped": "Stopped",
  "task.waiting": "Needs you",
  "task.writing": "Draft",
};

/**
 * Families recorded by the retired delegation model. Older workspaces still
 * hold these entries, and their internal names are not owner vocabulary.
 */
const retiredFamilies = new Set(["agent", "work_item", "worker"]);

/**
 * Kinds that report a step of work rather than something the owner acts on;
 * they form the Steps filter.
 */
const routine = new Set([
  "task.awaiting",
  "task.deciding",
  "task.landing",
  "task.planned",
  "task.queued",
  "task.picked-up",
  "task.triage",
  "task.reordered",
  "task.reviewing",
  "task.started",
  "task.writing",
  "memory.corrected",
  "memory.created",
  "memory.forgotten",
  "memory.updated",
]);

export function activityLabel(kind?: string): string {
  if (!kind) return "Update";
  const known = labels[kind];
  if (known) return known;
  if (retiredFamilies.has(kind.split(".")[0])) return "Earlier work";
  return "Update";
}

export function isRoutineActivity(kind?: string): boolean {
  return !!kind && routine.has(kind);
}

export type ActivityFilter = "all" | "needs" | "outcomes" | "steps";

export function matchesFilter(
  kind: string | undefined,
  filter: ActivityFilter,
): boolean {
  if (filter === "all") return true;
  if (filter === "steps") return isRoutineActivity(kind);
  if (filter === "needs")
    return kind === "decision.opened" || kind === "task.waiting";
  return [
    "task.landed",
    "task.delivered",
    "task.stopped",
    "task.pm_landing",
  ].includes(kind || "");
}

export interface ActivityRow {
  key: string;
  taskId?: string;
  entry: Activity;
  entries: Activity[];
  others: number;
}
export interface ActivityDay {
  key: string;
  label: string;
  rows: ActivityRow[];
}

export function activityDayKey(date: Date): string {
  return [
    date.getFullYear(),
    String(date.getMonth() + 1).padStart(2, "0"),
    String(date.getDate()).padStart(2, "0"),
  ].join("-");
}

/** Input is the project's newest-first feed. Invalid times stay reachable. */
export function foldActivity(
  entries: Activity[],
  filter: ActivityFilter,
  now: Date,
): ActivityDay[] {
  const today = activityDayKey(now);
  const yesterday = activityDayKey(
    new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1),
  );
  const days = new Map<string, ActivityDay>();
  for (const entry of entries) {
    if (!matchesFilter(entry.kind, filter)) continue;
    const date = recordedTime(entry.created_at);
    const key = date ? activityDayKey(date) : "undated";
    let day = days.get(key);
    if (!day) {
      day = {
        key,
        label:
          key === today
            ? "Today"
            : key === yesterday
              ? "Yesterday"
              : date
                ? date.toLocaleDateString(undefined, {
                    month: "short",
                    day: "numeric",
                  })
                : "Undated",
        rows: [],
      };
      days.set(key, day);
    }
    const previous = day.rows[day.rows.length - 1];
    if (entry.task_id && previous?.taskId === entry.task_id) {
      previous.entries.push(entry);
      previous.key = entry.id;
      previous.others++;
    } else {
      day.rows.push({
        key: entry.id,
        taskId: entry.task_id,
        entry,
        entries: [entry],
        others: 0,
      });
    }
  }
  const result = [...days.values()].sort((a, b) =>
    a.key === "undated"
      ? 1
      : b.key === "undated"
        ? -1
        : b.key.localeCompare(a.key),
  );
  for (const day of result) for (const row of day.rows) row.entries.reverse();
  return result;
}

/** Page on All, never the active filter. New arrivals do not move the cutoff. */
export function pageActivity(
  days: ActivityDay[],
  now: Date,
  loadedThrough?: string,
): { oldestKey?: string; hasOlder: boolean } {
  let end = loadedThrough
    ? days.findIndex((day) => day.key === loadedThrough)
    : -1;
  let count = 0;
  const start = end + 1;
  const recent = activityDayKey(
    new Date(now.getFullYear(), now.getMonth(), now.getDate() - 6),
  );
  for (let i = start; i < days.length; i++) {
    if (
      i > start &&
      (count + days[i].rows.length > 50 ||
        (!loadedThrough && days[i].key < recent))
    )
      break;
    count += days[i].rows.length;
    end = i;
  }
  return { oldestKey: days[end]?.key, hasOlder: end < days.length - 1 };
}
