import type { Activity } from "./api";

/**
 * Readable descriptions for recorded activity kinds. Unknown kinds fall back to
 * a readable phrase rather than leaking the internal identifier.
 */
const labels: Record<string, string> = {
  "assistant.review": "Assistant",
  "assistant.theme": "Appearance",
  "assistant.update": "Assistant",
  "brief.updated": "Brief",
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
  "recovery.pending": "Interrupted",
  "task.awaiting": "Waiting",
  "task.deciding": "Checks",
  "task.delivered": "Delivered",
  "task.landed": "Landed",
  "task.landing": "Landing",
  "task.message": "Message",
  "task.message_answered": "Reply",
  "task.message_failed": "No reply",
  "task.planned": "Planned",
  "task.queued": "Asked",
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
 * they are there on request, not by default.
 */
const routine = new Set([
  "task.awaiting",
  "task.deciding",
  "task.landing",
  "task.planned",
  "task.queued",
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
