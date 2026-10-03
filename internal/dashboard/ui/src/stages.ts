import { recordedTime } from "./ui";
import { approveLabel, isCode } from "./landing";
import { engineLabel } from "./engines";
import { holds, pmSeat, taskPlaybook, workingSeats } from "./members";
import {
  pendingDecisions,
  type Decision,
  type Observed,
  type Playbook,
  type Project,
  type Role,
  type Stage,
  type Task,
  type TeamMessage,
  type Turn,
  type Wait,
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

/** Optional overall limit; zero means no overall limit. */
export const activeCap = (playbook: Playbook) => playbook.max_active || 0;

/**
 * Requests under way, with the optional overall cap when set. Counts the
 * same statuses as the server, excluding owner waits, outside checks and
 * reviews, and the to-do list; "" for a project with no team.
 */
export function capLine(project: Project, tasks: Task[]) {
  if (!project.playbook) return "";
  const count = projectTasks(project, tasks).filter(active).length;
  const cap = activeCap(project.playbook);
  return `${count}${cap ? ` of ${cap}` : ""} under way`;
}

/** Started and not yet back with the owner or finished. */
export const underWay = (task: Task) =>
  !finished(task) &&
  task.status !== "waiting" &&
  task.status !== "queued" &&
  task.status !== "triage";

export const projectTasks = (project: Project, tasks: Task[]) =>
  tasks.filter((t) => t.project_id === project.id);

/**
 * The request an address names: its id, or its readable ID in any case, so
 * a link such as #/projects/<id>/requests/CA-12 opens it too.
 */
export const findRequest = (tasks: Task[], name: string) =>
  tasks.find((t) => t.id === name) ??
  tasks.find((t) => !!t.ref && t.ref.toLowerCase() === name.toLowerCase());

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
  research: { label: "Asked for research", tone: "needs" },
};

/** What a checker's recommended next step means. */
export const nextStep: Record<string, string> = {
  land: "Recommends it goes on",
  revise: "Recommends another revision",
  research: "Recommends more research",
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
  approve?: (playbook?: Playbook, task?: Task) => string;
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
    // An open pull request's approval is to merge it.
    approve: (playbook, task) =>
      task?.proposal?.number && playbook?.land?.pull_requests
        ? "Merge pull request"
        : approveLabel(playbook),
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
  "pr-flow": {
    ...otherDecision,
    badge: "Pull requests are off",
    step: () => "Waiting on whether it keeps its pull request",
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

/**
 * How a seat reads after "With" or "Waiting for": a member by its name, and
 * a template's seat by its role, as "the implementer" or "QA".
 */
export function seatWords(task: Task, name: string) {
  if (task.roles?.find((r) => r.name === name)?.member) return name;
  return name === name.toUpperCase() ? name : `the ${name.toLowerCase()}`;
}

/** How a seat reads, as seatWords does, when the task may not list it. */
const personWords = (task: Task, wait: Wait) =>
  wait.member ? wait.seat! : seatWords(task, wait.seat ?? "");

/** What the board calls a stage, as its lane does. */
export function stageLabel(stage: Stage, playbook?: Playbook) {
  switch (stage) {
    case "todo":
      return "To do";
    case "triage":
      return "Triage";
    case "researching":
      return "Researching";
    case "designing":
      return "Designing";
    case "implementing":
      return isCode(playbook) ? "Implementing" : "Writing";
    case "reviewing":
      return "Reviewing";
    case "qa":
      return "QA";
    case "pr_opening":
      return "PR to open";
    case "pr_open":
      return "PR open";
    case "ready":
      return isCode(playbook) ? "Ready to land" : "Ready";
  }
  return stage;
}

/** Column capacity is independent of how many teammates can work. */
export const stageLimit = (playbook: Playbook | undefined, stage: Stage) =>
  !playbook || stage === "triage"
    ? 0
    : playbook.stage_limits?.[stage] ||
      (["researching", "designing", "implementing", "reviewing", "qa"].includes(
        stage,
      )
        ? 10
        : 0);

/**
 * Who or what a ready step waits for, as "Waiting for Lucius (busy on
 * CA-27)". The project names the stages of a request not yet started.
 */
export function waitingWords(task: Task, wait: Wait, project?: Project) {
  switch (wait.kind) {
    case "stage": {
      const stage = stageLabel(wait.stage!, taskPlaybook(task, project));
      return `Waiting for room in ${stage} (${wait.count} of ${wait.limit})${wait.on ? ` · held by ${wait.on}` : ""}`;
    }
    case "member": {
      const on = [wait.on, wait.objective ? `“${wait.objective}”` : ""]
        .filter(Boolean)
        .join(" ");
      const busy = on
        ? ` (busy on ${on}${wait.project ? ` in ${wait.project}` : ""})`
        : wait.list
          ? ` (busy with the ${wait.list} to-do list)`
          : "";
      return `Waiting for ${personWords(task, wait)}${busy}`;
    }
    case "project_cap":
      return `Waiting for this project's cap (${wait.active} of ${wait.cap} active)`;
    case "engine_cap":
      return `Waiting for the ${engineLabel(wait.engine ?? "")} safety cap`;
    case "blocker":
      return wait.on ?? "Waiting on an external condition";
    case "owner":
      return "Waiting while you chat";
  }
  return "Waiting to start";
}

/**
 * What a request is doing now, in a few words. Given the turns running, a
 * request with a role says it is at work only while that role's turn runs,
 * and "With …" otherwise. The project, when known, names the stages of a
 * request not yet started.
 */
export function requestStep(
  task: Task,
  decision?: Decision,
  turns?: Turn[],
  project?: Project,
): string {
  if (held(task) && task.detail) return task.detail;
  const round = task.round > 1 ? `Round ${task.round} · ` : "";
  const code = isCode(task.playbook);
  const idle = !!turns && !turns.some((t) => t.task_id === task.id);
  // The seats at work on it, as its turns and the steps taken name them.
  const seats = workingSeats(task, turns ?? []);
  const withSeat = (name: string) => `${round}With ${seatWords(task, name)}`;
  if (task.waiting && task.status !== "waiting" && !finished(task))
    return `${round}${waitingWords(task, task.waiting, project)}`;
  switch (task.status) {
    case "queued":
      return "Waiting to start";
    case "triage": {
      const pm = task.checking || "the PM";
      if (decision) return `Waiting on your answer for ${pm}`;
      if (task.answered) return `Your answer is in; ${pm} looks again next`;
      return `With ${pm} for triage`;
    }
    case "researching": {
      if (idle)
        return `With ${seatWords(task, task.checking || roleName(task, "researcher", "Researcher"))}`;
      return task.checking ? `${task.checking} researching` : "Researching";
    }
    case "designing":
      return `With ${task.checking || roleName(task, "designer", "the designer")} for design input`;
    case "writing": {
      const writer =
        seats[0]?.name ??
        roleName(task, "implementer", code ? "Implementer" : "Writer");
      const pr = task.proposal;
      if (pr?.answering && pr.number)
        return `${round}${writer} answering pull request #${pr.number}`;
      return idle ? withSeat(writer) : `${round}${writer} working`;
    }
    case "reviewing":
    case "deciding": {
      // Checks of one draft run side by side, so each checker at work shows.
      const checkers = seats.filter(
        (r) => holds(r, "reviewer") || holds(r, "qa"),
      );
      if (checkers.length) {
        // Before their turns show, every checker that took a check is named.
        if (idle)
          return `${round}With ${checkers.map((r) => seatWords(task, r.name)).join(" and ")}`;
        return `${round}${checkers.map((r) => checkWords(task, r)).join(" · ")}`;
      }
      if (task.stage !== "qa") {
        const reviewer =
          task.checking || roleName(task, "reviewer", "Reviewer");
        return idle ? withSeat(reviewer) : `${round}${reviewer} reviewing`;
      }
      if (idle) return withSeat(task.checking || roleName(task, "qa", "QA"));
      const check = task.playbook?.check;
      return check ? `${round}QA running ${check}` : `${round}QA checking`;
    }
    case "waiting":
      if (task.answered) return "Your answer is in; it carries on next";
      return decisionKind(decision).step(task);
    case "landing": {
      if (task.playbook?.land?.pull_requests) {
        const n = task.proposal?.number;
        if (!n) return "Opening a pull request";
        return task.proposal?.observed?.ready
          ? `Merging pull request #${n}`
          : `Updating pull request #${n}`;
      }
      const target = task.playbook?.land?.target;
      return target ? `Landing on ${target}` : "Landing";
    }
    case "awaiting":
      return task.proposal?.number
        ? `Pull request #${task.proposal.number}: ${prWords(task.proposal.observed)}`
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

/** "Rune reviewing" or "QA running make check": one checker at work. */
function checkWords(task: Task, role: Role) {
  if (!holds(role, "qa")) return `${role.name} reviewing`;
  const check = task.playbook?.check;
  return check ? `${role.name} running ${check}` : `${role.name} checking`;
}

export function requestTone(task: Task): Tone {
  if (needsYou(task)) return "needs";
  if (active(task)) return "work";
  if (task.status === "landed" || task.status === "delivered") return "done";
  if (
    task.status === "awaiting" ||
    task.status === "queued" ||
    task.status === "triage" ||
    task.answered
  )
    return "wait";
  return "";
}

export type ProjectGroup = "needs" | "working" | "waiting" | "quiet";

/** Projects read in alphabetical order wherever they are listed. */
export const byTitle = (a: Project, b: Project) =>
  a.title.localeCompare(b.title, undefined, { sensitivity: "base" });

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
  triage: 3,
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

/** What an open pull request waits on, as the loop last saw it. */
export function prWords(observed?: Observed): string {
  if (!observed) return "waiting on checks and reviews";
  if (observed.ready) return "ready to land";
  const checks: Record<string, string> = {
    SUCCESS: "checks passed",
    FAILURE: "checks failed",
    PENDING: "checks running",
    NONE: "no checks",
  };
  const review: Record<string, string> = {
    APPROVED: "approved",
    CHANGES_REQUESTED: "changes requested",
    REVIEW_REQUIRED: "review required",
  };
  const unresolved = observed.unresolved ?? 0;
  return [
    checks[observed.checks] ?? `checks ${observed.checks.toLowerCase()}`,
    observed.review ? (review[observed.review] ?? observed.review) : "",
    unresolved
      ? `${unresolved} unresolved thread${unresolved === 1 ? "" : "s"}`
      : "",
    observed.conflicting ? "conflicts with its base" : "",
  ]
    .filter(Boolean)
    .join(", ");
}
