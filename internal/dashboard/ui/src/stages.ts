import { recordedTime } from "./ui";
import { approveLabel, isCode } from "./landing";
import { holds, pmSeat, taskPlaybook } from "./members";
import {
  pendingDecisions,
  type Decision,
  type Playbook,
  type Project,
  type Task,
  type TeamMessage,
} from "./api";

/** Colour roles: amber needs the owner, blue is under way, grey waits. */
export type Tone = "needs" | "work" | "wait" | "block" | "done" | "";

export { isCode, taskPlaybook };

export const finished = (task: Task) =>
  task.status === "delivered" ||
  task.status === "landed" ||
  task.status === "stopped";

/** Waiting on the owner. An answered task only waits for the loop's next step. */
export const needsYou = (task: Task) =>
  task.status === "waiting" && !task.answered;

const active = (task: Task) =>
  task.status === "researching" ||
  task.status === "designing" ||
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

export const isOpenMessage = (m: TeamMessage) =>
  m.status === "waiting" || m.status === "working";

export const verdictOutcome: Record<string, { label: string; tone: Tone }> = {
  pass: { label: "Passed", tone: "done" },
  revise: { label: "Asked for changes", tone: "needs" },
  question: { label: "Asked a question", tone: "needs" },
};

const when = (at?: string) => (at ? Date.parse(at) || 0 : 0);

/**
 * Finished requests, the most recently finished first. A task's update time
 * is when it finished; ties fall back to when it was asked for, then to the
 * server's order reversed.
 */
export function latestFirst(tasks: Task[]) {
  return tasks
    .map((task, i) => ({ task, i }))
    .sort(
      (a, b) =>
        when(b.task.updated_at) - when(a.task.updated_at) ||
        when(b.task.created_at) - when(a.task.created_at) ||
        b.i - a.i,
    )
    .map((x) => x.task);
}

export function roleName(task: Task, kind: string, fallback: string) {
  return task.roles?.find((r) => holds(r, kind))?.name ?? fallback;
}

/**
 * Who set the order of the to-do list, or the PM about to look at it; "" for
 * a list with no order to speak of.
 */
export function orderLine(project: Project, queued: number) {
  if (!queued) return "";
  const pm = pmSeat(project);
  if (project.pm_due && pm) return `${pm.name} is looking at the order`;
  switch (project.ordered_by) {
    case "owner":
      return "Ordered by you";
    case "assistant":
      return "Ordered by the assistant";
    case "pm":
      return `Ordered by ${pm?.name ?? "the PM"}`;
  }
  return queued > 1 ? "In the order asked for" : "";
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
  "pm-question": { ...otherDecision, badge: "Question from the PM" },
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
    case "researching":
      return task.checking ? `${task.checking} researching` : "Researching";
    case "designing":
      return `With ${task.checking || roleName(task, "designer", "the designer")} for design input`;
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
      if (task.answered) return "Your answer is in; it carries on next";
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
  if (task.status === "awaiting" || task.status === "queued" || task.answered)
    return "wait";
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
  researching: 1,
  designing: 1,
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
