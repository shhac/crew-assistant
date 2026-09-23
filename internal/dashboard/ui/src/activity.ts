import type { Activity } from "./api";

/**
 * Readable descriptions for recorded activity kinds. Unknown kinds fall back to
 * a readable phrase rather than leaking the internal identifier.
 */
const labels: Record<string, string> = {
  "assistant.review": "Assistant review",
  "assistant.theme": "Workspace palette changed",
  "assistant.update": "Assistant update",
  "coordination.paused": "Dispatch paused",
  "decision.dismissed": "Decision dismissed",
  "decision.opened": "Decision raised",
  "decision.resolved": "Decision answered",
  "demo.created": "Demo workspace created",
  "memory.corrected": "Memory corrected",
  "memory.created": "Remembered something new",
  "memory.forgotten": "Memory forgotten",
  "memory.updated": "Memory updated",
  "operation.acknowledged": "Interrupted operation inspected",
  "operation.interrupted": "Operation interrupted",
  "project.completed": "Project completed",
  "project.created": "Project added",
  "project.directories_updated": "Project folders updated",
  "project.refined": "Project brief refined",
  "recovery.pending": "Recovery needs attention",
};

/**
 * Families recorded by the retired delegation model. Older workspaces still
 * hold these entries, and their internal names are not owner vocabulary.
 */
const retiredFamilies = new Set(["agent", "work_item", "worker"]);

/** Kinds that report routine runtime progress rather than a change of state. */
const routine = new Set(["assistant.update"]);

export function activityLabel(kind?: string): string {
  if (!kind) return "Workspace";
  const known = labels[kind];
  if (known) return known;
  if (retiredFamilies.has(kind.split(".")[0])) return "Earlier work update";
  return kind.replaceAll("_", " ").replaceAll(".", " ");
}

export function isRoutineActivity(kind?: string): boolean {
  return !!kind && routine.has(kind);
}

export interface ActivityGroup {
  entry: Activity;
  label: string;
  count: number;
  routine: boolean;
}

/**
 * Collapses an adjacent run of the same routine kind within one project into a
 * single row. Entries are expected newest first; the newest entry of a run
 * represents it, so the count never implies a fresher event than was recorded.
 */
export function groupActivity(
  entries: Activity[],
  limit?: number,
): ActivityGroup[] {
  const groups: ActivityGroup[] = [];
  for (const entry of entries) {
    const previous = groups[groups.length - 1];
    const collapsible =
      previous &&
      previous.routine &&
      isRoutineActivity(entry.kind) &&
      previous.entry.kind === entry.kind &&
      (previous.entry.project_id || "") === (entry.project_id || "");
    if (collapsible) {
      previous.count += 1;
      continue;
    }
    groups.push({
      entry,
      label: activityLabel(entry.kind),
      count: 1,
      routine: isRoutineActivity(entry.kind),
    });
  }
  return limit ? groups.slice(0, limit) : groups;
}
