import type {
  Decision,
  LandPolicy,
  Playbook,
  Project,
  Stage,
  Task,
} from "./api";

/** Colour roles: amber needs the owner, blue is under way, grey waits. */
export type Tone = "needs" | "work" | "wait" | "block" | "done" | "";

export const isCode = (playbook?: Playbook) => playbook?.medium === "git";

export const finished = (task: Task) =>
  task.status === "delivered" ||
  task.status === "landed" ||
  task.status === "stopped";

export const needsYou = (task: Task) => task.status === "waiting";

const active = (task: Task) =>
  task.status === "writing" ||
  task.status === "reviewing" ||
  task.status === "deciding" ||
  task.status === "landing";

export interface Column {
  stage: Stage;
  label: string;
}

/**
 * The board's columns. QA shows when the team has it, or when a request
 * that started with it is still open after the team changed.
 */
export function boardColumns(project: Project, tasks: Task[]): Column[] {
  const code = isCode(project.playbook);
  const qa =
    !!project.playbook?.roles.some((r) => r.kind === "qa") ||
    tasks.some((t) => !finished(t) && t.roles?.some((r) => r.kind === "qa"));
  return [
    { stage: "todo", label: "To do" },
    { stage: "implementing", label: code ? "Implementing" : "Writing" },
    { stage: "reviewing", label: "Reviewing" },
    ...(qa ? [{ stage: "qa" as const, label: "QA" }] : []),
    { stage: "ready", label: code ? "Ready to land" : "Ready" },
    { stage: "done", label: code ? "Landed" : "Delivered" },
  ];
}

export function roleName(task: Task, kind: string, fallback: string) {
  return task.roles?.find((r) => r.kind === kind)?.name ?? fallback;
}

/** The reviewer still to judge the latest draft, or the first reviewer. */
function reviewerAtWork(task: Task) {
  const latest = task.revisions?.at(-1)?.n;
  const reviewers = (task.roles ?? []).filter((r) => r.kind === "reviewer");
  const waiting = reviewers.find(
    (r) =>
      !task.verdicts?.some((v) => v.role === r.name && v.revision === latest),
  );
  return (waiting ?? reviewers[0])?.name ?? "Reviewer";
}

const held = (task: Task) =>
  !!task.retry_at &&
  !task.retry_at.startsWith("0001-") &&
  new Date(task.retry_at).valueOf() > Date.now();

/** What a request is doing now, in a few words. */
export function requestStep(task: Task, decision?: Decision): string {
  if (held(task) && task.detail) return task.detail;
  const round = task.round > 1 ? `Round ${task.round} · ` : "";
  const code = isCode(task.playbook);
  switch (task.status) {
    case "queued":
      return "Waiting to start";
    case "writing":
      return `${round}${roleName(task, "implementer", code ? "Implementer" : "Writer")} working`;
    case "reviewing":
    case "deciding": {
      if (task.stage !== "qa")
        return `${round}${reviewerAtWork(task)} reviewing`;
      const check = task.playbook?.check;
      return check ? `${round}QA running ${check}` : `${round}QA checking`;
    }
    case "waiting":
      switch (decision?.kind) {
        case "delivery":
          return "Waiting for your approval";
        case "question":
          return "Question for you";
        case "escalation":
          return `Still has review points after ${task.round} rounds`;
        case "failure":
          return "Stuck until you decide";
      }
      return "Waiting for you";
    case "landing": {
      const target = task.playbook?.land?.target;
      return target ? `Landing on ${target}` : "Landing";
    }
    case "awaiting":
      return task.proposal?.number
        ? `Pull request #${task.proposal.number}: waiting on checks and reviews`
        : "Waiting on checks and reviews";
    case "landed":
      return task.delivered_to ? `Landed on ${task.delivered_to}` : "Landed";
    case "delivered":
      if (code)
        return task.delivered_to
          ? `On branch ${task.delivered_to}`
          : "Delivered";
      return task.delivered_to ? `Copied to ${task.delivered_to}` : "Approved";
    case "stopped":
      return "Stopped";
  }
  return task.status;
}

export function requestTone(task: Task): Tone {
  if (needsYou(task)) return "needs";
  if (active(task)) return "work";
  if (task.status === "landed" || task.status === "delivered") return "done";
  if (task.status === "awaiting" || task.status === "queued") return "wait";
  return "";
}

export type ProjectGroup = "needs" | "working" | "waiting" | "quiet";

export const projectGroups: { group: ProjectGroup; label: string }[] = [
  { group: "needs", label: "Needs you" },
  { group: "working", label: "Working" },
  { group: "waiting", label: "Waiting" },
  { group: "quiet", label: "Quiet" },
];

export function projectGroup(project: Project, tasks: Task[]): ProjectGroup {
  const own = tasks.filter((t) => t.project_id === project.id);
  if (own.some(needsYou)) return "needs";
  if (own.some(active)) return "working";
  if (own.some((t) => !finished(t))) return "waiting";
  return "quiet";
}

const groupOrder: Record<Task["status"], number> = {
  waiting: 0,
  landing: 1,
  writing: 1,
  reviewing: 1,
  deciding: 1,
  awaiting: 2,
  queued: 3,
  delivered: 4,
  landed: 4,
  stopped: 5,
};

/** The request that says most about what a project is doing now. */
export function leadRequest(project: Project, tasks: Task[]) {
  return tasks
    .filter((t) => t.project_id === project.id && !finished(t))
    .sort((a, b) => groupOrder[a.status] - groupOrder[b.status])[0];
}

/** How a project's approved work leaves it, in a few words. */
export function landsBy(playbook?: Playbook): string {
  if (!playbook) return "No team yet";
  if (!isCode(playbook))
    return playbook.deliver_to ? "Copied to a folder" : "Stays on its page";
  const land = playbook.land;
  if (land?.via === "push") return `Fast-forward ${land.target}`;
  if (land?.via === "pull-request")
    return `Pull request · ${land.method || "squash"}`;
  return "New local branch";
}

export function reversibility(land?: LandPolicy): string {
  if (land?.via === "push") return "Undoable with effort";
  if (land?.via === "pull-request") return "Permanent once merged";
  return "Undoable";
}

/** The approve button's words: what approving actually does. */
export function approveLabel(playbook?: Playbook): string {
  if (!isCode(playbook))
    return playbook?.deliver_to ? "Approve and copy" : "Approve";
  const land = playbook?.land;
  if (land?.via === "push") return `Land on ${land.target}`;
  if (land?.via === "pull-request") return "Open pull request";
  return "Create branch";
}

/** What approving leads to, step by step. */
export function whatHappens(playbook?: Playbook): string[] {
  if (!isCode(playbook))
    return playbook?.deliver_to
      ? [`The draft is copied into ${playbook.deliver_to}.`]
      : ["The draft stays here, marked approved."];
  const land = playbook?.land;
  if (land?.via === "push")
    return [
      `${land.target} moves forward to include this change. Nothing already on it is replaced.`,
      `If ${land.target} is checked out and has no uncommitted changes, your checkout updates too.`,
    ];
  if (land?.via === "pull-request")
    return [
      `A pull request opens on ${land.github} into ${land.target}.`,
      "The team answers its reviews and fixes failing checks.",
      `It merges by ${land.method || "squash"} once GitHub says it's approved and green.`,
    ];
  return [
    `A new branch starting ${playbook?.branch_prefix ?? "crew/"} is created in your repository.`,
    "Nothing is pushed.",
  ];
}
