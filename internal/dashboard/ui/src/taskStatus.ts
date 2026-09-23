import type { Project, Task } from "./api";

export interface StatusLine {
  label: string;
  tone: "" | "amber" | "green";
}

const latestDraft = (task: Task) => task.revisions?.at(-1)?.n ?? 0;

export function taskStatusLine(task: Task): StatusLine {
  switch (task.status) {
    case "queued":
      return { label: "Waiting to start", tone: "" };
    case "writing":
      return { label: `Writing draft ${latestDraft(task) + 1}`, tone: "" };
    case "reviewing":
      return { label: `Reviewing draft ${latestDraft(task)}`, tone: "" };
    case "deciding":
      return { label: "Weighing the reviews", tone: "" };
    case "waiting":
      return { label: "Needs your decision", tone: "amber" };
    case "delivered":
      return { label: "Delivered", tone: "green" };
    case "stopped":
      return { label: "Stopped", tone: "" };
  }
  return { label: task.status, tone: "" };
}

/**
 * The line under a request: where it was delivered, why it stopped, or the
 * loop's own note. Kept out of the status badge, whose styling would change
 * a path's case and cannot wrap it.
 */
export function taskDetail(task: Task) {
  if (task.status === "delivered")
    return task.delivered_to ? `Copied to ${task.delivered_to}` : "";
  return task.detail ?? "";
}

export function newestFirst(tasks: Task[]) {
  return tasks
    .map((task, index) => ({ task, index }))
    .sort(
      (a, b) =>
        (b.task.created_at ?? "").localeCompare(a.task.created_at ?? "") ||
        b.index - a.index,
    )
    .map(({ task }) => task);
}

export function projectTasks(project: Project, tasks: Task[]) {
  return newestFirst(tasks.filter((t) => t.project_id === project.id));
}

const attention: Task["status"][] = [
  "waiting",
  "writing",
  "reviewing",
  "deciding",
  "queued",
];

/** What the project is doing now, in one line: its most pressing open task. */
export function projectStatusLine(project: Project, tasks: Task[]): StatusLine {
  const open = projectTasks(project, tasks);
  for (const status of attention) {
    const task = open.find((t) => t.status === status);
    if (task) return taskStatusLine(task);
  }
  return { label: "Idle", tone: "" };
}
