import { recordedTime } from "./ui";
import {
  pendingDecisions,
  type Decision,
  type LandPolicy,
  type Playbook,
  type Project,
  type Stage,
  type Task,
  type TeamMessage,
} from "./api";

/** Colour roles: amber needs the owner, blue is under way, grey waits. */
export type Tone = "needs" | "work" | "wait" | "block" | "done" | "";

export const isCode = (playbook?: Playbook) => playbook?.medium === "git";

export const finished = (task: Task) =>
  task.status === "delivered" ||
  task.status === "landed" ||
  task.status === "stopped";

export const needsYou = (task: Task) => task.status === "waiting";

export const active = (task: Task) =>
  task.status === "writing" ||
  task.status === "reviewing" ||
  task.status === "deciding" ||
  task.status === "landing";

/** Started and not yet back with the owner or finished. */
export const underWay = (task: Task) =>
  !finished(task) && task.status !== "waiting" && task.status !== "queued";

export const projectTasks = (project: Project, tasks: Task[]) =>
  tasks.filter((t) => t.project_id === project.id);

/** The open decision a request is waiting on, if any. */
export const decisionFor = (task: Task, decisions: Decision[]) =>
  pendingDecisions(decisions).find((d) => d.id === task.decision_id);

export const projectKind = (playbook?: Playbook) =>
  !playbook ? "Tracking only" : isCode(playbook) ? "Code" : "Writing";

/** A request keeps the team it started with after the project's changes. */
export const taskPlaybook = (task?: Task, project?: Project) =>
  task?.playbook ?? project?.playbook;

export const isOpenMessage = (m: TeamMessage) =>
  m.status === "waiting" || m.status === "working";

export const verdictOutcome: Record<string, { label: string; tone: Tone }> = {
  pass: { label: "Passed", tone: "done" },
  revise: { label: "Asked for changes", tone: "needs" },
  question: { label: "Asked a question", tone: "needs" },
};

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

export interface DecisionKindWords {
  /** The badge on the inbox. */
  badge: string;
  /** What the waiting request is doing, for its board card and panel. */
  step: (task: Task) => string;
  recommend: boolean;
  /**
   * How the owner can answer in their own words: the form is open from the
   * start, a button offers it, or only a choice that asks for words opens it.
   */
  answering: "open" | "offered" | "through-choice";
  prompt: string;
  send: string;
  /** The words for "Approve", when approving sends the work on. */
  approve?: (playbook?: Playbook) => string;
  /** Whether the card says how hard landing is to undo. */
  reversible?: boolean;
}

const askForChanges = { prompt: "What should change?", send: "Send changes" };
const askForAnswer = { prompt: "Your answer", send: "Send answer" };

const otherDecision: DecisionKindWords = {
  badge: "Decision",
  step: () => "Waiting for you",
  recommend: true,
  answering: "offered",
  ...askForAnswer,
};

const decisionKinds: Record<string, DecisionKindWords> = {
  delivery: {
    badge: "Ready to approve",
    step: () => "Waiting for your approval",
    recommend: false,
    answering: "through-choice",
    ...askForChanges,
    approve: approveLabel,
    reversible: true,
  },
  update: {
    badge: "Update to check",
    step: () => "Update waiting for you",
    recommend: true,
    answering: "through-choice",
    ...askForChanges,
    approve: () => "Push the update",
  },
  question: {
    badge: "Question",
    step: () => "Question for you",
    recommend: false,
    answering: "open",
    ...askForAnswer,
  },
  escalation: {
    badge: "Review points left",
    step: (task) => `Still has review points after ${task.round} rounds`,
    recommend: true,
    answering: "through-choice",
    ...askForAnswer,
  },
  failure: {
    badge: "Stuck",
    step: () => "Stuck until you decide",
    recommend: false,
    answering: "through-choice",
    ...askForAnswer,
  },
};

/** How a kind of decision is shown and answered; any other kind is a plain choice. */
export function decisionKind(decision?: Decision): DecisionKindWords {
  const kind = decision?.kind ?? "";
  return Object.hasOwn(decisionKinds, kind)
    ? decisionKinds[kind]
    : otherDecision;
}

const held = (task: Task) => {
  const until = recordedTime(task.retry_at);
  return !!until && until.valueOf() > Date.now();
};

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
        return `${round}${task.checking || roleName(task, "reviewer", "Reviewer")} reviewing`;
      const check = task.playbook?.check;
      return check ? `${round}QA running ${check}` : `${round}QA checking`;
    }
    case "waiting":
      return decisionKind(decision).step(task);
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

/** Where a project sits, judged by its own requests. */
export function projectGroup(own: Task[]): ProjectGroup {
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

/** Of a project's own requests, the one that says most about it now. */
export function leadRequest(own: Task[]) {
  return own
    .filter((t) => !finished(t))
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
      `If ${land.target} is checked out, your checkout updates too; if it has uncommitted changes, landing stops and asks you first.`,
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
